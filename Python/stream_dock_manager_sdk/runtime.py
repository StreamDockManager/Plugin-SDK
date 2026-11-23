"""One reader, one event-loop writer, bounded work, cooperative cancellation."""
from copy import deepcopy
from .theme import THEME_EVENT, THEME_PROTOCOL, PluginTheme, decode_plugin_theme
import argparse
from contextvars import ContextVar
from dataclasses import dataclass
import json
import threading
import time

from .network import NetworkPayload
from .protocol import Event, FEATURES, HostInfo, UnsupportedFeatureError, parse_capabilities
from .hoststorage import Control, RPC_REQUEST
from .vault import format_text

_current = ContextVar('stream_dock_manager_action', default=None)
MAX_MESSAGE = 1 << 20
MAX_OUTPUT_BYTES = 8 * MAX_MESSAGE
MAX_LOG_BYTES = MAX_MESSAGE
MAX_INPUT_BYTES = 8 * MAX_MESSAGE
MAX_INPUT_ITEMS = 128


class _IncomingBudget:
    """Retained envelopes include queued, selected and dial-batched work."""
    # Initialize synchronized incoming message and byte accounting
    def __init__(self):
        self.condition = threading.Condition()
        self.bytes = self.items = 0

    # Reserve incoming message capacity, waiting until space or cancellation
    def acquire(self, size, stop):
        if not 0 <= size <= MAX_MESSAGE: raise ValueError('host message exceeds limit')
        with self.condition:
            while not stop.is_set():
                if self.items < MAX_INPUT_ITEMS and self.bytes + size <= MAX_INPUT_BYTES:
                    self.items += 1
                    self.bytes += size
                    return _IncomingLease(self, size)
                self.condition.wait(.05)
        return None


class _IncomingLease:
    # Track one incoming reservation and prevent double release
    def __init__(self, budget, size):
        self.budget, self.size, self.released = budget, size, False

    # Return the incoming message reservation exactly once
    def release(self):
        with self.budget.condition:
            if self.released: return
            self.released = True
            self.budget.bytes -= self.size
            self.budget.items -= 1
            self.budget.condition.notify_all()


# Load queue support after the authenticated connection is established
def _load_queue():
    # Queues are needed by the owned event loop, after the authenticated upgrade
    # Keep their optional native accelerators out of module import/registration
    global queue
    import queue


# Create a queue bounded by both message count and encoded byte size
def _output_queue(limit):
    class ByteQueue(queue.Queue):
        # Initialize message capacity and encoded byte accounting
        def __init__(self):
            super().__init__(128)
            self.bytes = 0
        # Store the supplied byte queue entry
        def _put(self, value):
            size = len(value)
            if self.bytes + size > limit: raise queue.Full
            super()._put(value)
            self.bytes += size
        # Return the requested byte queue value
        def _get(self):
            value = super()._get()
            self.bytes -= len(value)
            return value
    return ByteQueue()


class _StopEvent(threading.Event):
    # Bind cancellation to the event-loop wake signal
    def __init__(self, ready):
        super().__init__()
        self.ready = ready
    # Update the selected stop event value
    def set(self):
        super().set()
        self.ready.set()


class Completion:
    # Track pending operations and aggregate their final action result
    def __init__(self, emit):
        self._emit = emit
        self._lock = threading.Lock()
        self._pending = 0
        self._returned = self._failed = self._sent = False

    # Defer completion data
    def defer(self):
        with self._lock:
            if self._sent:
                raise RuntimeError('action already completed')
            self._pending += 1
        called = False
        lock = threading.Lock()
        # Finalize background completion and wake the event loop
        def done(error=None):
            nonlocal called
            with lock:
                if called:
                    return
                called = True
            self.finish(error, False)
        return done

    # Finalize the pending completion operation
    def finish(self, error=None, returned=True):
        with self._lock:
            self._failed |= error is not None
            if returned:
                self._returned = True
            else:
                self._pending -= 1
            send = self._returned and self._pending == 0 and not self._sent
            if send:
                self._sent = True
            success = not self._failed
        if send:
            self._emit(success)

    # Cancel completion data
    def cancel(self):
        with self._lock:
            send = not self._sent
            self._sent = True
        if send:
            self._emit(False)


# Defer action
def defer_action():
    action = _current.get()
    return action.defer() if action is not None else lambda error=None: None


class ProbePool:
    """Four lazy workers and 32 queued cooperative jobs. Results belong to the loop."""
    # Create the bounded job queue and worker shutdown state
    def __init__(self):
        _load_queue()
        self._jobs = queue.Queue(32)
        self._stop = threading.Event()
        self._workers = []
        self._outstanding = 0
        self._lock = threading.Lock()

    # Admit a probe job while enforcing worker and queue capacity
    def submit(self, work, cancel=None):
        with self._lock:
            if self._stop.is_set() or cancel is not None and cancel.is_set():
                return False
            try:
                self._jobs.put_nowait((work, cancel))
            except queue.Full:
                return False
            self._outstanding += 1
            if len(self._workers) < min(4, self._outstanding):
                worker = threading.Thread(target=self._worker, daemon=True)
                self._workers.append(worker)
                worker.start()
            return True

    # Process queued probe pool work until shutdown
    def _worker(self):
        while True:
            job = self._jobs.get()
            if job is None:
                return
            work, cancel = job
            try:
                if not self._stop.is_set() and not (cancel is not None and cancel.is_set()):
                    work()
            finally:
                del job, work, cancel
                with self._lock:
                    self._outstanding -= 1

    def close(self):
        with self._lock:
            if self._stop.is_set():
                return
            self._stop.set()
            while True:
                try:
                    self._jobs.get_nowait()
                except queue.Empty:
                    break
            for _ in self._workers:
                self._jobs.put_nowait(None)
        deadline = time.monotonic() + 2
        for worker in self._workers:
            if worker is not threading.current_thread():
                worker.join(max(0, deadline - time.monotonic()))


# Check whether should log action
def should_log_action(event):
    if event.event in ('keyDown', 'dialDown'):
        return True
    if event.event == 'dialRotate':
        return event.payload.get('ticks', 0) != 0
    if event.event != 'touchTap':
        return False
    touch = event.surface.touch if event.surface else None
    return touch.get('phase', '') in ('tap', 'end', '') if touch else not event.payload.get('hold', False)


class Handler:
    # Dispatch the incoming event through handler
    def handle(self, client, event):
        pass

    # Poll handler for ready work or updated state
    def poll(self, client):
        pass

    # Return the next scheduled poll deadline
    def next_poll(self, now):
        # Polling is explicit; event-only handlers consume no idle timer
        return None

    # Return batch dial for handler
    def batch_dial(self, action):
        return False

    # Check whether should log action
    def should_log_action(self, event):
        return should_log_action(event)

    # Stop handler activity and release owned resources
    def close(self):
        pass


class Client:
    # Initialize host messaging, control requests, logging, and background work queues
    def __init__(self, host=None, identity=''):
        _load_queue()
        self.host = host or HostInfo()
        self.identity = identity
        self.network = NetworkPayload()
        self._ready = threading.Event()
        self.stop = _StopEvent(self._ready)
        self._wake = threading.Event()
        self._out = _output_queue(MAX_OUTPUT_BYTES)
        self._logs = _output_queue(MAX_LOG_BYTES)
        self._log_lock = threading.Lock()
        self._dropped_logs = 0
        self.set_network_debug(self.host.manager.get('debug_network') is True)
        self._results = queue.Queue(36)
        self._inputs = {}
        self._actions = {}
        self._jobs = {}
        self._job_redactors = {}
        self._redactor_lock = threading.Lock()
        self._pool = ProbePool()
        self.control = Control(self)
        self._plugin_theme = None
        self._theme_lock = threading.Lock()
        self._flush_control = lambda: None

    # Check whether the host advertises the requested capability
    def supports(self, feature):
        return self.host.supports(feature)

    def _network_demand(self):
        run = self.host.manager.get("run_id")
        return self.supports("plugin-protocol-v1") and self.supports("network-demand-v1") and self.supports("network-payload-v1") and isinstance(run, str) and 0 < len(run.encode()) <= 128

    # Check whether theme requests can use the negotiated host capability
    def supports_theme_rpc(self):
        return self.supports(THEME_PROTOCOL) and self.host.manager.get('theme_rpc') is True

    # Read plugin theme
    def get_plugin_theme(self, cancel=None) -> PluginTheme:
        if not self.supports_theme_rpc():
            raise UnsupportedFeatureError(THEME_PROTOCOL)
        value = decode_plugin_theme(self.rpc(THEME_PROTOCOL, 'get', {}, cancel))
        self._remember_plugin_theme(value)
        return self.current_plugin_theme()

    # Return the retained host-provided plugin theme
    def current_plugin_theme(self) -> PluginTheme | None:
        with self._theme_lock:
            return deepcopy(self._plugin_theme)

    # Update the retained plugin theme from host metadata
    def _remember_plugin_theme(self, value):
        with self._theme_lock:
            if self._plugin_theme is not None and self._plugin_theme['revision'] >= value['revision']:
                return False
            self._plugin_theme = deepcopy(value)
            return True

    def format_text(self, action: str, text: str, cancel: threading.Event | None = None) -> str:
        """Resolve one transient string without updating saved settings."""
        return format_text(self, action, text, cancel)

    # Return rpc for client
    def rpc(self, protocol, method, payload, cancel=None):
        return self.control.request(protocol, method, payload, cancel)

    # Send control
    def _send_control(self, message):
        self.send(message['event'], message.get('context', ''), message.get('payload'),
                  control={key: message[key] for key in ('protocol_id', 'request_id', 'method') if key in message})
        self._flush_control()

    # Send the supplied client message
    def send(self, event, context='', payload=None, *, action=None, control=None):
        if self.stop.is_set():
            raise RuntimeError('SDK session closed')
        feature = FEATURES.get(event)
        if feature and not self.supports(feature):
            raise UnsupportedFeatureError(feature)
        if event == RPC_REQUEST and not self.supports((control or {}).get('protocol_id', '')):
            raise UnsupportedFeatureError((control or {}).get('protocol_id', ''))
        message = dict(event=event)
        if control: message.update(control)
        if context:
            message['context'] = context
        if action:
            message['action'] = action
        if payload is not None:
            message['payload'] = payload
        # ASCII JSON makes character length the exact UTF-8 wire size.
        encoded = json.dumps(message, ensure_ascii=True, separators=(',', ':'), allow_nan=False)
        if len(encoded) > MAX_MESSAGE:
            raise ValueError('SDK message exceeds 1048576-byte UTF-8 JSON limit')
        cancel = None
        if event == 'desktopInput' and _current.get() is not None:
            request = payload.get('request_id')
            if not isinstance(request, str) or not request or request in self._inputs or len(self._inputs) >= 128:
                raise ValueError('invalid, duplicate, or excessive desktop input request ID')
            cancel = defer_action()
            self._inputs[request] = (context, cancel)
        try:
            (self._logs if event == 'logMessage' else self._out).put_nowait(encoded)
            self._ready.set()
        except queue.Full as error:
            if event == 'logMessage':
                with self._log_lock:
                    self._dropped_logs += 1
            if cancel:
                self._inputs.pop(payload['request_id'], None)
                cancel(error)
            raise RuntimeError('SDK outbound queue full') from error

    # Update the title displayed for the action context
    def title(self, context, text): self.send('setTitle', context, dict(title=text, target=0))
    # Update the image displayed for the action context
    def image(self, context, image): self.send('setImage', context, dict(image=image, target=0))
    # Select the action state displayed by the host
    def state(self, context, state): self.send('setState', context, dict(state=state))
    # Send feedback values to the action context
    def feedback(self, context, value): self.send('setFeedback', context, value)
    # Select the feedback layout for the action context
    def feedback_layout(self, context, layout): self.send('setFeedbackLayout', context, dict(layout=layout))
    # Write action settings through the base host protocol
    def settings(self, context, value): self.send('setSettings', context, value)
    # Request action settings from the host
    def get_settings(self, context): self.send('getSettings', context)
    def request_vsd_craft_settings(self):
        """Send VSD Craft's backend settings request; the reply is a didReceiveSettings event."""
        if not self.identity.strip():
            raise ValueError('VSD Craft settings require a registration UUID')
        self.send('getSettings', self.identity)

    # Write global plugin settings through the base host protocol
    def global_settings(self, value): self.send('setGlobalSettings', self.identity, value)
    # Request global plugin settings from the host
    def get_global_settings(self): self.send('getGlobalSettings', self.identity)
    # Send an action-scoped message to the property inspector
    def to_inspector(self, context, value, *, action):
        if not action:
            raise ValueError('inspector messaging requires an action UUID')
        self.send('sendToPropertyInspector', context, value, action=action)
    # Send an alert indicator to the action context
    def alert(self, context): self.send('showAlert', context, {})
    # Send a success indicator to the action context
    def ok(self, context): self.send('showOk', context, {})
    log_redactor = staticmethod(lambda text: text)

    def _safe_log(self, text):
        current = self.log_redactor
        with self._redactor_lock:
            previous = tuple(self._job_redactors.values())
        for redact in previous:
            if redact is not current: text = redact(text)
        return current(text)

    # Forward a message to the plugin stdout log stream
    def log(self, text): self.log_stream('stdout', text)
    # Frame and queue redacted output for the selected log stream
    def log_stream(self, stream, text):
        if stream not in ('stdout', 'stderr'): raise ValueError('invalid log stream')
        text = self._safe_log(str(text))
        raw = ''.join('�' if 0xd800 <= ord(char) <= 0xdfff else char for char in text).encode('utf-8')
        while raw:
            end = min(len(raw), 16 << 10)
            while end < len(raw) and raw[end] & 0xc0 == 0x80:
                end -= 1
            payload = dict(message=raw[:end].decode())
            raw = raw[end:]
            if self.supports('plugin-logs-v1'): payload['stream'] = stream
            self.send('logMessage', payload=payload)
    # Emit automatically captured output through the bounded logging path
    def _automatic_log(self, stream, text):
        try:
            self.log_stream(stream, text)
        except Exception:
            pass

    # Update network debug
    def set_network_debug(self, enabled):
        # Emit client data
        def emit(payload, failed):
            try:
                self.send('logMessage', payload=dict(payload, stream='stderr' if failed else 'stdout', source='network'))
            except Exception:
                pass
        self.network.diagnostics.configure(enabled and self.supports('plugin-logs-v1'), emit, self._safe_log)

    # Drain a bounded batch, prioritizing action/display output over diagnostics
    def drain(self):
        """Drain a bounded batch, prioritizing action/display output over diagnostics."""
        return [json.loads(frame) for frame in self._drain_encoded()]

    def _drain_encoded(self):
        values = []
        for _ in range(128):
            try:
                values.append(self._out.get_nowait())
            except queue.Empty:
                break
        with self._log_lock:
            lost, self._dropped_logs = self._dropped_logs, 0
        if lost:
            payload = dict(message=f'SDK log queue overloaded; {lost} messages were not captured', incomplete=True)
            if self.supports('plugin-logs-v1'):
                payload['stream'] = 'stderr'
            values.append(json.dumps(dict(event='logMessage', payload=payload), separators=(',', ':')))
        for _ in range(128):
            try:
                values.append(self._logs.get_nowait())
            except queue.Empty:
                break
        return values

    # Return stdout for client
    def stdout(self): return LogWriter(self, 'stdout')
    # Return stderr for client
    def stderr(self): return LogWriter(self, 'stderr')
    # Open url
    def open_url(self, url): self.send('openUrl', payload=dict(url=url))
    # Update the host-provided dynamic settings choices
    def dynamic_items(self, value): self.send('setDynamicItems', payload=value)
    # Subscribe to telemetry
    def subscribe_telemetry(self, topics): self.send('subscribeTelemetry', payload=dict(topics=topics))
    # Request desktop input for the specified action context
    def desktop_input(self, context, value): self.send('desktopInput', context, value)

    # Send the structured display update for an action context
    def display(self, context, *, title, image='', state=0, layout='', feedback=None):
        self.state(context, state)
        self.title(context, title)
        if image:
            self.image(context, image)
        if layout:
            self.feedback_layout(context, layout)
        if feedback is not None:
            self.feedback(context, feedback)

    # Signal the runtime loop that new work is ready
    def wake(self):
        self._wake.set()
        self._ready.set()

    # work(cancel_event) -> immutable result; apply(client, result) runs on the loop
    def background(self, context, work, apply, *, track_action=True):
        """work(cancel_event) -> immutable result; apply(client, result) runs on the loop."""
        if len(self._jobs) >= 36:
            raise RuntimeError('SDK background queue full')
        cancel, key = threading.Event(), object()
        complete = defer_action() if track_action else lambda error=None: None
        self._jobs[key] = (context, cancel, complete)
        sanitize = self.log_redactor
        def guarded(fn, *args):
            with self._redactor_lock: self._job_redactors[key] = sanitize
            try: return fn(*args)
            finally:
                with self._redactor_lock: self._job_redactors.pop(key, None)
        def run():
            value = error = None
            try:
                value = guarded(work, cancel)
            except Exception as failure:
                error = failure
            if not cancel.is_set() and not self.stop.is_set():
                self._results.put((key, value, error, lambda *args: guarded(apply, *args)))
                self._ready.set()
                self.wake()
        if not self._pool.submit(run, cancel):
            self._jobs.pop(key)
            complete(RuntimeError('SDK background queue full'))
            raise RuntimeError('SDK background queue full')
        return cancel

    # Drain results
    def _drain_results(self):
        while True:
            try:
                key, result, error, apply = self._results.get_nowait()
            except queue.Empty:
                return
            job = self._jobs.pop(key, None)
            if job is None:
                continue
            _, cancel, complete = job
            if cancel.is_set():
                error = RuntimeError('background job cancelled')
            if error is None:
                try:
                    apply(self, result)
                except Exception as failure:
                    error = failure
            complete(error)

    # Cancel one owned job on the event loop, including its deferred receipt
    def cancel_background(self, cancel):
        """Cancel one owned job on the event loop, including its deferred receipt."""
        for key, (_, flag, complete) in list(self._jobs.items()):
            if flag is cancel:
                self._jobs.pop(key)
                flag.set()
                complete(RuntimeError('background job cancelled'))
                return True
        return False

    # Cancel context
    def _cancel_context(self, context=None):
        for request, (owner, done) in list(self._inputs.items()):
            if context is None or owner == context:
                del self._inputs[request]
                done(RuntimeError('desktop input cancelled'))
        for key, (owner, cancel, done) in list(self._jobs.items()):
            if context is None or owner == context:
                del self._jobs[key]
                cancel.set()
                done(RuntimeError('background job cancelled'))
        for key, (owner, action) in list(self._actions.items()):
            if context is None or owner == context:
                action.cancel()
                self._actions.pop(key, None)

    # Route a host event to lifecycle, control, or plugin handlers
    def _dispatch(self, handler, event):
        if event.event == THEME_EVENT:
            if not self.supports_theme_rpc():
                return
            try:
                if not self._remember_plugin_theme(decode_plugin_theme(event.payload)):
                    return
            except ValueError:
                return
        if event.event == 'requestNetworkPayload' and self._network_demand():
            run = self.host.manager.get('run_id')
            if isinstance(run, str) and 0 < len(run) <= 128:
                sent, received = self.network.snapshot()
                self.send('networkPayload', payload=dict(run_id=run, sent_bytes=sent, received_bytes=received))
            return
        if event.event == 'didReceiveLogSettings' and self.supports('plugin-logs-v1'):
            enabled = event.payload.get('debug_network')
            if type(enabled) is bool:
                self.set_network_debug(enabled)
            return
        log_action = self.supports('plugin-logs-v1') and getattr(handler, 'should_log_action', should_log_action)(event)
        started = time.monotonic()
        if event.event == 'willDisappear':
            self._cancel_context(event.context)
        action = None
        tracked = event.surface and event.surface.request_id and self.supports('action-results-v1')
        if tracked or log_action:
            key = object()
            # Emit client data
            def emit(success):
                self._actions.pop(key, None)
                if not self.stop.is_set():
                    if tracked:
                        self.send('actionResult', event.context, dict(request_id=event.surface.request_id, success=success))
                    if log_action:
                        outcome = 'completed' if success else 'failed or cancelled'
                        self._automatic_log('stdout' if success else 'stderr', f'Action {event.action} {outcome} in {int((time.monotonic()-started)*1000)} ms')
            action = Completion(emit)
            self._actions[key] = (event.context, action)
        token = _current.set(action)
        error = None
        try:
            handler.handle(self, event)
            if self.supports('plugin-logs-v1') and event.event in ('didReceiveSettings', 'didReceiveGlobalSettings'):
                self._automatic_log('stdout', 'Settings updated')
        except Exception as failure:
            error = failure
            self._automatic_log('stderr', 'Action failure: ' + str(failure))
            self.title(event.context, 'Error: ' + str(failure))
            self.alert(event.context)
        finally:
            _current.reset(token)
            if event.event == 'desktopInputResult':
                request = event.payload.get('request_id')
                pending = self._inputs.get(request)
                if pending and pending[0] == event.context:
                    del self._inputs[request]
                    pending[1](error or (RuntimeError('desktop input failed') if event.payload.get('error') else None))
            if action:
                action.finish(error)

    # Stop client activity and release owned resources
    def close(self):
        self.control.close()
        self.set_network_debug(False)
        try:
            self._cancel_context()
        finally:
            self.stop.set()
            self._ready.set()
            self._pool.close()


@dataclass(frozen=True)
class LaunchOptions:
    port: int
    identity: str
    host: HostInfo

    # Parse host launch arguments and validate required connection metadata
    @classmethod
    def parse(cls, args=None):
        # HelpFormatter's terminal-size lookup imports shutil and native compression
        # libraries on Python 3.15. A fixed help width keeps them off startup
        parser = argparse.ArgumentParser(formatter_class=lambda prog: argparse.HelpFormatter(prog, width=80))
        parser.add_argument('-port', '--port', type=int, required=True)
        parser.add_argument('-pluginUUID', '--pluginUUID', required=True)
        parser.add_argument('-registerEvent', '--registerEvent', required=True)
        parser.add_argument('-info', '--info', default='{}')
        options = parser.parse_args(args)
        if not 1 <= options.port <= 65535 or not options.pluginUUID or options.registerEvent != 'registerPlugin':
            raise ValueError('invalid authenticated host arguments')
        return cls(options.port, options.pluginUUID, HostInfo.parse(options.info))


# Drive the runtime execution loop
def run(handler, args=None, *, descriptor=None, stop=None):
    options = LaunchOptions.parse(args)
    return _connect(handler, options.port, '/', options.identity, options.host,
                    descriptor=descriptor, stop=stop)


# Establish the validated transport connection
def connect(handler, address, identity, host=None, *, descriptor=None, stop=None):
    from urllib.parse import urlsplit
    parsed = urlsplit(address)
    if parsed.scheme != 'ws' or parsed.hostname not in ('127.0.0.1', 'localhost') or parsed.username or parsed.password:
        raise ValueError('host must be loopback ws')
    return _connect(handler, parsed.port or 80, parsed.path or '/', identity, host,
                    descriptor=descriptor, stop=stop)


# Establish the validated transport connection
def _connect(handler, port, target, identity, host=None, *, descriptor=None, stop=None):
    from .wire import connect_loopback
    host = host or HostInfo()
    capabilities = parse_capabilities(descriptor) if isinstance(descriptor, str) else descriptor
    for required in (capabilities or {}).get('required_capabilities', []):
        if not host.supports(required):
            raise UnsupportedFeatureError(required)
    client = None
    socket = None
    reader = None
    events = None
    batch_lease = None
    selected_lease = None
    try:
        # A fixed numeric endpoint prevents proxying or localhost DNS redirection
        socket = connect_loopback(port, target, timeout=1)
        client = Client(host, identity)
        registration = dict(event='registerPlugin', uuid=identity)
        if host.supports('plugin-protocol-v1'):
            registration['payload'] = dict(capabilities=capabilities, network_debug=True)
            if client._network_demand():
                registration['payload']['network_demand'] = True
        socket.send(json.dumps(registration))
        events = queue.Queue(MAX_INPUT_ITEMS)
        incoming = _IncomingBudget()
        # Read the next runtime value or data segment
        def read():
            while not client.stop.is_set():
                lease = None
                try:
                    raw = socket.recv()
                    if not raw:
                        raise ConnectionError('host disconnected')
                    if len(raw.encode() if isinstance(raw, str) else raw) > MAX_MESSAGE:
                        raise ValueError('host message exceeds limit')
                    value = Event.parse(raw)
                    if client.control.observe(value.raw, len(raw.encode() if isinstance(raw, str) else raw)): continue
                    lease = incoming.acquire(len(raw.encode() if isinstance(raw, str) else raw), client.stop)
                    if lease is None: return
                    del raw
                except TimeoutError:
                    if lease is not None: lease.release()
                    continue
                except Exception as error:
                    client.control.close()
                    if lease is not None: lease.release()
                    lease = None
                    value = error
                while not client.stop.is_set():
                    try:
                        events.put((value, lease), timeout=.05)
                        client._ready.set()
                        break
                    except queue.Full:
                        continue
                else:
                    if lease is not None: lease.release()
                    return
                terminal = isinstance(value, Exception) or value.event == 'shutdown'
                value = None
                if terminal:
                    return
        reader = threading.Thread(target=read, daemon=True)
        reader.start()
        deadline = handler.next_poll(time.monotonic())
        network_enabled = not client._network_demand() and host.supports('network-payload-v1') and bool(host.manager.get('run_id'))
        network_deadline = time.monotonic() + 1 if network_enabled else None
        backoff = 0
        backoff_until = 0
        batch = None
        batch_deadline = 0
        # Flush runtime data
        def flush():
            nonlocal batch, batch_lease
            if batch is not None:
                event, lease = batch, batch_lease
                batch = batch_lease = None
                try: client._dispatch(handler, event)
                finally: lease.release()
        write_lock = threading.Lock()
        # Flush queued protocol output to the authenticated host
        def writes():
            with write_lock:
                for frame in client._drain_encoded():
                    socket.send(frame)
        client._flush_control = writes
        if client.supports_theme_rpc():
            try:
                client.get_plugin_theme(stop)
            except Exception:
                # Theme bootstrap is optional. Legacy theme events are still queued
                pass
        while not client.stop.is_set() and not (stop is not None and stop.is_set()):
            client._ready.clear()
            now = time.monotonic()
            client._drain_results()
            writes()
            if batch is not None and now >= batch_deadline:
                flush()
            if client._wake.is_set() or deadline is not None and now >= max(deadline, backoff_until):
                client._wake.clear()
                try:
                    handler.poll(client)
                    backoff = 0
                except Exception as error:
                    backoff = min(60, backoff * 2 if backoff else 1)
                    client._automatic_log('stderr', str(error))
                backoff_until = now + backoff
                deadline = handler.next_poll(time.monotonic())
            if network_deadline is not None and now >= network_deadline:
                network_deadline = now + 1
                run_id = host.manager.get('run_id', '')
                if host.supports('network-payload-v1') and isinstance(run_id, str) and 0 < len(run_id) <= 128:
                    sent, received = client.network.snapshot()
                    client.send('networkPayload', payload=dict(run_id=run_id, sent_bytes=sent, received_bytes=received))
            writes()
            try:
                event, lease = events.get_nowait()
                selected_lease = lease
            except queue.Empty:
                if client.stop.is_set(): continue
                deadlines = [value for value in (max(deadline, backoff_until) if deadline is not None else None, batch_deadline if batch is not None else None, network_deadline) if value is not None]
                timeout = max(0, min(deadlines) - time.monotonic()) if deadlines else None
                # An externally supplied Event has no callback; keep only that
                # compatibility cancellation path on a bounded 250 ms check
                if stop is not None: timeout = min(timeout, .25) if timeout is not None else .25
                client._ready.wait(timeout)
                continue
            if isinstance(event, Exception):
                raise event
            if event.event == 'shutdown':
                lease.release()
                flush()
                break
            ticks = event.payload.get('ticks', 0)
            additive = (event.event == 'dialRotate' and handler.batch_dial(event.action)
                        and not (event.surface and event.surface.request_id))
            if additive:
                old = batch.payload.get('ticks', 0) if batch else 0
                if (batch and time.monotonic() < batch_deadline and
                    (batch.context, batch.action, batch.device, old > 0) ==
                    (event.context, event.action, event.device, ticks > 0) and -(2**63) <= old + ticks < 2**63):
                    raw = dict(batch.raw, payload=dict(batch.payload, ticks=old + ticks))
                    batch = Event.parse(raw)
                    del raw
                    lease.release()
                else:
                    flush()
                    batch, batch_deadline = event, time.monotonic() + .016
                    batch_lease = lease
            else:
                try:
                    flush()
                    client._dispatch(handler, event)
                finally:
                    lease.release()
            selected_lease = None
            event = lease = None
        client._cancel_context()
        writes()
    finally:
        try:
            if client is not None:
                client.close()
        finally:
            try:
                if socket is not None:
                    socket.close(timeout=1)
            finally:
                if reader is not None:
                    reader.join(2)
                if batch_lease is not None: batch_lease.release()
                if selected_lease is not None: selected_lease.release()
                if events is not None:
                    while True:
                        try: _, lease = events.get_nowait()
                        except queue.Empty: break
                        if lease is not None: lease.release()
                handler.close()


class LogWriter:
    """Text writer for RPC output; never echoes to the process console."""
    # Bind text output to the selected client log stream
    def __init__(self, client, stream): self.client, self.stream = client, stream
    # Write the supplied log writer value
    def write(self, text): self.client.log_stream(self.stream, text); return len(text)
    # Flush log writer data
    def flush(self): pass

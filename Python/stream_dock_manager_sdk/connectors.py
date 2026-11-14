"""Shared connection lifecycle; drivers own service-specific operations and data."""
from .settings import project
from .discovery import envelope as discovery_envelope, Request as DiscoveryRequest
from .polling import update_interval, NEVER
from copy import deepcopy
from dataclasses import dataclass
import threading
import time
from .authoring import Plugin, Action, Display, ViewTarget
from .polling import Resources
from .runtime import defer_action
from .credentials import safe_error


@dataclass
class Choice:
    id: str
    name: str
    kind: str = ''


@dataclass
class Outcome:
    message: str = ''
    value: object = None


@dataclass
class Options:
    actions: tuple
    interval: float
    shared_keys: tuple = ()
    dial_actions: tuple = ()
    targets: tuple = ()
    clock: object = time.monotonic
    read_timeout: float = 12
    command_timeout: float = 15
    authorize_timeout: float = 120


class Connector(Plugin):
    # Bind the service driver to settings, shared resources, and serialized work
    def __init__(self, driver, options):
        super().__init__(targets=options.targets or [ViewTarget(action, options.actions) for action in options.actions], clock=options.clock)
        self.driver, self.options = driver, options
        self.resources, self.queue, self.messages = Resources(), [], {}
        self.busy, self.active_context, self.active_receipt = False, '', None
        self.active_deadline, self.active_resource = None, None
        self.work_lock = threading.Lock()
        self.inspector = self._inspect
        for action in options.actions:
            self.register(action, Action(handle=self._action, render=self._view))

    # Resolve settings for the selected action binding
    def binding_settings(self, context):
        settings = project(self.global_settings, self.bindings[context].settings, self.options.shared_keys)
        settings['_action'] = self.bindings[context].action.rsplit('.',1)[-1]
        return settings

    # Watch connector data
    def watch(self):
        demand = []
        for context in self.bindings:
            settings = self.binding_settings(context); demand.append(settings)
            self.resources.watch(context, self.driver.key(settings), update_interval(settings, self.options.interval))
        if hasattr(self.driver, 'demand'): self.driver.demand(demand)

    # Build the settings or display projection for the current binding
    def _view(self, plugin, context, binding):
        self.watch(); state = self.resources.snapshot(context)
        text, visual = self.messages.get(context, ''), 0
        if not text and state and state.observed_at is not None:
            text = ('Stale\n' if state.stale else '') + self.driver.display(state.last_value, binding.action.rsplit('.',1)[-1], self.binding_settings(context))
            if hasattr(self.driver, 'visual_state'): visual = self.driver.visual_state(state.last_value, binding.action.rsplit('.',1)[-1])
        return Display(title=text or 'Waiting for connection', state=visual)

    # Queue supported button, dial, and touch actions for the connector
    def _action(self, plugin, client, event):
        if event.event not in ('keyDown', 'dialDown', 'touchTap', 'dialRotate'): return
        if event.event == 'dialRotate' and not self.batch_dial(event.action): return
        self.enqueue(client, event.context, event.action.rsplit('.',1)[-1], '', self.binding_settings(event.context), event.payload.get('ticks', 0) if event.event == 'dialRotate' else 0)

    # Inspect connector data
    def _inspect(self, plugin, client, event):
        payload = event.payload
        if payload.get('op') not in ('test', 'discover', 'authorize', 'disconnect'): return
        request_id = payload.get('request_id')
        if request_id is not None and (not isinstance(request_id,str) or len(request_id.encode())>128): raise ValueError('invalid discovery request ID')
        settings = project(self.global_settings, payload.get('settings', {}), () if payload.get('scope') == 'plugin' else self.options.shared_keys)
        self.enqueue(client, event.context, event.action, payload['op'], settings, 0, request_id, payload.get('scope','action'))

    # Queue a connector operation for serialized execution
    def enqueue(self, client, context, action, op, settings, ticks, request_id=None, request_scope='action'):
        if len(self.queue) >= 32:
            if request_id: client.to_inspector(context,dict(request_id=request_id,available=False,error='Command queue full; retry after it drains'),action=action)
            raise RuntimeError('command queue full')
        self.queue.append(dict(request_id=request_id,request_scope=request_scope,context=context, action=action, op=op, settings=deepcopy(settings), ticks=ticks, receipt=defer_action()))
        self.start(client)

    # Start connector data
    def start(self, client):
        if self.busy: return
        self.watch()
        if self.queue:
            request = self.queue.pop(0); self.busy = True
            self.active_deadline = self.clock() + (self.options.authorize_timeout if request['op']=='authorize' else self.options.command_timeout)
            self.active_resource = None
            self.active_context, self.active_receipt = request['context'], request['receipt']
            # Execute the queued connector operation
            def work(cancel):
                with self.work_lock:
                    if cancel.is_set(): raise RuntimeError('cancelled')
                    settings, op = request['settings'], request['op']
                    try:
                        if op == 'discover': return dict(devices=[vars(value) if isinstance(value, Choice) else value for value in self.driver.discover(cancel, settings)], message='Request completed')
                        if op == 'test': self.driver.read(cancel, settings); return dict(message='Connection succeeded')
                        outcome = self.driver.command(cancel, op or request['action'], settings, request['ticks'])
                        return vars(outcome) if isinstance(outcome, Outcome) else outcome
                    except Exception as error: return dict(error=str(safe_error(error, settings)))
            # Apply the completed connector update
            def apply(plugin, client, result):
                self.busy = False; self.active_deadline = None; self.active_context, self.active_receipt = '', None
                failure = result.get('error')
                self.messages[request['context']] = 'Unavailable\n' + failure if failure else result.get('message', 'Request completed')
                request['receipt'](RuntimeError(failure) if failure else None)
                if request['request_id']: result = discovery_envelope(DiscoveryRequest(request['context'],'service',request['request_id'],request['op'],action=request['action'],scope=request['request_scope']),result,'failed' if failure else '').payload
                if request['op']: client.to_inspector(request['context'], result, action=request['action'])
                for state in self.resources.states.values(): state.next = 0
            self.background(client, 'connector-work', request['context'], work, apply)
            return
        keys = self.resources.due(self.clock())
        if not keys: return
        key = keys[0]
        candidates = (self.binding_settings(context) for context in sorted(self.bindings) if self.driver.key(self.binding_settings(context)) == key)
        settings = min(candidates, key=lambda s: update_interval(s, self.options.interval) or NEVER, default=None)
        if settings is None: return
        generation = self.resources.begin(key); self.busy = True
        self.active_deadline = self.clock() + self.options.read_timeout
        self.active_resource = (key,generation)
        # Execute the queued connector operation
        def work(cancel):
            with self.work_lock:
                if cancel.is_set(): raise RuntimeError('cancelled')
                try: return (self.driver.read(cancel, settings), None, None)
                except Exception as error: return (None, str(safe_error(error, settings)), getattr(error, 'retry_at', None))
        # Apply the completed connector update
        def apply(plugin, client, result):
            self.busy = False; self.active_deadline, self.active_resource = None, None
            value, error, retry_at = result
            if retry_at is not None: retry_at = self.clock() + max(0, retry_at - time.time())
            previous = self.resources.states.get(key)
            failures = (previous.failures if previous else 0) + 1
            recovering = previous is None or previous.observed_at is None or previous.failures > 0
            if self.resources.complete(key, generation, value, error, self.clock(), .5, retry_at):
                if client.supports('plugin-logs-v1'):
                    if error and failures & (failures-1) == 0:
                        client._automatic_log('stderr', f'Connection read failed ({failures} consecutive failures): {error}')
                    elif not error and recovering:
                        client._automatic_log('stdout', 'Connection available')
                for context in self.bindings:
                    if self.driver.key(self.binding_settings(context)) != key: continue
                    self.messages.pop(context, None)
                    if error and self.resources.snapshot(context).observed_at is None: self.messages[context] = 'Unavailable\n' + error
        self.background(client, 'connector-work', next(iter(self.bindings)), work, apply)

    # Reset connector data
    def reset(self):
        if self._client: self.cancel_job(self._client, 'connector-work')
        if self.active_receipt: self.active_receipt(RuntimeError('cancelled'))
        for request in self.queue: request['receipt'](RuntimeError('cancelled'))
        self.active_receipt, self.active_context, self.busy = None, '', False
        self.active_deadline, self.active_resource = None, None
        self.queue, self.messages, self.resources = [], {}, Resources()
        self.driver.close()

    # Dispatch the incoming event through connector
    def handle(self, client, event):
        self._client = client
        if event.event in ('didReceiveSettings', 'didReceiveGlobalSettings'): self.reset()
        super().handle(client, event)
        if event.event == 'willDisappear':
            self.resources.unwatch(event.context); self.messages.pop(event.context, None)
            if not self.bindings: self.reset()
        if event.event == 'propertyInspectorDidDisappear' and event.context not in self.bindings:
            retained = []
            for request in self.queue:
                if request['context'] == event.context: request['receipt'](RuntimeError('cancelled'))
                else: retained.append(request)
            self.queue = retained
            if self.active_context == event.context: self.reset()

    # Poll connector for ready work or updated state
    def poll(self, client):
        if self.busy and self.active_deadline is not None and self.clock() >= self.active_deadline:
            self.cancel_job(client, 'connector-work')
            if self.active_receipt: self.active_receipt(RuntimeError('connector deadline exceeded'))
            if self.active_resource:
                key,generation = self.active_resource
                self.resources.complete(key,generation,None,'connector deadline exceeded',self.clock(),.5)
            self.active_receipt, self.active_deadline, self.active_resource = None, None, None
            self.busy = False
        if hasattr(self.driver, 'changed') and self.driver.changed():
            for state in self.resources.states.values(): state.next = 0
        self.start(client); super().poll(client)

    # Return the next scheduled poll deadline
    def next_poll(self, now):
        if self.busy: return self.active_deadline
        if self.queue: return now
        self.watch()
        return min((deadline or now for key in self.resources.states if (deadline := self.resources.deadline(key)) != NEVER), default=None)

    # Return batch dial for connector
    def batch_dial(self, action): return action.rsplit('.',1)[-1] in self.options.dial_actions
    # Stop connector activity and release owned resources
    def close(self): self.reset(); super().close()

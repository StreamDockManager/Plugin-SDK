"""Exercise the owned loop through the loopback transport boundary, without a host."""
import json
import socket as socket_module
from pathlib import Path
import queue
import sys
import threading
import time
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from stream_dock_manager_sdk import Handler, HostInfo
from stream_dock_manager_sdk.runtime import connect, _IncomingBudget, MAX_INPUT_BYTES, MAX_INPUT_ITEMS, MAX_MESSAGE
from stream_dock_manager_sdk import wire
from wsproto import WSConnection, ConnectionType
from wsproto.events import AcceptConnection, Request, TextMessage


class Socket:
    # Initialize socket state and dependencies
    def __init__(self, events=()):
        self.incoming = queue.Queue()
        for event in events:
            self.incoming.put(event if isinstance(event, str) else json.dumps(event))
        self.messages = []
        self.writers = []
        self.closed = False

    # Send the supplied socket message
    def send(self, raw):
        self.writers.append(threading.get_ident())
        self.messages.append(json.loads(raw))

    # Receive the next socket message
    def recv(self):
        try:
            return self.incoming.get(timeout=.01)
        except queue.Empty:
            raise TimeoutError()

    # Stop socket activity and release owned resources
    def close(self, **kwargs):
        self.closed = True


class RuntimeTests(unittest.TestCase):
    def test_portable_registration_with_descriptor(self):
        fixture = json.loads((Path(__file__).resolve().parents[2] / 'contracts/fixtures/protocol-v1.json').read_text())
        descriptor = fixture['registration_descriptor']
        for host in ('legacy', 'opendeck', 'manager'):
            with self.subTest(host=host):
                socket = Socket([{'event': 'shutdown'}])
                with patch.object(wire, 'connect_loopback', return_value=socket):
                    connect(Handler(), 'ws://127.0.0.1:1234', 'fixture', HostInfo.parse(fixture[host]), descriptor=descriptor)
                expected = {'event': 'registerPlugin', 'uuid': 'fixture'}
                if host == 'manager':
                    expected['payload'] = {'capabilities': descriptor, 'network_debug': True, 'network_demand': True}
                self.assertEqual(socket.messages, [expected])

    # Verify incoming flood preserves fifo release and shutdown
    def test_incoming_flood_preserves_fifo_release_and_shutdown(self):
        errors = []
        with socket_module.socket() as listener:
            listener.bind(('127.0.0.1', 0)); listener.listen(1); listener.settimeout(5)
            # Prepare host in the runtime tests fixture
            def host():
                try:
                    transport, _ = listener.accept()
                    with transport:
                        transport.settimeout(5)
                        server = WSConnection(ConnectionType.SERVER)
                        accepted = registered = False
                        while not registered:
                            server.receive_data(transport.recv(4096))
                            for event in server.events():
                                if isinstance(event, Request):
                                    transport.sendall(server.send(AcceptConnection()))
                                    accepted = True
                                elif isinstance(event, TextMessage) and event.message_finished:
                                    registered = True
                        self.assertTrue(accepted)
                        for index in range(24):
                            raw = json.dumps(dict(event='keyDown', payload=dict(index=index, text='é'*300000)), ensure_ascii=False)
                            transport.sendall(server.send(TextMessage(data=raw)))
                        for event in ('keyUp', 'shutdown'):
                            transport.sendall(server.send(TextMessage(data=json.dumps(dict(event=event)))))
                        transport.recv(4096)
                except BaseException as error: errors.append(error)
            peer = threading.Thread(target=host)
            peer.start()
            class Flood(Handler):
                # Initialize flood state and dependencies
                def __init__(self): self.events, self.closed = [], 0
                # Dispatch the incoming event through flood
                def handle(self, client, event):
                    if not self.events: time.sleep(.03)
                    self.events.append((event.event, event.payload.get('index')))
                # Return the next scheduled poll deadline
                def next_poll(self, now): return None
                # Stop flood activity and release owned resources
                def close(self): self.closed += 1
            handler = Flood()
            connect(handler, f'ws://127.0.0.1:{listener.getsockname()[1]}', 'fixture')
            peer.join(6)
            self.assertFalse(peer.is_alive())
            if errors: raise errors[0]
            self.assertEqual(handler.events, [('keyDown', index) for index in range(24)] + [('keyUp', None)])
            self.assertEqual(handler.closed, 1)

    # Verify incoming admission includes selected and batched envelopes
    def test_incoming_admission_includes_selected_and_batched_envelopes(self):
        contract = json.loads((Path(__file__).resolve().parents[2] / 'contracts/fixtures/protocol-v1.json').read_text())['incoming_admission']
        self.assertEqual((MAX_INPUT_BYTES, MAX_INPUT_ITEMS, MAX_MESSAGE), (contract['bytes'], contract['items'], contract['receive_staging_bytes']))
        budget, stop = _IncomingBudget(), threading.Event()
        leases = [budget.acquire(MAX_MESSAGE, stop) for _ in range(MAX_INPUT_BYTES // MAX_MESSAGE)]
        selected = leases.pop()
        result = []
        waiter = threading.Thread(target=lambda: result.append(budget.acquire(1, stop)))
        waiter.start()
        time.sleep(.02)
        self.assertEqual(result, [])
        self.assertEqual(budget.bytes, MAX_INPUT_BYTES)
        stop.set(); waiter.join(1)
        self.assertFalse(waiter.is_alive())
        self.assertEqual(result, [None])
        selected.release(); selected.release()
        for lease in leases: lease.release()
        self.assertEqual((budget.items, budget.bytes), (0, 0))
        stop.clear()
        leases = [budget.acquire(1, stop) for _ in range(MAX_INPUT_ITEMS)]
        waiter = threading.Thread(target=lambda: result.append(budget.acquire(0, stop)))
        waiter.start(); time.sleep(.02)
        self.assertEqual(budget.items, MAX_INPUT_ITEMS)
        leases.pop().release(); waiter.join(1)
        self.assertFalse(waiter.is_alive())
        result[-1].release()
        for lease in leases: lease.release()
        self.assertEqual((budget.items, budget.bytes), (0, 0))
        with self.assertRaises(ValueError): budget.acquire(MAX_MESSAGE + 1, stop)

    # Prepare session in the runtime tests fixture
    def session(self, handler, socket):
        with patch.object(wire, 'connect_loopback', return_value=socket):
            connect(handler, 'ws://127.0.0.1:1234', 'fixture')
        self.assertTrue(socket.closed)
        self.assertEqual(socket.messages[0], dict(event='registerPlugin', uuid='fixture'))
        self.assertEqual(set(socket.writers), {threading.get_ident()})

    # Verify dial batching preserves direction context and shutdown flush
    def test_dial_batching_preserves_direction_context_and_shutdown_flush(self):
        socket = Socket([dict(event='dialRotate', action='dial', context=context, payload=dict(ticks=ticks))
                         for context, ticks in [('a', 2), ('a', 3), ('a', -1), ('b', -4)]] + [dict(event='shutdown')])
        class Dials(Handler):
            # Initialize dials state and dependencies
            def __init__(self): self.values, self.closed = [], False
            # Dispatch the incoming event through dials
            def handle(self, client, event): self.values.append((event.context, event.payload['ticks']))
            # Return batch dial for dials
            def batch_dial(self, action): return True
            # Return the next scheduled poll deadline
            def next_poll(self, now): return None
            # Stop dials activity and release owned resources
            def close(self): self.closed = True
        handler = Dials()
        self.session(handler, socket)
        self.assertEqual(handler.values, [('a', 5), ('a', -1), ('b', -4)])
        self.assertTrue(handler.closed)

    # Verify scheduled work is serialized and waits between deadlines
    def test_scheduled_work_is_serialized_and_waits_between_deadlines(self):
        owner = threading.get_ident()
        class Scheduled(Handler):
            # Initialize scheduled state and dependencies
            def __init__(self): self.calls, self.closed = [], False
            # Return the next scheduled poll deadline
            def next_poll(self, now): return now + .03
            # Poll scheduled for ready work or updated state
            def poll(self, client):
                if threading.get_ident() != owner: raise AssertionError('poll escaped handler thread')
                self.calls.append(time.monotonic())
                client.title('a', str(len(self.calls)))
                if len(self.calls) == 3: client.stop.set()
            # Stop scheduled activity and release owned resources
            def close(self): self.closed = True
        handler = Scheduled()
        self.session(handler, Socket())
        self.assertEqual(len(handler.calls), 3)
        self.assertTrue(all(b-a >= .025 for a, b in zip(handler.calls, handler.calls[1:])))
        self.assertTrue(handler.closed)

    # Verify no deadline wait wakes for background completion and stop
    def test_no_deadline_wait_wakes_for_background_completion_and_stop(self):
        applied = []
        class Background(Handler):
            # Return the next scheduled poll deadline
            def next_poll(self, now): return None
            # Dispatch the incoming event through background
            def handle(self, client, event):
                client.background(event.context, lambda cancel: cancel.wait(.03) or 42,
                                  lambda owner, value: (applied.append(value), owner.stop.set()))
        socket = Socket([dict(event='keyDown', context='a')])
        self.session(Background(), socket)
        self.assertEqual(applied, [42])

    # Verify malformed frames always close the handler and transport
    def test_malformed_frames_always_close_the_handler_and_transport(self):
        class Closed(Handler):
            closed = False
            # Dispatch the incoming event through closed
            def handle(self, client, event): raise AssertionError('malformed event dispatched')
            # Stop closed activity and release owned resources
            def close(self): self.closed = True
        for raw in ['[]', '{}', '{"event":1}', '{"event":"keyUp","payload":[]}']:
            with self.subTest(raw=raw):
                socket, handler = Socket([raw]), Closed()
                with self.assertRaises(ValueError): self.session(handler, socket)
                self.assertTrue(handler.closed)
                self.assertTrue(socket.closed)


if __name__ == '__main__': unittest.main()


class ExplicitSchedulingTests(unittest.TestCase):
    # Verify base handler is event only
    def test_base_handler_is_event_only(self):
        from stream_dock_manager_sdk.runtime import Handler
        self.assertIsNone(Handler().next_poll(0))

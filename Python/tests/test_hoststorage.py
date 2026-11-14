import json
from pathlib import Path
import threading
import unittest
import queue
from unittest.mock import patch
from stream_dock_manager_sdk import Client, HostInfo, UnsupportedFeatureError
from stream_dock_manager_sdk.hoststorage import (HostStore, PortableSettings, RPCError,
                                       RPC_RESPONSE, STORAGE_PROTOCOL, SETTINGS_PROTOCOL)

FIXTURE = json.loads((Path(__file__).resolve().parents[2] / 'contracts/fixtures/storage-v1.json').read_text())


class HostStorageTests(unittest.TestCase):
    # Verify transport waits for control without reentering handler
    def test_transport_waits_for_control_without_reentering_handler(self):
        from stream_dock_manager_sdk import Handler, wire
        from stream_dock_manager_sdk.runtime import connect
        class Socket:
            # Initialize socket state and dependencies
            def __init__(self): self.incoming = queue.Queue(); self.closed = False
            # Send the supplied socket message
            def send(self, raw):
                event = json.loads(raw)
                if event['event'] == 'registerPlugin': self.incoming.put(json.dumps(dict(event='keyDown', context='a')))
                elif event['event'] == 'streamDockManagerRpcRequest':
                    self.incoming.put(json.dumps(dict(event='keyUp', context='a')))
                    self.incoming.put(json.dumps(dict(event=RPC_RESPONSE, protocol_id=STORAGE_PROTOCOL, request_id=event['request_id'], result=dict(found=False, generation='1', revision='0'))))
                elif event['event'] == 'setTitle': self.incoming.put(json.dumps(dict(event='shutdown')))
            # Receive the next socket message
            def recv(self):
                try: return self.incoming.get(timeout=.01)
                except queue.Empty: raise TimeoutError()
            # Stop socket activity and release owned resources
            def close(self, **kwargs): self.closed = True
        class Waiting(Handler):
            # Initialize waiting state and dependencies
            def __init__(self): self.events = []; self.active = False
            # Return the next scheduled poll deadline
            def next_poll(self, now): return None
            # Dispatch the incoming event through waiting
            def handle(self, client, event):
                self.assert_not_active()
                self.events.append(event.event)
                if event.event == 'keyDown':
                    self.active = True
                    value = HostStore(client, 'fixture').get('missing')
                    assert value['found'] is False
                    self.active = False
                    client.title('a', 'done')
            # Assert not active in the test fixture
            def assert_not_active(self): assert not self.active, 'handler reentered during RPC wait'
        socket = Socket(); handler = Waiting()
        host = HostInfo.parse(dict(streamDockManager=dict(contract_version=1, capabilities=[STORAGE_PROTOCOL])))
        with patch.object(wire, 'connect_loopback', return_value=socket):
            connect(handler, 'ws://127.0.0.1:1234', 'fixture', host)
        self.assertEqual(handler.events, ['keyDown', 'keyUp'])
        self.assertTrue(socket.closed)

    # Verify shared host storage and portable settings
    def test_shared_host_storage_and_portable_settings(self):
        base = Client(identity='opaque-session-token')
        for metadata in FIXTURE['malformed_manager']:
            self.assertFalse(HostInfo.parse(dict(streamDockManager=metadata)).supports(STORAGE_PROTOCOL))
        value = FIXTURE['value']
        base.get_settings('owned-context'); base.settings('owned-context', value)
        base.get_global_settings(); base.global_settings(value)
        self.assertEqual(base.drain(), FIXTURE['base_requests'])
        with self.assertRaises(UnsupportedFeatureError): HostStore(base, 'authoring')
        # Prepare answer base in the host storage tests fixture
        def answer_base():
            request, = base.drain()
            self.assertEqual(request['event'], 'getSettings')
            self.assertFalse(base.control.observe(dict(event='didReceiveSettings', context='owned-context', payload=dict(settings=value))))
        base._flush_control = answer_base
        settings = PortableSettings(base, 'action', 'owned-context')
        self.assertEqual(settings.read(), dict(settings=value, backend='base-vsd', assurance='observed'))
        self.assertEqual(settings.replace(value)['assurance'], 'sent')
        base.drain()
        client = Client(HostInfo.parse(dict(streamDockManager=dict(contract_version=1, capabilities=[STORAGE_PROTOCOL]))))
        # Prepare answer in the host storage tests fixture
        def answer():
            request, = client.drain()
            self.assertEqual(request['protocol_id'], STORAGE_PROTOCOL)
            result = dict(generation=FIXTURE['generation'], revision='0')
            if request['method'] == 'get':
                self.assertEqual(request['payload'], dict(namespace='authoring', key=FIXTURE['key']))
                result.update(found=True, value=value)
            else:
                self.assertEqual(request['payload']['mutations'], [dict(op='put', key='null', value=None)])
                result.update(committed=True, revision='1', operation_id=FIXTURE['operation_id'])
            client.control.observe(dict(event=RPC_RESPONSE, protocol_id=STORAGE_PROTOCOL, request_id=request['request_id'], result=result))
        client._flush_control = answer
        store = HostStore(client, 'authoring')
        found = store.get(FIXTURE['key'])
        self.assertEqual(found['value'], value)
        self.assertEqual(found['generation'], FIXTURE['generation'])
        self.assertTrue(store.commit(found, FIXTURE['operation_id'], [dict(op='put', key='null', value=None)])['committed'])

    # Verify correlation errors never downgrade writes
    def test_correlation_errors_never_downgrade_writes(self):
        client = Client(HostInfo.parse(dict(streamDockManager=dict(contract_version=1, capabilities=[STORAGE_PROTOCOL, SETTINGS_PROTOCOL]))))
        # Report the pending host storage tests operation as failed
        def fail():
            request, = client.drain()
            client.control.observe(dict(event=RPC_RESPONSE, protocol_id=request['protocol_id'], request_id=request['request_id'],
                                        error=dict(code='storage_failure', message='storage unavailable')))
        client._flush_control = fail
        with self.assertRaises(RPCError) as caught: PortableSettings(client, 'global').replace(dict(secret='not logged'))
        self.assertEqual(caught.exception.code, 'storage_failure')
        self.assertEqual(client.drain(), [])
        # Prepare wrong protocol in the host storage tests fixture
        def wrong_protocol():
            request, = client.drain()
            client.control.observe(dict(event=RPC_RESPONSE, protocol_id='wrong', request_id=request['request_id'], result={}))
        client._flush_control = wrong_protocol
        with self.assertRaises(RPCError) as caught: HostStore(client, 'a').get('b')
        self.assertEqual(caught.exception.code, 'invalid_response')
        cancelled = threading.Event(); cancelled.set()
        with self.assertRaises(RPCError) as caught: HostStore(client, 'a').get('b', cancelled)
        self.assertEqual(caught.exception.code, 'cancelled_before_commit')
        self.assertEqual(client.drain(), [])

    # Verify explicit unsupported only falls back
    def test_explicit_unsupported_only_falls_back(self):
        client = Client(HostInfo.parse(dict(streamDockManager=dict(contract_version=1, capabilities=[SETTINGS_PROTOCOL]))), 'opaque')
        # Prepare unsupported in the host storage tests fixture
        def unsupported():
            request, = client.drain()
            client.control.observe(dict(event=RPC_RESPONSE, protocol_id=SETTINGS_PROTOCOL, request_id=request['request_id'], error=dict(code='unsupported', message='not executed')))
        client._flush_control = unsupported
        result = PortableSettings(client, 'global').replace(dict(unknown=None))
        self.assertEqual(result['assurance'], 'sent')
        self.assertEqual(client.drain(), [dict(event='setGlobalSettings', context='opaque', payload=dict(unknown=None))])

    # Verify shared malformed storage and base responses
    def test_shared_malformed_storage_and_base_responses(self):
        base = Client(identity='opaque')
        portable = PortableSettings(base, 'action', 'owned')
        for payload in FIXTURE['invalid_base_payloads']:
            # Prepare answer base in the host storage tests fixture
            def answer_base():
                base.drain()
                from stream_dock_manager_sdk.protocol import Event
                base.control.observe(Event.parse(json.dumps(dict(event='didReceiveSettings', context='owned', payload=payload))).raw)
            base._flush_control = answer_base
            with self.assertRaises(RPCError) as caught: portable.read()
            self.assertEqual(caught.exception.code, 'invalid_response')
        client = Client(HostInfo.parse(dict(streamDockManager=dict(contract_version=1, capabilities=[STORAGE_PROTOCOL]))))
        store = HostStore(client, 'fixture')
        # Prepare answer in the host storage tests fixture
        def answer(field, value):
            request, = client.drain()
            client.control.observe(dict(event=RPC_RESPONSE, protocol_id=STORAGE_PROTOCOL, request_id=request['request_id'], **{field:value}))
        for value in FIXTURE['invalid_rpc_errors']:
            client._flush_control = lambda: answer('error', value)
            with self.assertRaises(RPCError) as caught: store.get('key')
            self.assertEqual(caught.exception.code, 'invalid_response')
        for revision in FIXTURE['invalid_receipt_revisions']:
            client._flush_control = lambda: answer('result', dict(found=False, generation=FIXTURE['generation'], revision=revision, operation_id=FIXTURE['operation_id']))
            with self.assertRaises(RPCError) as caught: store.receipt(FIXTURE['generation'], FIXTURE['operation_id'])
            self.assertEqual(caught.exception.code, 'invalid_response')


if __name__ == '__main__': unittest.main()

import base64
import json
from pathlib import Path
import unittest

from stream_dock_manager_sdk.protocol import Event, HostInfo
from stream_dock_manager_sdk.runtime import Client, Handler, defer_action

FIXTURE = json.loads((Path(__file__).resolve().parents[2] / 'contracts/fixtures/protocol-v1.json').read_text())


class LoggingParityTests(unittest.TestCase):
    # Return client for logging parity tests
    def client(self):
        return Client(HostInfo(FIXTURE['manager']))

    def test_optional_network_demand(self):
        from copy import deepcopy
        demand = FIXTURE['network_demand']
        for enabled in (False, True):
            host = deepcopy(FIXTURE['manager'])
            if not enabled: host['streamDockManager']['capabilities'].remove(demand['capability'])
            client = Client(HostInfo(host))
            class Observer(Handler):
                calls = 0
                def handle(self, client, event): self.calls += 1
            handler = Observer()
            client._dispatch(handler, Event.parse(demand['request']))
            self.assertEqual(handler.calls, int(not enabled))
            frames = client.drain()
            self.assertEqual(len(frames), int(enabled))
            if enabled:
                self.assertEqual(frames[0]['event'], demand['response'])
                self.assertEqual(frames[0]['payload']['run_id'], 'fixture-run')

    # Verify logging completion and isolation
    def test_logging_completion_and_isolation(self):
        client = self.client()
        class Action(Handler):
            done = None
            # Check whether should log action
            def should_log_action(self, event): return event.event == 'keyUp'
            # Dispatch the incoming event through action
            def handle(self, client, event): self.done = defer_action()
        action = Action()
        client._dispatch(action, Event.parse(dict(event='keyUp', action='counter', context='key')))
        self.assertEqual(client.drain(), [])
        action.done(RuntimeError('failed'))
        action.done()
        values = client.drain()
        self.assertEqual(len(values), 1)
        self.assertEqual(values[0]['payload']['stream'], 'stderr')
        self.assertIn('failed or cancelled', values[0]['payload']['message'])
        for _ in range(200):
            client._automatic_log('stdout', 'flood')
        client.title('key', 'still responsive')
        values = client.drain()
        self.assertEqual(values[0]['event'], 'setTitle')
        self.assertTrue(values[1]['payload']['incomplete'])
        self.assertEqual(len(values), 130)
        client.close()

    # Verify logging network contract
    def test_logging_network_contract(self):
        client = self.client()
        case = FIXTURE['logging']
        request, response = case['request'], case['response']
        self.assertIsNone(client.network.diagnostics.start('GET', request['url']))
        client._dispatch(Handler(), Event.parse(case['preferences_event']))
        trace = client.network.diagnostics.start(request['method'], request['url'], request['headers'])
        trace.request.write(json.dumps(request['body']).encode())
        trace.request.complete = True
        trace.received(response['headers'])
        trace.response.write(json.dumps(response['body']).encode())
        trace.response.complete = True
        trace.finish(response['status'])
        trace.finish(response['status'])
        values = client.drain()
        self.assertEqual(len(values), 1)
        payload = values[0]['payload']
        self.assertEqual((payload['source'], payload['stream']), ('network', 'stdout'))
        for secret in case['forbidden']: self.assertNotIn(secret, json.dumps(values))
        self.assertEqual(json.loads(payload['network']['request']['text'])['value'], 'retained')
        self.assertTrue(payload['network']['response']['complete'])
        stale = client.network.diagnostics.start('GET', request['url'])
        client.set_network_debug(False)
        client.set_network_debug(True)
        stale.finish(200)
        self.assertEqual(client.drain(), [])
        trace = client.network.diagnostics.start('GET', request['url'])
        trace.response.write(b'x' * (case['limits']['body_bytes'] + 1))
        trace.finish(200)
        body = client.drain()[0]['payload']['network']['response']
        self.assertTrue(body['truncated'])
        self.assertEqual(body['text'], '')
        client.log_redactor = lambda text: text.replace('application-secret', '[redacted]')
        trace = client.network.diagnostics.start('GET', request['url'])
        trace.response.write(b'\xffapplication-secret\x00payload')
        trace.finish(200)
        body = client.drain()[0]['payload']['network']['response']
        decoded = base64.b64decode(body['text'])
        self.assertNotIn(b'application-secret', decoded)
        self.assertIn(b'payload', decoded)
        trace = client.network.diagnostics.start('GET', request['url'])
        trace.received({'Content-Type':'application/json'})
        trace.response.write(case['partial_json'].encode())
        trace.finish(200)
        body = client.drain()[0]['payload']['network']['response']
        self.assertEqual(body['text'], '')
        self.assertTrue(body['truncated'])
        replacement = self.client()
        replacement.set_network_debug(True)
        client.close()
        replacement.network.diagnostics.io('read', 1)
        self.assertEqual(len(replacement.drain()), 1)
        replacement.close()

    # Verify legacy log api uses byte bounded frames
    def test_legacy_log_api_uses_byte_bounded_frames(self):
        client = self.client()
        message = '界' * 12000
        client.log(message)
        frames = client.drain()
        self.assertEqual(''.join(value['payload']['message'] for value in frames), message)
        self.assertTrue(all(len(value['payload']['message'].encode()) <= 16384 for value in frames))
        client.log('a'+chr(0xd800)+'b')
        self.assertEqual(client.drain()[0]['payload']['message'], 'a�b')
        client.close()

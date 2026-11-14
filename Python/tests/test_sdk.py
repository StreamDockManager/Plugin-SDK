import io
import json
from pathlib import Path
import sys
import threading
import unittest

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / 'Python'))
from stream_dock_manager_sdk import (Client, Completion, Event, Handler, HostInfo, Measurement,
                           NetworkPayload, ProbePool, UnsupportedFeatureError, TelemetrySnapshot,
                           defer_action, frame_capabilities, parse_capabilities)

FIXTURE = json.loads((ROOT / 'contracts/fixtures/protocol-v1.json').read_text())


class SDKTests(unittest.TestCase):
    # Verify required telemetry observer
    def test_required_telemetry_observer(self):
        for case in FIXTURE['telemetry']:
            with self.subTest(case=case['name']):
                raw = dict(event='didReceiveTelemetry', payload=case['payload'])
                if case['valid']:
                    self.assertEqual(TelemetrySnapshot(case['payload']).audio_observer, case['payload']['audio_observer'])
                    self.assertEqual(Event.parse(raw).payload, case['payload'])
                else:
                    with self.assertRaises(ValueError): TelemetrySnapshot(case['payload'])
                    with self.assertRaises(ValueError): Event.parse(raw)

    # Verify vendor inspector payload is json value
    def test_vendor_inspector_payload_is_json_value(self):
        for payload in [None, [1, {'unknown': True}], 'text', False, 0]:
            event = Event.parse(dict(event='sendToPlugin', context='action', payload=payload))
            self.assertEqual(event.payload, payload)
            self.assertEqual(event.settings, {})
            self.assertFalse(event.pressed)

    # Verify shared explicit negotiation
    def test_shared_explicit_negotiation(self):
        for case in FIXTURE['negotiation']:
            with self.subTest(case=case['name']):
                host = HostInfo.parse(case['host'])
                self.assertEqual(host.supports('plugin-protocol-v1'), case['supported'])
                client = Client(host)
                client.send('setTitle', 'context', {'title':'base'})
                client.send('setFeedback', 'context', {'value':1})
                client.close()

    # Prepare client in the sdktests fixture
    def client(self, manager=True):
        client = Client(HostInfo.parse(FIXTURE['manager' if manager else 'legacy']), 'fixture-token')
        self.addCleanup(client.close)
        return client

    # Prepare messages in the sdktests fixture
    def messages(self, client):
        values = []
        while not client._out.empty():
            values.append(json.loads(client._out.get_nowait()))
        return values

    # Verify aggregate byte budget releases and isolates logs
    def test_aggregate_byte_budget_releases_and_isolates_logs(self):
        from stream_dock_manager_sdk.runtime import MAX_MESSAGE
        client = self.client()
        base = len(json.dumps(dict(event='setTitle', context='a', payload=dict(title='é')), separators=(',', ':')).encode())
        payload = dict(title='é' + 'x' * (MAX_MESSAGE - base))
        for _ in range(8): client.send('setTitle', 'a', payload)
        with self.assertRaises(RuntimeError): client.send('setTitle', 'a', {})
        client.log_stream('stdout', 'independent')
        client._out.get_nowait()
        client.send('setTitle', 'a', payload)
        client.drain()
        for _ in range(128):
            try: client.log_stream('stdout', 'x' * 16384)
            except RuntimeError: pass
        client.send('actionResult', 'a', dict(success=True))

    # Verify encoded message limit
    def test_encoded_message_limit(self):
        from stream_dock_manager_sdk.runtime import MAX_MESSAGE
        for manager in (True, False):
            client = self.client(manager)
            for prefix in ('', 'é😀', 'data:image/png;base64,'):
                base = len(json.dumps(dict(event='setImage', context='a', payload=dict(image=prefix)), separators=(',', ':')).encode())
                for delta in (-1, 0, 1):
                    image = prefix + 'x' * (MAX_MESSAGE - base + delta)
                    if delta > 0:
                        with self.assertRaisesRegex(ValueError, '1048576'): client.send('setImage', 'a', dict(image=image))
                    else:
                        client.send('setImage', 'a', dict(image=image))
                        self.assertEqual(len(client._out.get_nowait().encode()), MAX_MESSAGE + delta)

    # Verify shared protocol fields and feature gates
    def test_shared_protocol_fields_and_feature_gates(self):
        device, rotation = map(Event.parse, FIXTURE['events'])
        self.assertEqual(device.device_info['name'], 'Fixture')
        self.assertTrue(device.raw['future'])
        self.assertTrue(rotation.pressed)
        self.assertEqual(rotation.settings, {'unknown': {'retain': True}})
        for message, feature in FIXTURE['gates'].items():
            with self.assertRaises(UnsupportedFeatureError) as failure:
                self.client(False).send(message, 'binding', {})
            self.assertEqual(failure.exception.feature, feature)
            self.client().send(message, 'binding', {})
        client = self.client(False)
        client.display('binding', title='Fallback', state=1, layout='layout.json', feedback={'value': 1})
        self.assertEqual([m['event'] for m in self.messages(client)], ['setState', 'setTitle', 'setFeedbackLayout', 'setFeedback'])
        self.assertFalse(HostInfo.parse({'streamDockManager': {'contract_version': 2, 'capabilities': ['telemetry-v1']}}).supports('telemetry-v1'))

    # Verify invalid host and events rejected
    def test_invalid_host_and_events_rejected(self):
        for raw in ['[]', 'null', '"host"', '{']:
            with self.assertRaises(ValueError): HostInfo.parse(raw)
        for raw in ['[]', '{}', '{"event":4}', '{"event":"keyUp","payload":[]}']:
            with self.assertRaises(ValueError): Event.parse(raw)

    # Verify bounded queue and session close
    def test_bounded_queue_and_session_close(self):
        client = self.client()
        for _ in range(128): client.title('binding', 'queued')
        with self.assertRaisesRegex(RuntimeError, 'queue full'): client.title('binding', 'excess')
        self.messages(client)
        client.close()
        with self.assertRaisesRegex(RuntimeError, 'session closed'): client.title('binding', 'late')

    # Verify descriptor roundtrip limits and corruption
    def test_descriptor_roundtrip_limits_and_corruption(self):
        value = dict(version=1, id='org.fixture', extra={'preserved': True})
        frame = frame_capabilities(value)
        self.assertEqual(parse_capabilities(frame), value)
        for invalid in [frame[:-1], frame + 'x', frame.replace('000000', 'ffffff', 1), 'SDMCAP1:00000004:????:ENDSDMCAP1']:
            with self.assertRaises(ValueError): parse_capabilities(invalid)
        with self.assertRaises(ValueError): frame_capabilities({'huge': 'x' * (64 << 10)})

    # Verify measurement unavailable expired future and nonfinite
    def test_measurement_unavailable_expired_future_and_nonfinite(self):
        for value, observed, age, error, expected in [(1, 10, 5, None, True), (1, 9, 5, None, False), (1, 16, 5, None, False), (None, 10, 5, None, False), (float('nan'), 10, 5, None, False), (1, 10, 5, 'missing', False)]:
            self.assertEqual(Measurement(value, observed, age, error).fresh(15), expected)

    # Verify inspector replies include action identity
    def test_inspector_replies_include_action_identity(self):
        client = self.client(False)
        client.to_inspector('binding', {'value': 1}, action='org.fixture.action')
        self.assertEqual(self.messages(client)[0]['action'], 'org.fixture.action')
        with self.assertRaises(ValueError): client.to_inspector('binding', {}, action='')

    # Verify cleanup stops workers when completion output is full
    def test_cleanup_stops_workers_when_completion_output_is_full(self):
        client = self.client()
        class Deferred(Handler):
            # Dispatch the incoming event through deferred
            def handle(self, client, event): self.done = defer_action()
        handler = Deferred()
        client._dispatch(handler, Event.parse(dict(event='keyUp', context='binding', streamDockManager=dict(request_id='request-1'))))
        for _ in range(128): client.title('binding', 'queued')
        with self.assertRaisesRegex(RuntimeError, 'queue full'): client.close()
        self.assertTrue(client.stop.is_set())
        self.assertTrue(client._pool._stop.is_set())
        handler.done()

    # Verify partial payload accounting
    def test_partial_payload_accounting(self):
        counters = NetworkPayload()
        stream = counters.track(io.BytesIO(b'abc'))
        self.assertEqual(stream.read(2), b'ab')
        stream.write(b'd')
        self.assertEqual(counters.snapshot(), (1, 2))

    # Verify deferred completion waits for every operation and retains failure
    def test_deferred_completion_waits_for_every_operation_and_retains_failure(self):
        results = []
        completion = Completion(results.append)
        first, second = completion.defer(), completion.defer()
        completion.finish()
        first(ValueError('failed'))
        first()
        self.assertEqual(results, [])
        second()
        self.assertEqual(results, [False])
        completion.cancel()
        self.assertEqual(results, [False])

    # Verify desktop input correlates context and disappearance cancels
    def test_desktop_input_correlates_context_and_disappearance_cancels(self):
        client = self.client()
        class Input(Handler):
            # Dispatch the incoming event through input
            def handle(self, client, event):
                if event.event == 'keyUp':
                    client.desktop_input(event.context, dict(request_id='input-1', op='copy'))
        handler = Input()
        tracked = Event.parse(dict(event='keyUp', context='binding', streamDockManager=dict(request_id='action-1')))
        client._dispatch(handler, tracked)
        self.assertEqual([m['event'] for m in self.messages(client)], ['desktopInput'])
        client._dispatch(handler, Event.parse(dict(event='desktopInputResult', context='foreign', payload=dict(request_id='input-1'))))
        self.assertEqual(self.messages(client), [])
        client._dispatch(handler, Event.parse(dict(event='willDisappear', context='binding')))
        self.assertEqual(self.messages(client)[0]['payload'], dict(request_id='action-1', success=False))

    # Verify background apply runs on loop and failure completes action
    def test_background_apply_runs_on_loop_and_failure_completes_action(self):
        client = self.client()
        thread = threading.get_ident()
        ready = threading.Event()
        class Background(Handler):
            # Dispatch the incoming event through background
            def handle(self, client, event):
                # Execute the queued background operation
                def work(cancel):
                    ready.set()
                    return {'value': 2}
                # Apply the completed background update
                def apply(client, result):
                    self_thread = threading.get_ident()
                    if self_thread != thread: raise AssertionError('apply escaped event loop')
                    client.title(event.context, str(result['value']))
                client.background(event.context, work, apply)
        client._dispatch(Background(), Event.parse(dict(event='keyUp', context='binding', streamDockManager=dict(request_id='action-1'))))
        self.assertTrue(ready.wait(1))
        import time
        deadline = time.monotonic() + 1
        while client._results.empty() and time.monotonic() < deadline: time.sleep(.001)
        client._drain_results()
        values = self.messages(client)
        self.assertEqual([m['event'] for m in values], ['setTitle', 'actionResult'])
        self.assertTrue(values[-1]['payload']['success'])

    # Verify probe pool is bounded and cancels queued jobs
    def test_probe_pool_is_bounded_and_cancels_queued_jobs(self):
        pool = ProbePool()
        release = threading.Event()
        started = [threading.Event() for _ in range(4)]
        for event in started:
            self.assertTrue(pool.submit(lambda event=event: (event.set(), release.wait(2))))
            self.assertTrue(event.wait(1))
        for _ in range(32): self.assertTrue(pool.submit(lambda: None))
        self.assertFalse(pool.submit(lambda: None))
        release.set()
        pool.close()
        self.assertFalse(pool.submit(lambda: None))

    # Verify background failure and context removal do not apply stale results
    def test_background_failure_and_context_removal_do_not_apply_stale_results(self):
        import time
        client = self.client()
        class Failing(Handler):
            # Dispatch the incoming event through failing
            def handle(self, client, event):
                # Apply the completed failing update
                def apply(client, value): raise ValueError('apply failed')
                client.background(event.context, lambda cancel: 42, apply)
        client._dispatch(Failing(), Event.parse(dict(event='keyUp', context='a', streamDockManager=dict(request_id='a1'))))
        deadline = time.monotonic() + 2
        while client._results.empty() and time.monotonic() < deadline: time.sleep(.001)
        client._drain_results()
        self.assertEqual(self.messages(client)[0]['payload'], dict(request_id='a1', success=False))
        started, applied = threading.Event(), []
        # Execute the queued sdktests operation
        def work(cancel):
            started.set()
            cancel.wait(2)
            return 42
        cancel = client.background('a', work, lambda client, value: applied.append(value))
        self.assertTrue(started.wait(1))
        client._dispatch(Handler(), Event.parse(dict(event='willDisappear', context='a')))
        self.assertTrue(cancel.is_set())
        client._drain_results()
        self.assertEqual(applied, [])


if __name__ == '__main__': unittest.main()

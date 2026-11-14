from stream_dock_manager_sdk import Handler, TelemetrySnapshot, THEME_EVENT, run


class Counter(Handler):
    # Initialize counter state and dependencies
    def __init__(self):
        self.binding = ''
        print('SDK probe handler initialized', flush=True)
        import os
        if os.environ.get("STREAM_DOCK_MANAGER_SDK_PROBE_ASSETS_ONLY") == "1":
            from stream_dock_manager_sdk.authoring import display_layout
            if display_layout().get('id') != 'plugin-display': raise RuntimeError('Packaged SDK layout is missing')
            print('Packaged SDK resources passed', flush=True)
            raise SystemExit(0)
        if os.environ.get("STREAM_DOCK_MANAGER_SDK_PROBE_TRACE") == "1":
            # Test-only phase markers make registration failures diagnosable without
            # logging launch tokens, host metadata or protocol payloads
            import time, faulthandler
            faulthandler.dump_traceback_later(3)
            started = time.monotonic()
            print('SDK probe importing transport', flush=True)
            from stream_dock_manager_sdk import wire
            print(f'SDK probe transport imported in {time.monotonic()-started:.3f}s', flush=True)
            connect = wire.connect_loopback
            # Prepare traced connect in the counter fixture
            def traced_connect(*args, **kwargs):
                started = time.monotonic()
                print('SDK probe handshake starting', flush=True)
                socket = connect(*args, **kwargs)
                print(f'SDK probe handshake completed in {time.monotonic()-started:.3f}s', flush=True)
                send = socket.send
                # Return the plugin registration metadata
                def registration(value):
                    started = time.monotonic()
                    print('SDK probe registration write starting', flush=True)
                    result = send(value)
                    faulthandler.cancel_dump_traceback_later()
                    print(f'SDK probe registration written in {time.monotonic()-started:.3f}s', flush=True)
                    socket.send = send
                    return result
                socket.send = registration
                return socket
            wire.connect_loopback = traced_connect

    # Check whether should log action
    def should_log_action(self, event):
        from stream_dock_manager_sdk.runtime import should_log_action
        return event.event == 'keyUp' or should_log_action(event)

    # Dispatch the incoming event through counter
    def handle(self, client, event):
        phase = event.settings.get('storage_phase')
        if phase:
            if event.event == 'willAppear':
                self.binding = event.context
                client.title(event.context, 'storage ready')
            elif event.event == 'keyUp':
                storage_probe(client, event.context, phase)
                client.title(event.context, 'storage ' + phase + ' passed')
            return
        if event.event == 'keyDown' and event.settings.get('fixture_message_limit'):
            import json
            from stream_dock_manager_sdk.runtime import MAX_MESSAGE
            prefix = 'é😀'
            base = len(json.dumps(dict(event='setFeedback', context=event.context, payload=dict(boundary=prefix)), separators=(',', ':')).encode())
            for delta in (-1, 0, 1):
                payload = dict(boundary=prefix + 'x' * (MAX_MESSAGE - base + delta))
                try: client.send('setFeedback', event.context, payload)
                except ValueError as error:
                    if delta != 1 or '1048576' not in str(error): raise
                else:
                    if delta == 1: raise RuntimeError('missing local message-limit rejection')
            client.feedback(event.context, dict(boundary=None))
            client.title(event.context, 'message-limit:ok')
        elif event.event == 'keyDown' and event.settings.get('fixture_endpoint'):
            from stream_dock_manager_sdk.service import Service
            service = Service(network=client.network)
            try: service.do('GET', event.settings['fixture_endpoint'], {'Authorization':'Bearer fixture-private'})
            finally: service.close()
            client.log_stream('stdout', 'SDK network request finished')
            client.title(event.context, 'network:done')
        elif event.event == 'willAppear':
            client.log_redactor = lambda text: text.replace('fixture-private','[redacted]')
            client.log_stream('stdout','SDK stdout fixture-private')
            client.log_stream('stderr','SDK stderr fixture-private')
            from stream_dock_manager_sdk.authoring import display_layout
            if display_layout().get('id') != 'plugin-display': raise RuntimeError('Packaged SDK layout is missing')
            self.binding = event.context
            client.settings(event.context, event.settings)
            client.global_settings(dict(fixture='saved'))
            client.subscribe_telemetry(['cpu'])
        elif event.event == 'didReceiveGlobalSettings' and event.settings.get('fixture') == 'saved':
            client.title(self.binding, 'global:saved')
        elif event.event == THEME_EVENT and self.binding:
            client.title(self.binding, 'theme:' + event.payload.get('resolved', ''))
        elif event.event == 'didReceiveTelemetry':
            measurement = TelemetrySnapshot(event.payload).measurement('cpu')
            if measurement.fresh(): client.title(self.binding, f'telemetry:{measurement.value:g}')
        elif event.event == 'dialRotate': client.title(event.context, f"dial:{event.payload.get('ticks', 0)}")
        elif event.event == 'touchTap': client.title(event.context, 'touch')
        elif event.event == 'keyUp': client.desktop_input(event.context, dict(op='copy', request_id=event.surface.request_id + '-input'))
        elif event.event == 'willDisappear': self.binding = ''


# Exercise the host storage contract against the disposable host
def storage_probe(client, binding, phase):
    from stream_dock_manager_sdk.hoststorage import HostStore, PortableSettings, RPCError
    operation = '12345678-1234-4234-8234-123456789abc'
    expected = dict(unicode='é😀', unknown=dict(keep=True), false=False, zero=0, null=None, empty=[], object={})
    store = HostStore(client, 'sdk-storage-v1')
    current = store.get('value')
    if phase == 'write':
        assert not current['found'], 'new private value already exists'
        mutations = [dict(op='put', key='value', value=expected), dict(op='put', key='null', value=None)]
        committed = store.commit(current, operation, mutations)
        assert store.commit(current, operation, mutations)['revision'] == committed['revision'], 'idempotent commit mismatch'
        try: store.commit(current, '12345678-1234-4234-8234-123456789abd', mutations)
        except RPCError as error:
            assert error.code == 'conflict', 'stale revision reported wrong error'
        else: raise RuntimeError('stale revision accepted')
        for scope in ('action', 'global'):
            settings = PortableSettings(client, scope, binding)
            before = settings.read()['settings']
            settings.update(dict(sdk_storage_probe='saved'))
            after = settings.read()['settings']
            assert all(key in after and after[key] == value for key, value in before.items()), 'portable settings lost fields'
            assert after.get('sdk_storage_probe') == 'saved', 'portable update not observed'
    elif phase == 'delete':
        assert current['found'], 'private value missing before delete'
        store.commit(current, '12345678-1234-4234-8234-123456789abe', [dict(op='delete', key='value'), dict(op='delete', key='null')])
        assert not store.get('value')['found'], 'delete failed'
        return
    elif phase != 'read': raise RuntimeError('unknown storage phase')
    value = store.get('value')
    assert value['found'] and value['value'] == expected, 'private value did not round trip'
    null = store.get('null')
    assert null['found'] and null['value'] is None, 'null was treated as missing'
    assert not store.get('missing')['found'], 'missing value appeared'
    assert store.list_keys()['keys'] == ['null', 'value'], 'list keys mismatch'
    receipt = store.receipt(value['generation'], operation)
    assert receipt['found'] and receipt['committed'], 'durable receipt missing'


if __name__ == '__main__': run(Counter())

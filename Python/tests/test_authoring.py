"""Authoring behavior is checked against the same v1 fixture as Go and Rust."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
import time
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / 'Python'))
from stream_dock_manager_sdk import HostInfo
from stream_dock_manager_sdk.authoring import Action, Binding, Display, Plugin, ViewTarget, can_update, display_messages
from stream_dock_manager_sdk.settings import validate, merge
from stream_dock_manager_sdk.storage import Store, write, MAXIMUM_BYTES
from stream_dock_manager_sdk.polling import Resources, retry_delay, resolve_interval
from stream_dock_manager_sdk.testkit import Clock, Recorder
from stream_dock_manager_sdk.httpclient import extract_path, parse_path, redact

FIXTURE = json.loads((ROOT / 'contracts/fixtures/authoring-v1.json').read_text())
HOST = HostInfo.parse(json.loads((ROOT / 'contracts/fixtures/protocol-v1.json').read_text())['manager'])


# Prepare directory link in the authoring fixture
def directory_link(target, link):
    try:
        link.symlink_to(target, target_is_directory=True)
        print('directory alias fixture: symlink')
        return
    except OSError:
        if os.name != 'nt':
            raise
    # Both endpoints are fixture-owned. Avoid cmd expansion even in an unusual
    # user temp directory, and bound the native junction helper's lifetime
    if any(character in str(target) + str(link) for character in '%!&|<>^"()\r\n'):
        raise RuntimeError('junction fixture cannot safely quote this temporary directory')
    result = subprocess.run(['cmd.exe', '/D', '/V:OFF', '/C', 'mklink', '/J', str(link), str(target)],
                            capture_output=True, timeout=10)
    if result.returncode:
        raise RuntimeError('directory junction fixture failed: ' +
                           (result.stdout + result.stderr).decode(errors='replace'))
    print('directory alias fixture: Windows junction (symlink privilege unavailable)')


class AuthoringTests(unittest.TestCase):
    # Verify refresh policy and independent deadlines
    def test_refresh_policy_and_independent_deadlines(self):
        fixture = FIXTURE['refresh_policy']
        for case in fixture['cases']:
            if case['valid']: self.assertEqual(resolve_interval(case['settings'], fixture['policy']) * 1000, case['expected_ms'])
            else:
                with self.assertRaises(ValueError): resolve_interval(case['settings'], fixture['policy'])
        r = Resources()
        for name, interval in [('fast', 1), ('slow', 10), ('off', 0)]: r.watch(name, 'shared', interval)
        r.complete('shared', r.begin('shared'), 1, None, 100)
        r.complete('shared', r.begin('shared'), 2, None, 101)
        self.assertEqual([r.snapshot(name).last_value for name in ['fast','slow','off']], [2,1,1])
        r.unwatch('fast'); r.unwatch('slow')
        self.assertEqual(r.due(3700), [])
        r.states['shared'].next = 0
        r.complete('shared', r.begin('shared'), 3, None, 3700)
        self.assertEqual(r.snapshot('off').last_value, 3)
        r.states['shared'].next = 0
        r.watch('off', 'shared', 0)
        self.assertEqual(r.due(3700), ['shared'])
        r.complete('shared', r.begin('shared'), None, ValueError('cooldown'), 3700, 0, 7300)
        r.states['shared'].next = 0
        r.watch('new', 'shared', 1)
        self.assertEqual(r.due(3700), [])
        self.assertEqual(r.deadline('shared'), 7300)
        r.complete('shared', r.begin('shared'), 4, None, 7300, 0, 7360)
        r.states['shared'].next = 0
        self.assertEqual(r.due(7300), [])



    # Verify shared missing state paths
    def test_shared_missing_state_paths(self):
        for case in FIXTURE['state_reads']['missing_paths']:
            with self.subTest(case=case['name']), tempfile.TemporaryDirectory() as root:
                blocker = Path(root) / 'blocked'; blocker.write_text('keep')
                path = Path(root) / case['path']; store = Store(path)
                if case['error']:
                    with self.assertRaises(OSError): store.load()
                    # Model Windows reporting a blocked ancestor as FileNotFound
                    with patch.object(Path, 'open', side_effect=FileNotFoundError()):
                        store._load_blocked = True
                        with self.assertRaises(OSError): store.load()
                    store.change(dict(pending=True))
                    with self.assertRaisesRegex(ValueError, 'protected'): store.close()
                else:
                    self.assertIsNone(store.load()); store.change(dict(saved=True)); store.close()
                self.assertEqual(blocker.read_text(), 'keep')

    # Verify missing state symlinks
    def test_missing_state_symlinks(self):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root); link = root / 'link'; target = root / 'target'
            target.mkdir(); directory_link(target, link); target.rmdir()
            for path in (link, link / 'state.json'):
                with self.assertRaises(OSError): Store(path).load()
            # Removing a directory junction uses rmdir; unlink removes symlinks
            if os.name == 'nt' and not link.is_symlink(): link.rmdir()
            else: link.unlink()
            target.mkdir(); directory_link(target, link)
            store = Store(link / 'state.json'); self.assertIsNone(store.load())
            store.change(dict(saved=True)); store.close()
            self.assertEqual(Store(target / 'state.json').load(), dict(saved=True))

    # Verify shared state writes serialize once and preserve failed write
    def test_shared_state_writes_serialize_once_and_preserve_failed_write(self):
        for repetition in range(5):
            with tempfile.TemporaryDirectory() as temporary:
                for case in FIXTURE['state_writes']['cases']:
                    with self.subTest(repetition=repetition, case=case['name']):
                        path = Path(temporary) / 'state.json'
                        store = Store(path, case['version'])
                        store.change(case['value'])
                        value = dict(version=case['version'], data=case['value']) if case['version'] else case['value']
                        expected = json.dumps(value, separators=(',', ':'), ensure_ascii=False, allow_nan=False).encode()
                        with patch('stream_dock_manager_sdk.storage.json.dumps', wraps=json.dumps) as encode:
                            store.close()
                            self.assertEqual(encode.call_count, 1)
                        self.assertEqual(path.read_bytes(), expected)
                        self.assertEqual(Store(path, case['version']).load(), case['value'])
                        store.change(dict(changed=True))
                        with patch('stream_dock_manager_sdk.storage.os.replace', side_effect=OSError('fixture write failure')):
                            with self.assertRaisesRegex(OSError, 'fixture write failure'): store.close()
                        self.assertTrue(store.dirty)
                        self.assertEqual(path.read_bytes(), expected)
                        store.close()
                        self.assertEqual(Store(path, case['version']).load(), dict(changed=True))
                        self.assertEqual(list(Path(temporary).glob('.state-*')), [])

    # Verify shared state read limits
    def test_shared_state_read_limits(self):
        contract = FIXTURE['state_reads']
        self.assertEqual(MAXIMUM_BYTES, contract['maximum_bytes'])
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / 'state.json'
            for case in contract['cases']:
                with self.subTest(case=case['name']):
                    value = contract['payload']
                    if case['version']: value = dict(version=case['version'], data=value)
                    raw = json.dumps(value, ensure_ascii=False).encode()
                    size = MAXIMUM_BYTES + case['extra_bytes']
                    with path.open('wb') as stream:
                        stream.write(raw)
                        remaining = size - len(raw)
                        while remaining:
                            chunk = min(64 << 10, remaining)
                            stream.write(b' ' * chunk)
                            remaining -= chunk
                    store = Store(path, case['version'])
                    store.value = dict(previous=True)
                    if case['valid']:
                        self.assertEqual(store.load(), contract['payload'])
                    else:
                        with self.assertRaisesRegex(ValueError, 'exceeds'):
                            store.load()
                        self.assertEqual(store.value, dict(previous=True))
                        self.assertFalse(store.dirty)
                    self.assertEqual(path.stat().st_size, size)
                    if not case['valid']:
                        store.change(dict(replacement=True))
                        with self.assertRaisesRegex(ValueError, 'saved state is protected'): store.close()
                        self.assertTrue(store.dirty)
                        self.assertEqual(path.stat().st_size, size)
                        backup = path.with_suffix('.saved')
                        if case['name'] == 'large_file': path.rename(backup)
                        else: path.write_bytes(raw)
                        restored = store.load()
                        if case['name'] != 'large_file': self.assertEqual(restored, contract['payload'])
                        store.change(contract['payload'])
                        store.close()
                        self.assertEqual(Store(path, case['version']).load(), contract['payload'])
                        if case['name'] == 'large_file': self.assertEqual(backup.stat().st_size, size)

    # Verify feedback numeric precision
    def test_feedback_numeric_precision(self):
        for a, b in [(9007199254740993, 9007199254740992.0), (100000000000000016384, 1.0000000000000002e20), (1.0, 1.0000000000000002), (True, 1)]:
            with self.subTest(a=a, b=b):
                before, after = Display(feedback={'value': a}), Display(feedback={'value': b})
                self.assertEqual(len(display_messages('a', before, after)), 1)

    # Verify rejects obsolete state without overwrite
    def test_rejects_obsolete_state_without_overwrite(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / 'state.json'
            for raw in ['{"count":1}', '{"version":0,"data":{}}', '{"version":2,"data":{}}', '{"version":1}', '{']:
                path.write_text(raw)
                store = Store(path)
                store.value = dict(previous=True)
                with self.assertRaises(ValueError): store.load()
                self.assertEqual(store.value, dict(previous=True))
                store.change(dict(changed=True))
                with self.assertRaises(ValueError): store.close()
                self.assertEqual(path.read_text(), raw)

    # Verify state version boundaries preserve files
    def test_state_version_boundaries_preserve_files(self):
        for version in [0, 2, -1, 2**32, True, 1.5]:
            for exists in [False, True]:
                with self.subTest(version=version, exists=exists), tempfile.TemporaryDirectory() as temporary:
                    path = Path(temporary) / 'state.json'
                    raw = '{"version":1,"data":{"count":7}}'
                    if exists: path.write_text(raw)
                    # Check authoring tests data
                    def check():
                        if exists: self.assertEqual(path.read_text(), raw)
                        else: self.assertFalse(path.exists())
                    with self.assertRaises(ValueError): Store(path, version)
                    check()
                    for dirty in [False, True]:
                        for force in [False, True]:
                            store = Store(path, clock=lambda: 0)
                            store.version = version
                            if dirty: store.change(dict(count=8))
                            with self.assertRaises(ValueError): store.flush(force=force)
                            self.assertEqual(store.dirty, dirty)
                            with self.assertRaises(ValueError): store.close()
                            check()
                    store = Store(path)
                    store.version = version
                    store.value = dict(count=99)
                    with self.assertRaises(ValueError): store.load()
                    self.assertEqual(store.value, dict(count=99))
                    store.version = 1
                    store.change(dict(count=7))
                    with self.assertRaisesRegex(ValueError, 'protected'): store.close()
                    check()
                    store.load()
                    store.change(dict(count=7))
                    store.close()
                    self.assertEqual(Store(path).load(), dict(count=7))

    # Verify store rejects invalid versions without overwrite
    def test_store_rejects_invalid_versions_without_overwrite(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / 'state.json'
            for version in [True, -1, 2**32, 1.5]:
                with self.subTest(version=version):
                    raw = json.dumps(dict(version=version, data={}))
                    path.write_text(raw)
                    store = Store(path)
                    with self.assertRaises(ValueError): store.load()
                    store.change(dict(replacement=True))
                    with self.assertRaises(ValueError): store.close()
                    self.assertEqual(path.read_text(), raw)

    # Verify store rejects nonfinite json without modifying saved state
    def test_store_rejects_nonfinite_json_without_modifying_saved_state(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / 'state.json'
            for number in ['NaN', 'Infinity', '-Infinity', '1e400']:
                with self.subTest(number=number):
                    raw = '{"version":1,"data":{"value":' + number + '}}'
                    path.write_text(raw)
                    store = Store(path)
                    with self.assertRaises(ValueError):
                        store.load()
                    self.assertIsNone(store.value)
                    self.assertFalse(store.dirty)
                    self.assertEqual(path.read_text(), raw)

    # Verify shared fields and unknown keys
    def test_shared_fields_and_unknown_keys(self):
        for case in FIXTURE['settings']:
            if case['valid']:
                value = validate(FIXTURE['fields'], case['input'])
                self.assertEqual(value, case['expected'])
                value['opaque']['retain'].append(3)
                self.assertEqual(case['input']['opaque']['retain'], [1, 2])
            else:
                with self.assertRaises(ValueError): validate(FIXTURE['fields'], case['input'])
        for schema in FIXTURE['invalid_schemas']:
            with self.assertRaises(ValueError): validate(schema, {})
        for invalid in (float('nan'), float('inf')):
            with self.assertRaises(ValueError): validate(FIXTURE['fields'], dict(count=invalid))

    # Verify shared display paths redaction and retries
    def test_shared_display_paths_redaction_and_retries(self):
        for case in FIXTURE['displays']:
            previous = Display(**case['previous']) if case['previous'] else None
            self.assertEqual(display_messages('a', previous, Display(**case['desired']), case['rich']), case['expected'], case['name'])
        for case in FIXTURE['retries']:
            self.assertEqual(round(retry_delay(case['interval_ms'] / 1000, case['failures'], case['jitter']) * 1000), case['expected_ms'])
        for case in FIXTURE['paths']:
            self.assertEqual(extract_path(case['root'], case['path']), (case['value'], case['found']))
        for path in FIXTURE['invalid_paths']:
            with self.assertRaises(ValueError): parse_path(path)
        case = FIXTURE['redaction']
        self.assertEqual(redact(case['input'], case['secrets']), case['expected'])

    # Verify private debounced store rejection and failed write
    def test_private_debounced_store_rejection_and_failed_write(self):
        clock = Clock()
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / 'state.json'
            store = Store(path, clock=clock.now)
            store.change(dict(count=1))
            self.assertFalse(store.flush())
            self.assertFalse(path.exists())
            clock.advance(.25)
            self.assertTrue(store.flush())
            self.assertEqual(Store(path).load(), dict(count=1))
            write(path, dict(legacy=True))
            with self.assertRaises(ValueError): Store(path).load()
            rejected = Store(path, clock=clock.now)
            with self.assertRaises(ValueError): rejected.load()
            rejected.change({})
            with self.assertRaises(ValueError): rejected.close()
            self.assertEqual(json.loads(path.read_text()), dict(legacy=True))
            blocked = Store(path / 'child', clock=clock.now)
            blocked.change({})
            with self.assertRaises(OSError): blocked.close()
            self.assertTrue(blocked.dirty)

    # Verify resources share fetches and reject retired generations
    def test_resources_share_fetches_and_reject_retired_generations(self):
        resources = Resources()
        resources.watch('a', 'resource', 1)
        resources.watch('b', 'resource', 2)
        self.assertEqual(resources.due(0), ['resource'])
        generation = resources.begin('resource')
        self.assertEqual(resources.due(0), [])
        resources.unwatch('a'); resources.unwatch('b')
        resources.watch('b', 'resource', 1)
        replacement = resources.begin('resource')
        self.assertNotEqual(generation, replacement)
        self.assertFalse(resources.complete('resource', generation, 42, None, 0))
        self.assertTrue(resources.complete('resource', replacement, 42, None, 0))
        again = resources.begin('resource')
        resources.complete('resource', again, None, RuntimeError('offline'), 1)
        self.assertTrue(resources.snapshot('b').stale)
        self.assertEqual(resources.snapshot('b').last_value, 42)

    # Verify view relationships and settings scope
    def test_view_relationships_and_settings_scope(self):
        bindings = {'a': Binding('start', dict(shared_id='one')), 'copy': Binding('start', dict(shared_id='two')), 'view': Binding('status', dict(shared_id='one'))}
        targets = {'status': ViewTarget('status', ('start',), shared=True), 'start': ViewTarget('start', live=False)}
        self.assertTrue(can_update(bindings, targets, {'a'}, 'view', 'a', 'start'))
        self.assertFalse(can_update(bindings, targets, {'copy'}, 'view', 'copy', 'start'))
        self.assertFalse(can_update(bindings, targets, {'a'}, 'view', 'missing', 'start'))
        original = dict(opaque={'retain': True})
        value = merge(original, dict(count=1)); value['opaque']['retain'] = False
        self.assertTrue(original['opaque']['retain'])

    # Verify real dispatch and background failure cleanup
    def test_real_dispatch_and_background_failure_cleanup(self):
        recorder, plugin = Recorder(HOST), Plugin()
        self.addCleanup(recorder.close); self.addCleanup(plugin.close)
        applied, started = [], threading.Event()
        # Dispatch the incoming event through authoring tests
        def handle(plugin, client, event):
            if event.event != 'keyUp': return
            # Execute the queued authoring tests operation
            def work(cancel): started.set(); return 42
            # Apply the completed authoring tests update
            def apply(plugin, client, value): applied.append(value); raise ValueError('application rejected')
            plugin.background(client, 'job', event.context, work, apply)
        plugin.register('org.fixture.action', Action(handle=handle, render=lambda *_: Display(title='Ready')))
        recorder.replay(plugin, [dict(event='willAppear', context='a', action='org.fixture.action'), dict(event='keyUp', context='a', action='org.fixture.action', streamDockManager=dict(request_id='request-1'))])
        self.assertTrue(started.wait(1))
        deadline = time.monotonic() + 2
        while not applied and time.monotonic() < deadline: recorder.poll(plugin); time.sleep(.001)
        self.assertEqual(applied, [42])
        self.assertEqual(plugin.jobs, {})
        receipts = [m for m in recorder.drain() if m['event'] == 'actionResult']
        self.assertEqual(receipts[-1]['payload'], dict(request_id='request-1', success=False))
        recorder.replay(plugin, [dict(event='willDisappear', context='a')])
        self.assertEqual(plugin.bindings, {})

    # Verify touch phase theme and fake clock store
    def test_touch_phase_theme_and_fake_clock_store(self):
        clock, recorder = Clock(), Recorder(HOST)
        plugin = Plugin(clock=clock.now)
        self.addCleanup(recorder.close); self.addCleanup(plugin.close)
        for phase, active in [('start', False), ('hold', False), ('end', True)]:
            recorder.replay(plugin, [dict(event='touchTap', context='a', action='org.fixture.action', streamDockManager=dict(touch=dict(phase=phase)))])
            self.assertEqual('a' in plugin.active, active)
        plugin.renderer.last['a'] = Display(title='literal-dark.svg', image='icon-dark.svg')
        from stream_dock_manager_sdk.protocol import Event
        plugin.handle(recorder.client, Event.parse(dict(event='didReceivePluginTheme', payload=dict(resolved='light'))))
        self.assertEqual(plugin.renderer.last['a'].title, 'literal-dark.svg')
        self.assertEqual(plugin.renderer.last['a'].image, 'icon-light.svg')
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary)/'state.json'
            plugin.store = Store(path, clock=clock.now)
            plugin.store.change(dict(count=1)); recorder.poll(plugin)
            self.assertFalse(path.exists()); clock.advance(.25); recorder.poll(plugin)
            self.assertTrue(path.exists())
        plugin.store = None


    # Verify appearance lifecycle incarnations
    def test_appearance_lifecycle_incarnations(self):
        spec = FIXTURE['appearance_lifecycle']
        recorder, plugin = Recorder(HOST), Plugin()
        self.addCleanup(recorder.close); self.addCleanup(plugin.close)
        # Render authoring tests data
        def render(plugin, context, binding):
            self.assertEqual(binding.settings, spec['settings'])
            return Display(title='Ready')
        plugin.register(spec['action'], Action(render=render))
        for step in spec['steps']:
            recorder.replay(plugin, [step['event']])
            self.assertEqual(sorted(plugin.bindings), step['bindings'])
            titles = [m['context'] for m in recorder.drain() if m['event'] == 'setTitle']
            self.assertEqual(titles, [] if step['render_context'] is None else [step['render_context']])


if __name__ == '__main__': unittest.main()

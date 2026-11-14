import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch
from concurrent.futures import ThreadPoolExecutor
ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / 'Python'))
from stream_dock_manager_sdk.polling import retry_after
from stream_dock_manager_sdk.settings import project
from stream_dock_manager_sdk import _atomicfile, storage, buildcache

class SharedPrimitives(unittest.TestCase):
    # Verify retry after contract
    def test_retry_after_contract(self):
        fixture = json.loads((ROOT / 'contracts/fixtures/retry-after-v1.json').read_text())
        now = fixture['now_ms'] / 1000
        for case in fixture['cases']:
            with self.subTest(case=case['name']):
                self.assertEqual(retry_after(case['value'], now), None if case['delay_ms'] is None else now + case['delay_ms'] / 1000)
    # Verify projection isolation
    def test_projection_isolation(self):
        global_settings = {'shared': {'v': [1]}, 'global': [2]}
        action = {'shared': {'v': [3]}, 'missing': True, 'local': [4]}
        value = project(global_settings, action, ('shared', 'missing'))
        self.assertNotIn('missing', value)
        self.assertEqual(value['shared'], {'v': [1]})
        value['shared']['v'].append(9); value['global'].append(9); value['local'].append(9)
        self.assertEqual(global_settings, {'shared': {'v': [1]}, 'global': [2]})
        self.assertEqual(action['local'], [4])
        self.assertEqual(project(global_settings, action)['shared'], action['shared'])
    # Verify connector binding and inspector projection
    def test_connector_binding_and_inspector_projection(self):
        from stream_dock_manager_sdk.connectors import Connector, Options
        from stream_dock_manager_sdk.authoring import Binding
        from types import SimpleNamespace
        connector = Connector(object(), Options(('status',), 10, shared_keys=('shared', 'missing')))
        connector.global_settings = {'shared': {'v': [1]}, 'global': [2]}
        connector.bindings['binding'] = Binding('test.status', {'shared': {'v': [3]}, 'missing': True})
        binding = connector.binding_settings('binding')
        self.assertEqual(binding['_action'], 'status')
        self.assertEqual(binding['shared'], {'v': [1]})
        self.assertNotIn('missing', binding)
        binding['shared']['v'].append(8)
        self.assertEqual(connector.global_settings['shared'], {'v': [1]})
        captured = []
        connector.enqueue = lambda *args: captured.append(args[4])
        for scope in ('action', 'plugin'):
            draft = {'shared': {'v': [3]}, 'missing': True, 'local': [4]}
            event = SimpleNamespace(context='inspector', action='status', payload={'op':'discover', 'scope':scope, 'settings':draft})
            connector._inspect(connector, None, event)
            value = captured.pop()
            self.assertEqual(value['shared'], {'v': [3 if scope == 'plugin' else 1]})
            self.assertEqual('missing' in value, scope == 'plugin')
            value['shared']['v'].append(8); value['global'].append(8); value['local'].append(8)
            self.assertEqual(connector.global_settings, {'shared': {'v': [1]}, 'global': [2]})
            self.assertEqual(draft, {'shared': {'v': [3]}, 'missing': True, 'local': [4]})

    # Verify atomic policies cleanup and concurrency
    def test_atomic_policies_cleanup_and_concurrency(self):
        with tempfile.TemporaryDirectory() as root:
            path = Path(root) / 'value'
            with patch.object(_atomicfile.os, 'fsync', wraps=_atomicfile.os.fsync) as sync:
                storage.write(path, {'a': 1}); self.assertEqual(sync.call_count, 1)
                buildcache.remember(path, 'key'); self.assertEqual(sync.call_count, 1)
            prior = path.read_bytes()
            with patch.object(_atomicfile.os, 'replace', side_effect=OSError('replace failed')):
                with self.assertRaises(OSError): storage.write(path, {'a': 2})
            self.assertEqual(path.read_bytes(), prior)
            self.assertEqual(sorted(p.name for p in Path(root).iterdir()), ['value', 'value.build.json'])
            with ThreadPoolExecutor(max_workers=8) as pool:
                list(pool.map(lambda i: storage.write(path, {'v': i, 'text': str(i) * 10000}), range(32)))
                list(pool.map(lambda i: buildcache.remember(path, str(i)), range(32)))
            value = json.loads(path.read_bytes()); self.assertEqual(value['text'], str(value['v']) * 10000)
            self.assertTrue(buildcache.cached(path, json.loads(Path(str(path) + '.build.json').read_text())['input']))
            self.assertEqual(len(list(Path(root).iterdir())), 2)


    # Verify transient Windows replacement errors use the same atomic temporary file
    def test_atomic_windows_retry_preserves_target_until_success(self):
        with tempfile.TemporaryDirectory() as root:
            target = Path(root) / 'value'
            target.write_bytes(b'old')
            original_replace = _atomicfile.os.replace
            sources = []
            codes = iter((5, 32, 33))

            def contention(source, path):
                sources.append(source)
                self.assertEqual(target.read_bytes(), b'old')
                self.assertEqual(Path(source).read_bytes(), b'new')
                code = next(codes, None)
                if code is not None:
                    error = PermissionError('temporary contention')
                    error.winerror = code
                    raise error
                original_replace(source, path)

            with (patch.object(_atomicfile, '_WINDOWS', True),
                  patch.object(_atomicfile.os, 'replace', side_effect=contention),
                  patch.object(_atomicfile.time, 'sleep') as sleep):
                _atomicfile.write(target, b'new', durable=True, prefix='.state-')
            self.assertEqual(len(sources), 4)
            self.assertEqual(len(set(sources)), 1)
            self.assertEqual([call.args[0] for call in sleep.call_args_list],
                             list(_atomicfile._REPLACE_RETRY_DELAYS[:3]))
            self.assertEqual(target.read_bytes(), b'new')
            self.assertEqual(list(Path(root).iterdir()), [target])

    # Verify permanent or unrelated replacement errors preserve the original and clean up
    def test_atomic_windows_retry_is_bounded_and_error_specific(self):
        with tempfile.TemporaryDirectory() as root:
            target = Path(root) / 'value'
            target.write_bytes(b'old')
            for windows, code, retries in (
                (True, 5, len(_atomicfile._REPLACE_RETRY_DELAYS)),
                (True, 65, 0),
                (False, 5, 0),
            ):
                with self.subTest(windows=windows, code=code):
                    error = PermissionError('permanent failure')
                    error.winerror = code
                    with (patch.object(_atomicfile, '_WINDOWS', windows),
                          patch.object(_atomicfile.os, 'replace', side_effect=error) as replace,
                          patch.object(_atomicfile.time, 'sleep') as sleep):
                        with self.assertRaises(PermissionError) as raised:
                            _atomicfile.write(target, b'new', durable=False, prefix='.cache-')
                    self.assertIs(raised.exception, error)
                    self.assertEqual(replace.call_count, retries + 1)
                    self.assertEqual(sleep.call_count, retries)
                    self.assertEqual(target.read_bytes(), b'old')
                    self.assertEqual(list(Path(root).iterdir()), [target])

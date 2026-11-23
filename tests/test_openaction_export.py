"""OpenAction manifest export regression cases (no host execution)."""
import sys
from pathlib import Path
import unittest
from unittest.mock import patch
from types import SimpleNamespace
sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'examples/tools'))
from openaction import export_manifest, export_universal_script_manifest


class OpenActionExportTests(unittest.TestCase):
    # Verify native opendeck single platform rejected before build
    def test_native_opendeck_single_platform_rejected_before_build(self):
        import package
        layout = SimpleNamespace(source=Path('.'), kind='go', manifest={'UUID': 'org.test.plugin'}, common=Path('.'))
        with patch.object(package, 'package_context', return_value=layout), patch.object(package, 'prepare') as prepare:
            with self.assertRaisesRegex(ValueError, 'one universal package'):
                package.package(kind='go', host='opendeck')
            prepare.assert_not_called()

    # Verify universal script declares all systems without source mutation
    def test_universal_script_declares_all_systems_without_source_mutation(self):
        source = {'CodePath': 'main.mjs', 'Icon': 'icon.png'}
        result = export_universal_script_manifest(source, {'icon.png': b'image'})
        self.assertEqual([item['Platform'] for item in result['OS']], ['mac', 'windows', 'linux'])
        self.assertNotIn('OS', source)
        self.assertEqual(result['Icon'], 'icon')

    # Verify universal script rejects limited or native payloads
    def test_universal_script_rejects_limited_or_native_payloads(self):
        for source, contents in [
            ({'CodePath': 'main.mjs', 'OS': [{'Platform': 'linux'}]}, {}),
            ({'CodePath': 'main.mjs', 'CodePathWin': 'windows.mjs'}, {}),
            ({'CodePath': 'bin/plugin'}, {}),
            ({'CodePath': 'main.mjs'}, {'addon.node': b'native'}),
        ]:
            with self.subTest(source=source, contents=contents), self.assertRaises(ValueError):
                export_universal_script_manifest(source, contents)

    # Verify node os and extensionless assets without source mutation
    def test_node_os_and_extensionless_assets_without_source_mutation(self):
        source = {'CodePath': 'main.mjs', 'Icon': 'icon.png', 'CategoryIcon': 'icon.png',
                  'StreamDockManager': {'RequiredCapabilities': ['plugin-storage-v1']},
                  'Actions': [{'Icon': 'icon.png', 'States': [{'Image': 'icons/even.svg'}]}]}
        result = export_manifest(source, {'icon.png': b'', 'icons/even.svg': b''}, 'mac')
        self.assertEqual(result['OS'], [{'Platform': 'mac'}])
        self.assertEqual(result['Icon'], 'icon')
        self.assertEqual(result['Actions'][0]['States'][0]['Image'], 'icons/even')
        self.assertEqual(result['CodePath'], 'main.mjs')
        self.assertEqual(result['StreamDockManager'], source['StreamDockManager'])
        self.assertNotIn('OS', source)
        self.assertEqual(source['Icon'], 'icon.png')

    def test_vsd_controller_names_map_to_openaction(self):
        source = {'Actions': [{'Controllers': ['Keypad', 'Knob', 'Encoder', 'SecondaryScreen']},
                              {'Controllers': ['Information'], 'Encoder': {'layout': 'custom.json'}},
                              {'Controllers': ['Encoder']}, {}]}
        actions = export_manifest(source, {}, 'mac')['Actions']
        self.assertEqual(actions, [{'Controllers': ['Keypad', 'Encoder'], 'Encoder': {}},
                                   {'Controllers': ['Encoder'], 'Encoder': {'layout': 'custom.json'}},
                                   {'Controllers': ['Encoder'], 'Encoder': {}}, {}])
        self.assertIn('Knob', source['Actions'][0]['Controllers'])
        self.assertNotIn('Encoder', source['Actions'][0])

    # Verify preserves asset precedence with verified alias
    def test_preserves_asset_precedence_with_verified_alias(self):
        source = {'Icon': 'icon.png'}
        contents = {'icon.png': b'raster', 'icon.svg': b'vector'}
        with self.assertRaises(ValueError):
            export_manifest(source, contents, 'linux')
        result = export_manifest(source, contents, 'linux', materialize_aliases=True)
        self.assertTrue(result['Icon'].startswith('openaction-assets/'))
        alias = result['Icon'] + '.png'
        self.assertEqual(contents[alias], b'raster')
        self.assertEqual(export_manifest(source, contents, 'linux'), result)
        contents[alias] = b'tampered'
        with self.assertRaises(ValueError):
            export_manifest(source, contents, 'linux')

    # Verify alias rejects new precedence collision
    def test_alias_rejects_new_precedence_collision(self):
        source = {'Icon': 'icon.png'}
        contents = {'icon.png': b'raster', 'icon.svg': b'vector'}
        result = export_manifest(source, contents, 'linux', materialize_aliases=True)
        contents[result['Icon'] + '.svg'] = b'wrong'
        with self.assertRaises(ValueError):
            export_manifest(source, contents, 'linux')

    # Verify rejects missing and traversal assets
    def test_rejects_missing_and_traversal_assets(self):
        for value in ('missing.png', '../icon.png', 'icon.jpg'):
            with self.subTest(value=value), self.assertRaises(ValueError):
                export_manifest({'Icon': value}, {'icon.png': b''}, 'windows')

    # Verify declared platform is not expanded
    def test_declared_platform_is_not_expanded(self):
        with self.assertRaises(ValueError):
            export_manifest({'OS': [{'Platform': 'windows'}]}, {}, 'mac')

    # Verify platform constraints preserved
    def test_platform_constraints_preserved(self):
        source = {'OS': [{'Platform': 'mac', 'MinimumVersion': '12'}]}
        self.assertEqual(export_manifest(source, {}, 'mac')['OS'], source['OS'])

    # Verify default state sentinel preserved
    def test_default_state_sentinel_preserved(self):
        source = {'Actions': [{'States': [{'Image': 'actionDefaultImage'}]}]}
        self.assertEqual(export_manifest(source, {}, 'linux')['Actions'], source['Actions'])


if __name__ == '__main__':
    unittest.main()

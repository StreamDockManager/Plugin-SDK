import json
from pathlib import Path
import sys
import tempfile
import unittest
sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "tools"))
from profiles import generate_profiles, models, package_profiles, validate_profiles
ROOT = Path(__file__).resolve().parents[1]
class Profiles(unittest.TestCase):
    def test_every_starter_covers_each_model_without_foreign_actions(self):
        for path in sorted(ROOT.glob('sdk-example-*/manifest.json')):
            manifest = json.loads(path.read_text())
            with self.subTest(starter=path.parent.name):
                files = generate_profiles(manifest)
                self.assertEqual(len(files), len(models()))
                self.assertEqual(files, generate_profiles(manifest))
                validate_profiles(files, manifest)
                for data in files.values():
                    document = json.loads(data)
                    self.assertEqual(document['schema_version'], 1)
                    self.assertEqual(document['configuration_revision'], 0)
                    self.assertEqual(document['saved_macros'], [])
                    self.assertEqual(document['rules'], [])
                    self.assertEqual(document['history'], {'undo': [], 'redo': []})
                    self.assertEqual(document['active_profile'], document['profiles'][0]['id'])
                    actions = [binding['action'] for page in document['profiles'][0]['pages'] for binding in page['bindings'].values()]
                    self.assertEqual({action['action_id'] for action in actions if action['kind'] == 'plugin'}, {action['UUID'] for action in manifest['Actions']})
    def test_multi_page_navigation_and_incompatible_controls(self):
        manifest = {'UUID':'org.test.profiles','Name':'Profiles','Actions':[{'UUID':f'org.test.profiles.action{i}','Name':f'Action {i}'} for i in range(45)]}
        files = generate_profiles(manifest)
        validate_profiles(files, manifest)
        document = json.loads(files['profiles/n4-pro.json'])
        self.assertGreater(len(document['profiles'][0]['pages']), 1)
        leaf = dict(kind='plugin',plugin_id=manifest['UUID'],action_id=manifest['Actions'][0]['UUID'],settings={})
        document['profiles'][0]['pages'][0]['bindings']['dial-1'] = dict(title='Invalid',action=dict(kind='dial',actions=[leaf, leaf, leaf]))
        with self.assertRaisesRegex(ValueError, 'incompatible'):
            validate_profiles({'profiles/n4-pro.json':json.dumps(document).encode()},manifest)

    def test_missing_required_workspace_fields_are_rejected(self):
        manifest = json.loads((ROOT / 'sdk-example-nodejs/manifest.json').read_text())
        name = 'profiles/n4-pro.json'
        data = generate_profiles(manifest)[name]
        for field in ('schema_version', 'configuration_revision', 'saved_macros', 'rules', 'history', 'active_profile', 'profiles'):
            with self.subTest(field=field):
                document = json.loads(data)
                del document[field]
                with self.assertRaisesRegex(ValueError, 'missing required field ' + field):
                    validate_profiles({name: json.dumps(document).encode()}, manifest)

    def test_wrong_typed_workspace_fields_are_rejected(self):
        manifest = json.loads((ROOT / 'sdk-example-nodejs/manifest.json').read_text())
        name = 'profiles/n4-pro.json'
        data = generate_profiles(manifest)[name]
        invalid = {
            'schema_version': [True, 1.0, 2, None],
            'configuration_revision': [True, 0.0, -1, 2**64, None],
            'saved_macros': [None, {}],
            'rules': [None, {}],
            'profiles': [None, {}],
            'active_profile': [None, 1],
            'history': [None, [], {}, {'undo': []}, {'redo': []}, {'undo': None, 'redo': []}, {'undo': [], 'redo': [], 'obsolete': []}],
        }
        for field, values in invalid.items():
            for value in values:
                with self.subTest(field=field, value=value):
                    document = json.loads(data)
                    document[field] = value
                    with self.assertRaises(ValueError):
                        validate_profiles({name: json.dumps(document).encode()}, manifest)

    def test_encoder_suggestions_use_explicit_independent_plugin_lanes(self):
        manifest = json.loads((ROOT / 'sdk-example-nodejs/manifest.json').read_text())
        action_id = manifest['Actions'][0]['UUID']
        manifest['StreamDockManager']['Capabilities']['actions'][action_id]['control_types'] = ['encoder']
        files = generate_profiles(manifest)
        validate_profiles(files, manifest)
        for model in models():
            document = json.loads(files[f'profiles/{model["model_id"]}.json'])
            assignments = [binding['action'] for page in document['profiles'][0]['pages'] for binding in page['bindings'].values()]
            if not any(control['kind'] == 'encoder' for control in model['controls']):
                self.assertEqual(assignments, [])
                self.assertEqual(document['unavailable_actions'], [action_id])
                continue
            self.assertEqual(len(assignments), 1)
            self.assertEqual(assignments[0], {'kind': 'dial', 'actions': [dict(kind='plugin', plugin_id=manifest['UUID'], action_id=action_id, settings={}) for _ in range(3)]})

    def test_invalid_encoder_and_nested_actions_are_rejected(self):
        manifest = json.loads((ROOT / 'sdk-example-nodejs/manifest.json').read_text())
        action_id = manifest['Actions'][0]['UUID']
        manifest['StreamDockManager']['Capabilities']['actions'][action_id]['control_types'] = ['encoder']
        name = 'profiles/n4-pro.json'
        source = generate_profiles(manifest)[name]
        for mutation in ('shared', 'short', 'extra', 'unknown', 'foreign', 'button', 'nested-unsupported', 'missing-kind', 'invalid-lane'):
            with self.subTest(mutation=mutation):
                document = json.loads(source)
                bindings = document['profiles'][0]['pages'][0]['bindings']
                control = next(iter(bindings))
                binding = bindings[control]
                action = binding['action']
                if mutation == 'shared': binding['action'] = action['actions'][0]
                elif mutation == 'short': action['actions'].pop()
                elif mutation == 'extra': action['actions'].append(action['actions'][0])
                elif mutation == 'unknown': action['actions'][0]['kind'] = 'obsolete'
                elif mutation == 'foreign': action['actions'][1]['plugin_id'] = 'org.foreign.plugin'
                elif mutation == 'button': bindings['key-1'] = bindings.pop(control)
                elif mutation == 'nested-unsupported': action['actions'][0] = {'kind': 'obsolete_container', 'actions': [], 'selected': 0}
                elif mutation == 'missing-kind': del action['actions'][0]['kind']
                elif mutation == 'invalid-lane': action['actions'][0] = None
                with self.assertRaises(ValueError):
                    validate_profiles({name: json.dumps(document).encode()}, manifest)

    def test_packaging_preserves_authored_profile_and_rejects_incomplete_data(self):
        manifest = json.loads((ROOT / 'sdk-example-nodejs/manifest.json').read_text())
        name = 'profiles/n4-pro.json'
        document = json.loads(generate_profiles(manifest)[name])
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / name
            path.parent.mkdir()
            source = json.dumps(document, separators=(',', ':')).encode()
            path.write_bytes(source)
            self.assertEqual(package_profiles(directory, manifest), {name: source})
            self.assertEqual(path.read_bytes(), source)
            del document['saved_macros']
            source = json.dumps(document).encode()
            path.write_bytes(source)
            with self.assertRaisesRegex(ValueError, 'missing required field saved_macros'):
                package_profiles(directory, manifest)
            self.assertEqual(path.read_bytes(), source)

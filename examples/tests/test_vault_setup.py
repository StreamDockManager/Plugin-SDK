import copy
from pathlib import Path
import sys
import unittest

sys.path.insert(0, str(Path(__file__).parents[1] / 'tools'))
from vault_setup import canonical_permissions, validate_setup_slots


class VaultSetupTests(unittest.TestCase):
    def candidate(self):
        manifest = {
            'Actions': [{'UUID': 'org.test.one'}, {'UUID': 'org.test.two'}],
            'StreamDockManager': {'Permissions': {'version': 1, 'actions': {
                'org.test.one': {'purpose': 'Authorize account reads.', 'optional': [
                    {'id': 'vault.read-secret', 'field': 'api_key', 'entry_kind': 'api-key', 'purpose': 'Authorize account reads.'}
                ]}
            }}, 'SetupSlots': {'version': 1, 'slots': [
                {'id': 'username', 'kind': 'value', 'entry_kind': 'text', 'mode': 'direct',
                 'required': True, 'purpose': 'Read the configured player name.', 'field': 'username',
                 'action': 'org.test.one'},
                {'id': 'api_key', 'kind': 'secret', 'entry_kind': 'api-key', 'mode': 'direct',
                 'required': False, 'purpose': 'Authorize account reads.', 'field': 'api_key',
                 'action': 'org.test.one'},
            ]}},
        }

        ext = manifest['StreamDockManager']
        ext['Capabilities'] = {'permissions': copy.deepcopy(ext['Permissions'])}
        return manifest

    def test_weather_and_webhook_destinations_match_manager(self):
        from vault_setup import _canonical_scope
        base = {'id':'network.https', 'recipe':'weather-forecast', 'data_class':'location.coordinates'}
        with self.assertRaises(ValueError):
            _canonical_scope(dict(base, origin='https://api.open-meteo.com'))
        webhook = {'id':'network.http','recipe':'manager-webhook','data_class':'example.ping'}
        for origin in ('http://127.0.0.2:8080','http://[::1]:8080','http://localhost'):
            _canonical_scope(dict(webhook,origin=origin))
        for origin in ('http://0.0.0.0','http://localhost/a','http://localhost:080','http://127.0.0.1:65536'):
            with self.assertRaises(ValueError):
                _canonical_scope(dict(webhook,origin=origin))
        _canonical_scope(dict(webhook,id='network.https',origin='https://127.0.0.1:39481'))

    def test_legacy_and_named_direct_slots(self):
        validate_setup_slots({'Actions': []})
        data = self.candidate()
        slots = data['StreamDockManager']['SetupSlots']['slots']
        slots.append(dict(slots[0], action='org.test.two'))
        validate_setup_slots(data)

    def test_rejects_unreviewed_authority_and_noncanonical_types(self):
        for patch in [
            {'value': 'sentinel'}, {'entry_ref': 'object-id'}, {'fields': []}, {'mode': 'broker'},
            {'action': 'org.test.foreign'}, {'required': 1}, {'purpose': '\nname'},
            {'entry_kind': 'api-key'}, {'id': '東京'}, {'field': 'x' * 129},
        ]:
            with self.subTest(patch=patch), self.assertRaises(ValueError):
                data = self.candidate()
                data['StreamDockManager']['SetupSlots']['slots'][0].update(patch)
                validate_setup_slots(data)

    def test_rejects_duplicate_and_cross_action_type_conflict(self):
        data = self.candidate()
        slots = data['StreamDockManager']['SetupSlots']['slots']
        slots.append(copy.deepcopy(slots[0]))
        with self.assertRaises(ValueError):
            validate_setup_slots(data)
        slots[-1].update(action='org.test.two', field='alternate')
        with self.assertRaises(ValueError):
            validate_setup_slots(data)

    def test_rejects_wrong_version(self):
        data = self.candidate()
        data['StreamDockManager']['SetupSlots']['version'] = True
        with self.assertRaises(ValueError):
            validate_setup_slots(data)

    def test_location_requires_exact_direct_scope(self):
        data = self.candidate()
        ext = data['StreamDockManager']
        slot = dict(id='place', kind='location', entry_kind='location', mode='direct', required=False,
                    purpose='Read saved coordinates.', field='place', action='org.test.one')
        ext['SetupSlots']['slots'] = [slot]
        with self.assertRaises(ValueError):
            validate_setup_slots(data)
        group = ext['Permissions']['actions']['org.test.one']
        group['optional'].append(dict(id='vault.read-location', field='place', entry_kind='location', purpose=slot['purpose']))
        ext['Capabilities']['permissions'] = ext['Permissions']
        validate_setup_slots(data)
        for key, value in [('origin','https://example.test'),('mode','broker'),('purpose','Other purpose')]:
            with self.subTest(key=key), self.assertRaises(ValueError):
                ext['SetupSlots']['slots'] = [dict(slot, **{key:value})]
                validate_setup_slots(data)

    def test_permission_scopes_match_registry_and_canonicalize_default_ports(self):
        data = self.candidate()
        extension = data['StreamDockManager']
        group = extension['Permissions']['actions']['org.test.one']
        scope = {'id': 'network.https', 'origin': 'https://api.example.test',
                 'recipe': 'clipboard-summary', 'data_class': 'clipboard.text'}
        group['required'] = [scope, dict(scope, origin='HTTPS://API.EXAMPLE.TEST:443')]
        self.assertEqual(len(canonical_permissions(data)['org.test.one']['required']), 1)
        for origin in ('http://api.example.test', 'https://api.example.test/',
                       'https://api.example.test:444', 'https://user@api.example.test'):
            with self.subTest(origin=origin), self.assertRaises(ValueError):
                group['required'] = [dict(scope, origin=origin)]
                canonical_permissions(data)
        group['required'] = [scope]
        group['optional'].append(scope)
        with self.assertRaises(ValueError):
            canonical_permissions(data)

    def test_direct_secret_requires_matching_descriptor_and_matching_slot(self):
        data = self.candidate()
        ext = data['StreamDockManager']
        ext['Capabilities']['permissions']['actions'].clear()
        with self.assertRaises(ValueError):
            validate_setup_slots(data)
        del ext['Permissions'], ext['Capabilities']['permissions']
        with self.assertRaises(ValueError):
            validate_setup_slots(data)

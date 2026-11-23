import json
from pathlib import Path
import pickle
import threading
import unittest
from dataclasses import FrozenInstanceError

from stream_dock_manager_sdk import Client, HostInfo, UnsupportedFeatureError, TEXT_REFERENCES_PROTOCOL
from stream_dock_manager_sdk.hoststorage import RPCError, RPC_RESPONSE
from stream_dock_manager_sdk.vault import Vault, VaultTextValue, VaultSecretValue, VAULT_VALUES_PROTOCOL, VAULT_SECRET_SHARING_PROTOCOL, _decode_text


FIXTURE = json.loads((Path(__file__).resolve().parents[2] / 'contracts/fixtures/vault-slots-v1.json').read_text())


class VaultTests(unittest.TestCase):
    def test_permission_scopes_survive_registration_framing(self):
        from stream_dock_manager_sdk import frame_capabilities, parse_capabilities
        descriptor = {'permissions': FIXTURE['permissions']}
        self.assertEqual(parse_capabilities(frame_capabilities(descriptor)), descriptor)

    def test_shared_location(self):
        f = FIXTURE['location']
        client, sent = self.client(f['result'])
        loc = Vault(client).read_location(**f['selector'])
        self.assertEqual((loc.latitude, loc.longitude, loc.revision), (12.34567, 0, '3'))
        self.assertNotIn('12.34567', repr(loc))
        self.assertEqual((sent[0]['protocol_id'], sent[0]['method'], sent[0]['payload']), (f['protocol_id'], f['method'], f['selector']))
        for bad in f['invalid_coordinates']:
            client, _ = self.client(dict(f['result'], value=bad))
            with self.assertRaises(RPCError):
                Vault(client).read_location(**f['selector'])

    def test_shared_wire_fixture(self):
        client, sent = self.client(FIXTURE['result'])
        key = FIXTURE['selector']
        result = Vault(client).read_value(key['slot'], key['field'], key['action'])
        self.assertEqual(result.value, FIXTURE['result']['value'])
        actual, = sent
        self.assertEqual(actual['event'], FIXTURE['request_event'])
        self.assertEqual(actual['protocol_id'], FIXTURE['methods'][0]['protocol_id'])
        self.assertEqual(actual['method'], FIXTURE['methods'][0]['method'])
        self.assertEqual(actual['payload'], key)

    def test_format_text_skips_literals_and_validates_reference_rpc(self):
        legacy = Client(HostInfo.parse({}))
        self.assertEqual(legacy.format_text('', 'plain'), 'plain')
        self.assertEqual(legacy.format_text('', '} then ${unfinished'), '} then ${unfinished')
        self.assertEqual(list(legacy.drain()), [])
        with self.assertRaises(UnsupportedFeatureError):
            legacy.format_text('example.action', '${missing}')
        with self.assertRaises(RPCError):
            legacy.format_text('', 'é' * 32769)
        client, sent = self.client({'text': 'Hello '})
        self.assertEqual(client.format_text('example.action', 'Hello ${missing}'), 'Hello ')
        self.assertEqual((sent[0]['protocol_id'], sent[0]['method'], sent[0]['payload']),
                         (TEXT_REFERENCES_PROTOCOL, 'format-text',
                          {'action': 'example.action', 'text': 'Hello ${missing}'}))
        for bad in (None, {'text': None}, {'text': 'x', 'extra': 1}, {'text': 'é' * 131073}):
            client, _ = self.client(bad)
            with self.assertRaises(RPCError) as error:
                client.format_text('example.action', '${missing}')
            self.assertEqual(error.exception.code, 'invalid_response')

    def client(self, response=None, error=None):
        client = Client(HostInfo.parse(dict(streamDockManager=dict(
            contract_version=1, capabilities=[VAULT_VALUES_PROTOCOL, VAULT_SECRET_SHARING_PROTOCOL, TEXT_REFERENCES_PROTOCOL]))))
        sent = []

        def answer():
            request, = client.drain()
            sent.append(request)
            message = dict(event=RPC_RESPONSE, protocol_id=request['protocol_id'],
                           request_id=request['request_id'])
            if error is not None:
                message['error'] = error
            else:
                message['result'] = response
            client.control.observe(message)
        client._flush_control = answer
        return client, sent

    def test_exact_authenticated_rpc_and_redacted_typed_value(self):
        client, sent = self.client(dict(entry_kind='text', value='private-test-value', revision='18446744073709551615'))
        result = Vault(client).read_value('slot-1', 'field.name', 'org.example.action')
        self.assertIsInstance(result, VaultTextValue)
        self.assertEqual(result.value, 'private-test-value')
        self.assertEqual(result.entry_kind, 'text')
        self.assertEqual(result.revision, '18446744073709551615')
        self.assertNotIn(result.value, repr(result))
        with self.assertRaises(FrozenInstanceError):
            result.value = 'changed'
        self.assertEqual(len(sent), 1)
        self.assertEqual(sent[0]['protocol_id'], VAULT_VALUES_PROTOCOL)
        self.assertEqual(sent[0]['method'], 'read-value')
        self.assertEqual(sent[0]['payload'], dict(slot='slot-1', field='field.name', action='org.example.action'))

    def test_unadvertised_capability_has_no_fallback(self):
        client = Client()
        with self.assertRaises(UnsupportedFeatureError): Vault(client)
        self.assertEqual(client.drain(), [])
        client, sent = self.client()
        vault = Vault(client)
        client.host = HostInfo.parse({})
        with self.assertRaises(UnsupportedFeatureError): vault.read_value('slot', 'field', 'action')
        self.assertEqual(sent, [])

    def test_identifiers_rejected_before_send(self):
        client, sent = self.client()
        vault = Vault(client)
        for slot, field, action in [('', 'f', 'a'), ('x' * 129, 'f', 'a'), ('s', 'x' * 129, 'a'),
                                    ('s', 'f', 'x' * 257), ('é', 'f', 'a'), ('s', 'f', 'has space'),
                                    ('s', 'f', 'a\n'), ('s', 'f', None)]:
            with self.subTest(slot=slot, field=field, action=action):
                with self.assertRaises(ValueError): vault.read_value(slot, field, action)
        self.assertEqual(sent, [])

    def test_strict_response_and_bounds(self):
        valid = dict(entry_kind='text', value='', revision='0')
        for response in [None, [], {}, dict(valid, extra=True), dict(valid, entry_kind='credential'),
                         dict(valid, value=123), dict(valid, value='x' * 65537),
                         dict(valid, value='é' * 32769),
                         *[dict(valid, revision=revision) for revision in
                           ('', '01', '-1', '+1', '1.0', ' 1', '١', '18446744073709551616', '1' * 100, 1)]]:
            client, sent = self.client(response)
            with self.assertRaises(RPCError) as caught:
                Vault(client).read_value('s', 'f', 'a')
            self.assertEqual(caught.exception.code, 'invalid_response')
            self.assertNotIn('private-test-value', str(caught.exception))
            self.assertEqual(len(sent), 1)
        for value in ('', 'x' * 65536, 'é' * 32768):
            client, _ = self.client(dict(valid, value=value))
            self.assertEqual(Vault(client).read_value('s', 'f', 'a').value, value)

    def test_host_denials_and_cancellation_not_retried(self):
        for code in ('permission_denied', 'vault_locked', 'needs_setup', 'not_declared'):
            client, sent = self.client(error=dict(code=code, message='Vault read rejected'))
            with self.assertRaises(RPCError) as caught:
                Vault(client).read_value('s', 'f', 'a')
            self.assertEqual(caught.exception.code, code)
            self.assertEqual(len(sent), 1)
        client, sent = self.client()
        cancel = threading.Event()
        cancel.set()
        with self.assertRaises(RPCError) as caught:
            Vault(client).read_value('s', 'f', 'a', cancel=cancel)
        self.assertEqual(caught.exception.code, 'cancelled_before_commit')
        self.assertEqual(sent, [])

    def test_invalid_unicode_response_is_rejected(self):
        with self.assertRaises(RPCError):
            _decode_text(dict(entry_kind='text', value='\ud800', revision='1'))

    def test_secret_opt_in_redaction_and_exact_rpc(self):
        client, sent = self.client(dict(entry_kind='api-key', value='fixture-secret', revision='2'))
        result = Vault(client).read_secret('slot', 'field', 'action')
        self.assertIsInstance(result, VaultSecretValue)
        self.assertEqual(result.reveal(), 'fixture-secret')
        self.assertEqual(result.entry_kind, 'api-key')
        self.assertEqual(result.revision, '2')
        self.assertNotIn('fixture-secret', str(result))
        self.assertNotIn('fixture-secret', repr(result))
        with self.assertRaises(TypeError): json.dumps(result)
        with self.assertRaises(TypeError): pickle.dumps(result)
        self.assertEqual(sent[0]['protocol_id'], VAULT_SECRET_SHARING_PROTOCOL)
        self.assertEqual(sent[0]['method'], 'read-secret')
        self.assertEqual(sent[0]['payload'], dict(slot='slot', field='field', action='action'))
        client.host = HostInfo.parse(dict(streamDockManager=dict(contract_version=1, capabilities=[VAULT_VALUES_PROTOCOL])))
        with self.assertRaises(UnsupportedFeatureError): Vault(client).read_secret('slot', 'field', 'action')
        self.assertEqual(len(sent), 1)

    def test_secret_kind_and_validation_do_not_downgrade(self):
        for response in [dict(entry_kind='text', value='fixture', revision='1'),
                         dict(entry_kind='api-key', value='fixture', revision='01'),
                         dict(entry_kind='api-key', value='fixture', revision='1', extra=True),
                         dict(entry_kind='api-key', value='x' * 65537, revision='1')]:
            client, sent = self.client(response)
            with self.assertRaises(RPCError): Vault(client).read_secret('s', 'f', 'a')
            self.assertEqual(len(sent), 1)
            self.assertEqual(sent[0]['method'], 'read-secret')
        client, sent = self.client()
        with self.assertRaises(ValueError): Vault(client).read_secret('', 'f', 'a')
        self.assertEqual(sent, [])

    def test_secret_kinds_and_malicious_host_errors_are_safe(self):
        for kind in ('api-key', 'bearer-token', 'credential'):
            client, _ = self.client(dict(entry_kind=kind, value='fixture-secret', revision='1'))
            self.assertEqual(Vault(client).read_secret('s', 'f', 'a').entry_kind, kind)
        client, _ = self.client(dict(entry_kind='oauth-refresh-token', value='fixture-secret', revision='1'))
        with self.assertRaises(RPCError) as caught:
            Vault(client).read_secret('s', 'f', 'a')
        self.assertEqual(caught.exception.code, 'invalid_response')
        for code in ('permission_denied', 'fixture-secret'):
            for method in ('read_value', 'read_secret'):
                client, _ = self.client(error=dict(code=code, message='fixture-secret private host detail'))
                with self.assertRaises(RPCError) as caught:
                    getattr(Vault(client), method)('s', 'f', 'a')
                self.assertNotIn('fixture-secret', str(caught.exception))
                self.assertNotIn('fixture-secret', caught.exception.code)
                self.assertEqual(caught.exception.code, code if code == 'permission_denied' else 'operation_unavailable')

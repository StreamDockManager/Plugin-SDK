import json
from pathlib import Path
import unittest
from stream_dock_manager_sdk import Client, HostInfo, Permissions, Vault, UnsupportedFeatureError

FIXTURE = json.loads((Path(__file__).resolve().parents[2] / 'contracts/fixtures/permissions-v1.json').read_text(encoding='utf-8'))


class PermissionServiceTests(unittest.TestCase):
    def test_permission_service_contract(self):
        f = FIXTURE
        client = Client(HostInfo.parse(json.dumps({'streamDockManager': {'contract_version': 1, 'capabilities': [f['permissions'], f['broker'], f['writes'],f['references']]}})), 'opaque')
        services = Permissions(client)
        token, scope, action = f['activation_token'], f['scope'], f['action']
        for protocol, method, payload, result, call in [
            (f['permissions'], 'get-permissions', {}, {'revision': '7', 'scopes': []}, services.get_permissions),
            (f['permissions'], 'has-permission', {'action': action, 'scope': scope}, {'revision': '7', 'available': True, 'granted': False}, lambda: services.has_permission(action, scope)),
            (f['permissions'], 'request-permissions', {'activation_token': token, 'scopes': [scope]}, {'state': 'pending', 'revision': '7', 'review_id': f['review_id']}, lambda: services.request_permissions(token, [scope])),
            (f['broker'], 'manager-webhook', {'activation_token': token, 'certificate': f['webhook']['certificate']}, {'state': 'pending', 'result_handle': f['result_handle']}, lambda: services.send_example_webhook(token, f['webhook']['certificate'])),
            (f['broker'], 'manager-local-webhook', {'activation_token': token, 'origin': f['webhook']['origin']}, {'state': 'pending', 'result_handle': f['result_handle']}, lambda: services.send_local_webhook(token, f['webhook']['origin'])),
            (f['broker'], 'clipboard-summary', {'activation_token': token}, {'state': 'pending', 'result_handle': f['result_handle']}, lambda: services.summarize_clipboard(token)),
            (f['permissions'],'read-clipboard-text',{'activation_token':token},{'revision':'7','text':f['text']},lambda:services.read_clipboard_text(token)),
            (f['permissions'],'show-text-result',{'activation_token':token,'text':f['text']},{'result_handle':f['result_handle'],'state':'pending'},lambda:services.show_text_result(token,f['text'])),
            (f['permissions'],'notify-completion',{'activation_token':token},{'state':'completed'},lambda:services.notify_completion(token)),
            (f['writes'],'write-secret',f['write'],f['receipt'],lambda:Vault(client).write_secret(f['write']['slot'],f['write']['field'],action,f['write']['entry_kind'],f['write']['value'],'3')),
            (f['references'],'list-vault-entries',{'action':action,'scope':f['browse'],'start':0},f['page'],lambda:services.list_vault_entries(action,f['browse'])),
            (f['references'],'select-vault-entry',{'action':action,'scope':f['use'],'activation_token':token},{'state':'pending','review_id':f['review_id'],'revision':'7'},lambda:services.select_vault_entry(token,action,f['use'])),
            (f['broker'],'clipboard-summary',{'scope':f['use'],'entry_ref':f['receipt']['entry_ref'],'activation_token':token},{'state':'pending','result_handle':f['result_handle']},lambda:services.run_vault_recipe(token,f['use'],f['receipt']['entry_ref'])),
        ]:
            def respond():
                req, = client.drain()
                self.assertEqual((req['protocol_id'], req['method'], req['payload']), (protocol, method, payload))
                client.control.observe({'event': 'streamDockManagerRpcResponse', 'request_id': req['request_id'], 'protocol_id': protocol, 'result': result})
            client._flush_control = respond
            self.assertEqual(call(), result)
        with self.assertRaises(UnsupportedFeatureError):
            Permissions(Client(HostInfo.parse('{}'), 'opaque')).summarize_clipboard(token)

    def test_typed_permission_notices_and_safe_errors(self):
        from stream_dock_manager_sdk import Event, permission_notice
        from stream_dock_manager_sdk.hoststorage import RPCError
        from unittest.mock import Mock
        for key in ('terminal_event', 'changed_event', 'selection_event'):
            expected = FIXTURE[key]
            notice = permission_notice(Event.parse(expected))
            self.assertEqual(notice.event, expected['event'])
            for field, value in expected.items():
                self.assertEqual(getattr(notice, field), tuple(value) if field == 'scopes' else value)
        client = Mock()
        client.rpc.side_effect = RPCError('activation_expired', 'private fixture contents')
        with self.assertRaises(RPCError) as failure:
            Permissions(client).summarize_clipboard('token')
        self.assertEqual(failure.exception.code, 'activation_expired')
        self.assertNotIn('private fixture contents', str(failure.exception))

    def test_plugin_notification_contract(self):
        from stream_dock_manager_sdk.hoststorage import RPCError
        f = FIXTURE
        msg = f['notification']
        legacy = Client(HostInfo.parse(json.dumps({'streamDockManager': {'contract_version': 1, 'capabilities': [f['permissions']]}})), 'opaque')
        with self.assertRaises(UnsupportedFeatureError):
            Permissions(legacy).create_notification(**msg)
        self.assertEqual(legacy.drain(), [])
        client = Client(HostInfo.parse(json.dumps({'streamDockManager': {'contract_version': 1, 'capabilities': [f['permissions'], f['notifications']]}})), 'opaque')
        service = Permissions(client)
        with self.assertRaises(RPCError) as failure:
            service.create_notification(msg['action'], msg['level'], 'é' * 257)
        self.assertEqual(failure.exception.code, 'too_large')
        self.assertEqual(client.drain(), [])
        for response, code in [({'result': f['notification_receipt']}, None), ({'result': {'state': 'unexpected'}}, 'invalid_response'), ({'error': {'code': 'permission_denied', 'message': 'private fixture content'}}, 'permission_denied')]:
            def respond():
                req, = client.drain()
                self.assertEqual((req['protocol_id'], req['method'], req['payload']), (f['permissions'], 'create-notification', msg))
                client.control.observe({'event': 'streamDockManagerRpcResponse', 'request_id': req['request_id'], 'protocol_id': f['permissions'], **response})
            client._flush_control = respond
            if code is None:
                self.assertIsNone(service.create_notification(**msg))
            else:
                with self.assertRaises(RPCError) as failure:
                    service.create_notification(**msg)
                self.assertEqual(failure.exception.code, code)
                self.assertNotIn('private fixture content', str(failure.exception))

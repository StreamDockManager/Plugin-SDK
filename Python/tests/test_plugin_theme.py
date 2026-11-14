import json
from pathlib import Path
import unittest

from stream_dock_manager_sdk import Client, HostInfo, THEME_EVENT, THEME_PROTOCOL, UnsupportedFeatureError
from stream_dock_manager_sdk.protocol import Event


class PluginThemeTest(unittest.TestCase):
    # Verify plugin theme rpc
    def test_plugin_theme_rpc(self):
        fixture = json.loads((Path(__file__).parents[2] / 'contracts/fixtures/protocol-v1.json').read_text())
        legacy = Client(HostInfo.parse(fixture['manager']))
        self.addCleanup(legacy.close)
        with self.assertRaises(UnsupportedFeatureError):
            legacy.get_plugin_theme()
        spec = fixture['theme_rpc']
        client = Client(HostInfo.parse(spec['host']))
        self.addCleanup(client.close)

        # Flush plugin theme test data
        def flush():
            request, = client.drain()
            self.assertEqual(request['protocol_id'], THEME_PROTOCOL)
            self.assertEqual(request['method'], 'get')
            self.assertEqual(request['payload'], {})
            self.assertTrue(client.control.observe(dict(event='streamDockManagerRpcResponse',
                protocol_id=THEME_PROTOCOL, request_id=request['request_id'], result=spec['response'])))

        client._flush_control = flush
        self.assertEqual(client.get_plugin_theme()['theme']['surface'], '#172138')

        class Handler:
            # Dispatch the incoming event through handler
            def handle(self, *_args):
                raise AssertionError('replayed revision was delivered')

        client._dispatch(Handler(), Event.parse(json.dumps(dict(event=THEME_EVENT, payload=spec['response']))))
        self.assertEqual(client.current_plugin_theme()['revision'], 1)

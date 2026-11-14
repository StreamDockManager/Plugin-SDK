import unittest
from stream_dock_manager_sdk.protocol import HostInfo
from stream_dock_manager_sdk.runtime import Client


class LoggingTests(unittest.TestCase):
    # Verify typed writers redact before framing and keep legacy fallback
    def test_typed_writers_redact_before_framing_and_keep_legacy_fallback(self):
        for typed in (False, True):
            client = Client(HostInfo({'streamDockManager': {'contract_version': 1, 'capabilities': ['plugin-logs-v1'] if typed else []}}))
            client.log_redactor = lambda text: text.replace('secret', '[redacted]')
            message = '界' * 9000 + 'secret'
            self.assertEqual(client.stderr().write(message), len(message))
            values = []
            for event in client.drain():
                self.assertEqual(event['event'], 'logMessage')
                self.assertEqual(event['payload'].get('stream'), 'stderr' if typed else None)
                values.append(event['payload']['message'])
            self.assertEqual(''.join(values), message.replace('secret', '[redacted]'))

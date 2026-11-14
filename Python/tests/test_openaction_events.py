import sys
from pathlib import Path
import unittest
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from stream_dock_manager_sdk import Event, openaction_settings_requested, openaction_deep_link


class OpenActionEventTests(unittest.TestCase):
    # Verify notifications preserve opaque url
    def test_notifications_preserve_opaque_url(self):
        self.assertTrue(openaction_settings_requested(Event('showSettingsInterface')))
        self.assertFalse(openaction_settings_requested(Event('keyDown')))
        url = 'openaction://plugins/message/example.sdPlugin/a%2Fb?openaction=hidden'
        self.assertEqual(openaction_deep_link(Event('didReceiveDeepLink', payload={'url': url})), url)
        self.assertIsNone(openaction_deep_link(Event('keyDown')))
        for payload in ({}, {'url': 2}, {'url': ''}, []):
            with self.subTest(payload=payload), self.assertRaises(ValueError):
                openaction_deep_link(Event('didReceiveDeepLink', payload=payload))

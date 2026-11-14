"""Authored protocol tests; no live host/device required."""
import json
import sys
import unittest
from pathlib import Path
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from stream_dock_manager_sdk.openaction_device import OpenActionDeviceProvider


class DeviceProviderTests(unittest.TestCase):
    # Verify envelopes
    def test_envelopes(self):
        provider = OpenActionDeviceProvider('xy')
        info = dict(id='xy-1', name='Virtual', rows=2, columns=3, encoders=1, type=0)
        frame = provider.register_device(info)
        self.assertEqual(frame['event'], 'registerDevice')
        self.assertEqual(frame['payload'], dict(info, touchpoints=0, infobars=0))
        frame['payload']['name'] = 'Changed'
        self.assertEqual(info['name'], 'Virtual')
        for method, event in [('key_down', 'keyDown'), ('key_up', 'keyUp'), ('encoder_down', 'encoderDown'), ('encoder_up', 'encoderUp')]:
            self.assertEqual(getattr(provider, method)('xy-1', 255), {'event': event, 'payload': {'device': 'xy-1', 'position': 255}})
        self.assertEqual(provider.deregister_device('xy-1'), {'event': 'deregisterDevice', 'payload': 'xy-1'})
        self.assertEqual(provider.rerender_images('xy-1'), {'event': 'rerenderImages', 'payload': 'xy-1'})
        self.assertEqual(provider.encoder_change('xy-1', 0, -32768)['payload']['ticks'], -32768)
        self.assertEqual(provider.touchscreen_press('xy-1', 0, 65535, 0)['payload']['hold'], False)

    # Verify reject invalid
    def test_reject_invalid(self):
        for namespace in ('', 'a', 'abc', 'éx', ' x', '\x00x'):
            with self.assertRaises(ValueError): OpenActionDeviceProvider(namespace)
        provider = OpenActionDeviceProvider('xy')
        for value in (-1, 256, True, 1.5):
            with self.assertRaises(ValueError): provider.key_down('xy-1', value)
        with self.assertRaises(ValueError): provider.key_up('ab-1', 0)
        with self.assertRaises(ValueError): provider.key_up('xy' + 'é' * 128, 0)
        with self.assertRaises(ValueError): provider.register_device(dict(id='xy-1', name='', rows=1, columns=1, encoders=0, type=0))
        with self.assertRaises(ValueError): provider.encoder_change('xy-1', 0, 32768)
        with self.assertRaises(ValueError): provider.touchscreen_press('xy-1', 0, 65536, 0)
        with self.assertRaises(ValueError): provider.touchscreen_press('xy-1', 0, 0, 0, 1)

    # Verify responses
    def test_responses(self):
        provider = OpenActionDeviceProvider('xy')
        for event in [dict(event='setBrightness', device='xy-1', brightness=255), dict(event='setImage', device='xy-1', controller=None, position=None, image=None), dict(event='setImage', device='xy-1', controller='Encoder', position=0, image='data:image/jpeg;base64,opaque')]:
            self.assertEqual(provider.parse_response(json.dumps(event)), event)
        for event in [dict(event='setBrightness', device='ab-1', brightness=1), dict(event='setBrightness', device='xy-1', brightness=True), dict(event='setImage', device='xy-1', position=0), dict(event='setImage', device='xy-1', controller='Keypad', position=0, image=42), dict(event='setImage', device='xy-1', payload={})]:
            with self.assertRaises(ValueError): provider.parse_response(json.dumps(event))
        with self.assertRaises(ValueError): provider.parse_response(' ' * 1048577)

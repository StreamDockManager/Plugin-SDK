import base64
from pathlib import Path
import sys
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from stream_dock_manager_sdk import image_data_uri, MAX_PORTABLE_IMAGE_BYTES


class PortableImageTests(unittest.TestCase):
    # Verify portable image data uri
    def test_portable_image_data_uri(self):
        for mime in ('image/png', 'image/svg+xml'):
            data = bytes([0, 1, 254, 255])
            self.assertEqual(image_data_uri(mime, data), f'data:{mime};base64,AAH+/w==')
            self.assertEqual(data, bytes([0, 1, 254, 255]))
            maximum = bytes(MAX_PORTABLE_IMAGE_BYTES)
            self.assertEqual(base64.b64decode(image_data_uri(mime, maximum).split(',')[1]), maximum)
            for invalid in (b'', bytes(MAX_PORTABLE_IMAGE_BYTES + 1)):
                with self.assertRaises(ValueError): image_data_uri(mime, invalid)
        for mime in ('image/jpeg', 'IMAGE/PNG', 'image/svg+xml;charset=utf-8', 'file:///tmp/icon.png'):
            with self.assertRaises(ValueError): image_data_uri(mime, b'x')
        with self.assertRaises(TypeError): image_data_uri('image/png', 'icons/even.png')

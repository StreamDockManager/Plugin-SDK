import base64
import hashlib
import importlib.util
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
import zipfile

spec = importlib.util.spec_from_file_location('sdk_zip_policy', Path(__file__).resolve().parents[1] / 'tools/zip_policy.py')
policy = importlib.util.module_from_spec(spec)
spec.loader.exec_module(policy)


class ZipPolicyTests(unittest.TestCase):
    # Verify owned wheel deflate6 preserves record and metadata
    def test_owned_wheel_deflate6_preserves_record_and_metadata(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'sdk.whl'
            digest = base64.urlsafe_b64encode(hashlib.sha256(b'code' * 1000).digest()).rstrip(b'=').decode()
            record = f'sdk/__init__.py,sha256={digest},4000\nsdk.dist-info/RECORD,,\n'.encode()
            with zipfile.ZipFile(path, 'w') as archive:
                archive.comment = b'comment'
                item = zipfile.ZipInfo('sdk/__init__.py', (2026, 1, 1, 0, 0, 0))
                item.external_attr = 0o100755 << 16
                archive.writestr(item, b'code' * 1000)
                archive.writestr('sdk.dist-info/RECORD', record)
            original = zipfile._get_compressor
            with patch.object(zipfile, '_get_compressor', wraps=original) as compressor:
                policy.normalize_owned_wheel(path)
            self.assertTrue(compressor.call_args_list)
            self.assertTrue(all(call.args == (zipfile.ZIP_DEFLATED, 6) for call in compressor.call_args_list))
            with zipfile.ZipFile(path) as archive:
                self.assertIsNone(archive.testzip())
                archive.extractall(Path(directory) / 'extracted')
                self.assertEqual((Path(directory) / 'extracted/sdk/__init__.py').read_bytes(), b'code' * 1000)
                self.assertEqual(archive.comment, b'comment')
                self.assertEqual(archive.read('sdk.dist-info/RECORD'), record)
                self.assertEqual(archive.read('sdk/__init__.py'), b'code' * 1000)
                self.assertEqual(archive.getinfo('sdk/__init__.py').external_attr, 0o100755 << 16)
                self.assertTrue(all(item.compress_type == zipfile.ZIP_DEFLATED for item in archive.infolist()))

    # Verify signed wheel preserved
    def test_signed_wheel_preserved(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'sdk.whl'
            with zipfile.ZipFile(path, 'w') as archive:
                archive.writestr('sdk.dist-info/RECORD.jws', b'signature')
            before = path.read_bytes()
            with self.assertRaisesRegex(ValueError, 'signed'):
                policy.normalize_owned_wheel(path)
            self.assertEqual(path.read_bytes(), before)

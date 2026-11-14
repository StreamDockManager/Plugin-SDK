import importlib.util
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('sync_output', ROOT / 'tools/sync_output.py')
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class SyncOutputTests(unittest.TestCase):
    def test_check_and_idempotent_write(self):
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / 'nested' / 'output.txt'
            data = 'é\r\n日本\n'.encode()
            with self.assertRaisesRegex(ValueError, 'regenerate'):
                module.sync_output(target, data, check=True, error=ValueError, diagnostic='regenerate')
            self.assertFalse(target.parent.exists())
            self.assertTrue(module.sync_output(target, data))
            stamp = target.stat().st_mtime_ns
            self.assertFalse(module.sync_output(target, data))
            self.assertEqual(target.stat().st_mtime_ns, stamp)
            self.assertFalse(module.sync_output(target, data, check=True))
            with self.assertRaises(SystemExit):
                module.sync_output(target, b'changed', check=True)
            self.assertEqual(target.read_bytes(), data)
            self.assertTrue(module.sync_output(target, b'changed'))
            self.assertEqual(target.read_bytes(), b'changed')

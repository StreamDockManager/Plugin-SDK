import importlib.util
from pathlib import Path
import tempfile
import unittest
ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('sync_starter_inspectors', ROOT / 'tools/sync_starter_inspectors.py')
sync = importlib.util.module_from_spec(spec)
spec.loader.exec_module(sync)


class StarterInspectors(unittest.TestCase):
    def test_snapshots_are_current(self):
        sync.sync(check=True)

    def test_generated_projects_keep_canonical_inspector_bytes(self):
        import sys
        sys.path.insert(0, str(ROOT / 'examples/tools'))
        from generate import generate
        with tempfile.TemporaryDirectory() as directory:
            for language in sync.LANGUAGES:
                target = Path(directory) / language
                generate(target, 'com.example.inspector.' + language, language)
                for name in ('inspector.html', 'inspector.js'):
                    self.assertEqual((target / name).read_bytes(), (ROOT / 'examples/inspector' / name).read_bytes())

    def test_drift_and_regeneration(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = root / 'examples/inspector'
            source.mkdir(parents=True)
            for name in ('inspector.html', 'inspector.js'):
                (source / name).write_text(name)
            sync.sync(root)
            sync.sync(root, check=True)
            target = root / 'examples/sdk-example-go/inspector.js'
            target.write_text('drift')
            with self.assertRaisesRegex(ValueError, 'drift'):
                sync.sync(root, check=True)
            sync.sync(root)
            self.assertEqual(target.read_text(), 'inspector.js')

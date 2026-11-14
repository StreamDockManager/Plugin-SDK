import importlib.util
from pathlib import Path
import sys
import unittest

TOOLS = Path(__file__).resolve().parents[1] / 'tools'
sys.path.insert(0, str(TOOLS))
spec = importlib.util.spec_from_file_location('sdk_sync_assets', TOOLS / 'sync_assets.py')
sync = importlib.util.module_from_spec(spec)
spec.loader.exec_module(sync)


class SettingsTypeGenerationTests(unittest.TestCase):
    # Keep dictionary indexing typed when canonical formatting wraps either initializer
    def test_dictionary_annotations_survive_initializer_line_breaks(self):
        for initializer in ['browser ? {} : {}', 'browser\n    ? {}\n    : {}', '\n    browser ? {} : {}']:
            source = 'const common = {};\nconst local = ' + initializer + ';\n'
            generated = sync.typed_settings_core(source)
            self.assertIn('const common: Record<string, string> =', generated)
            self.assertIn('const local: Record<string, string> =', generated)
            self.assertEqual(generated.replace(': Record<string, string>', ''), source)

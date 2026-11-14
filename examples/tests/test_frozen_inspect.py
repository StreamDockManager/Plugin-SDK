"""Frozen source lookup retains base-library, PYZ, main and fallback semantics."""
import inspect
from pathlib import Path
import runpy
import sys
import tempfile
import unittest
from unittest.mock import patch
import zipfile


class FrozenInspectTests(unittest.TestCase):
    def test_source_lookup_is_lazy_and_preserves_paths(self):
        hook = Path(__file__).resolve().parents[1] / 'sdk-example-python/frozen_inspect.py'
        original = inspect.getsourcefile
        with tempfile.TemporaryDirectory() as directory:
            prefix = str(Path(directory).resolve())
            archive = Path(prefix) / 'base_library.zip'
            with zipfile.ZipFile(archive, 'w') as output: output.writestr('base.pyc', b'')
            def compiled(filename):
                return compile('pass', filename, 'exec')
            try:
                with patch.object(sys, '_MEIPASS', prefix, create=True), patch.object(zipfile, 'ZipFile', wraps=zipfile.ZipFile) as opened:
                    runpy.run_path(str(hook))
                    opened.assert_not_called()
                    main_file = sys.modules['__main__'].__file__
                    self.assertEqual(inspect.getsourcefile(compiled(Path(main_file).name)), main_file)
                    opened.assert_not_called()
                    self.assertEqual(inspect.getsourcefile(compiled('base.py')), str(archive / 'base.pyc'))
                    self.assertEqual(inspect.getsourcefile(compiled('package/module.py')), str(Path(prefix) / 'package/module.py'))
                    opened.assert_called_once()
                    self.assertEqual(inspect.getsourcefile(compiled(str(Path(prefix) / 'absolute.py'))), str(Path(prefix) / 'absolute.py'))
                    self.assertEqual(inspect.getsourcefile(FrozenInspectTests), original(FrozenInspectTests))
            finally:
                inspect.getsourcefile = original

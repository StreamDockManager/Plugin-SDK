"""Resolve each supported layout once and reuse bounded asset inventories."""
import contextlib
import io
import json
from pathlib import Path
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch
import zipfile

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / 'tools'))
from generate import generate
from package import package
from project import package_context, tree_files
from test_starters import script_bundle_fixture


class PackageLayouts(unittest.TestCase):
    def test_native_snapshot_is_inspected_and_archived_once(self):
        import package as packager
        from descriptor import frame, capabilities
        for os_name, entry in (('posix', 'CodePath'), ('nt', 'CodePathWin')):
            with self.subTest(platform=os_name), tempfile.TemporaryDirectory() as temporary:
                source = generate(Path(temporary) / 'project', 'com.example.snapshot', 'go').resolve()
                manifest = json.loads((source / 'manifest.json').read_text())
                executable = source / 'build' / Path(manifest[entry]).name
                executable.parent.mkdir()
                data = frame(capabilities(manifest)).encode()
                executable.write_bytes(data)
                original = packager.inspect

                # Change the on-disk file after the captured bytes have been inspected
                def replace_after_inspection(snapshot, expected):
                    self.assertEqual(snapshot, data)
                    original(snapshot, expected)
                    executable.write_bytes(b'changed after inspection')

                # Model Windows without changing pathlib's host OS
                environment = SimpleNamespace(**{**vars(packager.os), 'name': os_name})
                with patch.object(packager, 'os', environment), contextlib.redirect_stdout(io.StringIO()):
                    with patch.object(packager, 'inspect', side_effect=replace_after_inspection) as inspect:
                        archive = package(project=source, build=False, force=True)
                    self.assertEqual(inspect.call_count, 1)
                    self.assertEqual(executable.read_bytes(), b'changed after inspection')
                    with zipfile.ZipFile(archive) as bundle:
                        self.assertEqual(bundle.read(manifest['UUID'] + '.sdPlugin/' + manifest[entry]), data)
                    before = archive.read_bytes()
                    with executable.open('r+b') as output:
                        output.truncate(64 * 1024 * 1024 + 1)
                    with self.assertRaisesRegex(ValueError, 'bounded regular'):
                        package(project=source, build=False, force=True)
                    self.assertEqual(archive.read_bytes(), before)

    def test_maintained_context(self):
        context = package_context(ROOT, 'nodejs')
        self.assertEqual(context.source, ROOT / 'sdk-example-nodejs')
        self.assertEqual(context.common, ROOT)
        self.assertEqual(context.license, ROOT.parent / 'LICENSE')
        self.assertEqual(context.artifacts, ROOT / 'artifacts')
        self.assertEqual(context.label, 'sdk-example-nodejs')

    def test_standalone_context_inventory_and_determinism(self):
        with tempfile.TemporaryDirectory() as temporary:
            source = generate(Path(temporary) / 'project', 'com.example.inventory', 'nodejs').resolve()
            context = package_context(ROOT, project=source)
            self.assertEqual(context.source, source)
            self.assertEqual(context.common, source)
            self.assertEqual(context.license, source / 'LICENSE')
            self.assertEqual(context.artifacts, source / 'artifacts')
            self.assertEqual(context.label, 'com.example.inventory')
            with self.assertRaisesRegex(ValueError, 'Language'):
                package_context(ROOT, 'go', source)
            script_bundle_fixture(source)
            (source / 'assets/unused.svg').write_text('<svg/>')
            with patch('package.tree_files', wraps=tree_files) as inventory, contextlib.redirect_stdout(io.StringIO()):
                archive = package(project=source, build=False, force=True)
                self.assertEqual(inventory.call_count, 1)
                first = archive.read_bytes()
                archive = package(project=source, build=False, force=True)
                self.assertEqual(inventory.call_count, 2)
                self.assertEqual(first, archive.read_bytes())
            with zipfile.ZipFile(archive) as bundle:
                names = bundle.namelist()
                self.assertTrue(any(name.endswith('/icons/even.svg') for name in names))
                self.assertTrue(any(name.endswith('/icons/odd.svg') for name in names))
                self.assertFalse(any(name.endswith('/unused.svg') for name in names))
            # Inventory reuse must not skip validation of even unreachable files.
            (source / 'assets/bad.svg').symlink_to(source / 'manifest.json')
            with self.assertRaisesRegex(ValueError, 'Symlinks'):
                package(project=source, build=False)
            self.assertEqual(archive.read_bytes(), first)

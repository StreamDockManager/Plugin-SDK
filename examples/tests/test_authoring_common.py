"""Versioned standalone authoring snapshot and bounded-read regressions."""
import hashlib
import json
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / 'tools'))
import authoring_common as common
from sync_authoring import synchronize


class AuthoringPrimitives(unittest.TestCase):
    def test_snapshot(self):
        manifest = json.loads((ROOT / 'tools/authoring_common.v1.json').read_text())
        self.assertEqual(manifest['version'], common.AUTHORING_PRIMITIVES_VERSION)
        for name, digest in manifest['sha256'].items():
            self.assertEqual(hashlib.sha256((ROOT / 'tools' / name).read_bytes()).hexdigest(), digest)

    # Keep generated snapshot metadata stable under the repository JSON formatting policy
    def test_snapshot_generator_matches_formatted_manifest(self):
        with tempfile.TemporaryDirectory() as directory:
            synchronize(directory)
            synchronize(directory, check=True)
            generated = (Path(directory) / 'authoring_common.v1.json').read_bytes()
            self.assertEqual(generated, (ROOT / 'tools/authoring_common.v1.json').read_bytes())
            self.assertEqual(generated, (json.dumps(json.loads(generated), indent=4) + '\n').encode())

    def test_bounded_snapshot_and_symlinks(self):
        with tempfile.TemporaryDirectory() as directory:
            file = Path(directory) / 'source'
            file.write_bytes(b'1234')
            self.assertEqual(common.regular(file, 4), b'1234')
            with self.assertRaises(ValueError): common.regular(file, 3)
            link = file.with_name('link'); link.symlink_to(file)
            with self.assertRaises(ValueError): common.regular(link)
            with self.assertRaises(ValueError): common.regular(Path(directory))

    def test_replaced_between_lstat_and_open(self):
        with tempfile.TemporaryDirectory() as directory:
            file = Path(directory) / 'source'; file.write_bytes(b'old')
            replacement = file.with_name('replacement'); replacement.write_bytes(b'new')
            original = os.open
            def replace(path, flags):
                replacement.replace(file)
                return original(path, flags)
            with patch.object(common.os, 'open', side_effect=replace), self.assertRaises(ValueError):
                common.regular(file)

    def test_growth_after_fstat_is_bounded(self):
        with tempfile.TemporaryDirectory() as directory:
            file = Path(directory) / 'source'; file.write_bytes(b'123')
            original = os.fstat
            def grow(fd):
                info = original(fd)
                file.write_bytes(b'12345')
                return info
            with patch.object(common.os, 'fstat', side_effect=grow), self.assertRaises(ValueError):
                common.regular(file, 4)

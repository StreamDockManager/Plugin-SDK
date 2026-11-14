"""Matrix validation tests; no upstream requests or vendor execution."""
import copy
import hashlib
import importlib.util
import json
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('gate', ROOT / 'tools/check_vendor_compatibility.py')
gate = importlib.util.module_from_spec(spec)
spec.loader.exec_module(gate)

class GateTests(unittest.TestCase):
    # Prepare isolated fixtures for gate tests checks
    def setUp(self):
        self.matrix = json.loads((ROOT / 'contracts/vendor/compatibility.json').read_text())

    # Verify unverified blocks claim and release
    def test_unverified_blocks_claim_and_release(self):
        self.assertTrue(gate.validate(self.matrix))
        with self.assertRaises(ValueError): gate.validate(self.matrix, release=True)
        self.matrix['full_compatibility_claim'] = True
        with self.assertRaises(ValueError): gate.validate(self.matrix)

    # Verify missing downgraded and falsely verified rows rejected
    def test_missing_downgraded_and_falsely_verified_rows_rejected(self):
        for mutation in ('missing', 'downgraded', 'verified'):
            value = copy.deepcopy(self.matrix)
            if mutation == 'missing': value['rows'].pop()
            elif mutation == 'downgraded': value['rows'][0]['required'] = False
            else: value['rows'][0]['status'] = 'verified'
            with self.assertRaises(ValueError): gate.validate(value)

    # Verify Git for Windows checkout settings preserve the pinned upstream bytes
    def test_checkout_with_autocrlf_preserves_pinned_sources(self):
        with tempfile.TemporaryDirectory() as temporary:
            repository = Path(temporary) / 'source'
            checkout = Path(temporary) / 'checkout'
            repository.mkdir()
            checkout.mkdir()
            shutil.copyfile(ROOT / '.gitattributes', repository / '.gitattributes')
            files = self.matrix['source_files']
            self.assertTrue(files)
            for source in files:
                relative = Path('contracts/vendor') / source['path']
                destination = repository / relative
                destination.parent.mkdir(parents=True, exist_ok=True)
                shutil.copyfile(ROOT / relative, destination)
            commands = ('git', '-c', 'core.autocrlf=true')
            subprocess.run([*commands, 'init', '-q'], cwd=repository, check=True)
            subprocess.run([*commands, 'add', '.'], cwd=repository, check=True)
            subprocess.run(
                [*commands, 'checkout-index', '--all', '--prefix=' + checkout.as_posix() + '/'],
                cwd=repository, check=True,
            )
            for source in files:
                contents = (checkout / 'contracts/vendor' / source['path']).read_bytes()
                self.assertEqual(hashlib.sha256(contents).hexdigest(), source['sha256'], source['path'])


if __name__ == '__main__': unittest.main()

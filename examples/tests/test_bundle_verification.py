"""Shared contract cases run without Node, compilers, or provisioned SDK dependencies."""
import hashlib
import json
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / 'examples/tools'))
from javascript import verify


class BundleVerification(unittest.TestCase):
    def test_shared_contract(self):
        cases = json.loads((ROOT / 'contracts/bundle-verification.v1.json').read_text())['cases']
        for case in cases:
            with self.subTest(case=case['name']), tempfile.TemporaryDirectory() as temporary:
                project = Path(temporary)
                output = project / 'build/javascript'
                output.mkdir(parents=True)
                sdk = project / 'sdk'
                sdk.mkdir()
                (project / 'assets').mkdir()
                paths = {'input': project / 'main.mjs', 'sdk': sdk / 'index.js',
                         'asset': project / 'assets/icon.svg', 'output': output / 'main.mjs'}
                for path in paths.values():
                    path.write_bytes(b'fixture')
                digest = hashlib.sha256(b'fixture').hexdigest()
                report = {'settings': {'treeShaking': True},
                          'input_sha256': {'main.mjs': digest, 'sdk:index.js': digest, 'asset:icon.svg': digest},
                          'output_sha256': {'main.mjs': digest}}
                mutation = case['mutation']
                if mutation.startswith('optional-'):
                    report['input_sha256']['optional.json'] = None
                    if mutation == 'optional-appeared':
                        (project / 'optional.json').write_bytes(b'new')
                elif mutation in paths:
                    paths[mutation].write_bytes(b'changed')
                elif mutation.startswith('missing-'):
                    paths[mutation[8:]].unlink()
                elif mutation == 'tree-shaking':
                    report['settings']['treeShaking'] = False
                elif mutation == 'escape':
                    (project / 'build/escape.mjs').write_bytes(b'fixture')
                    report['output_sha256'] = {'../escape.mjs': digest}
                elif mutation == 'symlink':
                    paths['output'].unlink()
                    paths['output'].symlink_to(paths['input'])
                (output / 'bundle-report.json').write_text(json.dumps(report))
                with patch.dict(os.environ, {'STREAM_DOCK_MANAGER_SDK_JS_ROOT': str(sdk)}):
                    try:
                        verify(project)
                        accepted = True
                    except (ValueError, OSError):
                        accepted = False
                self.assertEqual(accepted, case['accept'])

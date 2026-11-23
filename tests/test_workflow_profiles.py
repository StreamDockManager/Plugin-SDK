"""Guard SDK check profile coverage without extra workflow test dependencies."""

import json
from pathlib import Path
import re
import unittest

WORKFLOWS = Path(__file__).resolve().parents[1] / '.github/workflows'
RUNNERS = {'linux': 'ubuntu-24.04', 'windows': 'windows-2022', 'macos': 'macos-15'}
BLACKSMITH_RUNNERS = {'ubuntu-24.04': 'blacksmith-4vcpu-ubuntu-2404',
                      'windows-2022': 'blacksmith-4vcpu-windows-2025',
                      'macos-15': 'blacksmith-6vcpu-macos-15'}
MANUAL_RUNNER_INPUT = (r'^            runner_service:\n {16}description: [^\n]+\n'
                       r' {16}type: choice\n {16}default: github\n {16}options: \[github, blacksmith\]$')


class SDKWorkflowProfiles(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.checks = (WORKFLOWS / 'checks.yml').read_text(encoding='utf-8')
        cls.jobs = dict(re.findall(r'^    (libraries|python|javascript):\n(.*?)(?=^    \w+:\n|\Z)',
                                   cls.checks, flags=re.MULTILINE | re.DOTALL))

    def test_manual_inputs_and_release_caller_cannot_reduce_coverage(self):
        self.assertRegex(self.checks, r'(?m)^    push:\n        branches: \[test-build\]$')
        self.assertRegex(self.checks, r'(?m)^            profile:\n(?: {16}.*\n)*?'
                                     r' {16}default: quick\n {16}options: \[quick, full\]$')
        self.assertRegex(self.checks, r'(?m)^                options: \[all, linux, windows, macos\]$')
        self.assertRegex(self.checks, r'(?m)^    workflow_call:\n        inputs:\n'
                                     r' {12}runner_service:\n {16}description: [^\n]+\n'
                                     r' {16}type: string\n {16}default: github\npermissions:$')
        prepare = (WORKFLOWS / 'prepare-release.yml').read_text(encoding='utf-8')
        self.assertRegex(prepare, r'(?m)^        uses: \./\.github/workflows/checks\.yml\n'
                                  r' {8}with:\n {12}runner_service: \$\{\{ inputs.runner_service \}\}\n'
                                  r' {4}candidates:$')
        self.assertRegex(prepare, r'(?m)^        needs: checks$')

    def test_os_and_version_matrices(self):
        self.assertEqual(set(self.jobs), {'libraries', 'python', 'javascript'})
        per_os_counts = [0, 0]  # Quick, Full
        for name, job in self.jobs.items():
            with self.subTest(job=name):
                runner_map = re.search(r"runs-on: >-\n\s*\$\{\{ inputs\.runner_service == 'blacksmith' &&\n"
                                       r"\s*fromJSON\('([^']+)'\)\[matrix\.os\] \|\|\n\s*matrix\.os \}\}", job)
                self.assertIsNotNone(runner_map)
                self.assertEqual(json.loads(runner_map[1]), BLACKSMITH_RUNNERS)
                selector = re.search(r'                os: >-\n(.*?\) \}\})', job, re.DOTALL)
                self.assertIsNotNone(selector)
                cases = re.findall(r"inputs\.targets == '(\w+)' && '(\[.*?\])'", selector[1])
                self.assertEqual({target: json.loads(runners) for target, runners in cases},
                                 {target: [runner] for target, runner in RUNNERS.items()})
                fallback = re.findall(r"'(\[.*?\])'", selector[1])[-1]
                self.assertEqual(json.loads(fallback), list(RUNNERS.values()))
                if name == 'libraries':
                    options = [[None], [None]]
                else:
                    axis = 'python' if name == 'python' else 'node'
                    versions = re.search(rf"(?m)^                {axis}: .*inputs\.profile == 'quick'"
                                         r" && '(\[.*?\])' \|\| '(\[.*?\])'", job)
                    self.assertIsNotNone(versions)
                    options = [json.loads(value) for value in versions.groups()]
                    self.assertEqual(options, [['3.11'], ['3.11', '3.12', '3.13', '3.14']]
                                     if name == 'python' else [['22'], ['22', '24']])
                for index, versions in enumerate(options):
                    per_os_counts[index] += len(versions)
        self.assertEqual(per_os_counts, [3, 7])
        self.assertEqual([count * len(RUNNERS) for count in per_os_counts], [9, 21])

    def test_manual_runner_choices_and_release_jobs_use_matching_services(self):
        for name in ('checks.yml', 'prepare-release.yml', 'publish-release.yml'):
            with self.subTest(workflow=name):
                workflow = (WORKFLOWS / name).read_text(encoding='utf-8')
                self.assertRegex(workflow, '(?m)' + MANUAL_RUNNER_INPUT)
                if name != 'checks.yml':
                    self.assertIn("runs-on: ${{ inputs.runner_service == 'blacksmith' && "
                                  "'blacksmith-4vcpu-ubuntu-2404' || 'ubuntu-24.04' }}", workflow)

    def test_integrated_source_and_full_only_checks(self):
        library = self.jobs['libraries']
        self.assertIn("python-version: '3.15'", library)
        self.assertIn("node-version: '26'", library)
        self.assertIn("run: python tools/build.py test ${{ inputs.profile != 'quick' && '--go-race' || '' }}", library)
        self.assertNotIn('run: go test -race', library)
        self.assertIn('name: javascript-bundles-${{ matrix.os }}-node-26', library)
        for command in ('python tools/build.py package-built', 'python tests/validate_candidates.py'):
            self.assertIn(f"run: {command}\n              if: inputs.profile != 'quick'", library)

    def test_prepare_seals_only_verified_candidates_from_current_attempt(self):
        library = self.jobs['libraries']
        producer = 'name: sdk-candidates-${{ matrix.os }}-attempt-${{ github.run_attempt }}'
        order = [library.index(command) for command in (
            'run: python tools/build.py test',
            'run: python tools/build.py package-built',
            'run: python tests/validate_candidates.py',
            producer,
        )]
        self.assertEqual(order, sorted(order))
        prepare = (WORKFLOWS / 'prepare-release.yml').read_text(encoding='utf-8')
        self.assertIn('needs: checks', prepare)
        self.assertIn("if: github.repository == 'StreamDockManager/Plugin-SDK'", prepare)
        self.assertRegex(prepare, r'uses: actions/download-artifact@[^\n]+\n +with:\n'
                                  r' +name: sdk-candidates-ubuntu-24\.04-attempt-\$\{\{ github.run_attempt \}\}\n'
                                  r' +path: artifacts\n +digest-mismatch: error\n'
                                  r' +- run: python tools/release.py prepare')
        for redundant in ('tools/build.py', 'tests/validate_candidates.py', 'actions/setup-go', 'rust-toolchain'):
            self.assertNotIn(redundant, prepare)


if __name__ == '__main__':
    unittest.main()

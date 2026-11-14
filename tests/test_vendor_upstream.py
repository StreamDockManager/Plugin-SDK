"""Official metadata watcher tests. No upstream requests or downloaded code execution."""
import copy
import importlib.util
import io
import json
import os
from pathlib import Path
import unittest
from unittest.mock import patch
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location('vendor_upstream', ROOT / 'tools/check_vendor_compatibility.py')
gate = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(gate)


class UpstreamTests(unittest.TestCase):
    # Prepare isolated fixtures for upstream tests checks
    def setUp(self):
        self.matrix = json.loads((ROOT / 'contracts/vendor/compatibility.json').read_text())
        self.responses = {}
        for source in self.matrix['sources']:
            repo = source['repository'].removeprefix('https://github.com/')
            self.responses[f'https://api.github.com/repos/{repo}/commits/HEAD'] = {'sha': source['revision']}
            if source['vendor'] == 'elgato':
                for name, version in [(source['package'], source['package_version']),
                                      (source['schema_package'], source['schema_version'])]:
                    self.responses[f'https://registry.npmjs.org/{name}/latest'] = {
                        'name': name, 'version': version,
                        'repository': {'url': 'git+https://github.com/elgatosf/streamdeck.git'},
                    }
        version = self.matrix['application_releases']['elgato']['observed_version']
        self.responses[gate.ELGATO_RELEASES] = {'articles': [{'title': f'Elgato Stream Deck {version} Release Notes'}]}

    # Prepare review in the upstream tests fixture
    def review(self, matrix=None):
        calls = []
        # Fetch upstream tests data
        def fetch(url):
            calls.append(url)
            return copy.deepcopy(self.responses[url])
        with patch.object(gate, 'fetch', side_effect=fetch), patch('sys.stdout', new_callable=io.StringIO) as output:
            gate.upstream(self.matrix if matrix is None else matrix)
        return calls, output.getvalue()

    # Verify sdk schema repositories and app feed are checked
    def test_sdk_schema_repositories_and_app_feed_are_checked(self):
        calls, output = self.review()
        self.assertEqual(set(calls), set(self.responses))
        self.assertIn('NOT MONITORED: VSD Craft', output)

    # Verify schema release change requires review
    def test_schema_release_change_requires_review(self):
        self.responses['https://registry.npmjs.org/@elgato/schemas/latest']['version'] = '99.0.0'
        with self.assertRaisesRegex(ValueError, '@elgato/schemas changed'):
            self.review()

    # Verify application release change requires review
    def test_application_release_change_requires_review(self):
        self.responses[gate.ELGATO_RELEASES]['articles'].append({'title': 'Elgato Stream Deck 99.0 Release Notes'})
        with self.assertRaisesRegex(ValueError, 'app release changed'):
            self.review()

    # Verify source watch cannot be disabled by removing a vendor
    def test_source_watch_cannot_be_disabled_by_removing_a_vendor(self):
        self.matrix['sources'].pop()
        with self.assertRaisesRegex(ValueError, 'Both official vendor source baselines'):
            self.review()

    # Verify missing schema baseline is not silently skipped
    def test_missing_schema_baseline_is_not_silently_skipped(self):
        next(source for source in self.matrix['sources'] if source['vendor'] == 'elgato').pop('schema_package')
        with self.assertRaisesRegex(ValueError, 'SDK/schema release baseline'):
            self.review()

    # Verify wrong package origin is rejected
    def test_wrong_package_origin_is_rejected(self):
        self.responses['https://registry.npmjs.org/@elgato/schemas/latest']['repository']['url'] = 'git+https://github.com/other/project.git'
        with self.assertRaisesRegex(ValueError, 'identity/repository'):
            self.review()

    # Verify release feed order and drafts do not fake latest
    def test_release_feed_order_and_drafts_do_not_fake_latest(self):
        self.assertEqual(gate.latest_elgato_app_release({'articles': [
            {'title': 'Elgato Stream Deck 7.9 Release Notes'},
            {'title': 'Elgato Stream Deck 7.10.1 Release Notes (Windows)'},
            {'title': 'Elgato Stream Deck 100.0 Release Notes', 'draft': True},
            {'title': 'Elgato Stream Deck unrelated tutorial'},
        ]}), '7.10.1')
        with self.assertRaisesRegex(ValueError, 'no recognized'):
            gate.latest_elgato_app_release({'articles': []})

    # Verify ci token is required only for github and never sent elsewhere
    def test_ci_token_is_required_only_for_github_and_never_sent_elsewhere(self):
        requests = []
        class Response(io.BytesIO):
            pass
        class Opener:
            # Open opener data
            def open(self, request, timeout):
                requests.append(request)
                self_timeout = timeout
                if self_timeout != 30:
                    raise AssertionError('missing timeout')
                return Response(b'{}')
        with patch.dict(os.environ, {'GITHUB_ACTIONS': 'true', 'GITHUB_TOKEN': 'test-only-token'}, clear=True), \
                patch.object(urllib.request, 'build_opener', return_value=Opener()):
            for url in ['https://api.github.com/repos/elgatosf/streamdeck/commits/HEAD',
                        'https://registry.npmjs.org/@elgato/schemas/latest', gate.ELGATO_RELEASES]:
                gate.fetch(url)
        self.assertEqual(requests[0].get_header('Authorization'), 'Bearer test-only-token')
        self.assertIsNone(requests[1].get_header('Authorization'))
        self.assertIsNone(requests[2].get_header('Authorization'))
        with patch.dict(os.environ, {'GITHUB_ACTIONS': 'true'}, clear=True):
            with self.assertRaisesRegex(ValueError, 'CI upstream review requires'):
                gate.fetch('https://api.github.com/repos/elgatosf/streamdeck/commits/HEAD')
        with patch.dict(os.environ, {'GH_TOKEN': 'personal-never-read', 'GITHUB_TOKEN': 'personal-never-read'}, clear=True), \
                patch.object(urllib.request, 'build_opener', return_value=Opener()):
            gate.fetch('https://api.github.com/repos/elgatosf/streamdeck/commits/HEAD')
        self.assertIsNone(requests[-1].get_header('Authorization'))

    # Verify redirects unapproved hosts and oversized metadata fail closed
    def test_redirects_unapproved_hosts_and_oversized_metadata_fail_closed(self):
        with self.assertRaisesRegex(ValueError, 'redirects require review'):
            gate.NoRedirects().redirect_request(None, None, 302, '', {}, 'https://elsewhere.example/')
        for url in ['http://api.github.com/', 'https://api.github.com.evil.example/',
                    'https://user:secret@api.github.com/', 'https://api.github.com:8443/']:
            with self.subTest(url=url), self.assertRaisesRegex(ValueError, 'approved official'):
                gate.fetch(url)
        class Opener:
            # Open opener data
            def open(self, request, timeout):
                return io.BytesIO(b' ' * (gate.MAX_METADATA_BYTES + 1))
        with patch.object(urllib.request, 'build_opener', return_value=Opener()):
            with self.assertRaisesRegex(ValueError, 'size limit'):
                gate.fetch('https://registry.npmjs.org/@elgato/schemas/latest')


if __name__ == '__main__':
    unittest.main()

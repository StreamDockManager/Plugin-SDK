"""Exercise release provenance and collision protections without contacting GitHub"""
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch
import zipfile

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('sdk_release', ROOT / 'tools/release.py')
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)


# Construct the only accepted successful prepare provenance for isolated tests
def run_record():
    return dict(id=12, event='workflow_dispatch', head_branch='main', status='completed',
                conclusion='success', path='.github/workflows/prepare-release.yml',
                repository=dict(full_name=release.REPOSITORY), head_repository=dict(full_name=release.REPOSITORY),
                head_sha='a' * 40, run_attempt=2)


class ReleaseTests(unittest.TestCase):
    # Build tiny opaque packages and a valid manifest without executing candidate code
    def candidate(self, directory):
        for name in release.PACKAGES:
            (directory / name).write_bytes(name.encode())
        release.prepare(directory, 'a' * 40, '12', '2')
        return {p.name: p.read_bytes() for p in directory.iterdir()}

    # Encode exact archive entries to exercise extraction rejection paths
    def archive(self, files):
        output = io.BytesIO()
        with zipfile.ZipFile(output, 'w') as archive:
            for name, data in files.items():
                archive.writestr(name, data)
        return output.getvalue()

    # Require the expected repository, workflow, event, branch and successful conclusion
    def test_run_provenance(self):
        run = run_record()
        release.validate_run(run, '12')
        for field, value in [('id', 13), ('event', 'pull_request'), ('head_branch', 'topic'),
                             ('status', 'in_progress'), ('conclusion', 'failure'),
                             ('path', '.github/workflows/checks.yml'),
                             ('repository', {'full_name': 'fork/SDK'}),
                             ('head_repository', {'full_name': 'fork/SDK'}), ('head_sha', '../main')]:
            with self.subTest(field=field), self.assertRaises(ValueError):
                release.validate_run({**run, field: value}, '12')

    # Verify all bytes and exact attempt before extracting regular top-level files
    def test_candidate_integrity(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source, dest = root / 'source', root / 'dest'
            source.mkdir(); dest.mkdir()
            files = self.candidate(source)
            self.assertEqual(release.unpack_candidate(self.archive(files), run_record(), dest), files)
            for changed in [{**files, '../outside': b'bad'},
                            {**files, next(iter(release.PACKAGES)): b'tampered'}]:
                with self.assertRaises(ValueError):
                    release.unpack_candidate(self.archive(changed), run_record(), dest)
            with self.assertRaises(ValueError):
                release.unpack_candidate(self.archive(files), {**run_record(), 'run_attempt': 3}, dest)
            manifest = json.loads(files[release.MANIFEST]); manifest['qualification'] = 'fully qualified'
            files[release.MANIFEST] = json.dumps(manifest).encode()
            with self.assertRaises(ValueError):
                release.unpack_candidate(self.archive(files), run_record(), dest)

    # Require canonical LF metadata even if the host defaults to CRLF text files
    def test_prepare_metadata_ignores_platform_text_newlines(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source, dest = root / 'source', root / 'dest'
            source.mkdir(); dest.mkdir()
            original = Path.write_text

            def windows_write(path, text, *args, **kwargs):
                kwargs.setdefault('newline', '\r\n')
                return original(path, text, *args, **kwargs)

            with patch.object(Path, 'write_text', windows_write):
                files = self.candidate(source)
            manifest = json.loads(files[release.MANIFEST])
            expected = b''.join(f'{manifest["files"][name]}  {name}\n'.encode('ascii')
                                for name in sorted(release.PACKAGES))
            self.assertEqual(files[release.CHECKSUMS], expected)
            self.assertNotIn(b'\r', files[release.MANIFEST])
            crlf = {**files, release.CHECKSUMS: expected.replace(b'\n', b'\r\n')}
            with self.assertRaisesRegex(ValueError, 'Checksum inventory mismatch'):
                release.unpack_candidate(self.archive(crlf), run_record(), dest)
            self.assertEqual(list(dest.iterdir()), [])
            self.assertEqual(release.unpack_candidate(self.archive(files), run_record(), dest), files)

    # Reject duplicate names even when the set of archive entries appears correct
    def test_duplicate_entry(self):
        with tempfile.TemporaryDirectory() as temporary:
            files = self.candidate(Path(temporary))
            output = io.BytesIO(self.archive(files))
            with zipfile.ZipFile(output, 'a') as archive:
                import warnings
                with warnings.catch_warnings():
                    warnings.simplefilter('ignore', UserWarning)
                    archive.writestr(release.MANIFEST, files[release.MANIFEST])
            with self.assertRaises(ValueError):
                release.unpack_candidate(output.getvalue(), run_record(), Path(temporary))

    # Reject symlink entries and oversized metadata before materializing candidate files
    def test_entry_limits(self):
        with tempfile.TemporaryDirectory() as temporary:
            files = self.candidate(Path(temporary))
            huge = {**files, release.MANIFEST: b' ' * (1024 * 1024 + 1)}
            with self.assertRaises(ValueError):
                release.unpack_candidate(self.archive(huge), run_record(), Path(temporary))
            output = io.BytesIO()
            with zipfile.ZipFile(output, 'w') as archive:
                for name, data in files.items():
                    info = zipfile.ZipInfo(name)
                    if name == release.MANIFEST: info.external_attr = 0o120777 << 16
                    archive.writestr(info, data)
            with self.assertRaises(ValueError):
                release.unpack_candidate(output.getvalue(), run_record(), Path(temporary))

    # Keep both release and Go submodule tag identities immutable on retries
    def test_tag_collision_and_retry(self):
        for tag in [f'v{release.VERSION}', f'Go/v{release.VERSION}']:
            matching = {'object': {'sha': 'a' * 40, 'type': 'commit'}}
            with patch.object(release, 'api', return_value=matching) as api:
                release.ensure_tag(tag, 'a' * 40)
                self.assertEqual(api.call_count, 1)
            with patch.object(release, 'api', return_value={'object': {'sha': 'b' * 40, 'type': 'commit'}}):
                with self.assertRaises(ValueError): release.ensure_tag(tag, 'a' * 40)
            with patch.object(release, 'api', side_effect=[None, {}, matching]) as api:
                release.ensure_tag(tag, 'a' * 40)
                self.assertEqual(api.call_args_list[1].args[2], {'ref': 'refs/tags/' + tag, 'sha': 'a' * 40})

    # Fail invalid workflow identifiers before invoking external tools
    def test_untrusted_run_input(self):
        for value in ['0', '../12', '12; echo bad', '0012', '']:
            with patch.object(release, 'api') as api, self.assertRaises(ValueError):
                release.publish(value)
            api.assert_not_called()

    # Keep the release plan aligned with actual language package versions
    def test_current_versions(self):
        release.validate_versions(ROOT)

    # Model the real API state machine to prove draft retries never overwrite bytes
    def test_publish_and_retry(self):
        with tempfile.TemporaryDirectory() as temporary:
            files = self.candidate(Path(temporary))
            payload = self.archive(files)
            run = {**run_record(), 'workflow_id': 99}
            state = {'tags': {}, 'release': None, 'assets': {}, 'mutations': 0}

            def api(path, method='GET', data=None, raw=False):
                route = path.removeprefix('repos/' + release.REPOSITORY + '/')
                if method != 'GET': state['mutations'] += 1
                if route == 'actions/runs/12': return run.copy()
                if route == 'actions/workflows/prepare-release.yml':
                    return {'id': 99, 'path': '.github/workflows/prepare-release.yml'}
                if route == 'actions/runs/12/artifacts?per_page=100':
                    return {'artifacts': [dict(id=7, name='sdk-release-12-2', expired=False,
                            size_in_bytes=len(payload), digest='sha256:' + release.digest(payload),
                            workflow_run=run)]}
                if route.startswith('git/ref/tags/'):
                    sha = state['tags'].get(route.removeprefix('git/ref/tags/'))
                    return {'object': {'sha': sha, 'type': 'commit'}} if sha else None
                if route == 'git/refs':
                    state['tags'][data['ref'].removeprefix('refs/tags/')] = data['sha']; return {}
                if route == 'releases?per_page=100&page=1': return [state['release']] if state['release'] else []
                if route == 'releases' and method == 'POST':
                    state['release'] = {**data, 'id': 8}; return state['release']
                if route == 'releases/8/assets?per_page=100':
                    return [dict(id=index, name=name, size=len(value))
                            for index, (name, value) in enumerate(sorted(state['assets'].items()))]
                if route.startswith('releases/assets/'):
                    return sorted(state['assets'].items())[int(route.rsplit('/', 1)[1])][1]
                if route == 'releases/8' and method == 'PATCH':
                    state['release'].update(data); return state['release']
                raise AssertionError((route, method))

            def upload(command, **kwargs):
                self.assertEqual(command[:3], ['gh', 'release', 'upload'])
                path = Path(command[4])
                self.assertNotIn(path.name, state['assets'])
                state['assets'][path.name] = path.read_bytes()

            # Forged workflow identity or artifact origin must fail before release mutations
            for fault in ['workflow', 'origin', 'digest']:
                def forged(path, method='GET', data=None, raw=False):
                    result = api(path, method, data, raw)
                    if fault == 'workflow' and path.endswith('/actions/workflows/prepare-release.yml'):
                        result['id'] = 100
                    if path.endswith('/artifacts?per_page=100'):
                        if fault == 'origin': result['artifacts'][0]['workflow_run'] = {**run, 'head_sha': 'b' * 40}
                        if fault == 'digest': result['artifacts'][0]['digest'] = 'sha256:' + '0' * 64
                    return result
                with (patch.object(release, 'api', side_effect=forged),
                      patch.object(release, 'download_actions_artifact', return_value=payload),
                      self.assertRaises(ValueError)):
                    release.publish('12')
                self.assertEqual(state['mutations'], 0)

            with (patch.object(release, 'api', side_effect=api),
                  patch.object(release, 'download_actions_artifact', return_value=payload) as download,
                  patch.object(release.subprocess, 'run', side_effect=upload)):
                # A Go-tag collision must not leave a new root tag behind
                state['tags'][f'Go/v{release.VERSION}'] = 'b' * 40
                with self.assertRaises(ValueError): release.publish('12')
                self.assertEqual(state['mutations'], 0)
                state['tags'].clear()
                release.publish('12')
                self.assertFalse(state['release']['draft'])
                self.assertEqual(set(state['tags']), {f'v{release.VERSION}', f'Go/v{release.VERSION}'})
                self.assertTrue(all(call.args == ('7',) for call in download.call_args_list))
                before = state['mutations']
                release.publish('12')
                self.assertEqual(state['mutations'], before)
                # Public partial releases and mismatching bytes must fail without mutations
                removed = state['assets'].pop(release.MANIFEST)
                with self.assertRaises(ValueError): release.publish('12')
                self.assertEqual(state['mutations'], before)
                state['release']['draft'] = True
                release.publish('12')
                self.assertEqual(state['assets'][release.MANIFEST], removed)
                state['assets'][release.CHECKSUMS] = b'corrupt'
                before = state['mutations']
                with self.assertRaises(ValueError): release.publish('12')
                self.assertEqual(state['mutations'], before)

    # The artifact API authenticates once and its signed storage request has no credentials
    def test_actions_artifact_download_uses_anonymous_storage(self):
        location = 'https://productionresultssa0.blob.core.windows.net/artifacts/archive.zip?fixture=signature'
        requests = []

        def curl(command, *, input, env, timeout, **kwargs):
            self.assertNotIn('--location', command)
            self.assertNotIn(location, command)
            self.assertNotIn('fixture_token', command)
            self.assertNotIn('GH_TOKEN', env)
            self.assertNotIn('GITHUB_TOKEN', env)
            self.assertGreater(timeout, 0)
            fields = []
            for line in input.decode().splitlines():
                key, value = line.split(' = ', 1)
                fields.append((key, json.loads(value)))
            requests.append(fields)
            configuration = dict(fields)
            if 'dump-header' in configuration:
                Path(configuration['dump-header']).write_bytes(
                    b'HTTP/1.1 200 Connection established\r\n\r\nHTTP/2 302\r\n'
                    + f'location: {location}\r\n\r\n'.encode()
                )
                self.assertEqual(command[command.index('--max-filesize') + 1],
                                 str(release.MAX_REDIRECT_HEADERS))
                return subprocess.CompletedProcess(command, 0, b'302')
            Path(configuration['output']).write_bytes(b'fixture-zip')
            self.assertEqual(command[command.index('--max-filesize') + 1],
                             str(release.MAX_ARTIFACT_BYTES))
            return subprocess.CompletedProcess(command, 0, b'200')

        with (patch.dict(os.environ, {'GH_TOKEN': 'fixture_token', 'GITHUB_TOKEN': 'another_token'}),
              patch.object(release.subprocess, 'run', side_effect=curl)):
            self.assertEqual(release.download_actions_artifact('7'), b'fixture-zip')
        self.assertEqual(dict(requests[0])['url'],
                         f'https://api.github.com/repos/{release.REPOSITORY}/actions/artifacts/7/zip')
        self.assertEqual([value for key, value in requests[0] if key == 'header'],
                         ['Accept: application/vnd.github+json', 'Authorization: Bearer fixture_token'])
        self.assertEqual([value for key, value in requests[1] if key == 'header'], [])
        self.assertEqual(dict(requests[1])['url'], location)

    # Refuse ambiguous, insecure or foreign storage redirects before issuing another request
    def test_actions_artifact_refuses_invalid_redirects(self):
        host = 'productionresultssa0.blob.core.windows.net'
        for value in (
            f'http://{host}/file?key=test',
            f'https://{host}.example.com/file?key=test',
            f'https://fixture@{host}/file?key=test',
            f'https://{host}:8443/file?key=test',
            f'https://{host}/file',
            f'https://{host}/file?key=test#part',
            '//127.0.0.1/file?key=test',
        ):
            with self.subTest(value=value), self.assertRaisesRegex(RuntimeError, 'unsupported storage'):
                release.artifact_redirect(f'HTTP/2 302\r\nlocation: {value}\r\n\r\n'.encode())
        with self.assertRaisesRegex(RuntimeError, 'invalid redirect'):
            release.artifact_redirect(b'HTTP/2 302\r\nlocation: one\r\nlocation: two\r\n\r\n')

    # Reject the wrong API status and never follow a storage-side redirect
    def test_actions_artifact_rejects_unexpected_status(self):
        def redirect(_url, _output, _limit, _deadline, *, response_headers, request_headers):
            response_headers.write_bytes(
                b'HTTP/2 302\r\nlocation: https://productionresultssa1.blob.core.windows.net/'
                b'archive.zip?fixture=signature\r\n\r\n'
            )
            return 302

        with patch.dict(os.environ, {'GH_TOKEN': 'fixture_token'}):
            with patch.object(release, 'artifact_get', return_value=415) as get:
                with self.assertRaisesRegex(RuntimeError, 'HTTP 415; expected 302'):
                    release.download_actions_artifact('7')
                get.assert_called_once()
            with patch.object(release, 'artifact_get',
                              side_effect=lambda *args, **kwargs: redirect(*args, **kwargs) if kwargs else 302) as get:
                with self.assertRaisesRegex(RuntimeError, 'storage returned HTTP 302'):
                    release.download_actions_artifact('7')
                self.assertEqual(get.call_count, 2)

    # Surface timeouts without including the signed URL or subprocess error details
    def test_actions_artifact_timeout_is_sanitized(self):
        failure = subprocess.TimeoutExpired(['curl'], 1, output=b'fixture signed URL')
        with patch.object(release.subprocess, 'run', side_effect=failure):
            with self.assertRaisesRegex(RuntimeError, '^GitHub Actions artifact download timed out$'):
                release.artifact_get('https://productionresultssa0.blob.core.windows.net/file?sig=secret',
                                     Path('discard'), 42, release.time.monotonic() + 2)

    # Reject changes to a completed run while its artifact is being downloaded
    def test_rerun_race(self):
        run = {**run_record(), 'workflow_id': 9}
        for key, value in [('run_attempt', 3), ('head_sha', 'b' * 40), ('workflow_id', 8), ('conclusion', 'failure')]:
            with patch.object(release, 'api', return_value={**run, key: value}), self.assertRaises(ValueError):
                release.recheck_run(run, '12')

    # Kill a silent child at the wall-clock deadline instead of blocking on its stdout
    def test_api_silent_timeout(self):
        import subprocess
        import sys
        import time
        popen = subprocess.Popen
        children = []
        def launch(command, **kwargs):
            self.assertEqual(command[:4], ['gh', 'api', '--hostname', 'github.com'])
            child = popen([sys.executable, '-c', 'import time; time.sleep(30)'], **kwargs)
            children.append(child)
            return child
        start = time.monotonic()
        with patch.object(release.subprocess, 'Popen', side_effect=launch), patch.object(release, 'API_TIMEOUT_SECONDS', 0.1):
            with self.assertRaises(RuntimeError):
                release.api('repos/' + release.REPOSITORY + '/actions/runs/12')
        self.assertLess(time.monotonic() - start, 5)
        self.assertIsNotNone(children[0].poll())

    # Keep release workflows manual and prohibit registry publishing commands
    def test_workflow_boundaries(self):
        for name in ['prepare-release.yml', 'publish-release.yml']:
            text = (ROOT / '.github/workflows' / name).read_text()
            self.assertIn('workflow_dispatch:', text)
            self.assertNotIn('    push:', text)
            self.assertIn("github.event_name == 'workflow_dispatch' && github.ref == 'refs/heads/main'", text)
            self.assertIn('persist-credentials: false', text)
            for forbidden in ['npm publish', 'cargo publish', 'twine upload']:
                self.assertNotIn(forbidden, text)
        text = (ROOT / '.github/workflows/publish-release.yml').read_text()
        self.assertIn('PREPARE_RUN_ID: ${{ inputs.prepare_run_id }}', text)
        self.assertNotIn('run: ${{', text)


if __name__ == '__main__':
    unittest.main()

#!/usr/bin/env python3
"""Prepare and publish reviewed SDK downloads without publishing to language registries"""
import argparse
import hashlib
import io
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import threading
import time
import tomllib
import zipfile
from urllib.parse import urlsplit

ROOT = Path(__file__).resolve().parents[1]
REPOSITORY = 'StreamDockManager/Plugin-SDK'
VERSION = '0.1.1'
API_TIMEOUT_SECONDS = 120
MAX_ARTIFACT_BYTES = 256 * 1024 * 1024
MAX_REDIRECT_HEADERS = 64 * 1024
PACKAGES = {
    f'stream-dock-manager-plugin-sdk-go-{VERSION}.zip',
    f'stream-dock-manager-plugin-sdk-{VERSION}.crate',
    f'stream-dock-manager-plugin-sdk-{VERSION}.tgz',
    f'stream_dock_manager_plugin_sdk-{VERSION}-py3-none-any.whl',
}
MANIFEST = 'release-manifest.json'
CHECKSUMS = 'SHA256SUMS'
QUALIFICATION = 'development-preview; reference-host qualification incomplete'


# Hash actual bytes instead of trusting artifact metadata
def digest(data):
    return hashlib.sha256(data).hexdigest()


# Reject noncanonical identifiers before composing API paths or tag names
def identifier(value, pattern):
    if not isinstance(value, str) or not re.fullmatch(pattern, value):
        raise ValueError('Invalid release identifier')
    return value


# Keep authenticated requests on GitHub API endpoints under the SDK repository
def api(path, method='GET', data=None, raw=False):
    if not path.startswith(f'repos/{REPOSITORY}/'):
        raise ValueError('API request outside SDK repository')
    command = ['gh', 'api', '--hostname', 'github.com', path, '--method', method]
    if raw:
        command += ['-H', 'Accept: application/octet-stream']
    if data is not None:
        command += ['--input', '-']
    limit = 256 * 1024 * 1024 if raw else 8 * 1024 * 1024
    # Drain stdout incrementally with a hard bound, retaining no credentials or API errors in logs
    with tempfile.TemporaryFile() as errors, tempfile.TemporaryFile() as output:
        process = subprocess.Popen(command, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=errors)
        # Bound silent network stalls as well as actively streaming oversized responses
        def stop_request():
            try:
                process.kill()
            except ProcessLookupError:
                pass
        deadline = threading.Timer(API_TIMEOUT_SECONDS, stop_request)
        deadline.start()
        try:
            if data is not None:
                process.stdin.write(json.dumps(data).encode())
            process.stdin.close()
            size = 0
            while chunk := process.stdout.read(64 * 1024):
                size += len(chunk)
                if size > limit:
                    raise ValueError('GitHub API response exceeded bounded download size')
                output.write(chunk)
            process.wait(timeout=30)
        finally:
            deadline.cancel()
            deadline.join()
            if process.poll() is None:
                process.kill()
                process.wait()
            process.stdout.close()
        if process.returncode:
            errors.seek(0)
            error = errors.read(4096)
            if b'HTTP 404' in error and method == 'GET':
                return None
            raise RuntimeError('GitHub API request failed: ' + path)
        output.seek(0)
        return output.read() if raw else json.load(output)



# Keep curl configuration and signed URLs off the command line and out of errors
def artifact_get(url, output, limit, deadline, *, response_headers=None, request_headers=()):
    remaining = deadline - time.monotonic()
    if remaining <= 0:
        raise RuntimeError('GitHub Actions artifact download timed out')
    config = [('url', url), ('output', str(output))]
    if response_headers is not None:
        config.append(('dump-header', str(response_headers)))
    config.extend(('header', header) for header in request_headers)
    # Do not inherit credentials into either curl process
    env = {key: value for key, value in os.environ.items()
           if key not in ('GH_TOKEN', 'GITHUB_TOKEN', 'GH_ENTERPRISE_TOKEN', 'GITHUB_ENTERPRISE_TOKEN')}
    try:
        result = subprocess.run(
            ['curl', '--disable', '--silent', '--globoff', '--proto', '=https',
             '--connect-timeout', str(min(15, remaining)), '--max-time', str(remaining),
             '--max-filesize', str(limit), '--write-out', '%{http_code}', '--config', '-'],
            input=''.join(f'{key} = {json.dumps(value, ensure_ascii=False)}\n'
                          for key, value in config).encode('utf-8'),
            stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, env=env,
            timeout=remaining, check=False,
        )
    except subprocess.TimeoutExpired:
        raise RuntimeError('GitHub Actions artifact download timed out') from None
    except OSError:
        raise RuntimeError('Could not start GitHub Actions artifact transport') from None
    status = int(result.stdout) if re.fullmatch(rb'[1-5][0-9]{2}', result.stdout) else None
    if result.returncode:
        detail = f'HTTP {status}; ' if status is not None else ''
        raise RuntimeError(f'GitHub Actions artifact request failed ({detail}curl exit {result.returncode})')
    if status is None:
        raise RuntimeError('GitHub Actions artifact request returned no HTTP status')
    return status


# Accept a single temporary HTTPS URL from GitHub Actions artifact storage
def artifact_redirect(headers):
    responses = [block for block in re.split(rb'\r?\n\r?\n', headers) if block.startswith(b'HTTP/')]
    final = responses[-1].splitlines() if responses else []
    locations = [line.split(b':', 1)[1].strip() for line in final[1:]
                 if line.lower().startswith(b'location:')]
    if len(locations) != 1 or not re.fullmatch(rb'[!-~]{1,8192}', locations[0]):
        raise RuntimeError('GitHub Actions artifact returned an invalid redirect')
    location = locations[0].decode('ascii')
    try:
        parsed = urlsplit(location)
        host = parsed.hostname or ''
        allowed = re.fullmatch(r'[a-z0-9]+\.blob\.core\.windows\.net', host) or re.fullmatch(
            r'[a-z0-9-]+\.actions\.githubusercontent\.com', host)
        if (parsed.scheme == 'https' and allowed and parsed.port in (None, 443)
                and not parsed.username and not parsed.password and not parsed.fragment
                and parsed.path.startswith('/') and parsed.query and '\\' not in location):
            return location
    except ValueError:
        pass
    raise RuntimeError('GitHub Actions artifact redirected to an unsupported storage destination')


# Request the API redirect with GitHub credentials, then download without them
def download_actions_artifact(artifact_id):
    identifier(artifact_id, r'[1-9][0-9]*')
    token = os.environ.get('GH_TOKEN', '')
    if not re.fullmatch(r'[A-Za-z0-9_.-]+', token):
        raise RuntimeError('GitHub Actions artifact download requires GH_TOKEN')
    deadline = time.monotonic() + API_TIMEOUT_SECONDS
    with tempfile.TemporaryDirectory(prefix='sdk-artifact-') as temporary:
        root = Path(temporary)
        headers = root / 'headers'
        status = artifact_get(
            f'https://api.github.com/repos/{REPOSITORY}/actions/artifacts/{artifact_id}/zip',
            root / 'response', MAX_REDIRECT_HEADERS, deadline, response_headers=headers,
            request_headers=('Accept: application/vnd.github+json', 'Authorization: Bearer ' + token),
        )
        if status != 302:
            raise RuntimeError(f'GitHub Actions artifact API returned HTTP {status}; expected 302')
        with headers.open('rb') as stream:
            raw_headers = stream.read(MAX_REDIRECT_HEADERS + 1)
        if len(raw_headers) > MAX_REDIRECT_HEADERS:
            raise RuntimeError('GitHub Actions artifact response headers exceeded size limit')
        location = artifact_redirect(raw_headers)
        archive = root / 'archive.zip'
        status = artifact_get(location, archive, MAX_ARTIFACT_BYTES, deadline)
        if status != 200:
            raise RuntimeError(f'GitHub Actions artifact storage returned HTTP {status}; expected 200')
        if archive.stat().st_size > MAX_ARTIFACT_BYTES:
            raise RuntimeError('GitHub Actions artifact archive exceeded size limit')
        return archive.read_bytes()


# Bind candidate provenance to the exact successful manual prepare attempt
def validate_run(run, run_id):
    if (str(run.get('id')) != run_id or run.get('event') != 'workflow_dispatch'
            or run.get('head_branch') != 'main' or run.get('status') != 'completed'
            or run.get('conclusion') != 'success'
            or run.get('path') != '.github/workflows/prepare-release.yml'
            or run.get('repository', {}).get('full_name') != REPOSITORY
            or run.get('head_repository', {}).get('full_name') != REPOSITORY):
        raise ValueError('Expected a successful main-only SDK Prepare Release run')
    identifier(run['head_sha'], r'[0-9a-f]{40}')
    identifier(str(run['run_attempt']), r'[1-9][0-9]*')


# Require the declared language packages and Go module to match this release plan
def validate_versions(root):
    versions = [json.loads((root / 'Node/package.json').read_text())['version'],
                tomllib.loads((root / 'Rust/Cargo.toml').read_text())['package']['version'],
                tomllib.loads((root / 'Python/pyproject.toml').read_text())['project']['version']]
    module = (root / 'Go/go.mod').read_text().splitlines()[0]
    if versions != [VERSION] * 3 or module != 'module github.com/StreamDockManager/Plugin-SDK/Go':
        raise ValueError('Release version or Go module differs from the source manifests')


# Serialize the checksum inventory as canonical LF bytes on every OS
def checksum_inventory(files):
    return ''.join(f'{value}  {name}\n' for name, value in sorted(files.items())).encode('ascii')


# Produce a deterministic inventory with explicit preview qualification
def prepare(directory, sha, run_id, attempt):
    identifier(sha, r'[0-9a-f]{40}')
    identifier(run_id, r'[1-9][0-9]*')
    identifier(attempt, r'[1-9][0-9]*')
    files = {}
    for name in sorted(PACKAGES):
        path = directory / name
        if path.is_symlink() or not path.is_file():
            raise ValueError('Missing regular candidate: ' + name)
        files[name] = digest(path.read_bytes())
    manifest = dict(schema=1, repository=REPOSITORY, version=VERSION, sha=sha,
                    run_id=run_id, attempt=attempt, qualification=QUALIFICATION, files=files)
    (directory / MANIFEST).write_bytes((json.dumps(manifest, sort_keys=True, indent=2) + '\n').encode('utf-8'))
    (directory / CHECKSUMS).write_bytes(checksum_inventory(files))


# Validate archive paths and the entire file set before writing any downloaded bytes
def unpack_candidate(payload, run, directory):
    with zipfile.ZipFile(io.BytesIO(payload)) as archive:
        entries = archive.infolist()
        expected = PACKAGES | {MANIFEST, CHECKSUMS}
        if (len(entries) != len(expected) or {item.filename for item in entries} != expected
                or any(item.filename in {MANIFEST, CHECKSUMS} and item.file_size > 1024 * 1024 for item in entries)
                or sum(item.file_size for item in entries) > 256 * 1024 * 1024
                or any(item.file_size > 256 * 1024 * 1024 or (item.external_attr >> 16) & 0o170000 == 0o120000 for item in entries)):
            raise ValueError('Unexpected or oversized release artifact entries')
        data = {item.filename: archive.read(item) for item in entries}
    manifest = json.loads(data[MANIFEST])
    expected_manifest = dict(schema=1, repository=REPOSITORY, version=VERSION,
                             sha=run['head_sha'], run_id=str(run['id']), attempt=str(run['run_attempt']),
                             qualification=QUALIFICATION,
                             files={name: digest(data[name]) for name in sorted(PACKAGES)})
    if manifest != expected_manifest:
        raise ValueError('Candidate provenance or checksums do not match prepare run')
    if data[CHECKSUMS] != checksum_inventory(manifest['files']):
        raise ValueError('Checksum inventory mismatch')
    for name, value in data.items():
        (directory / name).write_bytes(value)
    return data


# Reject existing tag collisions without creating any release identity
def check_tag(tag, sha):
    existing = api(f'repos/{REPOSITORY}/git/ref/tags/{tag}')
    if existing is not None and (existing.get('object', {}).get('sha') != sha or existing.get('object', {}).get('type') != 'commit'):
        raise ValueError('Existing tag does not identify the exact prepared commit: ' + tag)
    return existing


# Create missing lightweight tags and recheck instead of replacing existing identities
def ensure_tag(tag, sha):
    if check_tag(tag, sha) is None:
        api(f'repos/{REPOSITORY}/git/refs', 'POST', {'ref': 'refs/tags/' + tag, 'sha': sha})
        if check_tag(tag, sha) is None:
            raise ValueError('Created tag could not be verified')


# Reject reruns or changed conclusions before publishing a downloaded candidate
def recheck_run(run, run_id):
    latest = api(f'repos/{REPOSITORY}/actions/runs/{run_id}')
    validate_run(latest or {}, run_id)
    for key in ['id', 'run_attempt', 'head_sha', 'workflow_id']:
        if latest.get(key) != run.get(key):
            raise ValueError('Prepare run changed during download')


# Include authenticated draft releases so interrupted uploads reuse the same draft
def find_release(tag):
    matches = []
    for page in range(1, 11):
        releases = api(f'repos/{REPOSITORY}/releases?per_page=100&page={page}')
        if not isinstance(releases, list):
            raise ValueError('Invalid release listing')
        matches.extend(item for item in releases if item.get('tag_name') == tag)
        if len(releases) < 100:
            if len(matches) > 1:
                raise ValueError('Multiple releases claim the same version tag')
            return matches[0] if matches else None
    raise ValueError('Release listing exceeds bounded collision search')


# Publish only verified downloads, preserving matching assets and rejecting collisions
def publish(run_id):
    identifier(run_id, r'[1-9][0-9]*')
    run = api(f'repos/{REPOSITORY}/actions/runs/{run_id}')
    validate_run(run or {}, run_id)
    workflow = api(f'repos/{REPOSITORY}/actions/workflows/prepare-release.yml')
    if not workflow or run.get('workflow_id') != workflow['id'] or workflow.get('path') != '.github/workflows/prepare-release.yml':
        raise ValueError('Prepare workflow identity mismatch')
    result = api(f'repos/{REPOSITORY}/actions/runs/{run_id}/artifacts?per_page=100')
    name = f'sdk-release-{run_id}-{run["run_attempt"]}'
    matches = [item for item in result['artifacts'] if item['name'] == name and not item['expired']]
    if len(matches) != 1 or matches[0]['size_in_bytes'] > MAX_ARTIFACT_BYTES:
        raise ValueError('Missing, ambiguous, expired or oversized prepare artifact')
    artifact_id = identifier(str(matches[0]['id']), r'[1-9][0-9]*')
    artifact = matches[0]
    origin = artifact.get('workflow_run', {})
    if origin.get('id') != run['id'] or origin.get('head_sha') != run['head_sha'] or origin.get('head_branch') != 'main':
        raise ValueError('Artifact workflow provenance mismatch')
    payload = download_actions_artifact(artifact_id)
    if not payload or artifact.get('digest') != 'sha256:' + digest(payload):
        raise ValueError('GitHub artifact digest mismatch')
    recheck_run(run, run_id)
    with tempfile.TemporaryDirectory(prefix='sdk-release-') as temporary:
        directory = Path(temporary)
        files = unpack_candidate(payload, run, directory)
        tag = 'v' + VERSION
        body = (f'SDK development preview {VERSION}\n\n{QUALIFICATION}\n\n'
                f'Commit: {run["head_sha"]}\nPrepare run: {run_id}, attempt: {run["run_attempt"]}\n'
                'Downloadable SDK packages only; no npm, PyPI or crates.io publication.\n')
        release = find_release(tag)
        if release is not None and (release.get('body') != body or not release.get('prerelease')
                                    or release.get('target_commitish') != run['head_sha']):
            raise ValueError('Existing release belongs to different provenance or qualification')
        # Check existing contents before any tag or asset mutation
        assets = api(f'repos/{REPOSITORY}/releases/{release["id"]}/assets?per_page=100') if release else []
        if len(assets) > len(files) or len({item['name'] for item in assets}) != len(assets):
            raise ValueError('Ambiguous existing assets')
        for asset in assets:
            if asset['name'] not in files or asset['size'] != len(files[asset['name']]):
                raise ValueError('Unexpected release asset')
            remote = api(f'repos/{REPOSITORY}/releases/assets/{asset["id"]}', raw=True)
            if digest(remote) != digest(files[asset['name']]):
                raise ValueError('Existing release asset differs; never overwrite')
        existing = {item['name'] for item in assets}
        if release is not None and not release['draft'] and existing != set(files):
            raise ValueError('Published release is incomplete; refusing to alter it')
        recheck_run(run, run_id)
        check_tag(tag, run['head_sha'])
        check_tag('Go/' + tag, run['head_sha'])
        ensure_tag(tag, run['head_sha'])
        ensure_tag('Go/' + tag, run['head_sha'])
        if release is None:
            release = api(f'repos/{REPOSITORY}/releases', 'POST', dict(tag_name=tag,
                          target_commitish=run['head_sha'], name=f'SDK {VERSION} development preview',
                          body=body, draft=True, prerelease=True))
        for name in sorted(set(files) - existing):
            subprocess.run(['gh', 'release', 'upload', tag, str(directory / name), '--repo', 'github.com/' + REPOSITORY], check=True, timeout=API_TIMEOUT_SECONDS)
        # Re-read all uploaded bytes before exposing a completed release
        assets = api(f'repos/{REPOSITORY}/releases/{release["id"]}/assets?per_page=100')
        if len(assets) != len(files) or {item['name'] for item in assets} != set(files):
            raise ValueError('Release upload inventory mismatch')
        for asset in assets:
            if digest(api(f'repos/{REPOSITORY}/releases/assets/{asset["id"]}', raw=True)) != digest(files[asset['name']]):
                raise ValueError('Uploaded release checksum mismatch')
        if release['draft']:
            api(f'repos/{REPOSITORY}/releases/{release["id"]}', 'PATCH', {'draft': False, 'make_latest': 'false'})


# Keep preparation and privileged publication as separate explicit commands
def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest='command', required=True)
    sub.add_parser('prepare')
    publish_parser = sub.add_parser('publish')
    publish_parser.add_argument('--run-id', required=True)
    args = parser.parse_args()
    if os.environ.get('GITHUB_REPOSITORY') != REPOSITORY or os.environ.get('GITHUB_REF') != 'refs/heads/main' or os.environ.get('GITHUB_EVENT_NAME') != 'workflow_dispatch':
        raise ValueError('Release operations require manual dispatch from SDK main')
    validate_versions(ROOT)
    if args.command == 'prepare':
        prepare(ROOT / 'artifacts', os.environ['GITHUB_SHA'], os.environ['GITHUB_RUN_ID'], os.environ['GITHUB_RUN_ATTEMPT'])
    else:
        publish(args.run_id)


if __name__ == '__main__':
    main()

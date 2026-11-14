#!/usr/bin/env python3
"""Validate evidence and block full-compatibility releases until qualified.

--upstream reads public version metadata only; it never downloads/runs vendor code.
"""
import argparse
import hashlib
import json
import os
import re
from pathlib import Path
import urllib.request
import urllib.parse

ROOT = Path(__file__).resolve().parents[1]


# Validate check vendor compatibility inputs before further processing
def validate(matrix, release=False):
    required = {f'{consumer}-{vendor}-{os}' for consumer in (
        'sdk-go', 'sdk-rust', 'sdk-python', 'sdk-node', 'starter-go', 'starter-rust',
        'starter-python', 'starter-node', 'official-plugins'
    ) for vendor in ('vsd', 'elgato') for os in ('macos', 'windows')}
    rows = matrix['rows']
    ids = [row['id'] for row in rows]
    if len(ids) != len(set(ids)) or not required.issubset(ids):
        raise ValueError('Required outbound consumer/vendor/OS rows are missing or duplicated')
    blockers = []
    for row in rows:
        if row['id'] in required and row.get('required') is not True:
            raise ValueError(f"Cannot downgrade required row: {row['id']}")
        if row['status'] not in ('verified', 'partial', 'unverified', 'missing'):
            raise ValueError(f"Invalid status: {row['id']}")
        if row['status'] == 'verified':
            if not all(row.get(key) for key in ('vendor_app_build', 'runtime', 'device_controller', 'tested_at', 'evidence')):
                raise ValueError(f"Reference-host evidence incomplete: {row['id']}")
        elif row['required']:
            if not row.get('blocker'):
                raise ValueError(f"Missing actionable blocker: {row['id']}")
            blockers.append(row['id'])
    if (release or matrix['full_compatibility_claim']) and blockers:
        raise ValueError('Full compatibility blocked by: ' + ', '.join(blockers))
    return blockers


# Fixed official providers. Never forward GitHub credentials to package registries,
# redirects, release notes, or URLs supplied by upstream content
OFFICIAL_REPOSITORIES = {
    'vsd': 'https://github.com/VSDinside/VSDinside-Plugin-SDK',
    'elgato': 'https://github.com/elgatosf/streamdeck',
}
ELGATO_RELEASES = 'https://help.elgato.com/api/v2/help_center/en-us/sections/5162671529357/articles.json?sort_by=created_at&sort_order=desc&per_page=100'
MAX_METADATA_BYTES = 2 * 1024 * 1024


class NoRedirects(urllib.request.HTTPRedirectHandler):
    # Apply the HTTP redirect policy before constructing a new request
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise ValueError('Upstream metadata redirects require review')


# Fetch check vendor compatibility data
def fetch(url):
    parsed = urllib.parse.urlsplit(url)
    if (parsed.scheme != 'https' or parsed.hostname not in
            ('api.github.com', 'registry.npmjs.org', 'help.elgato.com')
            or parsed.username or parsed.password or parsed.port not in (None, 443)):
        raise ValueError('Not an approved official metadata URL')
    headers = {'User-Agent': 'sdk-source-review', 'Accept': 'application/json'}
    if parsed.hostname == 'api.github.com':
        if os.environ.get('GITHUB_ACTIONS') == 'true':
            token = os.environ.get('GITHUB_TOKEN')
            if not token:
                raise ValueError('CI upstream review requires its read-only GITHUB_TOKEN')
            headers['Authorization'] = 'Bearer ' + token
        # Local public inspection is anonymous; never discover/read personal tokens
        headers['X-GitHub-Api-Version'] = '2022-11-28'
    request = urllib.request.Request(url, headers=headers)
    with urllib.request.build_opener(NoRedirects()).open(request, timeout=30) as response:
        data = response.read(MAX_METADATA_BYTES + 1)
    if len(data) > MAX_METADATA_BYTES:
        raise ValueError('Upstream metadata exceeds size limit')
    value = json.loads(data)
    if not isinstance(value, dict):
        raise ValueError('Expected an upstream metadata object')
    return value


# Package version
def package_version(package):
    value = fetch('https://registry.npmjs.org/' + package + '/latest')
    if value.get('name') != package or value.get('repository', {}).get('url') != 'git+https://github.com/elgatosf/streamdeck.git':
        raise ValueError('Official package identity/repository changed: ' + package)
    version = value.get('version')
    if not isinstance(version, str) or not re.fullmatch(r'[0-9]+\.[0-9]+\.[0-9]+', version):
        raise ValueError('Invalid official package release version: ' + package)
    return version


# Read the latest qualifying Elgato application release metadata
def latest_elgato_app_release(metadata):
    releases = []
    for article in metadata.get('articles', []):
        if article.get('draft') is True:
            continue
        match = re.fullmatch(r'Elgato Stream Deck ([0-9]+(?:\.[0-9]+){1,3}) Release Notes(?: \(macOS\)| \(Windows\))?', article.get('title', ''))
        if not match:
            continue
        version = match.group(1)
        parts = tuple(int(part) for part in version.split('.'))
        releases.append((parts + (0,) * (4 - len(parts)), version))
    if not releases:
        raise ValueError('Official Stream Deck release feed contains no recognized app versions')
    return max(releases)[1]


# Inspect upstream protocol and application metadata for compatibility drift
def upstream(matrix):
    if sorted(source.get('vendor', '') for source in matrix['sources']) != sorted(OFFICIAL_REPOSITORIES):
        raise ValueError('Both official vendor source baselines are required exactly once')
    changes = []
    for source in matrix['sources']:
        vendor = source['vendor']
        if source['repository'] != OFFICIAL_REPOSITORIES.get(vendor):
            raise ValueError('Unrecognized official source repository: ' + vendor)
        repo = source['repository'].removeprefix('https://github.com/')
        current = fetch(f'https://api.github.com/repos/{repo}/commits/HEAD')['sha']
        if not isinstance(current, str) or not re.fullmatch(r'[a-f0-9]{40}', current):
            raise ValueError('Invalid upstream commit identity: ' + vendor)
        if current != source['revision']:
            changes.append(f"{vendor} source changed: {source['revision']} -> {current}")
        if vendor == 'elgato':
            for key, expected_package, version_key in (
                ('package', '@elgato/streamdeck', 'package_version'),
                ('schema_package', '@elgato/schemas', 'schema_version'),
            ):
                if source.get(key) != expected_package or not source.get(version_key):
                    raise ValueError('Missing official SDK/schema release baseline: ' + expected_package)
                current = package_version(expected_package)
                if current != source[version_key]:
                    changes.append(f"{expected_package} changed: {source[version_key]} -> {current}")
    app_releases = matrix.get('application_releases', {})
    elgato = app_releases.get('elgato', {})
    if elgato.get('metadata_url') != ELGATO_RELEASES or not elgato.get('observed_version'):
        raise ValueError('Official Stream Deck app release baseline is missing')
    current = latest_elgato_app_release(fetch(ELGATO_RELEASES))
    if current != elgato['observed_version']:
        changes.append(f"Stream Deck app release changed: {elgato['observed_version']} -> {current}")
    vsd = app_releases.get('vsd', {})
    if vsd.get('status') != 'manual-required' or not vsd.get('blocker'):
        raise ValueError('VSD app release monitoring needs an explicit source or manual-review blocker')
    print('NOT MONITORED: VSD Craft app releases. ' + vsd['blocker'])
    if changes:
        raise ValueError('Review required; update fixtures and rerun qualification before changing claims.\n' + '\n'.join(changes))


# Run the check vendor compatibility command and enforce its input options
def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--release', action='store_true')
    parser.add_argument('--upstream', action='store_true')
    args = parser.parse_args()
    matrix = json.loads((ROOT / 'contracts/vendor/compatibility.json').read_text())
    for source in matrix.get('source_files', []):
        path = ROOT / 'contracts/vendor' / source['path']
        if hashlib.sha256(path.read_bytes()).hexdigest() != source['sha256']:
            raise ValueError('Pinned source hash changed: ' + source['path'])
    blockers = validate(matrix, args.release)
    if args.upstream:
        upstream(matrix)
    print(f'Compatibility evidence valid; {len(blockers)} required reference-host rows remain blocked.')


if __name__ == '__main__':
    main()

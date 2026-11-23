#!/usr/bin/env python3
"""Offline example source/package contract validation."""
import json
from pathlib import Path
import zipfile
from package import ROOT
from project import STARTER_KINDS
from project import distribution_manifest, native_binary_name

assets = ROOT / 'assets'
layout = json.loads((assets / 'display/counter.json').read_text())
assert layout['id'] == 'sdk-counter'
assert [(i['key'], i['type']) for i in layout['items']] == [('icon', 'pixmap'), ('prefix', 'text'), ('count', 'text')]
asset_names = {p.relative_to(assets).as_posix() for p in assets.rglob('*') if p.is_file()}
assert {'display/counter.json', 'icons/even.svg', 'icons/odd.svg'} <= asset_names
for name in asset_names:
    assert not Path(name).is_absolute() and '..' not in Path(name).parts
ids = set()
for kind in STARTER_KINDS:
    directory = ROOT / f'sdk-example-{kind}'
    manifest = json.loads((directory / 'manifest.json').read_text())
    for identity in [manifest['UUID'], manifest['Actions'][0]['UUID']]:
        assert identity not in ids, f'duplicate identity: {identity}'
        ids.add(identity)
    for name in ['README.md', 'icon.png', 'inspector.html', 'inspector.js']:
        assert (directory / name).is_file(), f'missing {kind}/{name}'
    assert manifest['Version'] == '0.1.1'
    assert manifest['SDKVersion'] == 1
    assert manifest['StreamDockManager']['ManifestDialect'] == 'vsd'
    assert [state['Title'] for state in manifest['Actions'][0]['States']] == ['Even', 'Odd']
    ext = 'svg' if kind in ('nodejs',) else 'png'
    assert [state['Image'] for state in manifest['Actions'][0]['States']] == [f'icons/even.{ext}', f'icons/odd.{ext}']
    for state in manifest['Actions'][0]['States']:
        assert state['Image'] in asset_names
    runtime = manifest['StreamDockManager'].get('Runtime')
    if kind in ('nodejs',):
        assert runtime['Kind'] == kind
        assert runtime['Channel'] == 'lts'
        assert (directory / 'main.mjs').is_file()
        assert 'https://' not in (directory / 'main.mjs').read_text()
    else:
        assert runtime is None
        assert native_binary_name(manifest) == f'sdk-example-{kind}-plugin'
    for package in (ROOT / 'artifacts').glob(f'sdk-example-{kind}-*.zip'):
        with zipfile.ZipFile(package) as archive:
            names = archive.namelist()
            prefix = manifest['UUID'] + '.sdPlugin/'
            assert all(name.startswith(prefix) and '..' not in Path(name).parts for name in names)
            contents = {name[len(prefix):] for name in names}
            assert {'manifest.json', 'icon.png', 'inspector.html', 'inspector.js'} <= contents
            assert (asset_names <= contents if kind not in ('nodejs',) else {'display/counter.json','icons/even.svg','icons/odd.svg'} <= contents), f'missing structured display assets in {package.name}'
            packaged_manifest = json.loads(archive.read(prefix + 'manifest.json'))
            assert packaged_manifest == distribution_manifest(manifest), f'stale manifest in {package.name}'
            for name in asset_names & contents:
                assert archive.read(prefix + name) == (assets / name).read_bytes(), f'stale asset {name}'
            assert not any(name.startswith(('node_modules/', 'target/', '.venv/')) for name in contents)
            entry = manifest['CodePathWin'] if any(name.endswith('.exe') for name in names) else manifest['CodePath']
            assert entry in contents, f'missing packaged entry {entry}'
print('All four example manifests and available package contents passed')

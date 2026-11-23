#!/usr/bin/env python3
"""Offline generic project/package checks, separate from maintained counter checks."""
import argparse
import json
from pathlib import Path
import re
import runpy
import stat
import zipfile
from vault_setup import canonical_permissions, validate_setup_slots
from descriptor import capabilities, inspect
from profiles import package_profiles, validate_profiles
from project import SCRIPT_KINDS, SOURCE_FILES, distribution_manifest, identity, native_binary_name, project_inputs, regular, relative, tree_files

ROOT = Path(__file__).resolve().parent


# Validate project
def validate_project(project, archive=None, host="manager"):
    if host not in ("manager", "opendeck"):
        raise ValueError("Unsupported package host")
    source, kind, manifest = project_inputs(project)
    if not isinstance(manifest.get('Name'), str) or not manifest['Name'].strip():
        raise ValueError('Manifest Name is required')
    if manifest.get('StreamDockManager', {}).get('ManifestDialect') not in ('vsd', 'streamdeck'):
        raise ValueError('Explicit supported ManifestDialect is required')
    if 'Nodejs' in manifest:
        raise ValueError('Use explicit StreamDockManager.Runtime metadata')
    version = str(manifest.get('Version', ''))
    if not re.fullmatch(r'[0-9]+(?:\.[0-9]+){0,3}', version) or any(int(part) > 2**64 - 1 for part in version.split('.')):
        raise ValueError('Manifest Version needs one to four numeric components, matching Manager')
    for name in SOURCE_FILES[kind] + ['icon.png', 'inspector.html', 'inspector.js', 'LICENSE', 'THIRD_PARTY_NOTICES.md', 'dependencies.lock.json']:
        regular(source / name)
    assets = tree_files(source / 'assets')
    runtime = manifest.get('StreamDockManager', {}).get('Runtime')
    if kind in SCRIPT_KINDS:
        if not runtime or runtime.get('Kind') != kind or runtime.get('Channel') != 'lts':
            raise ValueError('Script runtime declaration does not match the selected language')
    elif runtime:
        raise ValueError('Packaged native projects must not require an interpreter')
    if kind not in SCRIPT_KINDS:
        native_binary_name(manifest)
    actions = manifest.get('Actions')
    if not isinstance(actions, list) or not actions or len(actions) > 256:
        raise ValueError('Declare 1–256 actions')
    identities = {identity(manifest['UUID'])}
    expected = {'manifest.json': source / 'manifest.json', 'icon.png': source / 'icon.png', 'inspector.html': source / 'inspector.html', 'inspector.js': source / 'inspector.js', 'LICENSE': source / 'LICENSE', 'THIRD_PARTY_NOTICES.md': source / 'THIRD_PARTY_NOTICES.md', **assets}
    if kind in SCRIPT_KINDS and archive is not None:
        from javascript import verify
        expected.update(verify(source)[0])
    for icon in [manifest.get('Icon'), manifest.get('CategoryIcon'), *(a.get('Icon') for a in actions)]:
        relative(icon)
        if icon not in expected: raise ValueError(f'Unpackaged exact icon path: {icon}')
    for name in ('CodePath', 'CodePathWin'):
        relative(manifest.get(name, manifest.get('CodePath')))
    for action in actions:
        action_id = identity(action.get('UUID'))
        if action_id in identities:
            raise ValueError('Duplicate plugin/action identity')
        if not action_id.startswith(manifest['UUID'] + '.'):
            raise ValueError('Action identity must use the plugin namespace')
        identities.add(action_id)
        inspector = action.get('PropertyInspectorPath', manifest.get('PropertyInspectorPath'))
        if inspector:
            relative(inspector)
            if inspector not in expected:
                raise ValueError(f'Unpackaged property inspector: {inspector}')
        for state in action.get('States', []):
            image = state.get('Image')
            if image:
                relative(image)
                if image not in expected:
                    raise ValueError(f'Unpackaged state image: {image}')
    capabilities(manifest)
    validate_setup_slots(manifest)
    suggestions = package_profiles(source, manifest)
    if archive is not None:
        prefix = manifest['UUID'] + '.sdPlugin/'
        with zipfile.ZipFile(archive) as package:
            archived = json.loads(package.read(prefix + "manifest.json"))
            validate_setup_slots(archived)
            if canonical_permissions(archived) != canonical_permissions(manifest):
                raise ValueError('Missing or stale packaged Manager permissions')
            if archived.get("StreamDockManager", {}).get("SetupSlots") != manifest.get("StreamDockManager", {}).get("SetupSlots"):
                raise ValueError('Missing or stale packaged Manager SetupSlots')
            if archived.get("CodePaths"):
                raise ValueError("Unified archive: validate with tools/unified.py --validate <archive>; this command validates OS-specific source packages")
            infos = package.infolist()
            if len(infos) > 1024 or sum(info.file_size for info in infos) > 256 * 1024 * 1024 or any(info.file_size > 64 * 1024 * 1024 for info in infos):
                raise ValueError('Package exceeds its bounded inventory')
            names = [info.filename for info in infos]
            if len(set(name.lower() for name in names)) != len(names):
                raise ValueError('Duplicate archive entries')
            for info in infos:
                relative(info.filename)
                if not info.filename.startswith(prefix) or stat.S_ISLNK(info.external_attr >> 16):
                    raise ValueError('Package must contain one regular plugin root')
            contents = {name[len(prefix):] for name in names}
            if any(name.startswith('licenses/') for name in contents):
                raise ValueError('Blanket licenses/ folders must not be packaged; use collected runtime notices')
            if kind in SCRIPT_KINDS:
                expected = {name:path for name,path in expected.items() if name not in assets or name in contents}
            for name, path in expected.items():
                if prefix + name not in names or name != 'manifest.json' and package.read(prefix + name) != regular(path):
                    raise ValueError(f'Missing or stale packaged file: {name}')
            for name, data in suggestions.items():
                if prefix + name not in names or package.read(prefix + name) != data:
                    raise ValueError('Missing or stale suggested profile: ' + name)
            validate_profiles({name[len(prefix):]: package.read(name) for name in names if name.startswith(prefix + 'profiles/') or name == prefix + 'profile.json'}, manifest)
            from audit import resource_graph
            resource_graph({name:package.read(prefix+name) for name in contents},manifest)
            packaged = json.loads(package.read(prefix + 'manifest.json'))
            target = packaged.get('OS', [{}])[0].get('Platform') if 'OS' in manifest else None
            expected_manifest = distribution_manifest(manifest, target)
            if host == 'opendeck':
                if kind not in SCRIPT_KINDS:
                    raise ValueError('OpenDeck native packages must be universal; validate with unified.py --host opendeck')
                from openaction import export_universal_script_manifest
                expected_manifest = export_universal_script_manifest(manifest, {name: package.read(prefix + name) for name in contents})
            if packaged != expected_manifest:
                raise ValueError('Packaged manifest differs from the selected project')
            entry = manifest.get('CodePathWin', manifest['CodePath']) if any(name.endswith('.exe') for name in contents) else manifest['CodePath']
            if (kind in ('go', 'rust', 'python', 'cpp') or kind in SCRIPT_KINDS and manifest.get('StreamDockManager', {}).get('Capabilities')) and entry in contents:
                inspect(package.read(prefix + entry), capabilities(manifest))
            if entry not in contents:
                raise ValueError(f'Missing packaged entry: {entry}')
            if any(name.startswith(('node_modules/', '.venv/', 'target/', 'build/')) for name in contents):
                raise ValueError('Development dependencies must not be packaged')
    return {'identity': manifest['UUID'], 'language': kind, 'version': manifest['Version'], 'actions': len(actions), 'package': str(archive) if archive else None, 'execution': 'none; offline static validation only'}


# Run the validate command and enforce its input options
def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--project', type=Path)
    parser.add_argument('--package', type=Path)
    parser.add_argument('--host', choices=['manager', 'opendeck'], default='manager')
    args = parser.parse_args()
    project = args.project
    if project is None and (ROOT.parent / 'plugin-project.json').is_file():
        project = ROOT.parent
    if project is None:
        if args.package:
            parser.error('--package requires --project')
        runpy.run_path(str(ROOT / 'validate_examples.py'), run_name='__main__')
    else:
        print(json.dumps(validate_project(project, args.package, host=args.host), indent=2))


if __name__ == '__main__':
    main()

#!/usr/bin/env python3
"""Build/package a selected project. Only an explicit build command runs author tools."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import subprocess
import sys
import zipfile
from audit import collect, license_text, resource_graph
from profiles import package_profiles
from descriptor import capabilities, inspect, prepare
from project import KINDS, STARTER_KINDS, SCRIPT_KINDS, bundled_runtime_files, distribution_manifest, identity, native_binary_name, package_context, regular, tree_files

TOOLS = Path(__file__).resolve().parent
ROOT = TOOLS.parent


# Drive the package execution loop
def run(args, cwd):
    subprocess.run(args, cwd=cwd, check=True)


# Package package data
def package(kind=None, build=True, project=None, output=None, audit=False, force=False, host="manager", inputs=None):
    if host not in ("manager", "opendeck"):
        raise ValueError("Unsupported package host")
    if inputs:
        if host != "opendeck" or kind or project:
            raise ValueError('--input requires --host opendeck without a project/language selection')
        from unified import merge, read_package
        _, _, manifest = read_package(next(iter(inputs.values())))
        identity(manifest['UUID'])
        directory = Path(output).resolve() if output else ROOT / 'artifacts'
        archive = merge(inputs, directory / (manifest['UUID'] + '-opendeck-universal.zip'), host='opendeck')
        print(archive)
        return archive
    if project is not None:
        from validate import validate_project
        validate_project(project)
    layout = package_context(ROOT, kind, project)
    source, kind, manifest, common = layout.source, layout.kind, layout.manifest, layout.common
    identity(manifest['UUID'])
    native = kind not in SCRIPT_KINDS
    if host == 'opendeck' and native:
        raise ValueError('OpenDeck requires one universal package: build Manager-format target archives on macOS, Windows and Linux, then supply all three with --input OS/ARCH=ZIP')
    binary_name = native_binary_name(manifest) if native else None
    exe = binary_name + ('.exe' if os.name == 'nt' else '') if native else None
    descriptor = prepare(source, manifest, kind)
    script_bundle = None
    if kind in SCRIPT_KINDS:
        from javascript import bundle, verify
        script_bundle, script_report = bundle(source, kind, force=force) if build else verify(source)
    if build:
        if kind == 'go':
            (source / 'build').mkdir(exist_ok=True)
            run(['go', 'build', '-trimpath', '-ldflags=-s -w -X github.com/StreamDockManager/Plugin-SDK/Go.CompiledDescriptor=' + descriptor, '-o', str(source / 'build' / exe), '.'], source)
        elif kind == 'rust':
            run(['cargo', 'build', '--locked', '--release', '--bin', binary_name] + (['--config', os.environ['STREAM_DOCK_MANAGER_SDK_CARGO_CONFIG']] if os.environ.get('STREAM_DOCK_MANAGER_SDK_CARGO_CONFIG') else []), source)
        elif kind == 'cpp':
            run(['cmake', '-S', '.', '-B', 'build', '-DCMAKE_BUILD_TYPE=Release'], source)
            run(['cmake', '--build', 'build', '--config', 'Release'], source)
        elif kind == 'python':
            run([sys.executable, '-m', 'PyInstaller', '--clean', '--noconfirm', 'counter.spec'], source)
    binary = None
    if native:
        binary = {'go': source / 'build' / exe, 'rust': source / 'target' / 'release' / exe, 'python': source / 'dist' / binary_name / exe, 'cpp': source / 'build' / ('Release' if os.name == 'nt' else '') / exe}[kind]
    files = {name: source / name for name in ['manifest.json', 'icon.png', 'inspector.html', 'inspector.js']}
    assets = tree_files(common / 'assets')
    files.update(assets)
    files['LICENSE'] = layout.license
    files['THIRD_PARTY_NOTICES.md'] = common / 'THIRD_PARTY_NOTICES.md'
    if native:
        if not binary.is_file():
            raise RuntimeError(f'Missing {binary}; explicitly build this project on the target toolchain first')
        files['bin/' + exe] = binary
        if kind == 'python':
            files.update({'bin/' + name: path for name, path in bundled_runtime_files(binary.parent).items()})
    else:
        files.update(script_bundle)
    # Materialize every bounded input before replacing an existing archive
    contents = {name: regular(path, 64 * 1024 * 1024 if name.startswith('bin/') else 16 * 1024 * 1024) for name, path in files.items()}
    if native and descriptor is not None:
        inspect(contents['bin/' + exe], capabilities(manifest))
    contents.update(package_profiles(source, manifest))
    contents['manifest.json'] = (json.dumps(manifest if host == 'opendeck' else distribution_manifest(manifest), indent=4) + '\n').encode()
    manifest_bytes=contents['manifest.json']
    resources=resource_graph(contents,json.loads(manifest_bytes))
    audit_path=source/'build/runtime-audit.json'
    artifact_sha256=hashlib.sha256(json.dumps({name:hashlib.sha256(data).hexdigest() for name,data in sorted(contents.items()) if name.startswith('bin/') or kind in SCRIPT_KINDS and name.endswith('.mjs')},sort_keys=True).encode()).hexdigest()
    dependency_audit=None
    if kind in SCRIPT_KINDS:
        contents['bundle-report.json']=(json.dumps(script_report,indent=2)+'\n').encode()
        # SDK/browser assets needed by manifests, inspector HTML, or explicit dynamic declarations
        selected = {record['path'] for record in resource_graph(contents, manifest, script_report['settings'].get('assets', []), reachable_only=True)}
        selected.update(script_report['settings'].get('assets', []))
        contents = {name:data for name,data in contents.items() if name not in assets or name in selected}

    if kind in SCRIPT_KINDS:
        dependency_audit={'version':1,'language':kind,'artifact_sha256':artifact_sha256,'dependencies':script_report.get('dependencies',[]),'scope':'Actual bundled module graph; runtime built-ins supplied by the selected Manager runtime'}
    if kind in ('go','rust','python','cpp') and (build or audit):
        dependency_audit=collect(source,kind)
        dependency_audit['artifact_sha256']=artifact_sha256
        audit_path.parent.mkdir(exist_ok=True)
        audit_path.write_text(json.dumps(dependency_audit,indent=2)+'\n')
    elif audit_path.is_file():
        dependency_audit=json.loads(audit_path.read_text())
        if dependency_audit.get('artifact_sha256')!=artifact_sha256: raise ValueError('Runtime audit belongs to another executable; rebuild or repackage with --audit')
    if dependency_audit:
        if kind not in SCRIPT_KINDS: contents['RUNTIME_LICENSES.txt']=license_text(dependency_audit)
        contents['runtime-dependencies.json']=(json.dumps(dependency_audit,indent=2)+'\n').encode()
    if kind == 'python':
        contents['RUNTIME_LICENSES.txt'] = (contents.get('RUNTIME_LICENSES.txt', b'')
            + b'\n\nFrozen inspect hook, adapted from PyInstaller. Copyright 2021-2023 PyInstaller Development Team. Apache-2.0.\n\n'
            + regular(source / 'frozen-inspect-LICENSE.txt'))
    if host == 'opendeck':
        from openaction import export_universal_script_manifest
        exported = export_universal_script_manifest(json.loads(contents['manifest.json']), contents, materialize_aliases=True)
        contents['manifest.json'] = (json.dumps(exported, indent=4) + '\n').encode()
    contents['package-audit.json']=(json.dumps(dict(version=1,resources=resources,runtime_dependencies='collected' if dependency_audit else 'not collected; repackage with --audit'),indent=2)+'\n').encode()
    if len(contents) > 1024 or sum(map(len, contents.values())) > 256 * 1024 * 1024:
        raise ValueError('Package exceeds its bounded inventory')
    directory = Path(output).resolve() if output else layout.artifacts
    directory.mkdir(parents=True, exist_ok=True)
    label = layout.label + ("-opendeck" if host == "opendeck" else "")
    archive_path = directory / (f'{label}-universal.zip' if host == 'opendeck' else f'{label}-{sys.platform}-{platform.machine()}.zip')
    import tempfile
    from buildcache import fingerprint, cached, remember
    package_key = fingerprint(contents, {'policy':'zip-deflate-level6-v2','python':sys.version})
    if not force and cached(archive_path, package_key):
        print(archive_path)
        return archive_path
    handle, temporary = tempfile.mkstemp(prefix='.package-', suffix='.zip', dir=directory)
    os.close(handle)
    try:
        with zipfile.ZipFile(temporary, 'w', zipfile.ZIP_DEFLATED, compresslevel=6) as archive:
            for name, data in contents.items():
                info = zipfile.ZipInfo(manifest['UUID'] + '.sdPlugin/' + name, (2026, 1, 1, 0, 0, 0))
                info.external_attr = (0o100755 if name.startswith('bin/') else 0o100644) << 16
                archive.writestr(info, data, compress_type=zipfile.ZIP_DEFLATED, compresslevel=6)
        os.replace(temporary, archive_path)
        remember(archive_path, package_key)
    finally:
        Path(temporary).unlink(missing_ok=True)
    print(archive_path)
    return archive_path


# Run the package command and enforce its input options
def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('kind', nargs='?', choices=STARTER_KINDS + ['all'])
    parser.add_argument('--project', type=Path)
    parser.add_argument('--output', type=Path)
    parser.add_argument('--host', choices=['manager', 'opendeck'], default='manager', help='Explicit host manifest export; default preserves Manager format')
    parser.add_argument('--input', action='append', metavar='OS/ARCH=ZIP', help='Native OpenDeck universal inputs; requires macOS, Windows and Linux')
    parser.add_argument('--no-build', action='store_true')
    parser.add_argument('--force',action='store_true',help='Rebuild/repackage without verified caches')
    parser.add_argument('--audit', action='store_true', help='Collect actual provisioned runtime dependencies when repackaging')
    args = parser.parse_args()
    inputs = {}
    for item in args.input or []:
        target, separator, path = item.partition('=')
        if not separator or not path or target in inputs:
            parser.error('Inputs must be unique OS/ARCH=ZIP values')
        inputs[target] = Path(path)
    if args.host == 'opendeck' and args.kind == 'all':
        parser.error('OpenDeck requires per-plugin universal inputs; all cannot build foreign native targets')
    if args.project and args.kind == 'all':
        parser.error('--project selects one standalone project')
    for kind in STARTER_KINDS if args.kind == 'all' else [args.kind]:
        package(kind, not args.no_build, args.project, args.output, audit=args.audit,force=args.force,host=args.host,inputs=inputs)


if __name__ == '__main__':
    main()

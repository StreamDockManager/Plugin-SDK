"""Shared offline project inputs; importing this module never builds or executes code."""
from authoring_common import distribution_manifest, identity, relative, regular, read_json, tree_files

import json
from dataclasses import dataclass
from pathlib import Path, PurePosixPath
import re
import stat
import sys

STARTER_KINDS = ['nodejs', 'python', 'go', 'rust']
# C++ remains supported for independent projects, not as a bundled starter
KINDS = STARTER_KINDS + ['cpp']
SCRIPT_KINDS = {'nodejs'}
SOURCE_FILES = {
    'nodejs': ['main.mjs', 'counter.mjs', 'package.json'],
    'python': ['main.py', 'requirements.txt', 'counter.spec', 'frozen_inspect.py', 'frozen-inspect-LICENSE.txt'],
    'go': ['main.go', 'go.mod', 'go.sum'],
    'rust': ['main.rs', 'Cargo.toml', 'Cargo.lock', 'descriptor.txt'],
    'cpp': ['main.cpp', 'CMakeLists.txt', 'apply-envelope.cmake', 'stream-dock-manager-envelope.patch'],
}








# Use the stable ID slug, independently of the display name or action names
def default_binary_name(plugin_id):
    """Use the stable ID slug, independently of the display name or action names."""
    slug = identity(plugin_id).rsplit('.', 1)[-1]
    return slug if slug.endswith('-plugin') else slug + '-plugin'


# Native tooling follows the manifest, including existing/custom basenames
def native_binary_name(manifest):
    """Native tooling follows the manifest, including existing/custom basenames."""
    path = relative(manifest.get('CodePath'))
    name = path.name
    if (len(path.parts) != 2 or path.parts[0] != 'bin'
            or not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9._-]{0,199}', name)
            or name.endswith('.') or name.lower().endswith('.exe')):
        raise ValueError('Native CodePath must be bin/<portable executable name>, without .exe')
    if manifest.get('CodePathWin') != str(path) + '.exe':
        raise ValueError('Native CodePathWin must match CodePath with an .exe suffix')
    for key in ('CodePathMac', 'CodePathLinux'):
        if key in manifest and manifest[key] != str(path):
            raise ValueError(f'Native {key} must match CodePath')
    return name






# Validate standalone project metadata and resolve its source files
def project_inputs(path):
    source = Path(path).resolve(strict=True)
    metadata = read_json(source / 'plugin-project.json')
    if metadata.get('schema_version') != 1 or metadata.get('language') not in KINDS:
        raise ValueError('Unsupported plugin-project.json schema or language')
    manifest = read_json(source / 'manifest.json')
    if identity(metadata.get('identity')) != identity(manifest.get('UUID')):
        raise ValueError('Project and manifest identities differ')
    return source, metadata['language'], manifest




# Materialize compiler output aliases as regular ZIP files, within its root
def bundled_runtime_files(root):
    """Materialize compiler output aliases as regular ZIP files, within its root.

    PyInstaller uses library/framework symlinks on POSIX. Plugin archives reject
    symlinks, so retain each library-relative alias without following external or
    cyclic links. Project source/assets keep the stricter tree_files contract.
    """
    root = Path(root)
    if root.is_symlink() or not root.is_dir():
        raise ValueError(f'Expected a local runtime bundle: {root}')
    root = root.resolve(strict=True)
    files = {}
    visited = 0

    # Traverse the current input entry while enforcing collection constraints
    def visit(path, name, ancestors):
        nonlocal visited
        visited += 1
        if visited > 4096 or len(ancestors) > 64:
            raise ValueError('Runtime bundle exceeds its traversal bound')
        target = path.resolve(strict=True)
        if not target.is_relative_to(root):
            raise ValueError(f'Runtime bundle link escapes its build output: {path}')
        if target.is_dir():
            if target in ancestors:
                raise ValueError(f'Cyclic runtime bundle link: {path}')
            for child in sorted(target.iterdir()):
                visit(child, name + '/' + child.name if name else child.name, ancestors | {target})
        else:
            relative(name)
            # Verify regular files here; bytes/size are checked again before ZIP publication
            if not stat.S_ISREG(target.stat().st_mode):
                raise ValueError(f'Invalid runtime bundle entry: {path}')
            files[name] = target
            if len(files) > 1024:
                raise ValueError('Runtime bundle exceeds 1024 files')

    visit(root, '', set())
    return files


@dataclass(frozen=True)
class PackageContext:
    source: Path
    kind: str
    manifest: dict
    common: Path
    license: Path
    artifacts: Path
    label: str


# Resolve maintained and standalone layouts once, before packaging work
def package_context(examples, kind=None, project=None):
    """Resolve maintained and standalone layouts once, before packaging work."""
    examples = Path(examples)
    if project is not None:
        source, selected, manifest = project_inputs(project)
        if kind is not None and kind != selected:
            raise ValueError('Language does not match the project')
        return PackageContext(source, selected, manifest, source, source / 'LICENSE',
                              source / 'artifacts', manifest['UUID'])
    if kind not in STARTER_KINDS:
        raise ValueError('Select a language or an explicit --project')
    source = examples / f'sdk-example-{kind}'
    manifest = read_json(source / 'manifest.json')
    return PackageContext(source, kind, manifest, examples, examples.parent / 'LICENSE',
                          examples / 'artifacts', f'sdk-example-{kind}')

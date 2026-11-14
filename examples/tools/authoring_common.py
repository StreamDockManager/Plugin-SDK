# SPDX-License-Identifier: MIT
"""SDK authoring primitives v1. Synced consumers retain their own policy wrappers."""
import base64
import json
import os
from pathlib import Path, PurePosixPath
import re
import stat
import sys

AUTHORING_PRIMITIVES_VERSION = 1


# A native package declares only its target; Linux is a Manager extension
def distribution_manifest(manifest, target=None):
    """A native package declares only its target; Linux is a Manager extension."""
    if 'OS' not in manifest:
        return manifest
    target = target or {'darwin': 'mac', 'win32': 'windows', 'linux': 'linux'}.get(sys.platform)
    platforms = [item for item in manifest['OS'] if item.get('Platform') == target]
    if not platforms:
        raise ValueError(f'Manifest does not declare the package target {target}')
    return dict(manifest, OS=platforms)

# Validate the plugin identifier before using it in manifests or paths
def identity(value):
    if not isinstance(value, str) or len(value) > 180 or not re.fullmatch(r'[a-z][a-z0-9-]*(?:\.[a-z][a-z0-9-]*)+', value):
        raise ValueError('Use a lowercase reverse-domain identity, such as com.example.my-plugin')
    return value

# Validate a relative archive path before joining it to a trusted root
def relative(value):
    if not isinstance(value, str) or not value or '\\' in value or '\x00' in value or ':' in value:
        raise ValueError(f'Invalid project-relative path: {value!r}')
    path = PurePosixPath(value)
    if path.is_absolute() or any(part in ('', '.', '..') for part in value.split('/')):
        raise ValueError(f'Invalid project-relative path: {value!r}')
    return path

# Read a bounded snapshot from one verified handle, never an unchecked reread
def regular(path, limit=16 * 1024 * 1024):
    """Read a bounded snapshot from one verified handle, never an unchecked reread."""
    path = Path(path)
    before = path.lstat()
    if not stat.S_ISREG(before.st_mode) or before.st_size > limit:
        raise ValueError(f'Expected bounded regular source file: {path}')
    flags = os.O_RDONLY | getattr(os, 'O_NOFOLLOW', 0) | getattr(os, 'O_NONBLOCK', 0)
    fd = os.open(path, flags)
    with os.fdopen(fd, 'rb') as source:
        opened = os.fstat(source.fileno())
        if (not stat.S_ISREG(opened.st_mode) or opened.st_size > limit
                or (before.st_dev, before.st_ino) != (opened.st_dev, opened.st_ino)):
            raise ValueError(f'Expected bounded regular source file: {path}')
        data = source.read(limit + 1)
        if len(data) > limit:
            raise ValueError(f'Expected bounded regular source file: {path}')
        return data


# Read json
def read_json(path):
    return json.loads(regular(path, 1024 * 1024))

# Enumerate bounded regular source files without unsafe traversal
def tree_files(root):
    root = Path(root)
    if root.is_symlink() or not root.is_dir():
        raise ValueError(f'Expected a local asset directory: {root}')
    files = {}
    for item in sorted(root.rglob('*')):
        if item.is_symlink():
            raise ValueError(f'Symlinks are not project assets: {item}')
        if item.is_file():
            name = item.relative_to(root).as_posix()
            relative(name)
            files[name] = item
            if len(files) > 1024:
                raise ValueError('Project asset inventory exceeds 1024 files')
    return files

# Build the bounded wire envelope for the supplied payload
def frame(value):
    data = json.dumps(value, separators=(',', ':'), ensure_ascii=False).encode()
    if len(data) > 64 << 10:
        raise ValueError('descriptor exceeds limit')
    encoded = base64.b64encode(data).decode()
    return f'SDMCAP1:{len(encoded):08x}:{encoded}:ENDSDMCAP1'

# Inspect authoring common data
def inspect(data, expected):
    prefix, suffix = b'SDMCAP1:', b':ENDSDMCAP1'
    matches = []
    start = 0
    while True:
        offset = data.find(prefix, start)
        if offset < 0:
            break
        start = offset + len(prefix)
        header = data[start:start + 9]
        if len(header) != 9 or header[8:] != b':' or any(c not in b'0123456789abcdefABCDEF' for c in header[:8]):
            continue
        length = int(header[:8], 16)
        end = start + 9 + length
        if length > 128 << 10 or data[end:end + len(suffix)] != suffix:
            raise ValueError('invalid compiled capability descriptor')
        raw = base64.b64decode(data[start + 9:end], validate=True)
        if len(raw) > 64 << 10:
            raise ValueError('descriptor exceeds limit')
        value = json.loads(raw)
        if value != expected:
            raise ValueError('compiled capabilities disagree with manifest')
        matches.append(value)
    if not matches:
        raise ValueError('executable has no compiled capability descriptor')

# Load and verify the required third-party license notices
def notices(root, name, declared=None):
    root = Path(root)
    candidates = [root / declared] if declared else []
    candidates += sorted({path for pattern in ('LICENSE*', 'LICENCE*', 'COPYING*', 'NOTICE*') for path in root.glob(pattern) if path.is_file()})
    found = {}
    for path in candidates:
        if not path.is_file(): continue
        data = regular(path, 1 << 20)
        if len(data) > 1 << 20: raise ValueError(f'{name}: license exceeds 1 MiB')
        found[path.name] = data.decode('utf-8', errors='strict')
    if not any(key.upper().startswith(('LICENSE', 'LICENCE', 'COPYING')) for key in found): raise ValueError(f'{name}: runtime dependency license is missing')
    return found

# Render dependency license notices into packaged text
def license_text(audit):
    output = []
    for dependency in audit['dependencies']:
        for name, text in sorted(dependency['licenses'].items()): output.append(f"\n===== {dependency['name']} {dependency['version']} ({name}) =====\n{text}\n")
    return ''.join(output).encode()

# Add navigation
def add_navigation(pages, buttons):
    if len(pages) < 2:
        return
    if len(buttons) < 2:
        raise ValueError('Multiple pages require two navigation controls')
    for index, page in enumerate(pages):
        for control, title, target in ((buttons[-2], 'Previous page', pages[(index-1) % len(pages)]), (buttons[-1], 'Next page', pages[(index+1) % len(pages)])):
            if control['id'] in page['bindings']:
                raise ValueError('Navigation would overwrite a suggested binding')
            page['bindings'][control['id']] = dict(title=title, color='#00000000', action=dict(kind='page', page_id=target['id']))

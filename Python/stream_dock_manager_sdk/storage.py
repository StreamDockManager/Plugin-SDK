"""Atomic, explicitly versioned local state; host settings stay host-owned."""
import json
import math
import os
from pathlib import Path
from ._atomicfile import write as atomic_write
import time
import re
import sys
import stat
import errno

MAXIMUM_BYTES = 8 << 20


# Windows can report a blocked ancestor as a missing leaf
def _missing_parent(path):
    """Windows can report a blocked ancestor as a missing leaf."""
    parent = path
    while True:
        try:
            parent.lstat()
        except FileNotFoundError:
            if parent.parent == parent: raise
            parent = parent.parent
            continue
        if parent == path:
            raise OSError(errno.EIO, 'existing state path is unavailable', str(path))
        info = parent.stat()
        if not stat.S_ISDIR(info.st_mode):
            raise NotADirectoryError(errno.ENOTDIR, 'state parent is not a directory', str(parent))
        return


# Validate that the value is a finite numeric state value
def _finite_number(text):
    value = float(text)
    if not math.isfinite(value):
        raise ValueError('invalid state JSON number')
    return value


# Write the supplied storage value
def write(path, value):
    path = Path(path)
    data = json.dumps(value, separators=(',', ':'), ensure_ascii=False, allow_nan=False).encode()
    _write_encoded(path, data)
    return data


# Write encoded
def _write_encoded(path, data):
    if len(data) > MAXIMUM_BYTES: raise ValueError('state exceeds 8 MiB')
    path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    atomic_write(path, data, durable=True, prefix='.state-')


class Store:
    # Load versioned local state while retaining dirty and recovery status
    def __init__(self, path, version=1, clock=time.monotonic):
        if type(version) is not int or version != 1: raise ValueError('invalid state version')
        self.path, self.version, self.clock = Path(path), version, clock
        self.value, self.due, self.dirty, self._written = None, None, False, None
        self._load_blocked = False

    # Load store data
    def load(self):
        self._load_blocked = True
        if type(self.version) is not int or self.version != 1: raise ValueError("invalid state version")
        try:
            with self.path.open('rb') as stream: raw = stream.read(MAXIMUM_BYTES + 1)
        except FileNotFoundError:
            _missing_parent(self.path)
            if self._load_blocked: self._written = None
            self._load_blocked = False
            return None
        if len(raw) > MAXIMUM_BYTES:
            self._load_blocked = True
            raise ValueError('state exceeds 8 MiB')
        value = json.loads(raw, parse_float=_finite_number, parse_constant=_finite_number)
        if not isinstance(value, dict) or type(value.get('version')) is not int or value['version'] != 1 or 'data' not in value:
            raise ValueError('unsupported state format: version 1 envelope required')
        value = value['data']
        self.value, self._written = value, raw
        self._load_blocked = False
        return value

    # Apply a state transformation while retaining failed-write safety
    def change(self, value):
        self.value, self.dirty = value, True
        if self.due is None: self.due = self.clock() + .25

    # Flush store data
    def flush(self, force=False):
        if self._load_blocked:
            raise ValueError('saved state is protected: repair or move it and reload before writing')
        if type(self.version) is not int or self.version != 1: raise ValueError("invalid state version")
        if not self.dirty or not force and self.clock() < self.due: return False
        value = dict(version=self.version, data=self.value)
        data = json.dumps(value, separators=(',', ':'), ensure_ascii=False, allow_nan=False).encode()
        if len(data) > MAXIMUM_BYTES: raise ValueError('state exceeds 8 MiB')
        if data != self._written:
            _write_encoded(self.path, data)
            self._written = data
        self.due, self.dirty = None, False
        return True

    # Stop store activity and release owned resources
    def close(self): self.flush(force=True)


# Resolve the plugin-specific state filename under the configuration root
def path_for(root, identity):
    if not isinstance(identity,str) or not re.fullmatch(r'[A-Za-z0-9._-]{1,180}',identity) or '..' in identity:
        raise ValueError('invalid plugin state identity')
    return Path(root)/'stream-dock-manager-plugins'/identity/'state.json'


# Resolve the default local storage location
def default_path(identity):
    root = os.environ.get('STREAM_DOCK_MANAGER_PLUGIN_STATE_DIR')
    if not root:
        if sys.platform=='win32': root=os.environ.get('APPDATA')
        elif sys.platform=='darwin': root=Path.home()/'Library/Application Support'
        else: root=os.environ.get('XDG_CONFIG_HOME') or Path.home()/'.config'
    if not root: raise ValueError('user configuration directory unavailable')
    return path_for(root,identity)

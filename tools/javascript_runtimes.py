"""Resolve concrete test executables, including npm's Windows command shims."""
import os
from pathlib import Path
import shutil


# Resolve the selected JavaScript runtime executable
def executable(name, selected=None):
    path=Path(selected or shutil.which(name) or '')
    if not path.is_file() or path.suffix.lower() in ('.cmd','.bat'):
        raise RuntimeError(f'Provision {name} or set an absolute concrete STREAM_DOCK_MANAGER_{name.upper()}; required conformance cannot be skipped')
    return str(path.resolve())


# Resolve runtime tool paths without changing global configuration
def environment(source=None):
    result=dict(os.environ if source is None else source)
    for key,name in [('NODE','node')]:
        result['STREAM_DOCK_MANAGER_'+key]=executable(name,result.get('STREAM_DOCK_MANAGER_'+key))
    return result

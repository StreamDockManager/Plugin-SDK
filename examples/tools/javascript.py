"""Build/verify script outputs explicitly; launching a plugin never invokes this."""
import hashlib
import json
import os
from pathlib import Path
import subprocess


# Bundle JavaScript and declared assets for the selected runtime
def bundle(project, runtime, force=False):
    project = Path(project)
    subprocess.run([os.environ.get('STREAM_DOCK_MANAGER_NODE', 'node'), str(Path(__file__).with_suffix('.mjs')), str(project), runtime, *(['--force'] if force else [])], check=True)
    return verify(project)


# Verify javascript data
def verify(project):
    project = Path(project)
    output = project / 'build/javascript'
    report = json.loads((output / 'bundle-report.json').read_text())
    for name, expected in report['input_sha256'].items():
        path = Path(name) if Path(name).is_absolute() else project / name
        if name.startswith('asset:'):
            asset_root = project / 'assets' if (project / 'assets').is_dir() else project.parent / 'assets'
            path = asset_root / name[6:]
        if name.startswith('sdk:'):
            root = os.environ.get('STREAM_DOCK_MANAGER_SDK_JS_ROOT') or project / 'node_modules/stream-dock-manager-plugin-sdk'
            if not Path(root).is_dir(): raise ValueError('Provision the SDK dependency or its explicit development override to verify')
            path = Path(root) / name[4:]
        actual=hashlib.sha256(path.read_bytes()).hexdigest() if path.is_file() else None
        if actual != expected: raise ValueError('Stale JavaScript build input: ' + name)
    for name, expected in report['output_sha256'].items():
        path = output / name
        if not path.resolve().is_relative_to(output.resolve()): raise ValueError('JavaScript output escapes bundle')
        if hashlib.sha256(path.read_bytes()).hexdigest() != expected: raise ValueError('Stale JavaScript bundle: ' + name)
    if not report['settings'].get('treeShaking'): raise ValueError('JavaScript packaging requires tree shaking')
    return {name: output / name for name in report['output_sha256']}, report

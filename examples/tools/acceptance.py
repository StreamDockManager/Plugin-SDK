#!/usr/bin/env python3
"""Explicit executable validation through a disposable SDK host; never installs a plugin."""
import argparse
import os
from pathlib import Path
import shutil
import subprocess
from package import ROOT, KINDS, package
from project import STARTER_KINDS, SCRIPT_KINDS, project_inputs
from validate import validate_project


# Run the acceptance command and enforce its input options
def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('kind', nargs='?', choices=STARTER_KINDS)
    parser.add_argument('--project', type=Path)
    parser.add_argument('--host', type=Path, help='Absolute path to a separately built disposable SDK validation host')
    parser.add_argument('--manager', type=Path, help='Explicit Manager source path for the maintained example suite only')
    parser.add_argument('--scenario', choices=['protocol', 'counter'])
    args = parser.parse_args()
    project = args.project
    if project is None and (ROOT / 'plugin-project.json').is_file():
        project = ROOT
    if project is not None:
        if not args.host or not args.scenario:
            parser.error('Standalone executable validation requires explicit --host and --scenario')
        _, kind, _ = project_inputs(project)
        if args.kind and args.kind != kind:
            parser.error('Language does not match the selected project')
    else:
        if not args.kind:
            parser.error('Select --project or one maintained example language')
        kind = args.kind
    scenario = args.scenario or 'counter'
    if args.host:
        if not args.host.is_absolute() or not args.host.is_file():
            parser.error('--host must name an existing absolute executable path')
        host = args.host
    else:
        if not args.manager:
            parser.error('Supply --host or an explicit --manager source checkout')
        manager = args.manager.resolve(strict=True)
        name = 'sdk_protocol_probe' if scenario == 'protocol' else 'sdk_acceptance'
        subprocess.run(['cargo', 'build', '-p', 'stream-dock-manager-plugin-host', '--example', name], cwd=manager, check=True)
        host = manager / 'target' / 'debug' / 'examples' / (name + ('.exe' if os.name == 'nt' else ''))
    env = os.environ.copy()
    if kind in SCRIPT_KINDS:
        name, key = {'nodejs': ('node', 'STREAM_DOCK_MANAGER_NODE')}[kind]
        if key not in env and project is None:
            selected = shutil.which(name)
            if selected:
                env[key] = selected
        if key not in env or not Path(env[key]).is_absolute() or not Path(env[key]).is_file():
            parser.error(f'Supply an explicit compatible absolute {key} executable')
    archive = package(kind, build=False, project=project)
    if project is not None:
        validate_project(project, archive)
    print(f'Executing {scenario} validation with a disposable host; native qualification is separate', flush=True)
    if kind == 'cpp':
        from cpp_transport import validate_archive
        validate_archive(archive)
    subprocess.run([str(host), str(archive)], check=True, env=env)


if __name__ == '__main__':
    main()

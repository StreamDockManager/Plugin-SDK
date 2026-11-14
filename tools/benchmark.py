#!/usr/bin/env python3
"""Bounded real-renderer workloads. Reports in-process SDK work, never hardware FPS."""
import hashlib
import platform
import argparse
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
ROOT=Path(__file__).resolve().parents[1]

# Run the benchmark command and enforce its input options
def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--language',choices=['all','go','rust','python','nodejs'],default='all')
    parser.add_argument('--frames',type=int,default=120);parser.add_argument('--surfaces',type=int,default=14)
    args=parser.parse_args()
    if not 1<=args.frames<=1200 or not 1<=args.surfaces<=14:parser.error('workload outside bounds')
    from javascript_runtimes import environment
    env=environment() if args.language in ('all','nodejs') else os.environ.copy();env['PYTHONPATH']=str(ROOT/'Python')+os.pathsep+env.get('PYTHONPATH','')
    reports=[]
    for language in ['go','rust','python','nodejs'] if args.language=='all' else [args.language]:
        if language=='go':command=['go','run','./examples/benchmark','--frames',str(args.frames),'--surfaces',str(args.surfaces)];cwd=ROOT/'Go'
        elif language=='rust':command=['cargo','run','--quiet','--locked','--manifest-path',str(ROOT/'Rust/Cargo.toml'),'--example','benchmark','--','--frames',str(args.frames),'--surfaces',str(args.surfaces)];cwd=ROOT
        elif language=='python':command=[sys.executable,str(ROOT/'tests/benchmark_python.py'),str(args.frames),str(args.surfaces)];cwd=ROOT
        else:
            key={'nodejs':'NODE'}[language];executable=env.get('STREAM_DOCK_MANAGER_'+key) or shutil.which('node' if language=='nodejs' else language)
            if not executable:raise SystemExit('Required runtime unavailable: '+language)
            command=[executable]+[str(ROOT/'Node/tests/benchmark.mjs'),str(args.frames),str(args.surfaces)];cwd=ROOT
        report=json.loads(subprocess.check_output(command,cwd=cwd,env=env,text=True));report['runtime']=language
        directory=ROOT/({'go':'Go','rust':'Rust','python':'Python'}.get(language,'Node'))
        inventory={path.relative_to(ROOT).as_posix():hashlib.sha256(path.read_bytes()).hexdigest() for path in directory.rglob('*') if path.is_file() and not any(part in {'node_modules','target','dist','build','__pycache__','.cache'} for part in path.relative_to(directory).parts)}
        version_command=['go','version'] if language=='go' else ['rustc','--version'] if language=='rust' else [sys.executable,'--version'] if language=='python' else [executable,'--version']
        report['provenance']={'source_sha256':hashlib.sha256(json.dumps(inventory,sort_keys=True).encode()).hexdigest(),'source_files':inventory,'toolchain':subprocess.check_output(version_command,text=True).strip(),'os':sys.platform,'architecture':platform.machine(),'measurement':'program interval only; build, process startup, host transport and hardware excluded'}
        if report['received_events']!=args.surfaces*(args.frames+1):raise ValueError('workload event count mismatch')
        reports.append(report)
    print(json.dumps(reports,indent=2))

if __name__=='__main__':main()

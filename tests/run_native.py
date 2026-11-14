#!/usr/bin/env python3
"""Run selected built SDK fixtures with an explicitly supplied disposable host."""
import hashlib
import json
import argparse
import platform
from pathlib import Path
import subprocess
import sys
import time


# Run the run native command and enforce its input options
def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--host',required=True,type=Path)
    parser.add_argument('--projects',required=True,type=Path)
    parser.add_argument('--report',type=Path)
    parser.add_argument('--scenario',choices=['counter','extensions','storage'],required=True)
    parser.add_argument('--language',action='append',choices=['go','rust','python','nodejs'],help='Select languages explicitly; default requires every language for the scenario')
    parser.add_argument('--repeat',type=int,default=1,help='Fixed number of fresh host installations; every attempt counts, including failures')
    args=parser.parse_args()
    if sys.platform == 'win32' and not args.host.suffix:
        args.host=args.host.with_suffix('.exe')
    if not args.host.is_absolute() or not args.host.is_file():
        parser.error('--host must be an existing absolute executable')
    languages=['go','rust','python','nodejs']
    if not 1 <= args.repeat <= 100:
        parser.error('--repeat must be between 1 and 100')
    if args.language:
        if any(language not in languages for language in args.language):
            parser.error('language does not implement this scenario')
        languages=[language for language in languages if language in args.language]
    results=[]
    for language in languages:
        archives=list((args.projects/language/'artifacts').glob(f'*-{sys.platform}-{platform.machine()}.zip'))
        if len(archives)!=1:raise ValueError(f'Expected one current-host {language} package')
        for attempt in range(args.repeat):
            started=time.monotonic()
            result=subprocess.run([str(args.host),str(archives[0].resolve()), *(['--storage'] if args.scenario=='storage' else [])],stdout=subprocess.PIPE,stderr=subprocess.STDOUT,text=True)
            print(result.stdout, end='', flush=True)
            results.append(dict(language=language,attempt=attempt+1,required_attempts=args.repeat,scenario=args.scenario,platform=platform.platform(),returncode=result.returncode,seconds=time.monotonic()-started,output=result.stdout,archive=str(archives[0].resolve()),archive_sha256=hashlib.sha256(archives[0].read_bytes()).hexdigest(),host_sha256=hashlib.sha256(args.host.read_bytes()).hexdigest()))
            if args.report:
                args.report.parent.mkdir(parents=True,exist_ok=True)
                args.report.write_text(json.dumps(results,indent=2)+'\n')
    if any(result['returncode'] for result in results):raise SystemExit(1)


if __name__=='__main__':main()

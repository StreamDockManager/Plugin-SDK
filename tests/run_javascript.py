#!/usr/bin/env python3
"""Run one shared implementation under each supported JS runtime; none are optional."""
from pathlib import Path
import sys
import subprocess
ROOT=Path(__file__).resolve().parents[1]

# Run the run javascript command and enforce its input options
def main():
    sys.path.insert(0,str(ROOT/'tools'))
    from javascript_runtimes import environment
    env=environment()
    script=ROOT/'Node/tests/conformance.mjs'
    for kind in ('NODE',):
        command=[env['STREAM_DOCK_MANAGER_'+kind]]+[str(script)]
        subprocess.run(command,cwd=ROOT,env=env,check=True)
    subprocess.run([env['STREAM_DOCK_MANAGER_NODE'],'--test',str(ROOT/'Node/tests/bundles.test.mjs'),str(ROOT/'Node/tests/bundle-verification.test.mjs'),str(ROOT/'Node/tests/settings-core.test.mjs'),str(ROOT/'Node/tests/session-lifecycle.test.mjs'),str(ROOT/'Node/tests/permission-services.test.mjs'),str(ROOT/'Node/tests/vault-values.test.mjs')],env=env,check=True)

if __name__=='__main__': main()

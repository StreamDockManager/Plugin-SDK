#!/usr/bin/env python3
"""Check the complete API inventory; behavioral conformance runs in the library suites."""
import json
from pathlib import Path
import re
ROOT=Path(__file__).resolve().parents[1]

# Run the check parity command and enforce its input options
def main():
    matrix=json.loads((ROOT/'contracts/api-parity.v1.json').read_text())
    if matrix['version']!=1 or set(matrix['languages'])!={'go','rust','python','javascript'}: raise SystemExit('Incomplete parity languages')
    if set(matrix['javascript_runtimes'])!={'nodejs'}: raise SystemExit('Incomplete JavaScript runtime coverage')
    seen=set()
    for feature in matrix['features']:
        if feature['id'] in seen: raise SystemExit('Duplicate parity feature')
        seen.add(feature['id'])
        if set(feature['implementations'])!=set(matrix['languages']): raise SystemExit('Missing implementation: '+feature['id'])
        for language,implementation in feature['implementations'].items():
            source=ROOT/implementation['source']
            if not source.resolve().is_relative_to(ROOT) or not source.is_file(): raise SystemExit('Missing parity source: '+str(source))
            if not re.search(r'\b'+re.escape(implementation['symbol'])+r'\b',source.read_text()): raise SystemExit('Missing API '+language+': '+feature['id'])
        if set(feature.get('conformance',{}))!=set(matrix['languages']):raise SystemExit('Missing conformance cases: '+feature['id'])
        for cases in feature['conformance'].values():
            if not cases:raise SystemExit('Empty required conformance')
            for case in cases:
                source=ROOT/case['source']
                if not source.resolve().is_relative_to(ROOT) or not source.is_file() or not case.get('required') or case['case'] not in source.read_text():raise SystemExit('Missing required case: '+str(case))
    for contract in matrix['contracts']:
        if json.loads((ROOT/contract).read_text())['version']!=1: raise SystemExit('Wrong contract version')
    print(f'{len(seen)} capabilities mapped across all four SDKs; behavioral suites remain mandatory')

if __name__=='__main__':main()

#!/usr/bin/env python3
"""Explicitly synchronize the versioned MIT authoring primitives to a consumer tools directory."""
import argparse
import hashlib
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parent
FILES = ('authoring_common.py', 'authoring_common.LICENSE')


# Refresh authoring primitives from their verified canonical sources
def synchronize(destination, check=False):
    manifest = {'version': 1, 'license': 'MIT', 'source': 'StreamDockManager-Plugins-SDK/examples/tools',
                'sha256': {name: hashlib.sha256((ROOT / name).read_bytes()).hexdigest() for name in FILES}}
    expected = {name: (ROOT / name).read_bytes() for name in FILES}
    # Match the repository JSON formatter so formatting preserves byte-exact sync checks
    expected['authoring_common.v1.json'] = (json.dumps(manifest, indent=4) + '\n').encode()
    for name, data in expected.items():
        target = Path(destination) / name
        if check:
            if not target.is_file() or target.read_bytes() != data:
                raise ValueError(f'Authoring primitives drift: {target}')
        else:
            target.write_bytes(data)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('destination', type=Path, nargs='?', default=ROOT)
    parser.add_argument('--check', action='store_true')
    args = parser.parse_args()
    synchronize(args.destination, args.check)

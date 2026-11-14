#!/usr/bin/env python3
"""Maintain self-contained starter snapshots from examples/inspector canonical sources."""
import argparse
import sys
from pathlib import Path

# Support the existing import-by-path authoring tests as well as direct CLI use
sys.path.insert(0, str(Path(__file__).resolve().parent))
from sync_output import sync_output
ROOT = Path(__file__).resolve().parents[1]
LANGUAGES = ('go', 'rust', 'python', 'nodejs')


# Synchronize sync starter inspectors data
def sync(root=ROOT, *, check=False):
    for name in ('inspector.html', 'inspector.js'):
        data = (root / 'examples/inspector' / name).read_bytes()
        for language in LANGUAGES:
            target = root / 'examples' / f'sdk-example-{language}' / name
            sync_output(target, data, check=check, error=ValueError,
                        diagnostic='Starter inspector drift; run tools/sync_starter_inspectors.py')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--check', action='store_true')
    sync(check=parser.parse_args().check)

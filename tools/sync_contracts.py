#!/usr/bin/env python3
"""Generate/check package-local copies of the shared conformance fixture."""
import argparse
from sync_output import sync_output
import json
from pathlib import Path
ROOT = Path(__file__).resolve().parents[1]


# Run the sync contracts command and enforce its input options
def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--check', action='store_true')
    args = parser.parse_args()
    for name in ('localhost-cert.pem','localhost-key.pem'):
        data=(ROOT/'contracts/fixtures/tls'/name).read_bytes()
        for target in [ROOT/'Rust/testdata/tls'/name,ROOT/'Python/stream_dock_manager_sdk/testdata/tls'/name]:
            sync_output(target, data, check=args.check, diagnostic='Regenerate TLS fixtures')
    cert=(ROOT/'contracts/fixtures/tls/localhost-cert.pem').read_text();key=(ROOT/'contracts/fixtures/tls/localhost-key.pem').read_text()
    text='// Public, disposable test credentials. Never use these outside a loopback test.\nexport const cert = '+json.dumps(cert)+';\nexport const key = '+json.dumps(key)+';\n'
    target=ROOT/'Node/src/test-certificates.ts'
    sync_output(target, text.encode(), check=args.check, diagnostic='Regenerate JS TLS fixtures')
    for name,directories in [('discovery.mjs', ['Go/testdata','Rust/testdata']),('service-peer.mjs',['Rust/testdata'])]:
        source=(ROOT/'tests'/name).read_bytes()
        for directory in directories:
            target=ROOT/directory/name
            sync_output(target, source, check=args.check, diagnostic='Regenerate behavioral peers')
    for name in ('protocol-v1.json', 'authoring-v1.json', 'services-v1.json', 'storage-v1.json', 'extensions-v1.json', 'helpers-v1.json', 'retry-after-v1.json', 'vault-slots-v1.json', 'permissions-v1.json'):
        source = (ROOT / 'contracts/fixtures' / name).read_bytes()
        for target in [ROOT / 'Go/testdata' / name, ROOT / 'Rust/testdata' / name]:
            sync_output(target, source, check=args.check, diagnostic='Regenerate contract fixtures with tools/sync_contracts.py')


if __name__ == '__main__': main()

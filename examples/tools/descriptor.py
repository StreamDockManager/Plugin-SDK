"""Language-neutral descriptor authoring; no runtime, compiler or network imports."""
from authoring_common import frame, inspect

import base64
import json


# Extract the manifest capability descriptor for native framing
def capabilities(manifest):
    declared = manifest.get('StreamDockManager', {}).get('Capabilities')
    if not isinstance(declared, dict) or declared.get('version') != 1:
        raise ValueError('Explicit supported capability descriptor is required')
    if declared.get('id') != manifest['UUID'] or declared.get('plugin_version') != manifest['Version'] or set(declared.get('actions', {})) != {a['UUID'] for a in manifest['Actions']}:
        raise ValueError('manifest and capability identity disagree')
    return declared




# Prepare descriptor data
def prepare(source, manifest, kind):
    if kind not in ('go', 'rust', 'python', 'cpp'):
        return None
    value = capabilities(manifest)
    if value['id'] != manifest['UUID'] or value['plugin_version'] != manifest['Version'] or set(value['actions']) != {a['UUID'] for a in manifest['Actions']}:
        raise ValueError('manifest and capability identity disagree')
    encoded = frame(value)
    if kind == 'rust':
        (source / 'descriptor.txt').write_text(encoded + '\n')
    return encoded




# Generate the C++ capability descriptor header
def cpp_header(manifest, output):
    from pathlib import Path
    value = capabilities(manifest)
    Path(output).write_text('#pragma once\n' +
        'static constexpr char capabilityFrame[] = ' + json.dumps(frame(value)) + ';\n' +
        'static constexpr char capabilityJson[] = ' + json.dumps(json.dumps(value, separators=(',', ':'))) + ';\n')


if __name__ == '__main__':
    import argparse
    from pathlib import Path
    parser = argparse.ArgumentParser()
    parser.add_argument('--cpp-manifest', required=True, type=Path)
    parser.add_argument('--output', required=True, type=Path)
    args = parser.parse_args()
    cpp_header(json.loads(args.cpp_manifest.read_text()), args.output)

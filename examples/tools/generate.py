#!/usr/bin/env python3
"""Generate one standalone starter offline; never invoke dependencies or compilers."""
import argparse
import hashlib
import json
import tomllib
from pathlib import Path
from descriptor import frame
from project import STARTER_KINDS, SCRIPT_KINDS, SOURCE_FILES, default_binary_name, identity, regular, tree_files

TOOLS = Path(__file__).resolve().parent
ROOT = TOOLS.parent


# Serialize the value using the module’s stable JSON representation
def encoded(value):
    return (json.dumps(value, indent=2, ensure_ascii=False) + '\n').encode()


# Generate generate data
def generate(destination, plugin_id, language, name=None):
    plugin_id = identity(plugin_id)
    if language not in STARTER_KINDS:
        raise ValueError('Select a supported language: nodejs, python, go, rust')
    destination = Path(destination).absolute()
    if destination.exists() or destination.is_symlink():
        raise FileExistsError(f'Destination already exists; nothing overwritten: {destination}')
    if not destination.parent.is_dir():
        raise ValueError('Destination parent must already exist')
    name = name or plugin_id.rsplit('.', 1)[-1].replace('-', ' ').title()
    if not isinstance(name, str) or not name.strip() or len(name) > 120 or any(ord(char) < 32 for char in name):
        raise ValueError('Choose a nonempty display name of at most 120 characters')
    template = ROOT / f'sdk-example-{language}'
    inputs = {path: regular(template / path) for path in SOURCE_FILES[language] + ['manifest.json', 'icon.png', 'inspector.html', 'inspector.js']}
    files = dict(inputs)
    files.update({'assets/' + path: regular(source) for path, source in tree_files(ROOT / 'assets').items()})
    files['LICENSE'] = regular(ROOT.parent / 'LICENSE')
    files['THIRD_PARTY_NOTICES.md'] = regular(ROOT / 'THIRD_PARTY_NOTICES.md')
    for tool in ('unified.py', '_atomicfile.py', 'authoring_common.py', 'authoring_common.v1.json', 'authoring_common.LICENSE', 'project.py', 'package.py', 'openaction.py', 'validate.py', 'acceptance.py', 'cpp_transport.py', 'descriptor.py', 'audit.py', 'javascript.py', 'javascript.mjs', 'buildcache.py', 'profiles.py', 'profile-hardware.v1.json'):
        files['tools/' + tool] = regular(TOOLS / tool)
    manifest = json.loads(files['manifest.json'])
    previous_id = manifest['UUID']
    template_version = manifest['Version']
    manifest['UUID'] = plugin_id
    manifest['Name'] = name
    manifest['Version'] = '0.1.0'
    manifest['Category'] = name
    manifest['Description'] = 'Standalone starter; customize the included action and property inspector'
    if language not in SCRIPT_KINDS:
        manifest['CodePath'] = 'bin/' + default_binary_name(plugin_id)
        manifest['CodePathWin'] = manifest['CodePath'] + '.exe'
    for action in manifest['Actions']:
        action['UUID'] = plugin_id + action['UUID'][len(previous_id):]
    descriptor = manifest.get('StreamDockManager', {}).get('Capabilities')
    if descriptor is not None:
        descriptor['id'] = plugin_id
        descriptor['actions'] = {plugin_id + key[len(previous_id):]: value for key, value in descriptor['actions'].items()}
        if language == 'rust':
            files['descriptor.txt'] = (frame(descriptor) + '\n').encode()
    files['manifest.json'] = encoded(manifest)
    from profiles import generate_profiles
    files.update(generate_profiles(manifest))
    for filename in ('package.json',):
        if filename in files:
            package = json.loads(files[filename])
            package['name'] = plugin_id
            package['version'] = '0.1.0'
            files[filename] = encoded(package)
            files['package-lock.json'] = encoded({'name': plugin_id, 'version': '0.1.0', 'lockfileVersion': 3, 'requires': True, 'packages': {'': {'name': plugin_id, 'version': '0.1.0', 'engines': package.get('engines', {}), 'dependencies': package.get('dependencies', {}), 'devDependencies': package.get('devDependencies', {})}}})
    if language == 'go':
        lines = files['go.mod'].decode().splitlines()
        module = next(line for line in lines if line.startswith('module '))
        files['go.mod'] = files['go.mod'].replace(module.encode(), ('module example.com/' + plugin_id).encode(), 1)
    if language == 'rust':
        # Update only the local package, preserving every locked third-party entry
        local_name = plugin_id.replace('.', '-')
        previous = tomllib.loads(files['Cargo.toml'].decode())['package']
        old_name = previous['name']
        old_version = previous['version']
        files['Cargo.toml'] = files['Cargo.toml'].replace(f'name = "{old_name}"'.encode(), f'name = "{local_name}"'.encode(), 1).replace(f'version = "{old_version}"'.encode(), b'version = "0.1.0"', 1)
        files['Cargo.lock'] = files['Cargo.lock'].replace(f'name = "{old_name}"\nversion = "{old_version}"'.encode(), f'name = "{local_name}"\nversion = "0.1.0"'.encode(), 1)
        old_binary = tomllib.loads(inputs['Cargo.toml'].decode())['bin'][0]['name']
        files['Cargo.toml'] = files['Cargo.toml'].replace(f'[[bin]]\nname = "{old_binary}"'.encode(), f'[[bin]]\nname = "{default_binary_name(plugin_id)}"'.encode(), 1)
    lock_inputs = {path: hashlib.sha256(files[path]).hexdigest() for path in ['package-lock.json', 'requirements.txt', 'go.mod', 'go.sum', 'Cargo.toml', 'Cargo.lock', 'CMakeLists.txt'] if path in files}
    files['dependencies.lock.json'] = encoded({'schema_version': 1, 'language': language, 'script_packages': [{'name': 'stream-dock-manager-plugin-sdk', 'version': '0.1.0'}] if language in ('nodejs',) else None, 'locked_inputs_sha256': lock_inputs, 'note': 'JavaScript SDK/tool dependencies require explicit provisioning before compilation. Native dependency versions/commits are pinned in the listed standard manifests and locks; provision them explicitly before building.'})
    files['plugin-project.json'] = encoded({'schema_version': 1, 'language': language, 'identity': plugin_id, **({'javascript': {'assets': ['display/counter.json', 'icons/even.svg', 'icons/odd.svg']}} if language in ('nodejs',) else {})})
    files['template-provenance.json'] = encoded({'schema_version': 1, 'template': f'sdk-example-{language}', 'template_version': template_version, 'source_sha256': {path: hashlib.sha256(data).hexdigest() for path, data in sorted(inputs.items())}})
    files['.gitignore'] = b'artifacts/\nbuild/\ndist/\ntarget/\nnode_modules/\n.venv/\n__pycache__/\n'
    files['README.md'] = readme(name, plugin_id, language).encode()
    # All source reading/validation precedes destination creation. mkdir is the
    # no-overwrite publication guard, including an empty-directory race
    destination.mkdir()
    for path, data in files.items():
        target = destination / path
        target.parent.mkdir(parents=True, exist_ok=True)
        with target.open('xb') as stream:
            stream.write(data)
    return destination


# Render setup and packaging instructions for the generated project
def readme(name, plugin_id, language):
    executable = default_binary_name(plugin_id)
    naming = (f'Native executables are named `bin/{executable}` (`bin/{executable}.exe` on Windows).\n'
              'The default uses the last segment of the plugin ID and adds `-plugin` if needed.\n'
              'The packager follows the manifest entry points; keep Rust’s `[[bin]]` name in sync when customizing them.\n\n') if language not in SCRIPT_KINDS else (
              'Node.js LTS packages retain `main.mjs` and run in the selected interpreter.\n'
              'They do not contain a standalone native executable.\n\n')
    prerequisites = {'nodejs': 'Node.js 22+ (explicitly provision the pinned SDK/esbuild packages)', 'python': 'Python 3.11+, a venv and the pinned requirements.txt build dependencies', 'go': 'Go 1.27.4 and the go.sum-locked module', 'rust': 'Rust 1.89+, Cargo and the Cargo.lock-locked dependencies'}[language]
    private_go = ('The Go SDK dependency uses an exact version; download it to record its checksums.\n'
                  'For private SDK access, configure GOPRIVATE, GONOPROXY and GONOSUMDB for\n'
                  'github.com/StreamDockManager/Plugin-SDK and authenticate through Git/SSH\n'
                  'or an HTTPS credential helper before building. Keep credentials out of this project.\n\n') if language == 'go' else ''
    javascript = ('Node.js plugins import run from stream-dock-manager-plugin-sdk and optional features from named subpaths.\n'
                  'Before registry publication, install an absolute local SDK candidate with npm install --ignore-scripts --save-exact /absolute/path/stream-dock-manager-plugin-sdk-0.1.0.tgz.\n'
                  'Provision the exact declared esbuild/TypeScript versions, then package. Declare workers and dynamic assets in plugin-project.json.\n'
                  'ZIPs contain bundled ESM, only required assets, bundle-report.json and runtime license notices; no node_modules is needed at launch.\n\n') if language in ('nodejs',) else ''
    helpers = ('The four SDK libraries also provide managed state, correlated discovery, service WebSocket sessions, '
               'observation jobs, shared reads, compact semantic layouts, bounded caches, backend discovery, and external operation history. Thumbnail fit/decode/PNG helpers default to 64 MiB / 32 MP with per-call limits. Thumbnail decoding is optional: '
               'enable Rust thumbnail/service-websocket features, install the Python thumbnail extra, or install '
               'the Node jpeg-js/pngjs/omggif codec peers and ws service peer as needed. Declare Node decoder '
               'workers in plugin-project.json. Keep dependency locks and notices current. '
               'See the SDK language README for exact versions and shared resource/cancellation limits.\n\n')
    return f'''# {name}

Standalone {language} plugin `{plugin_id}`. The included Counter is an editable starting action:
it renders on appearance, increments on key release, saves settings per context, and exposes a
local inspector for prefix/reset. Native packages include their executable; script packages use
a separately selected Manager runtime. Installation stays disabled until explicitly enabled.

Starter code and included authoring tools are [MIT-licensed](LICENSE).
Third-party dependencies retain their own licenses; see [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).

Requires Python 3.11+ for the local tooling and {prerequisites}. Generation is offline and performs
no builds or installations. Provision build dependencies explicitly; packaging native sources
invokes the selected author tools. No Manager checkout or sibling example is needed.

The full SDK Counter uses portable action settings: getSettings/setSettings and
getGlobalSettings/setGlobalSettings remain available on VSD Craft. Base writes
report sent, not durable commit. Private HostStore is a separate optional
plugin-storage-v1 handler; do not add it to required capabilities for this Counter,
map private history into global settings, or retry an uncertain write via a base
setter.

{naming}{private_go}{javascript}{helpers}From this project directory:

```sh
python3 tools/validate.py --project .
python3 tools/package.py --project .
# Repackage a previously built executable without invoking a compiler:
python3 tools/package.py --project . --no-build
# OpenDeck: Node.js exports one portable all-OS archive; native plugins require
# all three prebuilt target archives via --input OS/ARCH=ZIP (see SDK README).
# python3 tools/package.py --host opendeck --input darwin/arm64=mac.zip --input windows/amd64=win.zip --input linux/amd64=linux.zip
```

ZIPs are written under `artifacts/`. Validate one with
`python3 tools/validate.py --project . --package /absolute/path/to/package.zip`.

For optional native cross-platform bundles, `python3 tools/unified.py --help`
combines independently validated Windows, macOS, and Linux target archives.
Keep the OS-specific archives as the default downloads.
For Python, create/activate a development venv and explicitly install `requirements.txt` first;
PyInstaller bundles the interpreter beside the executable in `bin/_internal/`; retain the whole
folder bundle to avoid extraction work at plugin startup. CMake may fetch its pinned dependencies during an explicit
build. Go/Cargo may fetch their pinned dependencies during an explicit build. Runtime launch
never installs dependencies or executes a compiler. Preserve LICENSE, notices and dependency locks.
The dependency inventory and original template fingerprints are recorded locally; update standard
locks explicitly when changing dependencies. There is no global workspace dependency.

Executable validation is a separate explicit opt-in command. Obtain/build a native Manager SDK
validation host separately, then pass its absolute path:

```sh
python3 tools/acceptance.py --project . --host /absolute/path/to/sdk_protocol_probe --scenario protocol
# The maintained Counter scenario is separate and uses its own host:
python3 tools/acceptance.py --project . --host /absolute/path/to/sdk_acceptance --scenario counter
```

The hosts use disposable configurations, never your active installation. Script scenarios require
an explicitly supplied compatible STREAM_DOCK_MANAGER_NODE executable.
The generic probe checks authenticated registration and owned lifecycle; the Counter scenario
checks Counter behavior. Neither qualifies VSD Craft, physical hardware, native UI or another
OS/architecture. Build and qualify distribution artifacts on their declared native targets.
'''


# Run the generate command and enforce its input options
def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--destination', required=True, type=Path)
    parser.add_argument('--identity', required=True)
    parser.add_argument('--language', required=True, choices=STARTER_KINDS)
    parser.add_argument('--name')
    args = parser.parse_args()
    print(generate(args.destination, args.identity, args.language, args.name))


if __name__ == '__main__':
    main()

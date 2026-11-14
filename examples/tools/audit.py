"""Build-time dependency notices and resource audits for Go, Rust, C++ and frozen Python.

This module never downloads, builds, installs or launches a plugin. Its metadata
commands inspect already provisioned dependency graphs in the chosen project.
"""
from authoring_common import notices, license_text

import argparse
import ast
import hashlib
from html.parser import HTMLParser
import importlib.metadata as metadata
import json
import os
import posixpath
from pathlib import Path, PurePosixPath
import subprocess
import sys
import sysconfig


# Drive the packaging execution loop
def run(command, project):
    environment = {**os.environ, 'GOPROXY': 'off', 'GOTOOLCHAIN': 'auto'}
    return subprocess.check_output(command, cwd=project, text=True, env=environment)


# Return digest for packaging
def digest(data): return hashlib.sha256(data).hexdigest()




# Inspect the selected Go module dependency graph
def go_dependencies(project, target=None):
    command = ['go', 'list', '-mod=readonly', '-deps', '-json', '.']
    if target: raise ValueError('Select Go target through explicit GOOS/GOARCH environment values')
    raw, decoder, packages = run(command, project), json.JSONDecoder(), []
    while raw.strip():
        value, consumed = decoder.raw_decode(raw.lstrip())
        raw = raw.lstrip()[consumed:]
        packages.append(value)
    modules = {}
    for package in packages:
        module = package.get('Module')
        if not module or module.get('Main') and Path(module.get('Dir', '')).resolve() == project: continue
        selected = module.get('Replace') or module
        name = module['Path']
        item = modules.setdefault(name, dict(name=name, version=selected.get('Version') or module.get('Version') or 'local', licenses=notices(selected['Dir'], name), inputs={}))
        directory = Path(package['Dir'])
        for category in ('GoFiles', 'CgoFiles', 'CFiles', 'HFiles', 'SFiles', 'SysoFiles', 'EmbedFiles'):
            for relative in package.get(category, []):
                path = directory / relative
                item['inputs'][str(path.relative_to(Path(selected['Dir'])))] = digest(path.read_bytes())
    toolchain = run(['go', 'env', 'GOROOT'], project).strip()
    modules['go-runtime'] = dict(name='go-runtime', version=run(['go', 'env', 'GOVERSION'], project).strip(), licenses={'LICENSE': (Path(toolchain) / 'LICENSE').read_text()}, inputs={})
    return list(modules.values())


# Inspect the locked Cargo dependency graph
def rust_dependencies(project, target=None):
    command = ['cargo', 'metadata', '--locked', '--offline', '--format-version', '1']
    if os.environ.get('STREAM_DOCK_MANAGER_SDK_CARGO_CONFIG'): command += ['--config', os.environ['STREAM_DOCK_MANAGER_SDK_CARGO_CONFIG']]
    if target is None:
        version = run(['rustc', '-vV'], project)
        target = next(line.split(': ', 1)[1] for line in version.splitlines() if line.startswith('host: '))
    command += ['--filter-platform', target]
    graph = json.loads(run(command, project))
    packages = {p['id']: p for p in graph['packages']}
    nodes = {p['id']: p for p in graph['resolve']['nodes']}
    root = graph['resolve']['root']
    if root is None: raise ValueError('Select one Cargo package, not an ambiguous workspace')
    seen, output = {root}, []
    # Traverse nested values or files using the module’s bounds
    def walk(identity):
        for dependency in nodes[identity]['deps']:
            if not any(kind['kind'] is None for kind in dependency['dep_kinds']): continue
            identifier = dependency['pkg']
            if identifier in seen: continue
            seen.add(identifier)
            package = packages[identifier]
            if any('proc-macro' in target['kind'] for target in package['targets']): continue
            directory = Path(package['manifest_path']).parent
            license_files = notices(directory, package['name'], package.get('license_file'))
            inputs = {p.relative_to(directory).as_posix():digest(p.read_bytes()) for p in directory.rglob('*.rs') if p.is_file() and not any(part in ('target', '.git') for part in p.relative_to(directory).parts)}
            inputs['Cargo.toml'] = digest(Path(package['manifest_path']).read_bytes())
            output.append(dict(name=package['name'], version=package['version'], licenses=license_files, inputs=inputs))
            walk(identifier)
    walk(root)
    return output


# Inspect the configured CMake dependency graph and licenses
def cpp_dependencies(project, target=None):
    if target: raise ValueError('Inspect C++ dependencies from the selected native CMake build')
    cache = project / 'build/CMakeCache.txt'
    if not cache.is_file(): raise ValueError('Configure the native C++ bundle before collecting its dependency graph')
    entries = {}
    for line in cache.read_text().splitlines():
        if not line or line.startswith(('#', '//')) or '=' not in line or ':' not in line.split('=', 1)[0]: continue
        key, value = line.split('=', 1)
        entries[key.split(':', 1)[0]] = value
    output = []
    for name in ('ixwebsocket', 'nlohmann_json'):
        directory = entries.get(name + '_SOURCE_DIR')
        if not directory: raise ValueError(name + ': configured CMake source directory is missing')
        root = Path(directory)
        version = run(['git', '-C', str(root), 'rev-parse', 'HEAD'], project).strip() if (root / '.git').exists() else 'local'
        inputs = {path.relative_to(root).as_posix(): digest(path.read_bytes()) for path in root.rglob('*') if path.is_file() and path.suffix in ('.c', '.cc', '.cpp', '.h', '.hpp', '.in') and '.git' not in path.relative_to(root).parts}
        output.append(dict(name=name, version=version, licenses=notices(root, name), inputs=inputs))
    return output


# Collect dependency license notices for the packaged distribution
def distribution_notices(distribution):
    found = {}
    for path in distribution.files or []:
        if any(part.upper().startswith(('LICENSE', 'LICENCE', 'COPYING', 'NOTICE')) for part in path.parts):
            full = distribution.locate_file(path)
            if full.is_file():
                data = full.read_bytes()
                if len(data) > 1 << 20: raise ValueError('distribution license exceeds 1 MiB')
                found[path.name] = data.decode('utf-8')
    if not any(key.upper().startswith(('LICENSE', 'LICENCE', 'COPYING')) for key in found):
        # Editable distributions can retain license files in their source root
        direct = distribution.read_text('direct_url.json')
        if direct:
            from urllib.parse import unquote, urlsplit
            info = json.loads(direct)
            if info.get('dir_info', {}).get('editable'):
                return notices(Path(unquote(urlsplit(info['url']).path)), distribution.metadata['Name'])
        raise ValueError(distribution.metadata['Name'] + ': installed runtime license is missing')
    return found


# Collect the selected runtime license for distribution
def runtime_license(project, filename):
    variables = {'python-runtime-LICENSE': 'STREAM_DOCK_MANAGER_PYTHON_LICENSE', 'openssl-LICENSE.txt': 'STREAM_DOCK_MANAGER_OPENSSL_LICENSE'}
    variable = variables[filename]
    explicit = os.environ.get(variable)
    candidates = [Path(explicit)] if explicit else []
    if not explicit:
        if filename == 'python-runtime-LICENSE':
            candidates += [Path(sysconfig.get_path('stdlib')) / 'LICENSE.txt', Path(sys.base_prefix) / 'LICENSE.txt']
        else:
            import ssl
            major = ssl.OPENSSL_VERSION.split()[1].split('.')[0]
            prefix = Path(sys.base_prefix)
            candidates += [prefix / 'share/doc/openssl/LICENSE.txt', prefix / 'Library/share/doc/openssl/LICENSE.txt']
            for parent in prefix.parents:
                if parent.name == 'opt':
                    candidates.append(parent / ('openssl@' + major) / 'LICENSE.txt')
        # An author may supply just the relevant build-runtime notice as an input
        candidates += [project / filename, project.parent / filename]
    path = next((path for path in candidates if path.is_file()), None)
    if path is None: raise ValueError(f'Supply the build runtime notice with {variable}')
    if path.stat().st_size > 1 << 20: raise ValueError('Runtime license exceeds 1 MiB')
    return path.read_text()


# Inspect the frozen Python dependency inventory
def python_dependencies(project, target=None):
    if target: raise ValueError('Frozen Python must be inspected on its native build interpreter')
    analysis = project / 'build/counter/Analysis-00.toc'
    if not analysis.is_file(): raise ValueError('Build the native Python bundle before collecting its actual module graph')
    # PyInstaller's pinned TOC is literal metadata, never executable Python
    if analysis.stat().st_size > 16 << 20: raise ValueError('oversized PyInstaller analysis')
    data = ast.literal_eval(analysis.read_text())
    modules, sources = set(), {}
    # Traverse the current input entry while enforcing collection constraints
    def visit(value):
        if isinstance(value, (tuple, list)):
            if len(value) == 3 and all(isinstance(item, str) for item in value) and value[2] in ('PYMODULE', 'EXTENSION'):
                modules.add(value[0].split('.')[0])
                path = Path(value[1])
                if path.is_file(): sources[value[0]] = digest(path.read_bytes())
            else:
                for item in value: visit(item)
    visit(data)
    owners = metadata.packages_distributions()
    selected = set()
    for module in modules: selected.update(owners.get(module, []))
    selected.add('stream-dock-manager-plugin-sdk')
    output = []
    for name in sorted(selected):
        distribution = metadata.distribution(name)
        output.append(dict(name=distribution.metadata['Name'], version=distribution.version, licenses=distribution_notices(distribution), inputs=sources))
    bootloader = metadata.distribution('pyinstaller')
    output.append(dict(name='pyinstaller-bootloader', version=bootloader.version, licenses=distribution_notices(bootloader), inputs={}))
    for name, filename, version in [('python-runtime', 'python-runtime-LICENSE', sys.version.split()[0]), ('openssl', 'openssl-LICENSE.txt', __import__('ssl').OPENSSL_VERSION)]:
        output.append(dict(name=name, version=version, licenses={filename:runtime_license(project, filename)}, inputs={}))
    return output


# Collect packaging data
def collect(project, language, target=None):
    project = Path(project).resolve(strict=True)
    collectors = {'go':go_dependencies, 'rust':rust_dependencies, 'python':python_dependencies, 'cpp':cpp_dependencies}
    if language not in collectors: raise ValueError('Dependency collection supports Go, Rust, C++ and frozen Python')
    dependencies = collectors[language](project, target)
    dependencies.sort(key=lambda item:(item['name'], item['version']))
    for item in dependencies:
        raw = json.dumps(item.pop('inputs'), sort_keys=True).encode()
        item['source_sha256'] = digest(raw)
    return dict(version=1, language=language, dependencies=dependencies, scope='Provisioned runtime dependencies; no downloads, build tools or registry publication were executed')




# Check local resources after packaging; paths refer to the single ZIP root
def resource_graph(files, manifest, declared=(), reachable_only=False):
    """Check local resources after packaging; paths refer to the single ZIP root."""
    paths = set(files)
    required = set()
    # Resolve and validate a referenced dependency artifact
    def reference(path, parent=''):
        if not isinstance(path, str) or not path or path.startswith(('data:', '#')) or '://' in path: return
        clean = posixpath.normpath(str(PurePosixPath(parent) / path).split('?', 1)[0].split('#', 1)[0])
        if path.startswith('/') or '\\' in clean or ':' in clean or any(part in ('', '.', '..') for part in clean.split('/')): raise ValueError('invalid resource reference: '+path)
        if clean not in paths:
            raise ValueError('missing packaged resource: '+clean)
        required.add(clean)
    for key in ('Icon', 'CategoryIcon', 'PropertyInspectorPath'): reference(manifest.get(key))
    for action in manifest.get('Actions', []):
        for key in ('Icon', 'PropertyInspectorPath'): reference(action.get(key))
        for state in action.get('States', []): reference(state.get('Image'))
    # Extract and validate the compiled capability descriptor
    def descriptor(value):
        if isinstance(value, dict):
            for key, item in value.items():
                if key in ('light', 'dark', 'settings', 'plugin_settings', 'feedback_layout') and isinstance(item, str): reference(item)
                elif isinstance(item, (dict, list)): descriptor(item)
        elif isinstance(value, list):
            for item in value: descriptor(item)
    descriptor(manifest.get('StreamDockManager', {}).get('Capabilities', {}))
    class Links(HTMLParser):
        # Initialize HTML link collection relative to the parent URL
        def __init__(self, parent): super().__init__(); self.parent = parent
        # Collect dependency-license links from the parsed HTML tag
        def handle_starttag(self, tag, attributes):
            for key, value in attributes:
                if key == 'src' or key == 'href' and tag == 'link':
                    if tag in ('script','link') and isinstance(value,str) and (value.startswith('data:') or '://' in value): raise ValueError('Inspector scripts and styles must be packaged locally')
                    reference(value, self.parent)
    for name in declared: reference(name)
    scanned = set()
    while True:
        names = (required - scanned) if reachable_only else (set(files) - scanned)
        if not names: break
        for name in sorted(names):
            scanned.add(name)
            data = files[name]
            if name.endswith('.html'): Links('' if str(PurePosixPath(name).parent)=='.' else str(PurePosixPath(name).parent)).feed(data.decode())
            if name.endswith('.json') and name.startswith(('display/', 'layouts/')):
                # Traverse nested values or files using the module’s bounds
                def walk(value):
                    if isinstance(value, (dict, list)):
                        for item in value.values() if isinstance(value, dict) else value: walk(item)
                    elif isinstance(value, str) and value.endswith(('.svg', '.png', '.json')): reference(value)
                walk(json.loads(data))
    for name in files:
        if name == 'profile.json' or name.startswith('profiles/'):
            reference(name)
    return [dict(path=path, bytes=len(files[path]), sha256=digest(files[path])) for path in sorted(required)]


# Run the packaging command and enforce its input options
def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--project', required=True, type=Path)
    parser.add_argument('--language', required=True, choices=('go', 'rust', 'python', 'cpp'))
    parser.add_argument('--target')
    parser.add_argument('--output', required=True, type=Path)
    args = parser.parse_args()
    audit = collect(args.project, args.language, args.target)
    args.output.write_text(json.dumps(audit, indent=2) + '\n')
    args.output.with_suffix('.licenses.txt').write_bytes(license_text(audit))


if __name__ == '__main__': main()

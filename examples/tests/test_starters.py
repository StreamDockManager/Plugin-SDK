"""Standalone starter generation/package contracts; no compilers, runtimes or host execution."""
import contextlib
import io
import json
import os
import types
from pathlib import Path
import subprocess
import sys
import tempfile
import tomllib
import unittest
from unittest.mock import patch
import zipfile
import warnings

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "tools"))
from descriptor import capabilities, frame
from generate import generate
from package import package
from project import STARTER_KINDS as KINDS, SOURCE_FILES, SCRIPT_KINDS, default_binary_name, distribution_manifest, native_binary_name
from validate import validate_project


def script_bundle_fixture(target):
    import hashlib
    output=target/'build/javascript'; output.mkdir(parents=True,exist_ok=True)
    compiled=('/*'+frame(capabilities(json.loads((target/'manifest.json').read_text())))+'*/\nconsole.log("static bundle fixture; no execution claim")').encode()
    (output/'main.mjs').write_bytes(compiled)
    report={'version':1,'settings':{'treeShaking':True,'assets':['display/counter.json','icons/even.svg','icons/odd.svg']},'input_sha256':{'main.mjs':hashlib.sha256((target/'main.mjs').read_bytes()).hexdigest()},'output_sha256':{'main.mjs':hashlib.sha256(compiled).hexdigest()}}
    (output/'bundle-report.json').write_text(json.dumps(report))


class Starters(unittest.TestCase):
    def test_retired_runtime_starters_are_rejected(self):
        with tempfile.TemporaryDirectory() as temporary:
            for kind in ('deno', 'bun'):
                with self.subTest(language=kind), self.assertRaises(ValueError):
                    generate(Path(temporary) / kind, 'com.example.retired', kind, 'Retired runtime')
                self.assertFalse((Path(temporary) / kind).exists())

    def test_all_languages_generate_offline_outside_the_checkout_and_validate_without_siblings(self):
        with tempfile.TemporaryDirectory() as temporary:
            for kind in KINDS:
                with self.subTest(language=kind):
                    target = Path(temporary) / kind
                    with patch('subprocess.run', side_effect=AssertionError('Generation must not fork')), patch('subprocess.Popen', side_effect=AssertionError('Generation must not fork')):
                        generate(target, 'com.example.starter.' + kind, kind, 'Standalone sample')
                    result = validate_project(target)
                    self.assertEqual(result['identity'], 'com.example.starter.' + kind)
                    self.assertEqual(result['version'], '0.1.1')
                    self.assertEqual(result['execution'], 'none; offline static validation only')
                    self.assertTrue((target / 'LICENSE').is_file())
                    self.assertFalse((target / 'licenses').exists())
                    self.assertTrue((target / 'tools/acceptance.py').is_file())
                    self.assertTrue((target / 'tools/cpp_transport.py').is_file())
                    readme = (target / 'README.md').read_text()
                    self.assertNotIn(str(ROOT.resolve()), readme)
                    self.assertNotIn('../stream-dock-manager', readme)
                    subprocess.run([sys.executable, str(target / 'tools/validate.py'), '--project', str(target)], cwd=temporary, check=True, capture_output=True)
                    blocked = subprocess.run([sys.executable, str(target / 'tools/acceptance.py'), '--project', str(target), '--scenario', 'protocol'], cwd=temporary, capture_output=True, text=True)
                    self.assertEqual(blocked.returncode, 2)
                    self.assertIn('requires explicit --host', blocked.stderr)
                    self.assertFalse((target / 'artifacts').exists())
                    if kind == 'rust':
                        self.assertIn('name = "com-example-starter-rust"\nversion = "0.1.1"', (target / 'Cargo.lock').read_text())
                        self.assertNotIn('sdk-example-rust', (target / 'Cargo.toml').read_text())
                    if kind == 'go':
                        self.assertIn('module example.com/com.example.starter.go', (target / 'go.mod').read_text())
                    if kind in ('go', 'rust', 'python'):
                        manifest = json.loads((target / 'manifest.json').read_text())
                        self.assertEqual(manifest['SDKVersion'], 1)
                        self.assertEqual({item['Platform'] for item in manifest['OS']}, {'mac', 'windows', 'linux'})
                        for platform in ('mac', 'windows', 'linux'):
                            self.assertEqual([item['Platform'] for item in distribution_manifest(manifest, platform)['OS']], [platform])
                        self.assertEqual(manifest['Icon'], 'icon.png')
                        self.assertEqual(manifest['Actions'][0]['Controllers'], ['Keypad'])
                        self.assertTrue(all(state['TitleAlignment'] == 'center' for state in manifest['Actions'][0]['States']))

    def test_modern_metadata_rejects_guesses_and_mismatched_descriptors(self):
        with tempfile.TemporaryDirectory() as temporary:
            for kind in KINDS:
                target = generate(Path(temporary) / kind, 'com.example.current.' + kind, kind)
                path = target / 'manifest.json'
                original = json.loads(path.read_text())
                for change in ('dialect', 'icon', 'missing-descriptor', 'future-descriptor', 'mismatched-descriptor'):
                    manifest = json.loads(json.dumps(original))
                    if change == 'dialect': del manifest['StreamDockManager']['ManifestDialect']
                    elif change == 'icon': manifest['Icon'] = 'icon'
                    elif change == 'missing-descriptor': del manifest['StreamDockManager']['Capabilities']
                    elif change == 'future-descriptor': manifest['StreamDockManager']['Capabilities']['version'] = 2
                    else: manifest['StreamDockManager']['Capabilities']['id'] = 'com.example.wrong'
                    path.write_text(json.dumps(manifest))
                    with self.subTest(kind=kind, change=change), self.assertRaises(ValueError): validate_project(target)
                path.write_text(json.dumps(original))
                (target / 'profile.json').write_text('{}')
                with self.assertRaisesRegex(ValueError, 'profile.json'): validate_project(target)

    def test_native_names_follow_the_id_slug_and_script_entries_remain_scripts(self):
        with tempfile.TemporaryDirectory() as temporary:
            for kind in KINDS:
                target = generate(Path(temporary) / kind, 'com.example.weather', kind, 'Friendly display title')
                manifest = json.loads((target / 'manifest.json').read_text())
                if kind in SCRIPT_KINDS:
                    self.assertEqual(manifest['CodePath'], 'main.mjs')
                    self.assertEqual(manifest['CodePathWin'], 'main.mjs')
                else:
                    self.assertEqual(manifest['CodePath'], 'bin/weather-plugin')
                    self.assertEqual(manifest['CodePathWin'], 'bin/weather-plugin.exe')
                    self.assertEqual(native_binary_name(manifest), 'weather-plugin')
                    self.assertIn('bin/weather-plugin', (target / 'README.md').read_text())
                    if kind == 'rust':
                        self.assertEqual(tomllib.loads((target / 'Cargo.toml').read_text())['bin'][0]['name'], 'weather-plugin')
            self.assertEqual(default_binary_name('com.example.weather-plugin'), 'weather-plugin')
            self.assertEqual(default_binary_name('com.example.my-weather'), 'my-weather-plugin')
            target = generate(Path(temporary) / 'rust-name-collision', 'sdk.example.rust.plugin', 'rust')
            cargo = tomllib.loads((target / 'Cargo.toml').read_text())
            self.assertEqual(cargo['package']['name'], 'sdk-example-rust-plugin')
            self.assertEqual(cargo['bin'][0]['name'], 'plugin-plugin')
            self.assertIn('name = "sdk-example-rust-plugin"\nversion = "0.1.1"', (target / 'Cargo.lock').read_text())

    def test_removed_cpp_starter_is_not_generated(self):
        with tempfile.TemporaryDirectory() as temporary:
            target = Path(temporary) / 'cpp'
            with self.assertRaises(ValueError):
                generate(target, 'com.example.removed', 'cpp')
            self.assertFalse(target.exists())

    def test_named_native_packages_include_the_declared_posix_and_windows_executable(self):
        import package as packager
        from cpp_transport import validate_archive
        with tempfile.TemporaryDirectory() as temporary:
            for windows in [False, True]:
                for kind in ('go', 'rust', 'python', 'cpp'):
                    with self.subTest(windows=windows, language=kind):
                        target = generate(Path(temporary) / (kind + str(windows)), 'com.example.weather', 'go' if kind == 'cpp' else kind)
                        if kind == 'cpp':
                            # Independent C++ project fixture, not a maintained starter.
                            metadata_path = target / 'plugin-project.json'
                            metadata = json.loads(metadata_path.read_text())
                            metadata['language'] = 'cpp'
                            metadata_path.write_text(json.dumps(metadata))
                            for name in SOURCE_FILES['cpp']:
                                (target / name).write_text('// static test fixture; never compiled\n')
                        manifest = json.loads((target / 'manifest.json').read_text())
                        exe = 'weather-plugin.exe' if windows else 'weather-plugin'
                        directory = {'go': 'build', 'rust': 'target/release', 'python': 'dist/weather-plugin', 'cpp': 'build/Release' if windows else 'build'}[kind]
                        binary = target / directory / exe
                        binary.parent.mkdir(parents=True)
                        payload = b'named fixture ' + frame(capabilities(manifest)).encode()
                        binary.write_bytes(payload)
                        # Model Windows paths without changing pathlib's actual host OS.
                        environment = types.SimpleNamespace(**{**vars(os), 'name': 'nt' if windows else 'posix'})
                        with patch.object(packager, 'os', environment), patch('subprocess.run', side_effect=AssertionError('No implicit compiler')), contextlib.redirect_stdout(io.StringIO()):
                            archive = package(build=False, project=target)
                        validate_project(target, archive)
                        with zipfile.ZipFile(archive) as packaged:
                            prefix = 'com.example.weather.sdPlugin/'
                            self.assertEqual(packaged.read(prefix + 'bin/' + exe), payload)
                            self.assertEqual(packaged.getinfo(prefix + 'bin/' + exe).external_attr >> 16 & 0o111, 0o111)
                            self.assertNotIn(prefix + 'bin/counter', packaged.namelist())
                            self.assertNotIn(prefix + 'bin/counter.exe', packaged.namelist())
                        if kind == 'cpp':
                            def inspect_binary(path):
                                self.assertEqual(path.name, exe)
                                self.assertEqual(path.read_bytes(), payload)
                            with patch('cpp_transport.os', environment), patch('cpp_transport.validate_binary', side_effect=inspect_binary) as invoked:
                                validate_archive(archive)
                            invoked.assert_called_once()

    def test_native_manifest_mismatch_or_unsafe_names_fail_before_building(self):
        with tempfile.TemporaryDirectory() as temporary:
            target = generate(Path(temporary) / 'native', 'com.example.native', 'go')
            path = target / 'manifest.json'
            manifest = json.loads(path.read_text())
            for updates in [dict(CodePath='bin/../../escape'), dict(CodePath='bin/name;command'), dict(CodePathWin='bin/other.exe'), dict(CodePathLinux='bin/other')]:
                path.write_text(json.dumps({**manifest, **updates}))
                with patch('subprocess.run', side_effect=AssertionError('No compiler for an invalid entry')):
                    with self.assertRaises(ValueError): package(project=target)
            # The convention is a default; valid customized/legacy basenames remain usable.
            self.assertEqual(native_binary_name(dict(CodePath='bin/counter', CodePathWin='bin/counter.exe')), 'counter')

    def test_existing_destinations_and_invalid_inputs_are_preserved_before_any_publication(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            for target in [root / 'existing-file', root / 'existing-directory']:
                if target.name.endswith('file'):
                    target.write_bytes(b'keep')
                else:
                    target.mkdir()
                with self.assertRaises(FileExistsError):
                    generate(target, 'com.example.keep', 'nodejs')
            self.assertEqual((root / 'existing-file').read_bytes(), b'keep')
            self.assertEqual(list((root / 'existing-directory').iterdir()), [])
            for identity in ['../../escape', 'com.example;touch-no', 'Com.Example', 'single', 'com.example..empty']:
                with self.assertRaises(ValueError):
                    generate(root / 'invalid', identity, 'nodejs')
                self.assertFalse((root / 'invalid').exists())
            with self.assertRaises(ValueError):
                generate(root / 'invalid', 'com.example.valid', 'unknown')
            self.assertFalse((root / 'invalid').exists())

    def test_script_packages_are_standalone_and_native_packaging_never_builds_implicitly_when_disabled(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            for kind in KINDS:
                target = generate(root / kind, 'com.example.package.' + kind, kind)
                if kind in ('nodejs',):
                    with self.assertRaises(FileNotFoundError): package(kind, build=False, project=target)
                    script_bundle_fixture(target)
                if kind not in ('nodejs',):
                    with patch('subprocess.run', side_effect=AssertionError('No implicit compiler')):
                        with self.assertRaises(RuntimeError):
                            package(kind, build=False, project=target)
                    binary = target / {'go': 'build/go-plugin', 'rust': 'target/release/rust-plugin', 'python': 'dist/python-plugin/python-plugin', 'cpp': 'build/cpp-plugin'}[kind]
                    if sys.platform == 'win32':
                        binary = target / {'go': 'build/go-plugin.exe', 'rust': 'target/release/rust-plugin.exe', 'python': 'dist/python-plugin/python-plugin.exe', 'cpp': 'build/Release/cpp-plugin.exe'}[kind]
                    binary.parent.mkdir(parents=True)
                    binary.write_bytes(b'static archive fixture; never executable qualification' + (frame(capabilities(json.loads((target / 'manifest.json').read_text()))).encode() if kind in ('go', 'rust', 'python', 'cpp') else b''))
                with patch('subprocess.run', side_effect=AssertionError('No implicit compiler')), contextlib.redirect_stdout(io.StringIO()):
                    archive = package(kind, build=False, project=target)
                validate_project(target, archive)
                with zipfile.ZipFile(archive) as package_file:
                    self.assertTrue(all(name.startswith('com.example.package.' + kind + '.sdPlugin/') for name in package_file.namelist()))
                    self.assertTrue(any('/display/' in name for name in package_file.namelist()))
                    self.assertFalse(any('/licenses/' in name for name in package_file.namelist()))
                    self.assertIn('com.example.package.' + kind + '.sdPlugin/LICENSE', package_file.namelist())
                if kind in ('nodejs',):
                    subprocess.run([sys.executable, str(target / 'tools/package.py'), '--project', str(target), '--no-build'], cwd=temporary, capture_output=True, check=True)

    def test_python_runtime_aliases_are_materialized_and_external_or_cyclic_links_cannot_replace_an_archive(self):
        with tempfile.TemporaryDirectory() as temporary:
            target = generate(Path(temporary) / 'project', 'com.example.python', 'python')
            bundle = target / 'dist/python-plugin'
            internal = bundle / '_internal'
            internal.mkdir(parents=True)
            (bundle / ('python-plugin.exe' if sys.platform == 'win32' else 'python-plugin')).write_bytes(frame(capabilities(json.loads((target / 'manifest.json').read_text()))).encode())
            (internal / 'runtime').write_bytes(b'library')
            alias = internal / 'alias'
            try:
                alias.symlink_to('runtime')
            except OSError:
                self.skipTest('Creating local symlinks is unavailable')
            with contextlib.redirect_stdout(io.StringIO()):
                archive = package(build=False, project=target)
            original = archive.read_bytes()
            validate_project(target, archive)
            with zipfile.ZipFile(archive) as packaged:
                self.assertEqual(packaged.read('com.example.python.sdPlugin/bin/_internal/alias'), b'library')
            alias.unlink()
            alias.symlink_to(target / 'manifest.json')
            with self.assertRaisesRegex(ValueError, 'escapes'):
                package(build=False, project=target)
            self.assertEqual(archive.read_bytes(), original)
            alias.unlink()
            alias.symlink_to(internal, target_is_directory=True)
            with self.assertRaisesRegex(ValueError, 'Cyclic'):
                package(build=False, project=target)
            self.assertEqual(archive.read_bytes(), original)

    def test_generic_validation_accepts_changed_actions_and_versions_and_rejects_path_escape_stale_assets_and_duplicate_archives(self):
        with tempfile.TemporaryDirectory() as temporary:
            target = generate(Path(temporary) / 'project', 'com.example.generic', 'nodejs')
            manifest = json.loads((target / 'manifest.json').read_text())
            manifest['Name'] = 'Another action'
            manifest['Version'] = '3.7.2'
            manifest['Actions'][0]['UUID'] = 'com.example.generic.another'
            manifest['Actions'][0]['Name'] = 'Anything'
            descriptor = manifest['StreamDockManager']['Capabilities']
            descriptor['plugin_version'] = manifest['Version']
            descriptor['actions'] = {manifest['Actions'][0]['UUID']: next(iter(descriptor['actions'].values()))}
            (target / 'manifest.json').write_text(json.dumps(manifest))
            from profiles import generate_profiles
            for name, contents in generate_profiles(manifest).items():
                (target / name).write_bytes(contents)
            validate_project(target)
            script_bundle_fixture(target)
            with contextlib.redirect_stdout(io.StringIO()):
                archive = package(build=False, project=target)
            validate_project(target, archive)
            original = archive.read_bytes()
            asset = target / 'assets/icons/even.svg'
            previous_asset = asset.read_bytes()
            asset.write_text('<svg xmlns="http://www.w3.org/2000/svg"/>')
            with self.assertRaisesRegex(ValueError, 'stale'):
                validate_project(target, archive)
            manifest['Actions'][0]['States'][0]['Image'] = '../outside.svg'
            (target / 'manifest.json').write_text(json.dumps(manifest))
            with self.assertRaisesRegex(ValueError, 'Invalid project-relative'):
                validate_project(target)
            self.assertEqual(archive.read_bytes(), original)
            manifest['Actions'][0]['States'][0]['Image'] = 'icons/even.svg'
            (target / 'manifest.json').write_text(json.dumps(manifest))
            asset.write_bytes(previous_asset)
            with warnings.catch_warnings():
                warnings.simplefilter('ignore', UserWarning)
                with zipfile.ZipFile(archive, 'a') as package_file:
                    package_file.writestr(manifest['UUID'] + '.sdPlugin/manifest.json', json.dumps(manifest))
            with self.assertRaisesRegex(ValueError, 'Duplicate archive'):
                validate_project(target, archive)

    def test_validation_matches_manager_version_and_namespace_contract(self):
        with tempfile.TemporaryDirectory() as temporary:
            target = generate(Path(temporary) / 'project', 'com.example.contract', 'nodejs')
            path = target / 'manifest.json'
            manifest = json.loads(path.read_text())
            manifest['Version'] = '1.2.3-preview.1'
            path.write_text(json.dumps(manifest))
            with self.assertRaisesRegex(ValueError, 'numeric components'): validate_project(target)
            manifest['Version'] = '1.2.3.4'
            manifest['StreamDockManager']['Capabilities']['plugin_version'] = manifest['Version']
            path.write_text(json.dumps(manifest))
            validate_project(target)
            manifest['Actions'][0]['UUID'] = 'com.other.plugin.action'
            path.write_text(json.dumps(manifest))
            with self.assertRaisesRegex(ValueError, 'plugin namespace'): validate_project(target)

    def test_symlink_assets_and_mismatched_project_identity_fail_closed(self):
        with tempfile.TemporaryDirectory() as temporary:
            target = generate(Path(temporary) / 'project', 'com.example.checked', 'nodejs')
            metadata = json.loads((target / 'plugin-project.json').read_text())
            metadata['identity'] = 'com.example.different'
            (target / 'plugin-project.json').write_text(json.dumps(metadata))
            with self.assertRaisesRegex(ValueError, 'identities differ'):
                validate_project(target)
            metadata['identity'] = 'com.example.checked'
            (target / 'plugin-project.json').write_text(json.dumps(metadata))
            asset = target / 'assets/icons/even.svg'
            asset.unlink()
            try:
                asset.symlink_to(target / 'manifest.json')
            except OSError:
                self.skipTest('Creating local symlinks is unavailable')
            with self.assertRaisesRegex(ValueError, 'Symlinks'):
                validate_project(target)


if __name__ == '__main__':
    unittest.main()

#!/usr/bin/env python3
"""Standalone SDK checks and release candidate preparation; never publishes."""
import argparse
import os
from pathlib import Path
import shutil
import subprocess
import sys
import zipfile

ROOT = Path(__file__).resolve().parents[1]


# Execute a command with Windows npm resolution and propagate failures
def run(args, cwd=ROOT, env=None):
    command = [str(arg) for arg in args]
    if os.name == 'nt' and command[0] == 'npm':
        # CreateProcess does not search PATHEXT for npm.cmd as a shell does
        executable = shutil.which('npm.cmd', path=(env if env is not None else os.environ).get('PATH'))
        if executable is None:
            raise FileNotFoundError('npm.cmd is required on PATH; install Node.js before running SDK checks')
        command[0] = executable
    subprocess.run(command, cwd=cwd, env=env, check=True)


# Exercise the Python SDK and Python-based starter and release tooling
def test_python(env):
    run([sys.executable, '-m', 'unittest', 'discover', '-s', ROOT / 'Python/tests', '-v'], env=env)
    run([sys.executable, '-m', 'unittest', 'discover', '-s', ROOT / 'examples/tests', '-v'], env=env)
    run([sys.executable, '-m', 'unittest', 'discover', '-s', ROOT / 'tests', '-p', 'test_zip_policy.py', '-v'], env=env)
    run([sys.executable, '-m', 'unittest', 'discover', '-s', ROOT / 'tests', '-p', 'test_release.py', '-v'], env=env)


# Run the build command and enforce its input options
def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=['deps', 'build', 'check', 'test', 'test-python', 'package', 'fmt', 'fmt-check', 'clean'])
    args = parser.parse_args()
    env = os.environ.copy()
    env.setdefault('GOCACHE', str(ROOT / '.cache/go-build'))
    env.setdefault('CARGO_TARGET_DIR', str(ROOT / '.cache/rust-target'))
    action = args.action
    if action == 'test-python':
        from javascript_runtimes import environment
        env = environment(env)
        test_python(env)
        run([sys.executable, ROOT / 'tools/benchmark.py', '--language', 'python', '--frames', '3', '--surfaces', '2'], env=env)
        return
    if action != 'clean':
        run([sys.executable, ROOT / 'tools/sync_contracts.py', *([] if action=='fmt' else ['--check'])], env=env)
        run([sys.executable, ROOT / 'tools/sync_assets.py', *([] if action=='fmt' else ['--check'])], env=env)
        run([sys.executable, ROOT / 'tools/sync_starter_inspectors.py', *([] if action=='fmt' else ['--check'])], env=env)
    if action not in ('clean', 'deps'):
        run([sys.executable, ROOT / 'tools/check_parity.py'], env=env)
    if action in ('fmt', 'fmt-check'):
        run(['npm', 'run', 'format' if action == 'fmt' else 'format:check'], ROOT / 'Node', env)
        sources = sorted(str(p) for directory in [ROOT / 'Go', ROOT / 'examples/sdk-example-go'] for p in directory.rglob('*.go'))
        if action == 'fmt':
            run(['gofmt', '-w', *sources], env=env)
        elif subprocess.check_output(['gofmt', '-l', *sources], text=True):
            raise SystemExit('Unformatted SDK Go sources')
        run(['cargo', 'fmt', '--manifest-path', ROOT / 'Rust/Cargo.toml', *(['--check'] if action == 'fmt-check' else [])], env=env)
        run(['rustfmt', '--edition', '2021', *(['--check'] if action == 'fmt-check' else []), ROOT / 'examples/sdk-example-rust/main.rs'], env=env)
        return
    if action == 'clean':
        import shutil
        for path in [ROOT / 'artifacts', ROOT / '.cache']:
            if path.is_symlink():
                raise ValueError('Refusing symlink cleanup')
            if path.exists():
                shutil.rmtree(path)
        return
    if action == 'deps':
        run(['npm', 'ci', '--ignore-scripts'], ROOT / 'Node', env)
        run(['go', 'mod', 'download'], ROOT / 'Go', env)
        run(['cargo', 'fetch', '--locked', '--manifest-path', ROOT / 'Rust/Cargo.toml'], env=env)
        run([sys.executable, '-m', 'pip', 'install', '-e', str(ROOT / 'Python') + '[thumbnail]'], env=env)
        return
    run(['npm','run','check'], ROOT / 'Node', env)
    run(['npm','run','build'], ROOT / 'Node', env)
    if action == 'test':
        from javascript_runtimes import environment
        env=environment(env)
        run([sys.executable, ROOT / 'tests/run_javascript.py'], env=env)
        run([env['STREAM_DOCK_MANAGER_NODE'], ROOT / 'tests/inspector-requests.mjs'], env=env)
    run(['go', 'test' if action == 'test' else 'build', './...'], ROOT / 'Go', env)
    run(['cargo', 'test' if action == 'test' else 'check', '--locked', '--manifest-path', ROOT / 'Rust/Cargo.toml', '--features', 'http,testkit-tls,thumbnail,service-websocket' if action == 'test' else 'http,thumbnail,service-websocket'], env=env)
    if action == 'test':
        # Downstream Cargo feature unification may change serde_json's map order
        # Keep exact state bytes compatible with the original envelope in both modes
        run(['cargo', 'test', '--locked', '--manifest-path', ROOT / 'Rust/Cargo.toml',
             '--features', 'http,testkit-tls,serde_json/preserve_order', '--test', 'authoring',
             'shared_state_writes_preserve_exact_format_and_failed_write', '--', '--exact'], env=env)
        test_python(env)
        run([sys.executable, ROOT / 'tools/benchmark.py', '--frames', '3', '--surfaces', '2'], env=env)
    else:
        run([sys.executable, '-m', 'compileall', '-q', ROOT / 'Python/stream_dock_manager_sdk'], env=env)
    run([sys.executable, ROOT / 'examples/tools/validate.py'], env=env)
    if action == 'package':
        output = ROOT / 'artifacts'
        output.mkdir(exist_ok=True)
        run(['npm','pack','--ignore-scripts','--pack-destination',output], ROOT / 'Node', env)
        run(['cargo', 'package', '--locked', '--allow-dirty', '--no-verify', '--manifest-path', ROOT / 'Rust/Cargo.toml', '--features', 'http,testkit-tls,thumbnail,service-websocket' if action == 'test' else 'http,thumbnail,service-websocket'], env=env)
        import shutil
        shutil.copyfile(Path(env['CARGO_TARGET_DIR']) / 'package/stream-dock-manager-plugin-sdk-0.1.0.crate', output / 'stream-dock-manager-plugin-sdk-0.1.0.crate')
        python_build = ROOT / 'Python/build'
        if python_build.is_symlink():
            raise ValueError('Refusing symlink build cleanup')
        if python_build.exists():
            shutil.rmtree(python_build)
        # Normalize only this build's first-party wheel, never cached/external wheels
        import tempfile
        from zip_policy import normalize_owned_wheel
        with tempfile.TemporaryDirectory(prefix='sdk-wheel-', dir=output) as wheel_directory:
            run([sys.executable, '-m', 'pip', 'wheel', '--no-build-isolation', '--no-deps', '--wheel-dir', wheel_directory, ROOT / 'Python'], env=env)
            wheels = list(Path(wheel_directory).glob('stream_dock_manager_plugin_sdk-0.1.0-*.whl'))
            if len(wheels) != 1:
                raise ValueError('Expected exactly one newly built SDK wheel')
            normalize_owned_wheel(wheels[0])
            os.replace(wheels[0], output / wheels[0].name)
        with zipfile.ZipFile(output / 'stream-dock-manager-plugin-sdk-go-0.1.0.zip', 'w', zipfile.ZIP_DEFLATED, compresslevel=6) as archive:
            for path in sorted((ROOT / 'Go').rglob('*')):
                if path.is_file() and not any(part.startswith('.') for part in path.relative_to(ROOT / 'Go').parts):
                    archive.write(path, 'github.com/StreamDockManager/Plugin-SDK/Go@v0.1.0/' + path.relative_to(ROOT / 'Go').as_posix())


if __name__ == '__main__':
    main()

#!/usr/bin/env python3
"""Verify the local SDK distributions outside their source checkout; no publication."""
import base64
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import tempfile
import zipfile

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / 'tools'))
from build import run
from release import VERSION


# Install the candidate with the same locked tarballs warmed by the SDK's npm ci
def install_node_candidate(project, archive, env):
    lock = json.loads((ROOT / 'Node/package-lock.json').read_text(encoding='utf8'))
    sdk = lock['packages']['']
    name = sdk['name']
    package = project / archive.name
    shutil.copyfile(archive, package)
    with tarfile.open(package) as tar:
        metadata = json.load(tar.extractfile('package/package.json'))
    if (metadata['name'], metadata['version']) != (name, sdk['version']):
        raise ValueError('Unexpected npm release candidate identity')
    candidate_ref = 'file:' + package.name
    dependencies = sdk['devDependencies'].copy()
    dependencies.pop('prettier')
    lock['packages'].pop('node_modules/prettier')
    manifest = dict(name='sdk-candidate-consumer', version='0.0.0', private=True,
                    devDependencies={**dependencies, name: candidate_ref})
    lock.update(name=manifest['name'], version=manifest['version'])
    lock['packages'][''] = manifest
    install_metadata = {key: metadata[key] for key in (
        'license', 'engines', 'dependencies', 'optionalDependencies', 'peerDependencies',
        'peerDependenciesMeta',
    ) if key in metadata}
    integrity = base64.b64encode(hashlib.sha512(package.read_bytes()).digest()).decode('ascii')
    lock['packages']['node_modules/' + name] = dict(
        install_metadata, version=metadata['version'], resolved=candidate_ref,
        integrity='sha512-' + integrity, dev=True,
    )
    (project / 'package.json').write_text(json.dumps(manifest), encoding='utf8')
    (project / 'package-lock.json').write_text(json.dumps(lock), encoding='utf8')
    run(['npm', 'ci', '--include=dev', '--ignore-scripts', '--no-audit', '--no-fund', '--offline'],
        cwd=project, env=env)


# Run the validate candidates command and enforce its input options
def main():
    env=os.environ.copy()
    env.pop('STREAM_DOCK_MANAGER_SDK_JS_ROOT',None)
    env['STREAM_DOCK_MANAGER_WS_MODULE']=str(ROOT/'Node/node_modules/ws/wrapper.mjs')
    env.update(PIP_DISABLE_PIP_VERSION_CHECK='1',GOWORK='off',GOPROXY='off',CARGO_NET_OFFLINE='true',GOCACHE=str(ROOT/'.cache/go-build'),PIP_CACHE_DIR=str(ROOT/'.cache/pip'))
    with tempfile.TemporaryDirectory() as temporary:
        target=Path(temporary)
        # Crates normalize source mtimes. A reused target can mistake a changed
        # same-version candidate for an older extraction, so qualify a fresh build
        env['CARGO_TARGET_DIR']=str(target/'cargo-target')
        with zipfile.ZipFile(ROOT/f'artifacts/stream-dock-manager-plugin-sdk-go-{VERSION}.zip') as archive:
            archive.extractall(target/'go')
        module=next((target/'go').glob(f'github.com/StreamDockManager/Plugin-SDK/Go@v{VERSION}'))
        subprocess.run(['go','test','./...'],cwd=module,env=env,check=True)
        with tarfile.open(ROOT/f'artifacts/stream-dock-manager-plugin-sdk-{VERSION}.crate') as archive:
            archive.extractall(target/'rust',filter='data')
        manifest=target/f'rust/stream-dock-manager-plugin-sdk-{VERSION}/Cargo.toml'
        subprocess.run(['cargo','test','--locked','--offline','--all-features','--manifest-path',str(manifest)],env=env,check=True)
        wheel=next((ROOT/'artifacts').glob(f'stream_dock_manager_plugin_sdk-{VERSION}-*.whl'))
        with zipfile.ZipFile(wheel) as archive:
            assert not any('__pycache__' in name or name.endswith('.pyc') for name in archive.namelist()), 'wheel contains build caches'
        subprocess.run([sys.executable,'-m','pip','install','--no-index','--no-deps','--no-compile','--target',str(target/'python'),str(wheel)],env=env,check=True)
        env['PYTHONPATH']=str(target/'python')
        check = """import stream_dock_manager_sdk, importlib, sys
from pathlib import Path
import shutil
from stream_dock_manager_sdk import testkit, credentials, process
for name in ['authoring','connectors','credentials','service','httpclient','process','settings','storage','hoststorage','buildcache','inspectors','testkit','managedstate','requests','connectedws','thumbnail','cache','discovery','operations']:
    module=importlib.import_module('stream_dock_manager_sdk.'+name)
    assert Path(module.__file__).is_relative_to(Path(sys.argv[1]))
assert stream_dock_manager_sdk.__version__==sys.argv[2]
assert credentials.secret({'password':'${KEY}'},'password',lambda _: 'value')=='value'
assert testkit.tls()[0] is not None
assert process.run(sys.executable,['-c','print("isolated")']).strip()=='isolated'
print('isolated wheel modules, TLS resources and process passed')
"""
        subprocess.run([sys.executable,'-c',check,str(target/'python'),VERSION],env=env,check=True)
    with tempfile.TemporaryDirectory() as temporary:
        project=Path(temporary)
        install_node_candidate(project, ROOT/f'artifacts/stream-dock-manager-plugin-sdk-{VERSION}.tgz', env)
        (project/'main.mjs').write_text("import {Client,run} from 'stream-dock-manager-plugin-sdk'; const c=new Client(); c.title('a','isolated'); console.log(c.drain()[0].payload.title);\n")
        compile_script="""import {bundlePlugin} from 'stream-dock-manager-plugin-sdk/build';
import {readFile} from 'node:fs/promises';
const pkg=JSON.parse(await readFile('./node_modules/stream-dock-manager-plugin-sdk/package.json','utf8'));
for(const subpath of Object.keys(pkg.exports))await import('stream-dock-manager-plugin-sdk'+(subpath==='.'?'':subpath.slice(1)));
await bundlePlugin({project:process.cwd(),runtime:'nodejs'});
"""
        subprocess.run(['node','--input-type=module','-e',compile_script],cwd=project,env=env,check=True)
        (project/'consumer.mts').write_text("import {Client,run} from 'stream-dock-manager-plugin-sdk'; import {bundlePlugin} from 'stream-dock-manager-plugin-sdk/build'; import {Service} from 'stream-dock-manager-plugin-sdk/httpclient'; import {WorkerPool} from 'stream-dock-manager-plugin-sdk/workers'; const client:Client = new Client(); const service:Service = new Service(); const pool:WorkerPool = new WorkerPool(); void bundlePlugin; void run; void [client,service,pool];\n")
        tsc=project/'node_modules/typescript/bin/tsc'
        subprocess.run(['node',str(tsc),'--noEmit','--module','NodeNext','--moduleResolution','NodeNext','--target','ES2022',str(project/'consumer.mts')],cwd=project,env=env,check=True)
        with tempfile.TemporaryDirectory() as launch:
            shutil.copytree(project/'build/javascript',Path(launch)/'plugin')
            result=subprocess.check_output(['node',str(Path(launch)/'plugin/main.mjs')],text=True)
            if result.strip()!='isolated':raise ValueError('Isolated npm bundle did not execute')
    print('All four local SDK distributions passed isolated validation; registries were not contacted')


if __name__=='__main__':main()

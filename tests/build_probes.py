#!/usr/bin/env python3
"""Explicitly build test-only native SDK probes; never installs or enables them."""
import argparse
import hashlib
import subprocess
import json
import os
from pathlib import Path
import shutil
import sys
import tempfile
ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0,str(ROOT/'examples/tools'))
from generate import generate
from descriptor import prepare
from package import package


# Run the build probes command and enforce its input options
def main():
    languages = [('go','go'),('rust','rs'),('python','py'),('nodejs','mjs')]
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--language', action='append', choices=[language for language, _ in languages], help='Build only the selected language (repeatable); default builds every probe')
    parser.add_argument('--scenario', choices=['extensions', 'storage'], default='extensions')
    options = parser.parse_args()
    parent=ROOT/('artifacts/storage-conformance' if options.scenario=='storage' else 'artifacts/conformance')
    parent.mkdir(parents=True,exist_ok=True)
    files=subprocess.check_output(['git','ls-files','--cached','--others','--exclude-standard'],cwd=ROOT,text=True).splitlines()
    inputs={name:hashlib.sha256((ROOT/name).read_bytes()).hexdigest() for name in files if (ROOT/name).is_file()}
    outputs=[]
    for language,extension in languages:
        if options.language and language not in options.language:
            continue
        project=parent/language
        # Refresh owned generated source, while retaining language build caches
        with tempfile.TemporaryDirectory(prefix='probe-source-', dir=parent) as temporary:
            fresh=generate(Path(temporary)/language,'app.streamdockmanager.plugins.probe.'+language,language)
            shutil.copytree(fresh,project,dirs_exist_ok=True)
        shutil.copyfile(ROOT/'tests/probes'/('main.'+extension),project/('probe.mjs' if extension=='mjs' else 'main.'+extension))
        path=project/'manifest.json'
        manifest=json.loads(path.read_text())
        from descriptor import capabilities
        descriptor=capabilities(manifest)
        manifest['StreamDockManager']['Capabilities']=descriptor
        descriptor['input_protocol']=1
        descriptor['required_capabilities']=['plugin-protocol-v1','telemetry-v1','desktop-input-v1','action-results-v1']
        if options.scenario == 'storage': descriptor['required_capabilities'] = ['plugin-protocol-v1']
        descriptor['actions'][manifest['Actions'][0]['UUID']]['control_types']=['button','encoder','touch']
        manifest['Actions'][0]['Controllers']=['Keypad','Encoder']
        path.write_text(json.dumps(manifest,indent=2)+'\n')
        prepare(project,manifest,language)
        if extension=='mjs':
            (project/'main.mjs').write_text("import {run} from 'stream-dock-manager-plugin-sdk';\nimport {Probe} from './probe.mjs';\nawait run(new Probe());\n")
            os.environ['STREAM_DOCK_MANAGER_SDK_JS_ROOT']=str(ROOT/'Node')
        # Explicit test-only overrides qualify this source checkout, without
        # editing a published manifest or a developer's own configuration
        cache=ROOT/'.cache'
        cache.mkdir(exist_ok=True)
        if language=='go':
            work=cache/'probe.work'
            work.write_text('go 1.27.4\nuse (\n'+json.dumps(str(ROOT/'Go'))+'\n'+json.dumps(str(project))+'\n)\nreplace github.com/StreamDockManager/Plugin-SDK/Go v0.1.0 => '+json.dumps(str(ROOT/'Go'))+'\n')
            os.environ['GOWORK']=str(work)
            os.environ.setdefault('GOCACHE',str(cache/'go-build'))
        if language=='rust':
            cargo=project/'Cargo.toml'
            cargo.write_text(cargo.read_text().replace('stream-dock-manager-plugin-sdk = "=0.1.0"', 'stream-dock-manager-plugin-sdk = { version = "=0.1.0", features = ["http"] }'))
            config=cache/'probe-cargo.toml'
            config.write_text('[patch.crates-io]\nstream-dock-manager-plugin-sdk = { path = '+json.dumps(str(ROOT/'Rust'))+' }\n')
            os.environ['STREAM_DOCK_MANAGER_SDK_CARGO_CONFIG']=str(config)
            subprocess.run(['cargo','generate-lockfile','--offline','--config',str(config)],cwd=project,check=True)
        if language=='python':
            os.environ['PYTHONPATH']=str(ROOT/'Python')+(os.pathsep+os.environ['PYTHONPATH'] if os.environ.get('PYTHONPATH') else '')
            os.environ.setdefault('PYINSTALLER_CONFIG_DIR',str(cache/'pyinstaller'))
        archive=package(project=project)
        outputs.append(dict(language=language,archive=str(archive),sha256=hashlib.sha256(archive.read_bytes()).hexdigest()))
    report = 'build-report-' + '-'.join(sorted(set(options.language))) + '.json' if options.language else 'build-report.json'
    (parent/report).write_text(json.dumps(dict(version=1,sdk_revision=subprocess.check_output(['git','rev-parse','HEAD'],cwd=ROOT,text=True).strip(),source_sha256=inputs,packages=outputs),indent=2)+'\n')


if __name__=='__main__':main()

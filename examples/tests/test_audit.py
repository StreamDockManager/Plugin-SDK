"""Packaging audits inspect metadata, reject missing resources, and retain licenses."""
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT=Path(__file__).resolve().parents[1]
sys.path.insert(0,str(ROOT / "tools"))
import audit


class AuditTests(unittest.TestCase):
    def test_python_notice_follows_the_build_interpreter_and_explicit_override(self):
        with tempfile.TemporaryDirectory() as temporary:
            project=Path(temporary)
            (project/'python-runtime-LICENSE').write_text('author runtime notice')
            library=project/'stdlib';library.mkdir();(library/'LICENSE.txt').write_text('actual build interpreter')
            with patch.object(audit.sysconfig,'get_path',return_value=str(library)), patch.object(audit.sys,'base_prefix',str(project)), patch.dict(audit.os.environ,{'STREAM_DOCK_MANAGER_PYTHON_LICENSE':''}):
                self.assertEqual(audit.runtime_license(project,'python-runtime-LICENSE'),'actual build interpreter')
                (library/'LICENSE.txt').unlink()
                self.assertEqual(audit.runtime_license(project,'python-runtime-LICENSE'),'author runtime notice')
                with patch.dict(audit.os.environ,{'STREAM_DOCK_MANAGER_PYTHON_LICENSE':str(project/'missing')}):
                    with self.assertRaisesRegex(ValueError,'STREAM_DOCK_MANAGER_PYTHON_LICENSE'):
                        audit.runtime_license(project,'python-runtime-LICENSE')
                with patch.dict(audit.os.environ,{'STREAM_DOCK_MANAGER_PYTHON_LICENSE':str(project/'python-runtime-LICENSE')}):
                    self.assertEqual(audit.runtime_license(project,'python-runtime-LICENSE'),'author runtime notice')

    def test_cpp_notices_follow_the_configured_dependencies(self):
        with tempfile.TemporaryDirectory() as temporary:
            project=Path(temporary);build=project/'build';build.mkdir()
            entries=[]
            for name in ('ixwebsocket','nlohmann_json'):
                source=project/name;source.mkdir();(source/'LICENSE').write_text(name+' notice');(source/'source.hpp').write_text('// source')
                entries.append(name+'_SOURCE_DIR:STATIC='+str(source))
            (build/'CMakeCache.txt').write_text('\n'.join(entries))
            result=audit.collect(project,'cpp')
            self.assertEqual([item['name'] for item in result['dependencies']],['ixwebsocket','nlohmann_json'])
            text=audit.license_text(result)
            self.assertIn(b'ixwebsocket notice',text)
            self.assertIn(b'nlohmann_json notice',text)
            self.assertNotIn(b'python',text)

    def test_resource_graph_checks_html_descriptor_and_layout_links(self):
        files={'inspector.html':b'<script src="helper.js"></script>', 'helper.js':b'local', 'icon.png':b'icon', 'display/key.json':b'{"icon":"icon.png"}'}
        manifest={'Icon':'icon.png','PropertyInspectorPath':'inspector.html','StreamDockManager':{'Capabilities':{'actions':{'action':{'feedback_layout':'display/key.json'}}}}}
        result=audit.resource_graph(files,manifest)
        self.assertEqual({item['path'] for item in result},set(files))
        with self.assertRaisesRegex(ValueError,'missing packaged'):audit.resource_graph(files,dict(manifest,Icon='icon'))
        files.pop('helper.js')
        with self.assertRaisesRegex(ValueError,'missing packaged'):audit.resource_graph(files,manifest)
        files['helper.js']=b'ok';files['inspector.html']=b'<script src="../outside.js"></script>'
        with self.assertRaisesRegex(ValueError,'invalid resource'):audit.resource_graph(files,manifest)

    def test_go_closure_keeps_local_sdk_and_omits_own_module(self):
        with tempfile.TemporaryDirectory() as temporary:
            project=Path(temporary);sdk=project/'sdk';sdk.mkdir();(sdk/'LICENSE').write_text('SDK license');(sdk/'source.go').write_text('package sdk')
            runtime=project/'toolchain';runtime.mkdir();(runtime/'LICENSE').write_text('Go runtime license')
            packages=[dict(Module=dict(Path='own',Main=True,Dir=str(project))),dict(Dir=str(sdk),GoFiles=['source.go'],Module=dict(Path='SDK',Main=True,Dir=str(sdk)))]
            def run(command,cwd):
                if command[:2]==['go','list']:return '\n'.join(json.dumps(package) for package in packages)
                if command[-1]=='GOROOT':return str(runtime)
                return 'go1.27.4'
            with patch.object(audit,'run',side_effect=run):data=audit.collect(project,'go')
            self.assertEqual([item['name'] for item in data['dependencies']],['SDK','go-runtime'])
            self.assertIn(b'SDK license',audit.license_text(data))
            self.assertNotIn(str(project),json.dumps(data))
            (sdk/'LICENSE').unlink()
            with patch.object(audit,'run',side_effect=run),self.assertRaisesRegex(ValueError,'license is missing'):audit.collect(project,'go')

    def test_rust_follows_normal_edges_and_excludes_build_only_dependencies(self):
        with tempfile.TemporaryDirectory() as temporary:
            root=Path(temporary);(root/'Cargo.toml').write_text('root')
            dependency=root/'runtime';dependency.mkdir();(dependency/'Cargo.toml').write_text('runtime');(dependency/'LICENSE-MIT').write_text('MIT');(dependency/'LICENSE-APACHE').write_text('Apache')
            graph={'packages':[dict(id='root',name='root',version='0.1.0',manifest_path=str(root/'Cargo.toml'),targets=[]),dict(id='runtime',name='runtime',version='1',manifest_path=str(dependency/'Cargo.toml'),targets=[])], 'resolve':{'root':'root','nodes':[dict(id='root',deps=[dict(pkg='runtime',dep_kinds=[dict(kind=None)]),dict(pkg='build-only',dep_kinds=[dict(kind='build')])]),dict(id='runtime',deps=[])]}}
            with patch.object(audit,'run',return_value=json.dumps(graph)):data=audit.rust_dependencies(root,'native-test')
            self.assertEqual([item['name'] for item in data],['runtime'])
            self.assertEqual(set(data[0]['licenses']),{'LICENSE-MIT','LICENSE-APACHE'})

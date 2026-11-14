# Run from this directory using a supported build Python (3.11-3.15).
from pathlib import Path
import sys
from PyInstaller.utils.hooks import collect_data_files
source = Path(SPECPATH)
sys.path.insert(0, str(source / 'tools' if (source / 'tools').is_dir() else source.parent / 'tools'))
from descriptor import capabilities, frame
from project import native_binary_name
import json
manifest = json.loads((source / 'manifest.json').read_text())
binary_name = native_binary_name(manifest)
encoded = frame(capabilities(manifest))
entry = source / 'build' / 'sdk-entry.py'
entry.parent.mkdir(exist_ok=True)
entry.write_text('from main import Counter\nfrom stream_dock_manager_sdk import run\nimport signal, threading\nstop = threading.Event()\nsignal.signal(signal.SIGTERM, lambda *_: stop.set())\nsignal.signal(signal.SIGINT, lambda *_: stop.set())\nDESCRIPTOR = ' + repr(encoded) + '\nrun(Counter(), descriptor=DESCRIPTOR, stop=stop)\n')
analysis = Analysis([str(entry)], pathex=[str(source)], binaries=[], datas=collect_data_files('stream_dock_manager_sdk'), hiddenimports=['main'], hookspath=[], hooksconfig={}, runtime_hooks=[str(source / 'frozen_inspect.py')], excludes=[])
# Replace only the eager inspect hook. Its ZIP inventory loads native compression
# libraries before registration; our equivalent resolves that inventory on demand.
analysis.scripts = [item for item in analysis.scripts if item[0] != 'pyi_rth_inspect']
pyz = PYZ(analysis.pure)
# The descriptor stays visible to Manager's bounded static scan, before signing.
compression = dict(EXTENSION=True, DATA=True, BINARY=True, EXECUTABLE=True, PYSOURCE=False, PYMODULE=True, SPLASH=True, PYZ=False, SYMLINK=False)
exe = EXE(pyz, analysis.scripts, [], exclude_binaries=True, name=binary_name, debug=False, strip=False, upx=False, console=True, cdict=compression)
collect = COLLECT(exe, analysis.binaries, analysis.datas, strip=False, upx=False, name=binary_name)

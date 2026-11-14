"""Preserve PyInstaller source lookup without opening base_library.zip at startup.

Adapted from PyInstaller's pyi_rth_inspect.py (Copyright 2021-2023 PyInstaller
Development Team), Apache-2.0: https://www.apache.org/licenses/LICENSE-2.0
"""


def _install():
    import inspect
    import os
    import sys

    prefix = os.path.normpath(sys._MEIPASS)
    base_library = os.path.join(prefix, 'base_library.zip')
    base_files = None
    original = inspect.getsourcefile

    def getsourcefile(obj):
        nonlocal base_files
        filename = os.path.normpath(inspect.getfile(obj))
        if not os.path.isabs(filename):
            main_file = getattr(sys.modules['__main__'], '__file__', None)
            if main_file and filename == os.path.basename(main_file):
                return main_file
            if base_files is None:
                import zipfile
                if os.path.isfile(base_library):
                    with zipfile.ZipFile(base_library) as archive:
                        base_files = {os.path.normpath(name) for name in archive.namelist()}
                else:
                    base_files = set()
            pyc = filename + 'c'
            if pyc in base_files:
                return os.path.normpath(os.path.join(base_library, pyc))
            return os.path.normpath(os.path.join(prefix, filename))
        if filename.startswith(prefix):
            return filename
        return original(obj)

    inspect.getsourcefile = getsourcefile


_install()
del _install

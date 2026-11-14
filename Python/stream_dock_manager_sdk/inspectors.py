"""Canonical local inspector helpers, also available inside the installed wheel."""
from importlib.resources import files

# Load the bundled property-inspector assets
def assets():
    result={}
    # Traverse nested values or files using the module’s bounds
    def walk(directory,prefix=''):
        for path in directory.iterdir():
            name=prefix+path.name
            if path.is_file():result[name]=path.read_bytes()
            elif path.is_dir():walk(path,name+'/')
    walk(files('stream_dock_manager_sdk').joinpath('web'))
    return result

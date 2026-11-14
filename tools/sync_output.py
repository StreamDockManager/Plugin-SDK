"""Byte-exact generated output synchronization; check mode never writes."""
from pathlib import Path


# Synchronize output
def sync_output(target, data, *, check=False, diagnostic='Generated output drift', create_parents=True, error=SystemExit):
    target = Path(target)
    if target.is_file() and target.read_bytes() == data:
        return False
    if check:
        raise error(f'{diagnostic}: {target}')
    if create_parents:
        target.parent.mkdir(parents=True, exist_ok=True)
    target.write_bytes(data)
    return True

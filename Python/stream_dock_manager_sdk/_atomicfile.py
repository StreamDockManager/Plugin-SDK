"""Same-directory atomic replacement; callers explicitly choose file sync policy."""
import os
from pathlib import Path
import tempfile
import time


_WINDOWS = os.name == 'nt'
_REPLACE_RETRY_DELAYS = (0.005, 0.010, 0.020, 0.040, 0.080, 0.160, 0.160)
_WINDOWS_CONTENTION = (5, 32, 33)


# Retain atomic replacement while giving temporary Windows file handles time to close
def _replace(source, target):
    delays = iter(_REPLACE_RETRY_DELAYS)
    while True:
        try:
            os.replace(source, target)
            return
        except PermissionError as error:
            if not _WINDOWS or getattr(error, 'winerror', None) not in _WINDOWS_CONTENTION:
                raise
            delay = next(delays, None)
            if delay is None:
                raise
            time.sleep(delay)


# Write complete bytes to a sibling file before atomically promoting it
def write(path, data, *, durable, prefix):
    path = Path(path)
    fd, temporary = tempfile.mkstemp(prefix=prefix, dir=path.parent)
    try:
        with os.fdopen(fd, 'wb') as stream:
            stream.write(data)
            if durable:
                stream.flush()
                os.fsync(stream.fileno())
        _replace(temporary, path)
    finally:
        Path(temporary).unlink(missing_ok=True)

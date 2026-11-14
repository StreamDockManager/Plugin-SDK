"""Compression policy for freshly built first-party SDK wheel archives only.

Member bytes (including wheel RECORD hashes), names, ordering and metadata are
preserved. Never apply this to downloaded or signed third-party distributions.
"""
import copy
import os
from pathlib import Path
import tempfile
import zipfile


# Normalize owned wheel
def normalize_owned_wheel(path):
    path = Path(path)
    if path.is_symlink() or not path.is_file() or path.suffix != '.whl':
        raise ValueError('Expected a newly built regular first-party wheel')
    fd, temporary = tempfile.mkstemp(prefix='.wheel-deflate6-', suffix='.whl', dir=path.parent)
    os.close(fd)
    try:
        with zipfile.ZipFile(path) as source, zipfile.ZipFile(temporary, 'w', compression=zipfile.ZIP_DEFLATED, compresslevel=6) as output:
            entries = source.infolist()
            if len(entries) > 65536 or sum(item.file_size for item in entries) > 512 * 1024 * 1024:
                raise ValueError('Wheel exceeds compression policy bounds')
            if any(item.filename.endswith(('/RECORD.jws', '/RECORD.p7s')) or item.flag_bits & 1 for item in entries):
                raise ValueError('Refusing to rewrite signed or encrypted wheel')
            output.comment = source.comment
            for item in entries:
                output.writestr(copy.copy(item), source.read(item), compress_type=zipfile.ZIP_DEFLATED, compresslevel=6)
        os.replace(temporary, path)
    finally:
        Path(temporary).unlink(missing_ok=True)

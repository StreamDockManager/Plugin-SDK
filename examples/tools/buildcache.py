"""Deterministic input fingerprints and verified artifact cache records."""
import hashlib
import json
import os
from pathlib import Path
from _atomicfile import write as atomic_write


# Stream file contents into the artifact integrity digest
def _file_hash(path):
    digest = hashlib.sha256()
    buffer = bytearray(64 << 10)
    view = memoryview(buffer)
    with Path(path).open('rb', buffering=0) as stream:
        while size := stream.readinto(buffer):
            digest.update(view[:size])
    return digest.hexdigest()


# Hash ordered source inputs and build configuration for cache identity
def fingerprint(inputs, configuration):
    digest = hashlib.sha256(json.dumps(configuration, sort_keys=True, separators=(',', ':'), ensure_ascii=False).encode())
    for name in sorted(inputs):
        data = inputs[name].encode() if isinstance(inputs[name], str) else inputs[name]
        digest.update(f'{name}\0{len(data)}\0'.encode()); digest.update(data)
    return digest.hexdigest()


# Read cached build artifacts only when their identity and integrity match
def cached(path, key):
    try:
        record = json.loads(Path(str(path) + '.build.json').read_text())
        return record['input'] == key and record['output'] == _file_hash(path)
    except (OSError, ValueError, KeyError, TypeError): return False


# Retain the result for subsequent cache or diagnostic reads
def remember(path, key):
    path = Path(path)
    value = dict(input=key, output=_file_hash(path))
    atomic_write(str(path) + '.build.json', json.dumps(value).encode(), durable=False, prefix='.cache-')


# Run build tasks with bounded worker concurrency
def parallel(items, jobs, work, cancel=None):
    from concurrent.futures import ThreadPoolExecutor
    if type(jobs) is not int or not 1 <= jobs <= 128: raise ValueError('jobs must be between 1 and 128')
    # Drive the buildcache execution loop
    def run(item):
        if cancel is not None and cancel.is_set(): raise RuntimeError('build cancelled')
        return work(item)
    with ThreadPoolExecutor(max_workers=jobs) as pool:
        return list(pool.map(run, items))

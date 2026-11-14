"""Bounded subprocesses with cooperative cancellation and exact argument vectors."""
import os
import subprocess
import threading
import time


# Run a command through the cancellable background worker
def background_command(name, args=(), **options):
    if os.name == 'nt': options['creationflags'] = options.get('creationflags', 0) | subprocess.CREATE_NO_WINDOW
    return subprocess.Popen([name, *args], shell=False, **options)


# Drive the process execution loop
def run(name, args=(), *, timeout=8, output_limit=1 << 20, cancel=None, cwd=None, env=None):
    if cancel is not None and cancel.is_set(): raise RuntimeError('process cancelled')
    process = background_command(name, args, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE, cwd=cwd, env=env)
    outputs, overflow = [bytearray(), bytearray()], threading.Event()
    # Read available subprocess output without waiting for a full buffer or process exit
    def read(stream, output):
        try:
            while True:
                data = stream.read1(65536)
                if not data: break
                remaining = max(0, output_limit - len(output))
                output.extend(data[:remaining])
                if len(data) > remaining: overflow.set()
        finally: stream.close()
    workers = [threading.Thread(target=read, args=(stream, output), daemon=True) for stream, output in zip((process.stdout, process.stderr), outputs)]
    for worker in workers: worker.start()
    deadline = time.monotonic() + timeout
    failure = None
    try:
        while process.poll() is None:
            if cancel is not None and cancel.is_set(): failure = RuntimeError('process cancelled')
            elif time.monotonic() >= deadline: failure = RuntimeError('process deadline exceeded')
            elif overflow.is_set(): failure = ValueError('process response exceeds limit')
            if failure: process.kill(); break
            time.sleep(.005)
        process.wait(timeout=1)
        for worker in workers: worker.join(1)
        if failure is not None: raise failure
        if overflow.is_set(): raise ValueError('process response exceeds limit')
        if any(worker.is_alive() for worker in workers): raise RuntimeError('process output did not close')
        if process.returncode: raise RuntimeError(f'{name} failed; check selection and operating-system permissions')
        return outputs[0].decode()
    finally:
        if process.poll() is None: process.kill(); process.wait()

"""Opt-in payload adapters. Control WebSocket traffic is never instrumented."""
import threading


class NetworkPayload:
    # Initialize synchronized sent and received byte counters
    def __init__(self):
        from .diagnostics import NetworkDiagnostics
        self.diagnostics = NetworkDiagnostics()
        self._lock = threading.Lock()
        self._sent = self._received = 0

    # Add network payload data
    def add(self, sent=0, received=0):
        if sent < 0 or received < 0:
            raise ValueError('payload increments must be nonnegative')
        with self._lock:
            self._sent += sent
            self._received += received

    # Copy the current state for an isolated reader
    def snapshot(self):
        with self._lock:
            return self._sent, self._received

    # Return track for network payload
    def track(self, stream):
        return TrackedStream(stream, self)


class TrackedStream:
    """Wrap a binary body or TCP stream once, before any decompression layer.

    Attribute access delegates to the original stream; callers retain its ownership.
    Do not wrap both an HTTP body and its underlying socket (that double counts).
    """
    # Wrap the stream with network byte accounting
    def __init__(self, stream, counters):
        self.stream, self.counters = stream, counters

    # Resolve delegated tracked stream attributes
    def __getattr__(self, name):
        return getattr(self.stream, name)

    # Read the next tracked stream value or data segment
    def read(self, *args):
        data = self.stream.read(*args)
        self.counters.add(received=len(data or b''))
        self.counters.diagnostics.io('read', len(data or b''))
        return data

    # Read bounded response bytes into the supplied buffer
    def readinto(self, buffer):
        size = self.stream.readinto(buffer)
        self.counters.add(received=size or 0)
        self.counters.diagnostics.io('read', size or 0)
        return size

    # Write the supplied tracked stream value
    def write(self, data):
        size = self.stream.write(data)
        self.counters.add(sent=size or 0)
        self.counters.diagnostics.io('write', size or 0)
        return size

    # Receive the next tracked stream message
    def recv(self, *args):
        data = self.stream.recv(*args)
        self.counters.add(received=len(data))
        self.counters.diagnostics.io('read', len(data))
        return data

    # Send the supplied tracked stream message
    def send(self, data, *args):
        size = self.stream.send(data, *args)
        self.counters.add(sent=size)
        self.counters.diagnostics.io('write', size)
        return size

    # Write the complete framed byte sequence to the transport
    def sendall(self, data, *args):
        view = memoryview(data)
        while view:
            size = self.send(view, *args)
            if not size:
                raise ConnectionError('payload socket stopped writing')
            view = view[size:]

    # Enter the managed tracked stream scope
    def __enter__(self):
        return self

    # Release tracked stream resources when leaving the scope
    def __exit__(self, *args):
        return self.stream.__exit__(*args)

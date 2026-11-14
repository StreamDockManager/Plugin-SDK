"""Connection-oriented service API; independent of webhook extraction settings."""
import json
import io
import time
import http.client
import threading
import socket
from urllib.parse import urlsplit
from .polling import retry_after


class HTTPError(RuntimeError):
    # Retain the HTTP status and optional retry deadline
    def __init__(self, status, retry_at=None):
        self.status, self.retry_at = status, retry_at
        super().__init__(f'Service returned HTTP {status}; check credentials, permissions and selection')
    # Return safe error for httperror
    def safe_error(self): return HTTPError(self.status, self.retry_at)


class _ResponseReader(io.RawIOBase):
    # Wrap the response stream with cancellation-aware bounded reads
    def __init__(self, sock, source, remaining):
        self.sock, self.source, self.remaining = sock, source, remaining
    # Return readable for response reader
    def readable(self): return True
    # Read bounded response bytes into the supplied buffer
    def readinto(self, buffer):
        if self.closed: raise ValueError('read from closed response')
        while True:
            # Do not rely on cross-thread shutdown to wake a timed Winsock read
            # Retry below BufferedReader so a polling timeout cannot discard
            # partial HTTP headers/body or poison socket.makefile's SocketIO
            self.sock.settimeout(min(.05, self.remaining()))
            try: return self.sock.recv_into(buffer)
            except TimeoutError: pass
    # Stop response reader activity and release owned resources
    def close(self):
        try: self.source.close()
        finally: super().close()


class _Connection:
    # Track connection ownership, active socket, and reuse time
    def __init__(self, connection):
        self.connection = connection
        self.busy = False
        self.active_socket = None
        self.used = time.monotonic()
    # Stop connection activity and release owned resources
    def close(self):
        # Wake an active read before closing. No pool lock is held during I/O
        sock = self.connection.sock or self.active_socket
        if sock is not None:
            try: sock.shutdown(socket.SHUT_RDWR)
            except OSError: pass
        self.connection.close()


class Service:
    # Initialize the bounded HTTP connection pool and request timeout
    def __init__(self, limit=4 << 20, timeout=10, network=None):
        self.limit, self.timeout, self.network = limit, timeout, network
        self.connections = {}
        self.lock = threading.Condition(threading.RLock())
        self.closed = False

    # Acquire an available pooled HTTP connection under the caller deadline
    def _lease(self, key, deadline, cancel):
        with self.lock:
            while True:
                if self.closed: raise RuntimeError('service closed')
                if cancel is not None and cancel.is_set(): raise RuntimeError('request cancelled')
                remaining = deadline - time.monotonic()
                if remaining <= 0: raise TimeoutError('service request timed out waiting for connection')
                entry = self.connections.get(key)
                if entry is None:
                    if len(self.connections) >= 64:
                        idle = [(value.used, origin) for origin, value in self.connections.items() if not value.busy]
                        if not idle:
                            self.lock.wait(min(.05, remaining))
                            continue
                        self.connections.pop(min(idle)[1]).close()
                    scheme, hostname, port = key
                    connection = (http.client.HTTPSConnection if scheme == 'https' else http.client.HTTPConnection)(hostname, port, timeout=remaining)
                    entry = self.connections[key] = _Connection(connection)
                if not entry.busy:
                    entry.busy = True
                    entry.connection.timeout = remaining
                    if entry.connection.sock is not None: entry.connection.sock.settimeout(remaining)
                    return entry
                self.lock.wait(min(.05, remaining))

    # Release service data
    def _release(self, key, entry, failed):
        with self.lock:
            entry.busy = False
            entry.active_socket = None
            entry.used = time.monotonic()
            if failed and self.connections.get(key) is entry: self.connections.pop(key)
            self.lock.notify_all()
        if failed: entry.close()

    # Execute the HTTP request under cancellation and response limits
    def do(self, method, endpoint, headers=None, body=b'', cancel=None):
        url = urlsplit(endpoint)
        if url.scheme not in ('http', 'https') or not url.hostname or url.username or url.password or url.fragment: raise ValueError('invalid service endpoint')
        if cancel is not None and cancel.is_set(): raise RuntimeError('request cancelled')
        key = (url.scheme, url.hostname, url.port or (443 if url.scheme == 'https' else 80))
        deadline = time.monotonic() + self.timeout
        entry = self._lease(key, deadline, cancel)
        connection = entry.connection
        response_class = getattr(connection, 'response_class', http.client.HTTPResponse)
        # Calculate time remaining before the service deadline
        def remaining():
            if self.closed or cancel is not None and cancel.is_set(): raise RuntimeError('request cancelled')
            seconds = deadline - time.monotonic()
            if seconds <= 0: raise TimeoutError('service request timed out')
            return seconds
        # Wrap the response body with deadline and cancellation handling
        def response_reader(sock, *args, **kwargs):
            response = response_class(sock, *args, **kwargs)
            # Keep the original raw file's socket ownership: http.client may
            # close/detach connection.sock before a Connection: close body ends
            source = response.fp.detach()
            response.fp = io.BufferedReader(_ResponseReader(sock, source, remaining))
            return response
        connection.response_class = response_reader
        failed = False
        finished = threading.Event()
        watcher = None
        if cancel is not None:
            # Watch cancel
            def watch_cancel():
                while not finished.wait(.05):
                    if cancel.is_set():
                        entry.close()
                        return
            watcher = threading.Thread(target=watch_cancel, daemon=True)
            watcher.start()
        trace = self.network.diagnostics.start(method, endpoint, headers) if self.network else None
        status = 0
        try:
            connection.request(method, (url.path or '/') + ('?' + url.query if url.query else ''), body=body or None, headers=headers or {})
            # getresponse may detach the connection's socket for Connection: close
            # keep this lease's socket so cancellation still interrupts body reads
            entry.active_socket = connection.sock
            if self.network: self.network.add(len(body), 0)
            if trace:
                trace.request.write(body)
                trace.request.complete = True
            response = connection.getresponse()
            status = response.status
            if trace:
                grouped = {}
                for name, value in response.getheaders(): grouped.setdefault(name, []).append(value)
                trace.received(grouped)
            with response:
                data = bytearray()
                while True:
                    remaining()
                    chunk = response.read(min(65536, self.limit + 1 - len(data)))
                    remaining()
                    if not chunk:
                        if trace: trace.response.complete = True
                        break
                    if trace: trace.response.write(chunk)
                    if self.network: self.network.add(0,len(chunk))
                    data.extend(chunk)
                    if len(data) > self.limit: raise ValueError('invalid or oversized service response')
                if trace: trace.finish(status)
                if not 200 <= response.status < 300: raise HTTPError(response.status, retry_after(response.getheader('Retry-After', ''), time.time()))
                return bytes(data)
        except Exception:
            failed = True
            raise
        finally:
            finished.set()
            if watcher is not None: watcher.join(.2)
            connection.response_class = response_class
            self._release(key, entry, failed)
            if trace: trace.finish(status, True)
    # Decode the bounded service response as JSON
    def json(self, method, endpoint, headers=None, body=None, cancel=None):
        headers = dict(headers or {})
        if body is not None and not any(key.lower()=='content-type' for key in headers): headers['Content-Type']='application/json'
        data = self.do(method, endpoint, headers, b'' if body is None else json.dumps(body, allow_nan=False).encode(), cancel)
        try: return json.loads(data) if data else None
        except ValueError: raise ValueError('malformed service response') from None
    # Stop service activity and release owned resources
    def close(self):
        with self.lock:
            self.closed = True
            connections, self.connections = self.connections, {}
            self.lock.notify_all()
        for entry in connections.values(): entry.close()

"""Bounded, loopback-only WebSocket transport without TLS/proxy startup imports.

wsproto owns the handshake and RFC 6455 validation. HeaderBudget rejects oversized
messages before their payloads enter the protocol engine (including fragments).
"""
from collections import deque
import io
import socket
import threading
import time

from wsproto import ConnectionType, WSConnection
from wsproto.connection import ConnectionState
from wsproto.events import AcceptConnection, BytesMessage, CloseConnection, Ping, Pong, Request, TextMessage

MAX_MESSAGE = 1 << 20
MAX_UPGRADE = 64 << 10


class HeaderBudget:
    # Initialize incremental WebSocket header and message-size accounting
    def __init__(self, maximum=MAX_MESSAGE):
        self.maximum = maximum
        self.header = bytearray()
        self.remaining = 0
        self.message_size = 0
        self.fragmented = False
        self.ends_message = False

    # Account for incoming bytes against the header budget
    def feed(self, data):
        offset = 0
        while offset < len(data):
            if self.remaining:
                count = min(self.remaining, len(data) - offset)
                self.remaining -= count
                offset += count
                if self.remaining == 0 and self.ends_message:
                    self.message_size = 0
                continue
            needed = 2
            if len(self.header) >= 2:
                needed += {126: 2, 127: 8}.get(self.header[1] & 127, 0)
            count = min(needed - len(self.header), len(data) - offset)
            self.header.extend(data[offset:offset + count])
            offset += count
            if len(self.header) < 2:
                continue
            first, second = self.header[:2]
            if second & 128:
                raise ValueError('host frames must not be masked')
            short = second & 127
            needed = 2 + {126: 2, 127: 8}.get(short, 0)
            if len(self.header) < needed:
                continue
            length = short if short < 126 else int.from_bytes(self.header[2:], 'big')
            opcode, final = first & 15, bool(first & 128)
            if first & 112 or opcode not in (0, 1, 2, 8, 9, 10):
                raise ValueError('invalid host frame')
            if short == 126 and length < 126 or short == 127 and (length < 65536 or length >= 1 << 63):
                raise ValueError('invalid host frame length')
            self.ends_message = False
            if opcode >= 8:
                if not final or length > 125:
                    raise ValueError('invalid control frame length or fragmentation')
            else:
                if (opcode == 0) != self.fragmented:
                    raise ValueError('invalid message fragmentation')
                self.message_size += length
                if self.message_size > self.maximum:
                    raise ValueError('host message exceeds limit')
                self.fragmented = not final
                self.ends_message = final
            self.remaining = length
            self.header.clear()
            if length == 0 and self.ends_message:
                self.message_size = 0


class BoundedSocket:
    # Wrap the connection with WebSocket framing and message limits
    def __init__(self, transport, maximum=MAX_MESSAGE):
        self._socket = transport
        self._protocol = WSConnection(ConnectionType.CLIENT)
        self._lock = threading.Lock()
        self._budget = HeaderBudget(maximum)
        self.maximum = maximum
        self.last_pong = time.monotonic()
        self._messages = deque()
        self._message = None
        self._closed = False

    # Receive the next bounded socket message
    def _receive(self, data):
        self._budget.feed(data)
        self._protocol.receive_data(data)
        for event in self._protocol.events():
            if isinstance(event, (TextMessage, BytesMessage)):
                if self._message is None:
                    self._message = io.StringIO() if isinstance(event, TextMessage) else io.BytesIO()
                self._message.write(event.data)
                if event.message_finished:
                    self._messages.append(self._message.getvalue())
                    self._message.close()
                    self._message = None
            elif isinstance(event, Ping):
                self._socket.sendall(self._protocol.send(event.response()))
            elif isinstance(event, Pong):
                self.last_pong = time.monotonic()
            elif isinstance(event, CloseConnection):
                if self._protocol.state is ConnectionState.REMOTE_CLOSING:
                    self._socket.sendall(self._protocol.send(event.response()))
                if event.code not in (1000, 1001):
                    raise ValueError(f'host WebSocket closed: {event.code}: {event.reason}')
                self._closed = True
                self._messages.append('')

    # Send the supplied bounded socket message
    def send(self, text):
        if len(text.encode()) > self.maximum:
            raise ValueError('SDK message exceeds 1048576-byte UTF-8 JSON limit')
        with self._lock:
            self._socket.sendall(self._protocol.send(TextMessage(data=text)))

    # Receive the next bounded socket message
    def recv(self):
        while True:
            with self._lock:
                if self._messages:
                    return self._messages.popleft()
                if self._closed:
                    return ''
            # Timeouts preserve the parser, partial UTF-8 messages and budget
            data = self._socket.recv(64 << 10)
            if not data:
                return ''
            with self._lock:
                self._receive(data)

    # Respond to the WebSocket control frame without disturbing message assembly
    def ping(self):
        with self._lock:
            self._socket.sendall(self._protocol.send(Ping(payload=b'')))

    # Interrupt the active socket operation and release its connection
    def abort(self):
        # Shutdown interrupts a blocked writer; it must not wait for its lock
        self._closed = True
        try: self._socket.shutdown(socket.SHUT_RDWR)
        except OSError: pass
        self._socket.close()

    # Stop bounded socket activity and release owned resources
    def close(self, timeout=1):
        with self._lock:
            try:
                if self._protocol.state is ConnectionState.OPEN:
                    self._socket.settimeout(timeout)
                    self._socket.sendall(self._protocol.send(CloseConnection(code=1000)))
            except OSError:
                pass
            finally:
                self._closed = True
                try:
                    self._socket.shutdown(socket.SHUT_RDWR)
                except OSError:
                    pass
                self._socket.close()


# Use a numeric AF_INET endpoint; environment proxies and DNS never apply
def connect_loopback(port, target='/', *, timeout=1):
    """Use a numeric AF_INET endpoint; environment proxies and DNS never apply."""
    transport = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    try:
        connection = BoundedSocket(transport)
        # Request encoding can lazily load Python's IDNA/Unicode libraries. Do
        # that before opening TCP: the host bounds how long it waits for a GET
        upgrade = connection._protocol.send(Request(host=f'127.0.0.1:{port}', target=target))
        transport.settimeout(timeout)
        transport.connect(('127.0.0.1', port))
        transport.sendall(upgrade)
        response = bytearray()
        deadline = time.monotonic() + timeout
        while b'\r\n\r\n' not in response:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise TimeoutError('host WebSocket upgrade timed out')
            transport.settimeout(remaining)
            data = transport.recv(4096)
            if not data:
                raise ConnectionError('host disconnected during WebSocket upgrade')
            response.extend(data)
            if len(response) > MAX_UPGRADE:
                raise ValueError('host WebSocket upgrade exceeds limit')
        end = response.index(b'\r\n\r\n') + 4
        connection._protocol.receive_data(bytes(response[:end]))
        if not any(isinstance(event, AcceptConnection) for event in connection._protocol.events()):
            raise ConnectionError('host rejected WebSocket upgrade')
        if end < len(response):
            connection._receive(bytes(response[end:]))
        transport.settimeout(timeout)
        return connection
    except BaseException:
        transport.close()
        raise

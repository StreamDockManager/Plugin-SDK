"""Transport resource limits, RFC validation and cancellation on disposable peers."""
from collections import deque
from pathlib import Path
import socket
import subprocess
import sys
import threading
import time
import unittest
from unittest.mock import patch
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from stream_dock_manager_sdk import wire
from wsproto import WSConnection, ConnectionType
from wsproto.events import AcceptConnection, Request, TextMessage


# Build the bounded wire envelope for the supplied payload
def frame(data=b'', opcode=1, final=True):
    length = len(data)
    header = bytes([opcode | (128 if final else 0)])
    header += bytes([length]) if length < 126 else b'\x7e' + length.to_bytes(2, 'big')
    return header + data


class Transport:
    # Initialize transport state and dependencies
    def __init__(self, chunks=()):
        self.chunks = deque(chunks)
        self.sent = []
        self.closed = False
    # Receive the next transport message
    def recv(self, size):
        chunk = self.chunks.popleft()
        if isinstance(chunk, Exception): raise chunk
        self.chunks.appendleft(chunk[size:]) if len(chunk) > size else None
        return chunk[:size]
    # Write the complete framed byte sequence to the transport
    def sendall(self, data): self.sent.append(data)
    # Configure the transport operation timeout
    def settimeout(self, timeout): pass
    # Establish the validated transport connection
    def connect(self, endpoint): self.endpoint = endpoint
    # Stop accepting work and shut down transport resources
    def shutdown(self, how): pass
    # Stop transport activity and release owned resources
    def close(self): self.closed = True


# Create a bounded socket wrapper for the established transport
def connected(transport):
    client = wire.BoundedSocket(transport)
    server = WSConnection(ConnectionType.SERVER)
    server.receive_data(client._protocol.send(Request(host='127.0.0.1', target='/')))
    list(server.events())
    client._protocol.receive_data(server.send(AcceptConnection()))
    list(client._protocol.events())
    return client


class WireTests(unittest.TestCase):
    # Verify oversize length rejected before payload read
    def test_oversize_length_rejected_before_payload_read(self):
        transport = Transport([b'\x81\x7f' + (wire.MAX_MESSAGE + 1).to_bytes(8, 'big')])
        with self.assertRaisesRegex(ValueError, 'exceeds limit'):
            connected(transport).recv()
        self.assertFalse(transport.chunks)

    # Verify fragment total is bounded
    def test_fragment_total_is_bounded(self):
        budget = wire.HeaderBudget()
        budget.feed(b'\x01\x7f' + wire.MAX_MESSAGE.to_bytes(8, 'big'))
        for _ in range(wire.MAX_MESSAGE // 1024): budget.feed(b'a' * 1024)
        budget.feed(frame(b'ping', opcode=9))
        with self.assertRaisesRegex(ValueError, 'exceeds limit'): budget.feed(b'\x80\x01')

    # Verify masked host frames rejected
    def test_masked_host_frames_rejected(self):
        with self.assertRaisesRegex(ValueError, 'masked'):
            connected(Transport([b'\x81\x80'])).recv()

    # Verify invalid headers and utf8 rejected
    def test_invalid_headers_and_utf8_rejected(self):
        for data in [b'\x89\x7e\x00\x7e', b'\x09\x00', b'\xc1\x00', b'\x83\x00',
                     b'\x80\x00', b'\x81\x7e\x00\x01', frame(b'\xff')]:
            with self.subTest(data=data), self.assertRaises(ValueError):
                connected(Transport([data])).recv()

    # Verify partial fragments survive timeout with interleaved ping
    def test_partial_fragments_survive_timeout_with_interleaved_ping(self):
        data = '€'.encode()
        transport = Transport([b'\x01', b'\x01' + data[:1], TimeoutError(),
                               frame(b'ping', opcode=9) + frame(data[1:], opcode=0)])
        client = connected(transport)
        with self.assertRaises(TimeoutError): client.recv()
        self.assertEqual(client.recv(), '€')
        self.assertEqual(transport.sent[0][0], 0x8a)
        self.assertTrue(transport.sent[0][1] & 128)  # Client pong is masked

    # Verify message budget resets between complete messages
    def test_message_budget_resets_between_complete_messages(self):
        client = connected(Transport([frame(b'one') + frame(b'two')]))
        self.assertEqual([client.recv(), client.recv()], ['one', 'two'])

    # Verify close unblocks reader
    def test_close_unblocks_reader(self):
        local, remote = socket.socketpair()
        client = connected(local)
        result = []
        # Read the next wire tests value or data segment
        def read():
            try: result.append(client.recv())
            except OSError: result.append('closed')
        reader = threading.Thread(target=read)
        try:
            reader.start()
            client.close()
            reader.join(1)
            self.assertFalse(reader.is_alive())
            self.assertIn(result, [[''], ['closed']])
        finally:
            remote.close()
            local.close()

    # Verify rejected upgrade closes transport
    def test_rejected_upgrade_closes_transport(self):
        transport = Transport([b'HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n'])
        with patch.object(wire.socket, 'socket', return_value=transport), self.assertRaises(ConnectionError):
            wire.connect_loopback(1234)
        self.assertTrue(transport.closed)
        self.assertEqual(transport.endpoint, ('127.0.0.1', 1234))

    # Verify disposable tcp upgrade and coalesced message
    def test_disposable_tcp_upgrade_and_coalesced_message(self):
        errors = []
        with socket.socket() as listener:
            listener.bind(('127.0.0.1', 0)); listener.listen(1); listener.settimeout(2)
            # Prepare host in the wire tests fixture
            def host():
                try:
                    transport, _ = listener.accept()
                    with transport:
                        transport.settimeout(.5)
                        server = WSConnection(ConnectionType.SERVER)
                        request = False
                        while not request:
                            server.receive_data(transport.recv(4096))
                            request = any(isinstance(event, Request) for event in server.events())
                        transport.sendall(server.send(AcceptConnection()) + server.send(TextMessage(data='hello')))
                        message = None
                        while message is None:
                            server.receive_data(transport.recv(4096))
                            message = next((e.data for e in server.events() if isinstance(e, TextMessage)), None)
                        self.assertEqual(message, 'registration')
                except BaseException as error: errors.append(error)
            peer = threading.Thread(target=host)
            peer.start()
            class ColdTarget(str):
                # Encode cold target data
                def encode(self, *args, **kwargs):
                    # Simulate cold codec loading taking longer than the host's
                    # initial request timeout, without changing that deadline
                    time.sleep(.6)
                    return super().encode(*args, **kwargs)
            client = wire.connect_loopback(listener.getsockname()[1], ColdTarget('/'))
            try:
                self.assertEqual(client.recv(), 'hello')
                client.send('registration')
            finally:
                peer.join(3)
                client.close()
            self.assertFalse(peer.is_alive())
            if errors: raise errors[0]

    # Verify startup does not import optional native libraries
    def test_startup_does_not_import_optional_native_libraries(self):
        source = str(Path(__file__).resolve().parents[1])
        code = f'''import sys, socket
baseline = set(sys.modules)
sys.path.insert(0, {source!r})
import stream_dock_manager_sdk.wire
from stream_dock_manager_sdk.runtime import LaunchOptions
options = LaunchOptions.parse(['-port', '1234', '-pluginUUID', 'fixture', '-registerEvent', 'registerPlugin'])
assert options.port == 1234
assert 'shutil' not in sys.modules
for name in ('ssl', '_ssl', 'zipfile', 'bz2', 'lzma', 'array', 'math', '_queue', '_heapq'):
    assert name in baseline or name not in sys.modules, name
'''
        subprocess.run([sys.executable, '-I', '-c', code], check=True)

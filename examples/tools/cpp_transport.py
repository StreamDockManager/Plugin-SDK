"""Executable envelope checks for the maintained minimal C++ adapter (stdlib only)."""
import base64
from contextlib import contextmanager
import hashlib
import json
import os
from pathlib import Path
import socket
import struct
import subprocess
import tempfile
import threading
import time
import zipfile
from project import native_binary_name

LIMIT = 1 << 20


# Build the bounded wire envelope for the supplied payload
def frame(payload=b'', opcode=1, final=True, declared=None):
    size = len(payload) if declared is None else declared
    header = bytes([(128 if final else 0) | opcode])
    header += bytes([size]) if size < 126 else b'\x7e' + struct.pack('!H', size) if size < 65536 else b'\x7f' + struct.pack('!Q', size)
    return header + payload


# Receive the next cpp transport message
def receive(connection):
    # Read the requested byte count or fail on premature closure
    def exact(size):
        result = bytearray()
        while len(result) < size:
            part = connection.recv(size - len(result))
            if not part:
                raise EOFError('adapter closed the transport')
            result.extend(part)
        return bytes(result)
    first, second = exact(2)
    size = second & 127
    if size == 126:
        size = struct.unpack('!H', exact(2))[0]
    elif size == 127:
        size = struct.unpack('!Q', exact(8))[0]
    if size > LIMIT:
        raise AssertionError('adapter sent an oversized frame')
    mask = exact(4) if second & 128 else None
    data = exact(size)
    if mask:
        data = bytes(value ^ mask[i % 4] for i, value in enumerate(data))
    return first, data


# Launch the native adapter against a disposable local WebSocket host
@contextmanager
def adapter(binary):
    with socket.socket() as server:
        server.bind(('127.0.0.1', 0))
        server.listen(1)
        server.settimeout(5)
        process = subprocess.Popen([str(binary), '-port', str(server.getsockname()[1]), '-pluginUUID', 'transport-fixture', '-registerEvent', 'registerPlugin', '-info', json.dumps({'streamDockManager': {'contract_version': 1, 'capabilities': ['plugin-protocol-v1', 'action-results-v1']}})], stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
        try:
            connection, _ = server.accept()
            with connection:
                connection.settimeout(3)
                request = bytearray()
                while not request.endswith(b'\r\n\r\n'):
                    byte = connection.recv(1)
                    if not byte:
                        raise EOFError('adapter disconnected during handshake')
                    request.extend(byte)
                    if len(request) > 8192:
                        raise AssertionError('oversized handshake')
                headers = dict(line.split(': ', 1) for line in request.decode().split('\r\n')[1:] if ': ' in line)
                key = next(value for name, value in headers.items() if name.lower() == 'sec-websocket-key')
                accept = base64.b64encode(hashlib.sha1((key + '258EAFA5-E914-47DA-95CA-C5AB0DC85B11').encode()).digest())
                connection.sendall(b'HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: ' + accept + b'\r\n\r\n')
                _, registration = receive(connection)
                assert json.loads(registration)['event'] == 'registerPlugin'
                yield connection, process
        finally:
            if process.poll() is None:
                process.terminate()
            try:
                process.communicate(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.communicate(timeout=5)


# Assert that invalid transport input closes the adapter connection
def expect_close(connection, code):
    first, payload = receive(connection)
    assert first & 15 == 8 and struct.unpack('!H', payload[:2])[0] == code, (first, payload[:100])
    connection.sendall(frame(payload, opcode=8))


# Validate binary
def validate_binary(binary):
    for label, wire, code in [
        ('oversized header', frame(declared=LIMIT + 1), 1009),
        ('huge header', frame(declared=1 << 62), 1009),
        ('binary header', frame(opcode=2, declared=LIMIT), 1003),
        ('fragment total', frame(b' ' * LIMIT, final=False) + frame(opcode=0, declared=1), 1009),
    ]:
        with adapter(binary) as (connection, _):
            connection.sendall(wire)
            expect_close(connection, code)
        print('C++ envelope:', label, 'rejected before declared payload', flush=True)
    with adapter(binary) as (connection, _):
        event = json.dumps({'event': 'willAppear', 'context': 'counter', 'payload': {'settings': {'prefix': 'Test', 'count': 4}}}).encode()
        # Empty continuation frames do not retain a list node per fragment
        connection.sendall(frame(event[:10], final=False) + frame(opcode=0, final=False) * 5000 + frame(event[10:], opcode=0))
        unknown = b'{"event":"unknown"}'
        connection.sendall(frame(unknown + b' ' * (LIMIT - len(unknown))))
        connection.sendall(frame(json.dumps({'event': 'keyUp', 'context': 'counter'}).encode()))
        while True:
            first, payload = receive(connection)
            assert first & 15 == 1
            message = json.loads(payload)
            if message['event'] == 'setSettings':
                assert message['payload']['count'] == 5
                break
        connection.sendall(frame(b'{"event":"shutdown"}'))
    print('C++ envelope: exact limit, fragmented counter, empty continuations and shutdown passed', flush=True)
    with adapter(binary) as (connection, process):
        base = {'event': 'willAppear', 'context': '', 'payload': {'settings': {}}}
        encoded = json.dumps(base, separators=(',', ':')).encode()
        base['context'] = 'x' * (LIMIT - len(encoded))
        encoded = json.dumps(base, separators=(',', ':')).encode()
        assert len(encoded) == LIMIT
        connection.sendall(frame(encoded))
        # The echoed context makes setFeedbackLayout exceed the full envelope
        # It must fail before any fragment of that response is queued
        expect_close(connection, 1000)
        process.wait(timeout=5)
    print('C++ envelope: oversized outbound JSON rejected without a partial frame', flush=True)
    with adapter(binary) as (connection, process):
        # Pause the host reader, then drain every response and recover normally
        # IXWebSocket flushes before receiving more work; this exercises bounded
        # backpressure, not a promise that the application queue guard is reached
        event = json.dumps({'event': 'willAppear', 'context': 'x' * (128 << 10), 'payload': {'settings': {}}}).encode()
        failures = []
        # Send repeated host messages to exercise outbound backpressure
        def flood():
            try:
                for _ in range(8):
                    connection.sendall(frame(event))
            except OSError as error:
                failures.append(error)
        sender = threading.Thread(target=flood, daemon=True)
        sender.start()
        time.sleep(0.15)
        complete = 0
        message = bytearray()
        while complete < 32:
            first, payload = receive(connection)
            assert first & 15 in (0, 1)
            message.extend(payload)
            assert len(message) <= LIMIT
            if first & 128:
                assert json.loads(message)['event'] in ('setFeedbackLayout', 'setState', 'setTitle', 'setFeedback')
                complete += 1
                message.clear()
        sender.join(timeout=3)
        assert not sender.is_alive() and not failures
        connection.sendall(frame(b'{"event":"shutdown"}'))
        expect_close(connection, 1000)
        process.wait(timeout=5)
    print('C++ envelope: stalled host recovers all32 complete bounded responses', flush=True)



# Validate archive
def validate_archive(archive):
    with tempfile.TemporaryDirectory(prefix='stream-dock-manager-cpp-envelope-') as directory, zipfile.ZipFile(archive) as package:
        manifests = [name for name in package.namelist() if name.endswith('.sdPlugin/manifest.json')]
        if len(manifests) != 1 or package.getinfo(manifests[0]).file_size > 1 << 20:
            raise ValueError('C++ package must contain one bounded manifest')
        manifest = json.loads(package.read(manifests[0]))
        name = native_binary_name(manifest) + ('.exe' if os.name == 'nt' else '')
        entry = manifests[0][:-len('manifest.json')] + 'bin/' + name
        if package.namelist().count(entry) != 1 or package.getinfo(entry).file_size > 64 << 20:
            raise ValueError('C++ package must contain its bounded native entry point')
        binary = Path(directory) / name
        binary.write_bytes(package.read(entry))
        binary.chmod(0o700)
        validate_binary(binary)


if __name__ == '__main__':
    import argparse
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('binary', type=Path)
    parser.add_argument('--baseline', action='store_true')
    args = parser.parse_args()
    if args.baseline:
        for repeat in range(5):
            with adapter(args.binary.resolve()) as (connection, _):
                connection.settimeout(0.2)
                connection.sendall(frame(declared=LIMIT + 1))
                try:
                    result = receive(connection)
                except TimeoutError:
                    print('R22_CPP_BASELINE rep=', repeat, 'oversized_header_waits_for_body=true', flush=True)
                else:
                    raise AssertionError(result)
    else:
        validate_binary(args.binary.resolve())

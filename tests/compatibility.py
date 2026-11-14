#!/usr/bin/env python3
"""Replay pinned shared-protocol fixtures against a selected built native plugin.

This is a local protocol harness, not native VSD Craft qualification. No compiler,
installed plugin, profile, device or external service is invoked.
"""
import argparse
import base64
import hashlib
import json
from pathlib import Path
import socket
import struct
import subprocess
import tempfile
import zipfile

ROOT = Path(__file__).resolve().parents[1]
FIXTURE = json.loads((ROOT / 'contracts/fixtures/protocol-v1.json').read_text())


# Receive exact
def receive_exact(connection, size):
    value = bytearray()
    while len(value) < size:
        chunk = connection.recv(size - len(value))
        if not chunk: raise EOFError('plugin disconnected')
        value.extend(chunk)
    return bytes(value)


# Receive the next compatibility message
def receive(connection):
    first, second = receive_exact(connection, 2)
    length = second & 127
    if length == 126: length = struct.unpack('!H', receive_exact(connection, 2))[0]
    if length == 127: length = struct.unpack('!Q', receive_exact(connection, 8))[0]
    if length > 1 << 20: raise ValueError('plugin exceeded message bound')
    if not second & 128: raise ValueError('client frame must be masked')
    mask = receive_exact(connection, 4)
    raw = bytes(value ^ mask[i % 4] for i, value in enumerate(receive_exact(connection, length)))
    if first & 15 == 8: raise EOFError('plugin closed')
    if first & 15 == 9:
        connection.sendall(bytes([0x8A, len(raw)]) + raw)
        return receive(connection)
    if first & 15 != 1: return receive(connection)
    return json.loads(raw)


# Send the supplied compatibility message
def send(connection, value):
    raw = json.dumps(value).encode()
    header = bytes([0x81, len(raw)]) if len(raw) < 126 else b'\x81\x7e' + struct.pack('!H', len(raw))
    connection.sendall(header + raw)


# Prepare exercise in the compatibility fixture
def exercise(executable, host):
    transcript = []
    with socket.socket() as listener:
        listener.bind(('127.0.0.1', 0))
        listener.listen(1)
        listener.settimeout(5)
        port = listener.getsockname()[1]
        process = subprocess.Popen([str(executable), '-port', str(port), '-pluginUUID', 'local-fixture-token', '-registerEvent', 'registerPlugin', '-info', json.dumps(FIXTURE[host])], stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
        try:
            connection, _ = listener.accept()
            with connection:
                connection.settimeout(5)
                request = bytearray()
                while not request.endswith(b'\r\n\r\n'):
                    request.extend(receive_exact(connection, 1))
                    if len(request) > 64 << 10: raise ValueError('oversized upgrade')
                headers = dict(line.split(': ', 1) for line in request.decode().split('\r\n')[1:] if ': ' in line)
                key = next(value for name, value in headers.items() if name.lower() == 'sec-websocket-key')
                accept = base64.b64encode(hashlib.sha1((key + '258EAFA5-E914-47DA-95CA-C5AB0DC85B11').encode()).digest()).decode()
                connection.sendall(('HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: ' + accept + '\r\n\r\n').encode())
                registration = receive(connection)
                assert registration['event'] == 'registerPlugin' and registration['uuid'] == 'local-fixture-token'
                if host == 'legacy': assert set(registration) == {'event', 'uuid'}, registration
                # Prepare until title in the compatibility fixture
                def until_title(text):
                    while True:
                        message = receive(connection)
                        transcript.append(message)
                        if message['event'] == 'setTitle' and message['payload']['title'] == text: return
                for context, count in [('a', 0), ('b', 3)]:
                    send(connection, dict(event='willAppear', context=context, action='org.fixture.counter', payload=dict(settings=dict(prefix='Count', count=count))))
                    until_title('Count ' + str(count))
                send(connection, dict(event='keyUp', context='a', action='org.fixture.counter', payload={}, streamDockManager=dict(request_id='request-1')))
                until_title('Count 1')
                if host == 'manager':
                    while True:
                        message = receive(connection)
                        transcript.append(message)
                        if message['event'] == 'actionResult':
                            assert message['payload'] == dict(request_id='request-1', success=True)
                            break
                send(connection, dict(event='sendToPlugin', context='a', action='org.fixture.counter', payload=dict(settings=dict(prefix='SDK'))))
                until_title('SDK 1')
                send(connection, dict(event='sendToPlugin', context='a', action='org.fixture.counter', payload=dict(reset=True)))
                until_title('SDK 0')
                send(connection, dict(event='willDisappear', context='a'))
                send(connection, dict(event='shutdown'))
                try:
                    while True: transcript.append(receive(connection))
                except (EOFError, ConnectionResetError): pass
            assert process.wait(timeout=5) == 0, process.stderr.read().decode()
            for message in transcript:
                if message['event'] == 'sendToPropertyInspector':
                    assert message.get('action') == 'org.fixture.counter', message
            if host == 'legacy':
                assert not any(message['event'] in FIXTURE['gates'] for message in transcript), transcript
            return dict(host=host, messages=len(transcript), result='passed', scope='local protocol replay; VSD Craft unverified')
        finally:
            if process.poll() is None:
                process.kill()
                process.wait()
            process.stderr.close()


# Run the compatibility command and enforce its input options
def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--archive', required=True, type=Path)
    parser.add_argument('--host', choices=['legacy', 'manager', 'both'], default='both')
    args = parser.parse_args()
    with tempfile.TemporaryDirectory() as temporary:
        with zipfile.ZipFile(args.archive) as archive:
            for entry in archive.infolist():
                path = Path(entry.filename)
                if path.is_absolute() or '..' in path.parts: raise ValueError('unsafe package path')
                archive.extract(entry, temporary)
                if entry.external_attr >> 16 & 0o111: (Path(temporary) / entry.filename).chmod(0o755)
        manifests = list(Path(temporary).glob('*.sdPlugin/manifest.json'))
        if len(manifests) != 1: raise ValueError('expected one plugin root')
        manifest = json.loads(manifests[0].read_text())
        import os
        entry = manifest.get('CodePathWin', manifest['CodePath']) if os.name == 'nt' else manifest['CodePath']
        executable = manifests[0].parent / entry
        for host in ['legacy', 'manager'] if args.host == 'both' else [args.host]:
            print(json.dumps(exercise(executable, host)))


if __name__ == '__main__': main()

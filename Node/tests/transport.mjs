// Exercise the production send loop with an actual quiet/slow loopback peer
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { createHash } from 'node:crypto';
import { run } from '../dist/src/index.js';
import { MAX_OUTPUT_BYTES } from '../dist/src/runtime.js';
const WebSocketImpl = globalThis.WebSocket;
let peak = 0;
class ObservedWebSocket extends WebSocketImpl {
    // Provide send behavior for this transport fixture
    send(data) {
        super.send(data);
        peak = Math.max(peak, this.bufferedAmount);
        assert(
            this.bufferedAmount <= MAX_OUTPUT_BYTES,
            'transport exceeded encoded-byte backpressure limit',
        );
    }
}
const server = createServer();
const received = [];
let peer;
const timers = [];
server.on(
    'upgrade',
    // Handle upgrade on work for transport
    (request, socket) => {
        peer = socket;
        const key = createHash('sha1')
            .update(request.headers['sec-websocket-key'] + '258EAFA5-E914-47DA-95CA-C5AB0DC85B11')
            .digest('base64');
        socket.write(
            `HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: ${key}\r\n\r\n`,
        );
        let data = Buffer.alloc(0);
        // Provide event behavior for this transport fixture
        const event = (value) => {
            const payload = Buffer.from(JSON.stringify(value));
            assert(payload.length < 126);
            socket.write(Buffer.concat([Buffer.from([0x81, payload.length]), payload]));
        };
        socket.on(
            'data',
            // Handle data on work for transport
            (chunk) => {
                data = Buffer.concat([data, chunk]);
                while (data.length >= 2) {
                    let size = data[1] & 127,
                        offset = 2;
                    if (size === 126) {
                        if (data.length < 4) return;
                        size = data.readUInt16BE(2);
                        offset = 4;
                    } else if (size === 127) {
                        if (data.length < 10) return;
                        size = Number(data.readBigUInt64BE(2));
                        offset = 10;
                    }
                    assert(data[1] & 128);
                    if (data.length < offset + 4 + size) return;
                    const opcode = data[0] & 15,
                        mask = data.subarray(offset, offset + 4),
                        payload = Buffer.from(data.subarray(offset + 4, offset + 4 + size));
                    data = data.subarray(offset + 4 + size);
                    for (let i = 0; i < payload.length; i++) payload[i] ^= mask[i % 4];
                    if (opcode === 8) {
                        socket.end(Buffer.concat([Buffer.from([0x88, payload.length]), payload]));
                        return;
                    }
                    if (opcode !== 1) continue;
                    const message = JSON.parse(payload.toString());
                    if (message.event === 'setTitle') {
                        received.push(message.payload.index);
                        if (received.length === 12) event({ event: 'shutdown' });
                    }
                }
            },
        );
        socket.pause();
        timers.push(
            setTimeout(() => event({ event: 'keyDown', context: 'a', payload: { phase: 0 } }), 30),
        );
        timers.push(
            setTimeout(() => event({ event: 'keyDown', context: 'a', payload: { phase: 1 } }), 100),
        );
        timers.push(setTimeout(() => socket.resume(), 250));
    },
);
await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
globalThis.WebSocket = ObservedWebSocket;
const timeout = setTimeout(() => peer?.destroy(Error('transport fixture timed out')), 10000);
const originalArgs = process.argv;
process.argv = [
    process.execPath,
    'fixture.mjs',
    '-port',
    String(server.address().port),
    '-pluginUUID',
    'fixture',
    '-registerEvent',
    'registerPlugin',
];
try {
    assert.throws(
        () =>
            run(
                {
                    // Provide handle behavior for this transport fixture
                    handle() {},
                },
                ['-port'],
            ),
        /Invalid authenticated host arguments/,
    );
    await assert.rejects(
        run(
            {
                // Provide handle behavior for this transport fixture
                handle() {},
            },
            process.argv.slice(2),
            { required_capabilities: ['missing-feature'] },
        ),
        /missing-feature/,
    );
    await run({
        // Provide should log action behavior for this transport fixture
        shouldLogAction: () => false,
        // Provide next poll behavior for this transport fixture
        nextPoll: () => undefined,
        // Provide handle behavior for this transport fixture
        handle(client, event) {
            for (let index = event.payload.phase * 6; index < event.payload.phase * 6 + 6; index++)
                client.send('setTitle', 'a', { index, title: 'é' + 'x'.repeat(900000) });
        },
    });
    assert.deepEqual(
        received,
        Array.from({ length: 12 }, (_, i) => i),
    );
    assert(peak <= MAX_OUTPUT_BYTES);
} finally {
    process.argv = originalArgs;
    globalThis.WebSocket = WebSocketImpl;
    clearTimeout(timeout);
    for (const timer of timers) clearTimeout(timer);
    peer?.destroy();
    await new Promise((resolve) => server.close(resolve));
}

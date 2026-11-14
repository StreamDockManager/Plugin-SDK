import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { createHash } from 'node:crypto';
import { readFile } from 'node:fs/promises';
import { connect } from '../dist/src/runtime.js';
import { HostInfo, MAX_MESSAGE } from '../dist/src/protocol.js';
import { IncomingBudget, MAX_INPUT_BYTES, MAX_INPUT_ITEMS } from '../dist/src/internal/incoming.js';

// Exercise frame against the SDK contract
function frame(value) {
    const data = Buffer.from(JSON.stringify(value));
    let header;
    if (data.length < 126) header = Buffer.from([0x81, data.length]);
    else if (data.length <= 65535) {
        header = Buffer.alloc(4);
        header[0] = 0x81;
        header[1] = 126;
        header.writeUInt16BE(data.length, 2);
    } else {
        header = Buffer.alloc(10);
        header[0] = 0x81;
        header[1] = 127;
        header.writeBigUInt64BE(BigInt(data.length), 2);
    }
    return Buffer.concat([header, data]);
}

// Exercise session against the SDK contract
async function session(events, handler, overload = false) {
    const sockets = new Set();
    const server = createServer();
    const received = [];
    server.on(
        'upgrade',
        // Handle upgrade on work for session
        (request, socket) => {
            sockets.add(socket);
            socket.on('error', () => {});
            socket.on('close', () => sockets.delete(socket));
            const accept = createHash('sha1')
                .update(
                    request.headers['sec-websocket-key'] + '258EAFA5-E914-47DA-95CA-C5AB0DC85B11',
                )
                .digest('base64');
            socket.write(
                'HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: ' +
                    accept +
                    '\r\n\r\n',
            );
            let input = Buffer.alloc(0),
                started = false,
                sentRest = false;
            socket.on(
                'data',
                // Handle data on work for session
                (chunk) => {
                    input = Buffer.concat([input, chunk]);
                    while (input.length >= 2) {
                        const opcode = input[0] & 15;
                        let size = input[1] & 127,
                            offset = 2;
                        if (size === 126) {
                            if (input.length < 4) return;
                            size = input.readUInt16BE(2);
                            offset = 4;
                        } else if (size === 127) {
                            if (input.length < 10) return;
                            size = Number(input.readBigUInt64BE(2));
                            offset = 10;
                        }
                        const masked = (input[1] & 128) !== 0;
                        if (input.length < offset + (masked ? 4 : 0) + size) return;
                        const mask = input.subarray(offset, offset + 4);
                        offset += masked ? 4 : 0;
                        const body = Buffer.from(input.subarray(offset, offset + size));
                        if (masked) for (let i = 0; i < body.length; i++) body[i] ^= mask[i % 4];
                        input = input.subarray(offset + size);
                        if (opcode === 8) {
                            socket.end(Buffer.from([0x88, 0]));
                            return;
                        }
                        if (opcode !== 1) continue;
                        const event = JSON.parse(body.toString());
                        received.push(event);
                        if (!started) {
                            started = true;
                            for (const value of overload ? events.slice(0, 1) : events)
                                socket.write(frame(value));
                        } else if (overload && !sentRest && event.event === 'setTitle') {
                            sentRest = true;
                            for (const value of events.slice(1)) socket.write(frame(value));
                        }
                    }
                },
            );
        },
    );
    await new Promise((resolve, reject) => {
        server.once('error', reject);
        server.listen(0, '127.0.0.1', resolve);
    });
    let timeout;
    try {
        const result = connect(handler, {
            port: server.address().port,
            identity: 'incoming-fixture',
            host: new HostInfo({
                streamDockManager: { contract_version: 1, capabilities: ['action-results-v1'] },
            }),
        });
        const limit = new Promise((_, reject) => {
            timeout = setTimeout(() => reject(Error('incoming fixture timed out')), 10000);
        });
        if (overload)
            await assert.rejects(Promise.race([result, limit]), /SDK incoming queue full/);
        else await Promise.race([result, limit]);
        return received;
    } finally {
        clearTimeout(timeout);
        for (const socket of sockets) socket.destroy();
        await new Promise((resolve) => server.close(resolve));
    }
}

// Exercise incoming admission against the SDK contract
export async function incomingAdmission() {
    const contract = JSON.parse(
        await readFile(
            new URL('../../contracts/fixtures/protocol-v1.json', import.meta.url),
            'utf8',
        ),
    ).incoming_admission;
    assert.deepEqual(
        [MAX_INPUT_BYTES, MAX_INPUT_ITEMS, MAX_MESSAGE],
        [contract.bytes, contract.items, contract.receive_staging_bytes],
    );
    const budget = new IncomingBudget();
    const leases = Array.from({ length: MAX_INPUT_BYTES / MAX_MESSAGE }, () =>
        budget.acquire(MAX_MESSAGE),
    );
    const selected = leases.pop();
    assert.throws(() => budget.acquire(1), /queue full/);
    assert.equal(budget.bytes, MAX_INPUT_BYTES);
    selected();
    selected();
    for (const release of leases) release();
    assert.deepEqual([budget.bytes, budget.items], [0, 0]);
    const small = Array.from({ length: MAX_INPUT_ITEMS }, () => budget.acquire(1));
    assert.throws(() => budget.acquire(0), /queue full/);
    for (const release of small) release();
    assert.throws(() => budget.acquire(MAX_MESSAGE + 1), /message exceeds/);

    const order = [];
    let closed = 0;
    const handler = {
        // Provide handle behavior for this incoming fixture
        async handle(client, event) {
            order.push(event.event);
            await new Promise((resolve) => setTimeout(resolve, 5));
        },
        // Provide close behavior for this incoming fixture
        close() {
            closed++;
        },
    };
    await session([{ event: 'keyDown' }, { event: 'keyUp' }, { event: 'shutdown' }], handler);
    assert.deepEqual(order, ['keyDown', 'keyUp']);
    assert.equal(closed, 1);

    order.length = 0;
    closed = 0;
    let deferred,
        stopped = false;
    const large = { event: 'keyDown', context: 'a', payload: { text: 'é'.repeat(300000) } };
    const first = { ...large, streamDockManager: { request_id: 'tracked' } };
    const received = await session(
        [first, ...Array(20).fill(large)],
        {
            // Provide handle behavior for this incoming fixture
            async handle(client, event) {
                order.push(event.event);
                deferred = client.deferAction();
                client.title('a', 'started');
                await new Promise((resolve) => {
                    if (client.stop.signal.aborted) return resolve();
                    client.stop.signal.addEventListener('abort', resolve, { once: true });
                });
                stopped = client.stop.signal.aborted;
            },
            // Provide close behavior for this incoming fixture
            close() {
                closed++;
            },
        },
        true,
    );
    deferred?.finish();
    assert(stopped);
    assert.deepEqual(order, ['keyDown']);
    assert.equal(closed, 1);
    assert(!received.some((event) => event.event === 'actionResult' && event.payload.success));
}

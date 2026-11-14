import assert from 'node:assert/strict';
import { test } from 'node:test';
import { connect } from '../dist/src/runtime.js';
import { HostInfo } from '../dist/src/protocol.js';

class Socket extends EventTarget {
    static OPEN = 1;
    static current;
    readyState = 0;
    bufferedAmount = 0;
    closeCalls = 0;
    sent = [];
    // Initialize Socket and its owned state
    constructor() {
        super();
        Socket.current = this;
    }
    // Provide send behavior for this session lifecycle fixture
    send(value) {
        this.sent.push(JSON.parse(value));
    }
    // Provide close behavior for this session lifecycle fixture
    close() {
        this.closeCalls++;
    }
    // Provide open behavior for this session lifecycle fixture
    open() {
        this.readyState = 1;
        this.dispatchEvent(new Event('open'));
    }
    // Provide end behavior for this session lifecycle fixture
    end() {
        this.readyState = 3;
        this.dispatchEvent(new Event('close'));
    }
    // Provide message behavior for this session lifecycle fixture
    message(value) {
        this.dispatchEvent(new MessageEvent('message', { data: JSON.stringify(value) }));
    }
}
// Provide tick behavior for this session lifecycle fixture
const tick = () => new Promise((resolve) => setImmediate(resolve));
const options = { port: 1234, identity: 'fixture', host: new HostInfo({}) };

// Verify fatal error retires queued work, waits for active callback and closes once
test('fatal error retires queued work, waits for active callback and closes once', async () => {
    const original = globalThis.WebSocket;
    globalThis.WebSocket = Socket;
    let release,
        closed = 0,
        client;
    const seen = [];
    try {
        const result = connect(
            {
                // Provide should log action behavior for this session lifecycle fixture
                shouldLogAction: () => false,
                // Provide handle behavior for this session lifecycle fixture
                async handle(c, e) {
                    client = c;
                    seen.push(e.event);
                    await new Promise((r) => (release = r));
                },
                // Provide close behavior for this session lifecycle fixture
                close() {
                    closed++;
                },
            },
            options,
        );
        const rejection = assert.rejects(result, /Settings connection unavailable/);
        const socket = Socket.current;
        socket.open();
        socket.message({ event: 'keyDown' });
        await tick();
        socket.message({ event: 'keyUp' });
        socket.dispatchEvent(new Event('error'));
        socket.dispatchEvent(new Event('error'));
        socket.end();
        socket.end();
        socket.open();
        assert.equal(closed, 0);
        assert.equal(socket.closeCalls, 1);
        client.wake();
        client.outputReady();
        release();
        await rejection;
        assert.deepEqual(seen, ['keyDown']);
        assert.equal(closed, 1);
        assert.equal(socket.sent.filter((x) => x.event === 'registerPlugin').length, 1);
    } finally {
        globalThis.WebSocket = original;
    }
});
// Verify close before open and handler close failure settle once
test('close before open and handler close failure settle once', async () => {
    const original = globalThis.WebSocket;
    globalThis.WebSocket = Socket;
    let count = 0;
    try {
        const result = connect(
            {
                // Provide handle behavior for this session lifecycle fixture
                handle() {
                    throw Error('unexpected callback');
                },
                // Provide close behavior for this session lifecycle fixture
                close() {
                    count++;
                    throw Error('close failed');
                },
            },
            options,
        );
        const rejection = assert.rejects(result, /close failed/);
        const socket = Socket.current;
        socket.end();
        socket.end();
        socket.open();
        await rejection;
        assert.equal(count, 1);
        assert.equal(socket.sent.length, 0);
    } finally {
        globalThis.WebSocket = original;
    }
});

// Verify clean host close resolves without manufacturing a settings transport failure
test('clean host close resolves without manufacturing a settings transport failure', async () => {
    const original = globalThis.WebSocket;
    globalThis.WebSocket = Socket;
    let closed = 0;
    try {
        const result = connect(
            {
                // Provide handle behavior for this session lifecycle fixture
                handle() {},
                // Provide close behavior for this session lifecycle fixture
                close() {
                    closed++;
                },
            },
            options,
        );
        const socket = Socket.current;
        socket.open();
        socket.end();
        await result;
        assert.equal(closed, 1);
        assert.equal(socket.closeCalls, 0);
    } finally {
        globalThis.WebSocket = original;
    }
});

import process from 'node:process';
import { launchOwnerExited } from '../dist/src/internal/owner-lifecycle.js';
// Verify owner exit requires positive evidence
test('owner exit requires positive evidence', () => {
    assert.equal(launchOwnerExited(1, 1), false);
    assert.equal(launchOwnerExited(200, 1), true);
    assert.equal(
        launchOwnerExited(200, 200, () => {}),
        false,
    );
    assert.equal(
        launchOwnerExited(200, 200, () => {
            throw Object.assign(Error(), { code: 'EPERM' });
        }),
        false,
    );
    assert.equal(
        launchOwnerExited(200, 200, () => {
            throw Object.assign(Error(), { code: 'ESRCH' });
        }),
        true,
    );
});

// Verify idle owner exit resolves; buffered, preopen, pending work and cleanup errors remain failures
test('idle owner exit resolves; buffered, preopen, pending work and cleanup errors remain failures', async () => {
    const original = globalThis.WebSocket;
    const parent = Object.getOwnPropertyDescriptor(process, 'ppid');
    globalThis.WebSocket = Socket;
    let parentPid = 200;
    try {
        // Node 22 ignores writes to the native ppid value but honors a replacement getter
        Object.defineProperty(process, 'ppid', { get: () => parentPid, configurable: true });
        for (const mode of ['idle', 'buffered', 'output', 'preopen', 'pending', 'cleanup']) {
            parentPid = 200;
            let settingFailure,
                clientRef,
                closed = 0;
            const { PortableSettings } = await import('../dist/src/hoststorage.js');
            const result = connect(
                {
                    // Provide handle behavior for this session lifecycle fixture
                    async handle(client) {
                        clientRef = client;
                        if (mode === 'pending') {
                            try {
                                await new PortableSettings(client, 'action', 'a').read();
                            } catch (error) {
                                settingFailure = error;
                                throw error;
                            }
                        }
                    },
                    // Provide close behavior for this session lifecycle fixture
                    close() {
                        closed++;
                        if (mode === 'cleanup') throw Error('cleanup failed');
                    },
                },
                options,
            );
            const outcome =
                mode === 'idle'
                    ? result
                    : assert.rejects(
                          result,
                          mode === 'cleanup' ? /cleanup failed/ : /Settings connection unavailable/,
                      );
            const socket = Socket.current;
            if (mode !== 'preopen') socket.open();
            if (mode === 'buffered') socket.bufferedAmount = 1;
            if (mode === 'output') {
                socket.message({ event: 'keyDown', context: 'a' });
                await tick();
                clientRef.title('a', 'unsent');
            }
            if (mode === 'pending') {
                socket.message({ event: 'keyDown', context: 'a' });
                await tick();
            }
            parentPid = 1;
            socket.dispatchEvent(new Event('error'));
            socket.dispatchEvent(new Event('error'));
            socket.end();
            await outcome;
            assert.equal(closed, 1, mode);
            assert.equal(socket.closeCalls, 1, mode);
            if (mode === 'pending') assert.equal(settingFailure.code, 'indeterminate');
        }
    } finally {
        Object.defineProperty(process, 'ppid', parent);
        globalThis.WebSocket = original;
    }
});

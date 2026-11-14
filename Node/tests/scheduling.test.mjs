import { test } from 'node:test';
import assert from 'node:assert/strict';
import { connect } from '../dist/src/runtime.js';
import { HostInfo } from '../dist/src/protocol.js';

// Verify polling requires an explicit deadline
test('polling requires an explicit deadline', async () => {
    const original = globalThis.WebSocket;
    class FixtureSocket extends EventTarget {
        static OPEN = 1;
        readyState = 1;
        // Initialize FixtureSocket and its owned state
        constructor() {
            super();
            queueMicrotask(() => this.dispatchEvent(new Event('open')));
            this.timer = setTimeout(() => this.close(), 1150);
        }
        // Provide send behavior for this scheduling fixture
        send() {}
        // Provide close behavior for this scheduling fixture
        close() {
            if (this.readyState === 3) return;
            this.readyState = 3;
            clearTimeout(this.timer);
            this.dispatchEvent(new Event('close'));
        }
    }
    globalThis.WebSocket = FixtureSocket;
    try {
        for (const scheduled of [false, true]) {
            let polls = 0;
            const handler = {
                // Provide handle behavior for this scheduling fixture
                handle() {},
                // Provide poll behavior for this scheduling fixture
                poll() {
                    polls++;
                },
            };
            if (scheduled) handler.nextPoll = (now) => (polls ? undefined : now + 5);
            await connect(handler, { port: 12345, identity: 'fixture', host: new HostInfo({}) });
            assert.equal(polls, scheduled ? 1 : 0);
        }
    } finally {
        globalThis.WebSocket = original;
    }
});

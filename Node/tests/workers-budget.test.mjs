import assert from 'node:assert/strict';
import test from 'node:test';
import { WorkerPool } from '../dist/src/workers.js';

test('worker budgets count aliased storage once and release cancelled reservations', async () => {
    const module = new URL(
        'data:text/javascript,' +
            encodeURIComponent(
                "import {parentPort,workerData} from 'node:worker_threads'; if(workerData.gate) Atomics.wait(workerData.gate,0,0); parentPort.postMessage(workerData.bytes?.byteLength ?? 0);",
            ),
    );
    const pool = new WorkerPool({ maxBufferBytes: 8, maxQueuedBufferBytes: 8 });
    const gate = new Int32Array(new SharedArrayBuffer(4));
    const holders = Array.from({ length: 4 }, () => pool.submit(module, { gate }));
    try {
        const controller = new AbortController();
        const bytes = new Uint8Array(8);
        const pending = pool.submit(
            module,
            { bytes, otherView: bytes.subarray(1) },
            controller.signal,
            [bytes.buffer],
        );
        assert.equal(bytes.byteLength, 0);
        const next = new ArrayBuffer(8);
        await assert.rejects(pool.submit(module, { bytes: next }, undefined, [next]), /budget/);
        assert.equal(next.byteLength, 8);
        controller.abort();
        await assert.rejects(pending);
        const accepted = pool.submit(module, { bytes: next }, undefined, [next]);
        const large = new ArrayBuffer(9);
        await assert.rejects(pool.submit(module, { bytes: large }, undefined, [large]), /budget/);
        assert.equal(large.byteLength, 9);
        Atomics.store(gate, 0, 1);
        Atomics.notify(gate, 0);
        assert.equal(await accepted, 8);
    } finally {
        Atomics.store(gate, 0, 1);
        Atomics.notify(gate, 0);
        await Promise.allSettled(holders);
        pool.close();
    }
});

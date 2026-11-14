import { test } from 'node:test';
import { hostStorageConformance } from './hoststorage.mjs';
test('host storage and portable settings follow the shared wire contract', hostStorageConformance);

import assert from 'node:assert/strict';
import { Client } from '../dist/src/runtime.js';
import { HostInfo } from '../dist/src/protocol.js';
import {
    HostStore,
    STORAGE_PROTOCOL,
    RPC_RESPONSE,
    MAX_STORAGE_VALUE,
} from '../dist/src/hoststorage.js';

// Verify host mutations encode once for validation and retain UTF-8 quota boundaries
test('host mutations encode once for validation and retain UTF-8 quota boundaries', async () => {
    const client = new Client(
        new HostInfo({
            streamDockManager: { contract_version: 1, capabilities: [STORAGE_PROTOCOL] },
        }),
    );
    const store = new HostStore(client, 'test');
    const version = { generation: '1', revision: '0' };
    const operation = '11111111-1111-1111-1111-111111111111';
    let sent;
    client.outputReady = () => {
        const [request] = client.drain();
        sent = request.payload.mutations;
        client.control.observe({
            event: RPC_RESPONSE,
            protocol_id: STORAGE_PROTOCOL,
            request_id: request.request_id,
            result: { generation: '1', revision: '1', operation_id: operation, committed: true },
        });
    };
    const boundary = 'é'.repeat((MAX_STORAGE_VALUE - 2) / 2);
    let reads = 0;
    const mutation = {
        op: 'put',
        key: 'key',
        // Provide value behavior for this hoststorage fixture
        get value() {
            reads++;
            return boundary;
        },
    };
    await store.commit(version, operation, [mutation]);
    assert.equal(reads, 2, 'one validation read plus the explicit transport snapshot');
    assert.equal(sent[0].value, boundary);
    assert.throws(
        () => store.commit(version, operation, [{ op: 'put', key: 'key', value: boundary + 'é' }]),
        (error) => error.code === 'quota',
    );
    for (const value of [NaN, Infinity, -Infinity])
        assert.throws(
            () =>
                store.commit(version, operation, [
                    { op: 'put', key: 'key', value: { nested: [value] } },
                ]),
            /Non-finite JSON number/,
        );
    assert.throws(
        () => store.commit(version, operation, [{ op: 'put', key: 'key', value: undefined }]),
        /Put requires a JSON value/,
    );
    assert.throws(
        () => store.commit(version, operation, [{ op: 'put', key: 'key' }]),
        /Put requires a JSON value/,
    );
    const circular = {};
    circular.self = circular;
    assert.throws(
        () => store.commit(version, operation, [{ op: 'put', key: 'key', value: circular }]),
        TypeError,
    );
    await store.commit(version, operation, [
        { op: 'put', key: 'key', value: { absent: undefined, array: [undefined] } },
    ]);
    assert.deepEqual(sent[0].value, { array: [null] });
});

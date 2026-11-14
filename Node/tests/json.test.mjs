import assert from 'node:assert/strict';
import { test } from 'node:test';
import { mkdtemp, writeFile, rm, readFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { finiteJSONValue, encodeJSON } from '../dist/src/internal/json.js';
import { encoded } from '../dist/src/control.js';
import { Client } from '../dist/src/runtime.js';
import { Service } from '../dist/src/httpclient.js';
import { Store, write } from '../dist/src/storage.js';

// Verify finite JSON policy preserves ordinary omission and strict JSON stays distinct
test('finite JSON policy preserves ordinary omission and strict JSON stays distinct', () => {
    const value = { omitted: undefined, array: [undefined, () => {}, Symbol('x')], value: 'é' };
    assert.equal(encoded(value), '{"array":[null,null,null],"value":"é"}');
    assert.equal(encoded(undefined), undefined);
    for (const invalid of [undefined, () => {}, Symbol('x'), 1n, NaN, Infinity, -Infinity]) {
        assert.throws(() => encodeJSON(invalid), /Expected a JSON value/);
        assert.throws(() => encodeJSON({ nested: [invalid] }), /Expected a JSON value/);
    }
    for (const number of [NaN, Infinity, -Infinity]) {
        assert.throws(() => encoded({ nested: [number] }), /Non-finite JSON number/);
        assert.throws(
            () => JSON.stringify([number], finiteJSONValue('context')),
            /^Error: context$/,
        );
    }
    assert.throws(() => JSON.parse('{"nested":[1e400]}', finiteJSONValue('overflow')), /overflow/);
    const circular = {};
    circular.self = circular;
    assert.throws(() => encoded(circular), TypeError);
    assert.throws(() => encodeJSON(circular), TypeError);
});

// Verify transport and HTTP retain their JSON semantics and contextual errors
test('transport and HTTP retain their JSON semantics and contextual errors', async () => {
    const client = new Client();
    const service = new Service();
    let sent;
    service.do = async (_method, _endpoint, _headers, body) => {
        sent = body;
        return { status: 200, headers: {}, body: new Uint8Array() };
    };
    for (const number of [NaN, Infinity, -Infinity]) {
        assert.throws(
            () => client.send('setSettings', 'key', { nested: [number] }),
            /Non-finite JSON number/,
        );
        await assert.rejects(
            service.json('POST', 'http://localhost', {}, { nested: [number] }),
            /Invalid JSON request number/,
        );
    }
    client.send('setSettings', 'key', { absent: undefined, array: [undefined] });
    assert.deepEqual(client.drain()[0].payload, { array: [null] });
    await service.json('POST', 'http://localhost', {}, { absent: undefined, array: [undefined] });
    assert.equal(sent, '{"array":[null]}');
});

// Verify state writing rejects nonfinite values and reading rejects exponent overflow
test('state writing rejects nonfinite values and reading rejects exponent overflow', async () => {
    const dir = await mkdtemp(join(tmpdir(), 'sdk-json-'));
    const path = join(dir, 'state.json');
    try {
        for (const number of [NaN, Infinity, -Infinity])
            await assert.rejects(write(path, { nested: [number] }), /Invalid state JSON number/);
        await write(path, { absent: undefined, array: [undefined] });
        assert.equal(await readFile(path, 'utf8'), '{"array":[null]}');
        await writeFile(path, '{"version":1,"data":{"nested":[1e400]}}');
        await assert.rejects(new Store(path).load(), /Invalid state JSON number/);
    } finally {
        await rm(dir, { recursive: true, force: true });
    }
});

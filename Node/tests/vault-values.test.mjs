import { test } from 'node:test';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';
import { inspect } from 'node:util';
import { Client, formatText } from '../dist/src/index.js';
import { HostInfo, frameCapabilities, parseCapabilities } from '../dist/src/protocol.js';
import {
    VaultValues,
    VaultSecrets,
    VAULT_VALUES_PROTOCOL,
    VAULT_SECRET_SHARING_PROTOCOL,
} from '../dist/src/vault-values.js';
const fixture = JSON.parse(
    readFileSync(new URL('../../contracts/fixtures/vault-slots-v1.json', import.meta.url), 'utf8'),
);
function peer(
    capability,
    response,
    operation = fixture.methods.find((entry) => entry.protocol_id === capability),
    selector = fixture.selector,
) {
    const client = new Client(
        new HostInfo({ streamDockManager: { contract_version: 1, capabilities: [capability] } }),
        'opaque',
    );
    client.outputReady = () => {
        for (const request of client.drain()) {
            assert.deepEqual(request.payload, selector);
            assert.equal(request.event, fixture.request_event);
            assert.equal(request.method, operation.method);
            assert.equal(request.protocol_id, capability);
            client.control.observe({
                event: 'streamDockManagerRpcResponse',
                protocol_id: capability,
                request_id: request.request_id,
                result: response,
            });
        }
    };
    return client;
}
test('Vault text and secret reads are exact, separately gated and redacted', async () => {
    for (const [C, cap, method, kind] of [
        [VaultValues, VAULT_VALUES_PROTOCOL, 'readValue', 'text'],
        [VaultSecrets, VAULT_SECRET_SHARING_PROTOCOL, 'readSecret', 'api-key'],
    ]) {
        assert.throws(() => new C(new Client(new HostInfo({}), 'opaque')));
        const client = peer(cap, {
            entry_kind: kind,
            value: 'private-fixture',
            revision: '18446744073709551615',
        });
        const value = await new C(client)[method]('username', 'username', 'org.example.points');
        assert.equal(method === 'readSecret' ? value.reveal() : value.value, 'private-fixture');
        assert.ok(!inspect(value).includes('private-fixture'));
        assert.ok(!JSON.stringify(value).includes('private-fixture'));
        client.close();
        for (const patch of [
            { revision: '01' },
            { revision: '18446744073709551616' },
            { extra: true },
            { entry_kind: 'wrong' },
            { value: 'é'.repeat(32769) },
        ]) {
            const c = peer(cap, {
                entry_kind: kind,
                value: 'private-fixture',
                revision: '1',
                ...patch,
            });
            await assert.rejects(
                new C(c)[method]('username', 'username', 'org.example.points'),
                (e) => e.code === 'invalid_response' && !e.message.includes('private-fixture'),
            );
            c.close();
        }
        const c = peer(cap, {});
        await assert.rejects(
            new C(c)[method]('../bad', 'user', 'example.action'),
            (e) => e.code === 'invalid_request',
        );
        c.close();
    }
});
test('Vault failures retain only allowlisted codes and never host messages', async () => {
    const { RPCError } = await import('../dist/src/control.js');
    for (const code of [
        'permission_denied',
        'needs_setup',
        'vault_locked',
        'not_declared',
        'operation_unavailable',
        'indeterminate',
        'private-fixture',
    ]) {
        const client = peer(VAULT_SECRET_SHARING_PROTOCOL, {});
        client.outputReady = () => {
            for (const request of client.drain())
                client.control.observe({
                    event: 'streamDockManagerRpcResponse',
                    protocol_id: request.protocol_id,
                    request_id: request.request_id,
                    error: { code, message: 'private-fixture' },
                });
        };
        await assert.rejects(
            new VaultSecrets(client).readSecret('username', 'username', 'org.example.points'),
            (e) =>
                e instanceof RPCError &&
                e.code === (code === 'private-fixture' ? 'operation_unavailable' : code) &&
                !String(e).includes('private-fixture'),
        );
        client.close();
    }
});
test('approved direct secret kinds retain their exact kind', async () => {
    for (const kind of ['bearer-token', 'credential']) {
        const client = peer(VAULT_SECRET_SHARING_PROTOCOL, {
            entry_kind: kind,
            value: 'private-fixture',
            revision: '0',
        });
        const value = await new VaultSecrets(client).readSecret(
            'username',
            'username',
            'org.example.points',
        );
        assert.equal(value.entry_kind, kind);
        assert.equal(value.reveal(), 'private-fixture');
        client.close();
    }
});

test('Vault permission scopes survive registration framing', () => {
    const descriptor = parseCapabilities(frameCapabilities({ permissions: fixture.permissions }));
    assert.deepEqual(descriptor.permissions, fixture.permissions);
});
test('Shared location reads only coordinates', async () => {
    const f = fixture.location;
    async function read(result) {
        const c = peer(f.protocol_id, result, f, f.selector);
        try {
            return await new VaultValues(c).readLocation(
                f.selector.slot,
                f.selector.field,
                f.selector.action,
            );
        } finally {
            c.close();
        }
    }
    const loc = await read(f.result);
    assert.deepEqual([loc.latitude, loc.longitude, loc.revision], [12.34567, 0, '3']);
    assert.ok(!(inspect(loc) + JSON.stringify(loc)).includes('12.34567'));
    for (const value of f.invalid_coordinates)
        await assert.rejects(
            read({ ...f.result, value }),
            (err) => err.code === 'invalid_response',
        );
});

test('text formatting skips literals and delegates exact references only to a capable host', async () => {
    const capability = 'plugin-text-references-v1';
    const selector = { action: 'example.action', text: 'Hello ${missing}' };
    const legacy = new Client();
    assert.equal(await formatText(legacy, '', 'plain'), 'plain');
    assert.equal(await formatText(legacy, '', '} then ${unfinished'), '} then ${unfinished');
    assert.equal(legacy.drain().length, 0);
    await assert.rejects(
        formatText(legacy, selector.action, selector.text),
        /plugin-text-references-v1/,
    );
    await assert.rejects(
        formatText(legacy, '', 'é'.repeat(32769)),
        (e) => e.code === 'invalid_request',
    );
    for (const result of [
        { text: 'Hello ' },
        { text: null },
        { text: 'leak', extra: true },
        { text: 'é'.repeat(131073) },
    ]) {
        const host = peer(capability, result, { method: 'format-text' }, selector);
        if (result.text === 'Hello ')
            assert.equal(await formatText(host, selector.action, selector.text), result.text);
        else
            await assert.rejects(
                formatText(host, selector.action, selector.text),
                (e) => e.code === 'invalid_response',
            );
        host.close();
    }
    legacy.close();
});

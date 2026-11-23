import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { Client, HostInfo, Permissions, permissionNotice, VaultWrites } from '../dist/src/index.js';
const f = JSON.parse(
    readFileSync(new URL('../../contracts/fixtures/permissions-v1.json', import.meta.url), 'utf8'),
);
test('permission service contract', async () => {
    const c = new Client(
        new HostInfo({
            streamDockManager: {
                contract_version: 1,
                capabilities: [f.permissions, f.broker, f.writes, f.references],
            },
        }),
        'opaque',
    );
    const p = new Permissions(c);
    const token = f.activation_token;
    for (const [protocol, method, payload, result, run] of [
        [
            f.permissions,
            'get-permissions',
            {},
            { revision: '7', scopes: [] },
            () => p.getPermissions(),
        ],
        [
            f.permissions,
            'has-permission',
            { action: f.action, scope: f.scope },
            { revision: '7', granted: false, available: true },
            () => p.hasPermission(f.action, f.scope),
        ],
        [
            f.permissions,
            'request-permissions',
            { activation_token: token, scopes: [f.scope] },
            { revision: '7', state: 'pending', review_id: f.review_id },
            () => p.requestPermissions(token, [f.scope]),
        ],
        [
            f.broker,
            'manager-webhook',
            { activation_token: token, certificate: f.webhook.certificate },
            { state: 'pending', result_handle: f.result_handle },
            () => p.sendExampleWebhook(token, f.webhook.certificate),
        ],
        [
            f.broker,
            'manager-local-webhook',
            { activation_token: token, origin: f.webhook.origin },
            { state: 'pending', result_handle: f.result_handle },
            () => p.sendLocalWebhook(token, f.webhook.origin),
        ],
        [
            f.broker,
            'clipboard-summary',
            { activation_token: token },
            { state: 'pending', result_handle: f.result_handle },
            () => p.summarizeClipboard(token),
        ],
        [
            f.permissions,
            'read-clipboard-text',
            { activation_token: token },
            { revision: '7', text: f.text },
            () => p.readClipboardText(token),
        ],
        [
            f.permissions,
            'show-text-result',
            { activation_token: token, text: f.text },
            { state: 'pending', result_handle: f.result_handle },
            () => p.showTextResult(token, f.text),
        ],
        [
            f.permissions,
            'notify-completion',
            { activation_token: token },
            { state: 'completed' },
            () => p.notifyCompletion(token),
        ],
        [
            f.writes,
            'write-secret',
            f.write,
            f.receipt,
            () =>
                new VaultWrites(c).writeSecret(
                    f.write.slot,
                    f.write.field,
                    f.action,
                    f.write.entry_kind,
                    f.write.value,
                    '3',
                ),
        ],
        [
            f.references,
            'list-vault-entries',
            { action: f.action, scope: f.browse, start: 0 },
            f.page,
            () => p.listVaultEntries(f.action, f.browse),
        ],
        [
            f.references,
            'select-vault-entry',
            { action: f.action, scope: f.use, activation_token: token },
            { review_id: f.review_id, state: 'pending', revision: '7' },
            () => p.selectVaultEntry(token, f.action, f.use),
        ],
        [
            f.broker,
            'clipboard-summary',
            { scope: f.use, entry_ref: f.receipt.entry_ref, activation_token: token },
            { result_handle: f.result_handle, state: 'pending' },
            () => p.runVaultRecipe(token, f.use, f.receipt.entry_ref),
        ],
    ]) {
        c.outputReady = () => {
            for (const req of c.drain()) {
                assert.equal(req.protocol_id, protocol);
                assert.equal(req.method, method);
                assert.deepEqual(req.payload, payload);
                c.control.observe({
                    event: 'streamDockManagerRpcResponse',
                    protocol_id: protocol,
                    request_id: req.request_id,
                    result,
                });
            }
        };
        assert.deepEqual(await run(), result);
    }
    const legacy = new Client(new HostInfo({}), 'opaque');
    await assert.rejects(new Permissions(legacy).summarizeClipboard(token));
    assert.deepEqual(legacy.drain(), []);
    c.close();
    legacy.close();
});

test('permission notices retain only the correlated Manager result fields', async () => {
    for (const key of ['terminal_event', 'changed_event', 'selection_event']) {
        assert.deepEqual(permissionNotice({ ...f[key], unrelated: 'discard' }), f[key]);
    }
    assert.equal(permissionNotice({ event: 'keyDown' }), undefined);
    const { RPCError } = await import('../dist/src/control.js');
    const p = new Permissions({
        control: {
            request: async () => {
                throw new RPCError('activation_expired', 'private fixture contents');
            },
        },
    });
    await assert.rejects(
        p.summarizeClipboard('token'),
        (err) => err.code === 'activation_expired' && !err.message.includes('private fixture'),
    );
});

test('create notification negotiates separately and redacts denied responses', async () => {
    const msg = f.notification;
    const args = [msg.action, msg.level, msg.message];
    const legacy = new Client(
        new HostInfo({ streamDockManager: { contract_version: 1, capabilities: [f.permissions] } }),
        'legacy',
    );
    await assert.rejects(
        new Permissions(legacy).createNotification(...args),
        /plugin-notifications-v1/,
    );
    assert.deepEqual(legacy.drain(), []);
    legacy.close();
    const client = new Client(
        new HostInfo({
            streamDockManager: {
                contract_version: 1,
                capabilities: [f.permissions, f.notifications],
            },
        }),
        'fixture',
    );
    const service = new Permissions(client);
    await assert.rejects(service.createNotification(msg.action, msg.level, 'é'.repeat(257)), {
        code: 'too_large',
    });
    assert.deepEqual(client.drain(), []);
    for (const [reply, code] of [
        [{ result: f.notification_receipt }, undefined],
        [{ result: { state: 'unexpected' } }, 'invalid_response'],
        [
            { error: { code: 'permission_denied', message: 'private fixture content' } },
            'permission_denied',
        ],
    ]) {
        client.outputReady = () => {
            const [req, ...rest] = client.drain();
            assert.equal(rest.length, 0);
            assert.deepEqual(
                [req.protocol_id, req.method, req.payload],
                [f.permissions, 'create-notification', msg],
            );
            client.control.observe({
                event: 'streamDockManagerRpcResponse',
                protocol_id: req.protocol_id,
                request_id: req.request_id,
                ...reply,
            });
        };
        if (code === undefined) assert.equal(await service.createNotification(...args), undefined);
        else
            await assert.rejects(
                service.createNotification(...args),
                (err) => err.code === code && !err.message.includes('private fixture content'),
            );
    }
    client.close();
});

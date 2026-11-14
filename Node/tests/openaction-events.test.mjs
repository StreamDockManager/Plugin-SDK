import { test } from 'node:test';
import assert from 'node:assert/strict';
import { openActionSettingsRequested, openActionDeepLink } from '../dist/src/openaction-events.js';

// Verify openAction notifications preserve opaque URL without acting
test('OpenAction notifications preserve opaque URL without acting', () => {
    assert.equal(openActionSettingsRequested({ event: 'showSettingsInterface' }), true);
    assert.equal(openActionSettingsRequested({ event: 'keyDown' }), false);
    const url = 'openaction://plugins/message/example.sdPlugin/a%2Fb?openaction=hidden';
    assert.equal(openActionDeepLink({ event: 'didReceiveDeepLink', payload: { url } }), url);
    assert.equal(openActionDeepLink({ event: 'keyDown' }), undefined);
    for (const payload of [{}, { url: 2 }, { url: '' }, []])
        assert.throws(() => openActionDeepLink({ event: 'didReceiveDeepLink', payload }));
});

// Verify legacy inspector replies keep action context, not plugin identity
test('legacy inspector replies keep action context, not plugin identity', async () => {
    const { Client } = await import('../dist/src/runtime.js');
    const { HostInfo } = await import('../dist/src/protocol.js');
    for (const raw of [{}, { streamDockManager: { contract_version: 1, capabilities: [] } }]) {
        const client = new Client(new HostInfo(raw), 'top.example.node.sdPlugin');
        const context = 'device.profile.Keypad.0.0';
        client.toInspector(context, { count: 1 }, 'top.example.node.counter');
        assert.deepEqual(client.drain(), [
            {
                event: 'sendToPropertyInspector',
                context,
                action: 'top.example.node.counter',
                payload: { count: 1 },
            },
        ]);
        client.close();
    }
});

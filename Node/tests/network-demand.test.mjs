import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { Client } from '../dist/src/runtime.js';
import { HostInfo } from '../dist/src/protocol.js';

test('optional network demand', async () => {
    const fixture = JSON.parse(
        await readFile(
            new URL('../../contracts/fixtures/protocol-v1.json', import.meta.url),
            'utf8',
        ),
    );
    const demand = fixture.network_demand;
    for (const enabled of [false, true]) {
        const host = structuredClone(fixture.manager);
        if (!enabled)
            host.streamDockManager.capabilities = host.streamDockManager.capabilities.filter(
                (capability) => capability !== demand.capability,
            );
        const client = new Client(new HostInfo(host));
        let calls = 0;
        await client.dispatch(
            {
                handle() {
                    calls++;
                },
            },
            demand.request,
        );
        assert.equal(calls, Number(!enabled));
        const frames = client.drain();
        assert.equal(frames.length, Number(enabled));
        if (enabled) {
            assert.equal(frames[0].event, demand.response);
            assert.equal(frames[0].payload.run_id, 'fixture-run');
        }
    }
});

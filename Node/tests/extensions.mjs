import { helpersConformance, cachePersistenceConformance } from './helpers.mjs';
import { connectedSessionsConformance } from './connectedws.mjs';
import { sharedReadsConformance } from './requests.mjs';
import { managedStateConformance } from './managedstate.mjs';
import { decode, MAX_INPUT_BYTES } from '../dist/src/thumbnail.js';
import { spawnSync } from 'node:child_process';
import { assets } from '../dist/src/inspectors.js';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { present, compactDisplay, Plugin } from '../dist/src/authoring.js';
import { HostInfo } from '../dist/src/protocol.js';
import { Recorder } from '../dist/src/testkit.js';
import { fileURLToPath } from 'node:url';
// Exercise extensions conformance against the SDK contract
export async function extensionsConformance() {
    await helpersConformance();
    await cachePersistenceConformance();
    const discovery = spawnSync(
        process.execPath,
        [fileURLToPath(new URL('../../tests/discovery.mjs', import.meta.url)), '-'],
        { input: assets()['inspector-workflows.js'], encoding: 'utf8' },
    );
    assert.equal(discovery.status, 0, discovery.stderr);
    const fixture = JSON.parse(
        readFileSync(new URL('../../contracts/fixtures/extensions-v1.json', import.meta.url)),
    );
    await managedStateConformance(fixture);
    await sharedReadsConformance(fixture);
    await connectedSessionsConformance();
    for (const c of fixture.layouts) {
        const base = {
                title: 'Full title',
                image: 'icon.svg',
                state: 0,
                layout: 'custom.json',
                feedback: { custom: { value: 'kept' } },
            },
            snapshot = structuredClone(base);
        const result = compactDisplay(
            present(base, c.content, c.width, c.height),
            c.width,
            c.height,
        );
        assert.deepEqual(base, snapshot);
        assert.equal(result.title, base.title);
        assert.equal(result.layout, base.layout);
        assert.deepEqual(result.feedback.custom, base.feedback.custom);
        for (const [key, rect] of Object.entries(c.rects))
            assert.deepEqual(result.feedback[key].rect, rect, c.name + ':' + key);
        if (c.progress !== undefined) assert.equal(result.feedback.indicator.value, c.progress);
    }
    for (const c of fixture.thumbnails) {
        const data = Buffer.from(c.data, 'base64'),
            image = decode(data, c.width, c.height);
        assert.equal(image.data.length, c.rgba.length);
        for (let i = 0; i < c.rgba.length; i++)
            if (i % 4 === 3 || c.rgba[i - (i % 4) + 3] !== 0)
                assert(
                    Math.abs(image.data[i] - c.rgba[i]) <= c.tolerance,
                    `${c.name}:${i}:${image.data[i]} != ${c.rgba[i]}`,
                );
        assert.throws(() => decode(data, 321, 1));
        assert.throws(() => decode(data, 2, 2, () => true), /cancelled/);
        let checks = 0;
        assert.throws(() => decode(data, 2, 2, () => ++checks > 2), /cancelled/);
    }
    for (const data of [
        new Uint8Array(),
        new TextEncoder().encode('invalid'),
        new Uint8Array(MAX_INPUT_BYTES + 1),
    ])
        assert.throws(() => decode(data, 1, 1));
    for (const failure of [false, true]) {
        const protocol = JSON.parse(
            readFileSync(new URL('../../contracts/fixtures/protocol-v1.json', import.meta.url)),
        );
        const recorder = new Recorder(new HostInfo(protocol.manager)),
            plugin = new Plugin();
        let release,
            applied = false;
        const wait = new Promise((resolve) => (release = resolve));
        plugin.register('command', {
            // Provide handle behavior for this extensions fixture
            handle(p, c, e) {
                assert(
                    p.observe(
                        c,
                        'read',
                        e.context,
                        async () => {
                            await wait;
                            if (failure) throw Error('read failed');
                            return 42;
                        },
                        (_p, _c, result) => {
                            assert.equal(!!result.error, failure);
                            applied = true;
                        },
                    ),
                );
                assert.equal(
                    p.observe(
                        c,
                        'read',
                        e.context,
                        async () => 0,
                        () => assert.fail('duplicate'),
                    ),
                    undefined,
                );
            },
        });
        await recorder.replay(plugin, [
            {
                event: 'keyDown',
                action: 'command',
                context: 'key',
                streamDockManager: { request_id: 'command' },
            },
        ]);
        assert.deepEqual(
            recorder
                .drain()
                .filter((e) => e.event === 'actionResult')
                .map((e) => e.payload.success),
            [true],
        );
        release();
        for (let i = 0; i < 100 && !applied; i++) {
            await new Promise((r) => setTimeout(r, 1));
            await recorder.poll(plugin);
        }
        assert(applied);
        assert(!recorder.drain().some((e) => e.event === 'actionResult'));
        plugin.close();
        recorder.close();
    }
}

import assert from 'node:assert/strict';
import { RequestGroup } from '../dist/src/requests.js';
// Provide tick behavior for this requests fixture
const tick = () => new Promise((resolve) => setImmediate(resolve));
// Exercise shared reads conformance against the SDK contract
export async function sharedReadsConformance(fixture) {
    const spec = fixture.shared_reads,
        group = new RequestGroup();
    let release,
        calls = 0,
        shared;
    const wait = new Promise((resolve) => (release = resolve));
    // Provide work behavior for this requests fixture
    const work = async (signal) => {
        calls++;
        shared = signal;
        await wait;
        return spec.value;
    };
    const cancel = new AbortController();
    const first = group.get(spec.key, work, cancel.signal);
    const second = group.get(spec.key, work);
    await tick();
    cancel.abort();
    await assert.rejects(first);
    assert(!shared.aborted);
    release();
    assert.deepEqual(await second, spec.value);
    assert.equal(calls, 1);
    const failure = Error('service cooldown');
    await assert.rejects(
        group.get(
            'cooldown',
            async () => {
                throw failure;
            },
            undefined,
            () => spec.cooldown_ms,
        ),
    );
    await assert.rejects(
        group.get('cooldown', async () => assert.fail('ignored cooldown')),
        (error) => error === failure,
    );
    await assert.rejects(
        group.get(
            'expiry',
            async () => {
                throw failure;
            },
            undefined,
            () => 20,
        ),
    );
    await new Promise((resolve) => setTimeout(resolve, 30));
    assert.equal(await group.get('expiry', async () => 7), 7);
    let oldRelease;
    const oldCancel = new AbortController();
    const old = group.get(
        'retired',
        async () => {
            await new Promise((resolve) => (oldRelease = resolve));
            throw failure;
        },
        oldCancel.signal,
        () => spec.cooldown_ms,
    );
    await tick();
    oldCancel.abort();
    await assert.rejects(old);
    assert.deepEqual(await group.get('retired', async () => spec.value), spec.value);
    oldRelease();
    await tick();
    assert.deepEqual(await group.get('retired', async () => spec.value), spec.value);
    await assert.rejects(
        group.get('oversized', async () => 'x'.repeat((1 << 20) + 1)),
        /1 MiB/,
    );
    await assert.rejects(
        group.get('invalid-json', async () => ({ value: NaN })),
        /JSON/,
    );
    group.close();
    await assert.rejects(group.get('closed', work));
    const bounded = new RequestGroup(),
        releases = [],
        pending = [];
    for (let i = 0; i < spec.capacity; i++) {
        const stop = new AbortController();
        pending.push({
            stop,
            result: bounded.get(
                String(i),
                async () => {
                    await new Promise((resolve) => releases.push(resolve));
                    return 1;
                },
                stop.signal,
            ),
        });
    }
    await tick();
    for (const p of pending) {
        p.stop.abort();
        await assert.rejects(p.result);
    }
    await assert.rejects(
        bounded.get('overflow', async () => 1),
        /capacity/,
    );
    releases.forEach((resolve) => resolve());
    await tick();
    assert.equal(await bounded.get('reused', async () => 1), 1);
    bounded.close();
}

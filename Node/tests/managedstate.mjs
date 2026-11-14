import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { ManagedState } from '../dist/src/managedstate.js';
import { RPCError } from '../dist/src/hoststorage.js';
// Validate the managed-state namespace key
const key = (id) => 'record:' + createHash('sha256').update(id).digest('hex');
// Exercise managed state conformance against the SDK contract
export async function managedStateConformance(fixture) {
    const spec = fixture.managed_state;
    for (const recovery of spec.recoveries) {
        let revision = 0,
            fail = true;
        const records = new Map(
                Object.entries(spec.initial).map(([identity, value]) => [
                    key(identity),
                    { identity, value },
                ]),
            ),
            receipts = new Map(),
            commits = [];
        // Provide version behavior for this managedstate fixture
        const version = () => ({ generation: '1', revision: String(revision) });
        const backend = {
            // Provide list keys behavior for this managedstate fixture
            async listKeys() {
                return { ...version(), keys: [...records.keys()] };
            },
            // Provide get behavior for this managedstate fixture
            async get(key) {
                return {
                    ...version(),
                    found: records.has(key),
                    value: structuredClone(records.get(key)),
                };
            },
            // Provide receipt behavior for this managedstate fixture
            async receipt(generation, id) {
                return receipts.get(id) ?? { ...version(), operation_id: id, found: false };
            },
            // Provide commit behavior for this managedstate fixture
            async commit(v, id, mutations) {
                commits.push(structuredClone({ v, id, mutations }));
                if (v.revision !== String(revision)) throw new RPCError('conflict', 'changed');
                if (fail && recovery === 'not-applied') {
                    fail = false;
                    throw new RPCError('indeterminate', 'lost reply');
                }
                for (const m of mutations)
                    if (m.op === 'delete') records.delete(m.key);
                    else records.set(m.key, structuredClone(m.value));
                revision++;
                const receipt = { ...version(), operation_id: id, found: true, committed: true };
                receipts.set(id, receipt);
                if (fail) {
                    fail = false;
                    throw new RPCError('indeterminate', 'lost reply');
                }
                return receipt;
            },
        };
        const state = new ManagedState('private', backend);
        assert.deepEqual(await state.load(), spec.initial);
        await assert.rejects(state.save(spec.next), (e) => e.code === 'indeterminate');
        assert(state.dirty);
        assert.throws(() => state.checkPending());
        await state.save(spec.newer);
        if (recovery === 'not-applied') {
            assert(state.dirty);
            assert.equal(commits[0].id, commits[1].id);
            assert.deepEqual(commits[0].mutations, commits[1].mutations);
            await state.save(spec.newer);
        }
        assert(!state.dirty);
        state.checkPending();
        assert.equal(state.assurance, 'committed');
        assert.deepEqual(records.get(key('count')).value, spec.newer.count);
        assert(!records.has(key('removed')));
        const count = commits.length;
        await state.save(spec.newer);
        assert.equal(commits.length, count);
        await assert.rejects(
            state.save(Object.fromEntries(Array.from({ length: 33 }, (_, i) => ['new' + i, i]))),
            (e) => e.code === 'quota',
        );
        assert.equal(commits.length, count);
    }
    let settings = { unknown: { nested: true } },
        updates = 0;
    const portable = new ManagedState(
        'portable',
        {
            // Provide read behavior for this managedstate fixture
            async read() {
                return {
                    settings: structuredClone(settings),
                    backend: 'base-vsd',
                    assurance: 'observed',
                };
            },
            // Provide update behavior for this managedstate fixture
            async update(changes) {
                updates++;
                settings = { ...settings, ...changes };
                return { backend: 'base-vsd', assurance: 'sent' };
            },
        },
        'saved',
    );
    await portable.load();
    await portable.save(spec.next);
    await portable.save(spec.next);
    assert.equal(updates, 1);
    assert.deepEqual(settings.unknown, { nested: true });
    assert.equal(portable.assurance, 'sent');
    const session = new ManagedState('session');
    await session.load();
    await session.save(spec.next);
    assert.deepEqual(await session.load(), spec.next);
    for (const invalid of [{ value: NaN }, { value: undefined }])
        await assert.rejects(session.save(invalid), /JSON/);
    assert.deepEqual(await session.load(), spec.next);
    let release;
    const pendingState = new ManagedState('portable', {
        // Provide read behavior for this managedstate fixture
        async read() {
            return { settings: {}, backend: 'base-vsd', assurance: 'observed' };
        },
        // Provide update behavior for this managedstate fixture
        async update() {
            await new Promise((resolve) => (release = resolve));
            return { backend: 'base-vsd', assurance: 'sent' };
        },
    });
    await pendingState.load();
    const saving = pendingState.save(spec.next);
    pendingState.markDirty();
    release();
    await saving;
    assert(pendingState.dirty);
    await pendingState.save(spec.next);
    assert(!pendingState.dirty);
    let attempts = 0;
    const conflict = new ManagedState('private', {
        // Provide list keys behavior for this managedstate fixture
        async listKeys() {
            return { generation: '1', revision: '0', keys: [] };
        },
        // Provide commit behavior for this managedstate fixture
        async commit() {
            attempts++;
            throw new RPCError('conflict', 'changed');
        },
    });
    await conflict.load();
    await assert.rejects(conflict.save(spec.next));
    await assert.rejects(conflict.save(spec.newer));
    assert.equal(attempts, 1);
    const cycle = new ManagedState('private', {
        // Provide list keys behavior for this managedstate fixture
        async listKeys() {
            return { generation: '1', revision: '0', keys: [], cursor: 'same' };
        },
    });
    await assert.rejects(cycle.load(), /cursor/);
}

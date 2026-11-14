import { RPCError } from '../dist/src/hoststorage.js';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { Cache, Budget, Persistence } from '../dist/src/cache.js';
import { Ledger } from '../dist/src/operations.js';
import { Runner, envelope, MAX_PENDING, MAX_RESPONSE_BYTES } from '../dist/src/discovery.js';
import * as image from '../dist/src/thumbnail.js';
// Exercise helpers conformance against the SDK contract
export async function helpersConformance() {
    const fixture = JSON.parse(
        readFileSync(new URL('../../contracts/fixtures/helpers-v1.json', import.meta.url)),
    );
    {
        const s = fixture.cache,
            c = new Cache(s.capacity, s.bytes),
            [a, b, d] = s.keys;
        c.put(a, 'A', s.cost, 0, 0);
        c.put(b, 'B', s.cost, 0, 0);
        c.get(a, 1);
        assert.deepEqual(c.put(d, 'C', s.cost, s.expiry, 2), [b]);
        assert.throws(() => c.put(a, 'bad', 1000, 0, 3));
        assert.equal(c.peek(a, 3), 'A');
        assert.equal(c.get(d, s.expiry), undefined);
        assert.equal(c.bytes, 75);
        assert.deepEqual(c.trim(s.bytes, 31), [a]);
        assert.equal(c.bytes, 0);
        const budget = new Budget(100);
        assert(budget.reserve(80));
        assert(!budget.reserve(21));
        assert.throws(() => budget.release(81));
        assert.equal(budget.used, 80);
        budget.release(80);
    }
    {
        const s = fixture.operations,
            l = new Ledger(s.capacity, s.bytes),
            a = l.reserve(s.group, 'first', s.cost),
            b = l.reserve(s.group, 'second', s.cost),
            c = l.reserve('other', 'third', s.cost);
        assert.deepEqual([a.id, b.id, c.id], s.ids);
        l.update(b.id, 'uncertain', 'unknown', s.cost);
        assert.throws(() => l.reserve('fourth', 'no', s.cost));
        assert.deepEqual(l.retire(new Set()), []);
        l.update(a.id, 'confirmed', 'done', s.cost, true);
        assert.deepEqual(l.retire(new Set()), []);
        l.update(a.id, 'confirmed', 'done', s.cost);
        assert.deepEqual(l.retire(new Set([s.group])), []);
        assert.deepEqual(l.retire(new Set()), [a.id]);
        const before = l.bytes;
        assert.throws(() => l.update(b.id, 'confirmed', 'too big', s.bytes));
        assert.equal(l.bytes, before);
        assert.equal(l.get(b.id).state, 'uncertain');
        l.reserve('fourth', 'ok', s.cost);
    }
    {
        const s = fixture.discovery,
            r = new Runner(),
            req = { context: s.context, channel: s.channel, operation: s.operation, id: s.first },
            a = r.start(req, s.now, s.timeout);
        for (const id of [...s.invalid_ids, undefined, null, 1]) {
            assert.throws(() => r.start({ ...req, id }, s.now, s.timeout));
            assert.throws(() => envelope({ ...req, id }, {}));
        }
        const isolated = { ...req, context: 'other-context' };
        const other = r.start(isolated, s.now, s.timeout);
        const duplicate = r.start(req, s.now, s.timeout);
        assert(a.signal.aborted);
        assert(!other.signal.aborted);
        assert(r.cancel(isolated.context, isolated.channel, isolated.id));
        assert(other.signal.aborted);
        req.id = s.second;
        const b = r.start(req, s.now + 1, s.timeout);
        assert(a.signal.aborted);
        assert(duplicate.signal.aborted);
        assert.equal(r.complete(duplicate, {}, '', s.now), undefined);
        assert(!r.cancel(s.context, s.channel, s.first));
        assert.equal(r.complete(a, {}, '', s.now + 2), undefined);
        const reply = r.complete(
            b,
            { request_id: 'spoof', op: 'spoof', devices: ['one'] },
            '',
            s.now + 2,
        );
        assert.equal(reply.payload.request_id, s.second);
        assert.equal(reply.payload.op, s.operation);
        assert.equal(reply.payload.available, true);
        const t = r.start(req, s.now, s.timeout);
        assert.equal(r.nextDeadline(), s.now + s.timeout);
        assert.equal(r.expire(s.now + s.timeout)[0].payload.error, 'timeout');
        assert(t.signal.aborted);
        assert.equal(
            envelope(req, { data: 'x'.repeat(MAX_RESPONSE_BYTES) }).payload.error,
            'response_too_large',
        );
        for (let i = 0; i < MAX_PENDING; i++) r.start({ ...req, context: String(i) }, 0, 1);
        assert.throws(() => r.start({ ...req, context: 'overflow' }, 0, 1));
        r.clear('0');
        r.start({ ...req, context: 'overflow' }, 0, 1);
        r.close();
        assert.throws(() => r.start(req, 0, 1));
    }
    {
        const s = fixture.thumbnails;
        assert.equal(image.MAX_INPUT_BYTES, s.max_input_bytes);
        assert.equal(image.MAX_SOURCE_PIXELS, s.max_source_pixels);
        for (const c of s.fit)
            assert.deepEqual(
                image.fit(...c.source, ...c.box, c.pixels ? { maxSourcePixels: c.pixels } : {}),
                c.output,
            );
        assert.throws(() => image.fit(8000, 8000, 320, 320));
        const source = { width: 3, height: 2, data: new Uint8Array(24) };
        source.data.set([12, 34, 56, 128]);
        const encoded = image.png(source);
        assert.throws(() =>
            image.decode(encoded, 3, 2, undefined, { maxInputBytes: encoded.length - 1 }),
        );
        assert.throws(() => image.decode(encoded, 3, 2, undefined, { maxSourcePixels: 5 }));
        const rendered = image.render(encoded, 320, 320, undefined, {
            maxInputBytes: encoded.length,
            maxSourcePixels: 6,
        });
        assert.equal(rendered.width, 3);
        assert.equal(rendered.height, 2);
        assert.deepEqual(
            image.decode(Buffer.from(rendered.dataURI.split(',')[1], 'base64'), 3, 2).data,
            source.data,
        );
        assert.throws(() => image.png(source, () => true));
        assert.throws(() => image.decode(encoded, 1, 1, undefined, { maxSourcePixels: 0 }));
        assert.throws(() => image.decode(encoded.subarray(0, encoded.length / 2), 1, 1));
    }
}

// Exercise cache persistence conformance against the SDK contract
export async function cachePersistenceConformance() {
    for (const applied of [false, true]) {
        let revision = 0,
            fail = true;
        const records = new Map(),
            receipts = new Map(),
            commits = [];
        // Provide version behavior for this helpers fixture
        const version = () => ({ generation: '1', revision: String(revision) });
        const backend = {
            // Provide list keys behavior for this helpers fixture
            async listKeys() {
                return { ...version(), keys: [...records.keys()] };
            },
            // Provide get behavior for this helpers fixture
            async get(key) {
                return {
                    ...version(),
                    found: records.has(key),
                    value: structuredClone(records.get(key)),
                };
            },
            // Provide receipt behavior for this helpers fixture
            async receipt(_generation, id) {
                return receipts.get(id) ?? { ...version(), found: false };
            },
            // Provide commit behavior for this helpers fixture
            async commit(v, id, mutations) {
                commits.push(structuredClone({ v, id, mutations }));
                if (v.revision !== String(revision)) throw new RPCError('conflict', 'changed');
                if (fail && !applied) {
                    fail = false;
                    throw new RPCError('indeterminate', 'lost reply');
                }
                for (const m of mutations)
                    if (m.op === 'delete') records.delete(m.key);
                    else records.set(m.key, structuredClone(m.value));
                revision++;
                const reply = { ...version(), operation_id: id, found: true, committed: true };
                receipts.set(id, reply);
                if (fail) {
                    fail = false;
                    throw new RPCError('indeterminate', 'lost reply');
                }
                return reply;
            },
        };
        const c = new Cache(100, 100000),
            store = new Persistence(backend);
        await store.load(c, 0);
        for (let i = 0; i < 70; i++) c.put(String(i), i, 16, 0, 0);
        await assert.rejects(store.flush(c, 0));
        c.put('late', 99, 16, 0, 0);
        await store.flush(c, 0);
        assert.equal(records.size, 71);
        assert(commits.every((c) => c.mutations.length <= 32));
        if (!applied) assert.equal(commits[0].id, commits[1].id);
        const restored = new Cache(100, 100000);
        await new Persistence(backend).load(restored, 0);
        assert.equal(restored.get('late', 0), 99);
        c.put('late', 100, 16, 0, 0);
        revision++;
        await assert.rejects(store.flush(c, 0));
        const attempts = commits.length;
        await assert.rejects(store.flush(c, 0));
        assert.equal(commits.length, attempts);
        const large = new Cache(100, 1 << 20),
            largeStore = new Persistence(backend);
        await largeStore.load(large, 0);
        large.clear();
        for (let i = 0; i < 3; i++) large.put('big' + i, 'x'.repeat(210000), 220000, 0, 0);
        await largeStore.flush(large, 0);
        assert(commits.every((c) => Buffer.byteLength(JSON.stringify(c.mutations)) < 512 << 10));
    }
}

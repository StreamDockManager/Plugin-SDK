import './shared-primitives.mjs';
import { extensionsConformance } from './extensions.mjs';
import { Client } from '../dist/src/runtime.js';
import { startTrace } from '../dist/src/diagnostics.js';
import assert from 'node:assert/strict';
import {
    readFile,
    mkdtemp,
    rm,
    writeFile,
    open,
    stat,
    rename,
    mkdir,
    symlink,
    rmdir,
} from 'node:fs/promises';
import { spawnSync } from 'node:child_process';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { HostInfo, frameCapabilities, MAX_MESSAGE } from '../dist/src/protocol.js';
import { validate } from '../dist/src/settings.js';
import { displayMessages, Plugin } from '../dist/src/authoring.js';
import { retryDelay, Resources, resolveInterval } from '../dist/src/polling.js';
import {
    parsePath,
    extractPath,
    Service,
    request,
    ValidatorCache,
    redact as redactPreview,
} from '../dist/src/httpclient.js';
import { secret, redact } from '../dist/src/credentials.js';
import { Clock, Recorder } from '../dist/src/testkit.js';
import { Store } from '../dist/src/storage.js';
import { MAXIMUM_STATE_BYTES, readState, missingParent } from '../dist/src/internal/state-read.js';
import { runProcess } from '../dist/src/process.js';
import { fingerprint, cached, remember } from '../dist/src/packaging.js';
import { Connector } from '../dist/src/connectors.js';
import { createServer } from 'node:http';
import { gzipSync } from 'node:zlib';
import { incomingAdmission } from './incoming.mjs';
import { hostStorageConformance } from './hoststorage.mjs';
import { WorkerPool } from '../dist/src/workers.js';
import { NetworkPayload } from '../dist/src/network.js';
// Exercise plugin theme rpc against the SDK contract
async function pluginThemeRPC() {
    const protocol = JSON.parse(
        await readFile(
            new URL('../../contracts/fixtures/protocol-v1.json', import.meta.url),
            'utf8',
        ),
    );
    const legacy = new Client(new HostInfo(protocol.manager));
    await assert.rejects(legacy.getPluginTheme(), /plugin-theme-v1/);
    legacy.close();
    const { host, response } = protocol.theme_rpc;
    const client = new Client(new HostInfo(host));
    const result = client.getPluginTheme();
    const [request] = client.drain();
    assert.equal(request.protocol_id, 'plugin-theme-v1');
    assert.equal(request.method, 'get');
    assert.deepEqual(request.payload, {});
    assert(
        client.control.observe({
            event: 'streamDockManagerRpcResponse',
            protocol_id: request.protocol_id,
            request_id: request.request_id,
            result: response,
        }),
    );
    assert.equal((await result).theme.surface, '#172138');
    await client.dispatch(
        {
            // Provide handle behavior for this conformance fixture
            handle: () => assert.fail('stale revision delivered'),
        },
        { event: 'didReceivePluginTheme', payload: response },
    );
    assert.equal(client.currentPluginTheme().revision, 1);
    client.close();
}
await pluginThemeRPC();
await hostStorageConformance();
const fixture = JSON.parse(
    await readFile(new URL('../../contracts/fixtures/authoring-v1.json', import.meta.url), 'utf8'),
);

// Exercise appearance lifecycle incarnations against the SDK contract
async function appearanceLifecycleIncarnations() {
    const spec = fixture.appearance_lifecycle;
    const recorder = new Recorder(new HostInfo({}));
    const plugin = new Plugin();
    plugin.register(spec.action, {
        // Provide render behavior for this conformance fixture
        render: (_plugin, _context, binding) => {
            assert.deepEqual(binding.settings, spec.settings);
            return { title: 'Ready' };
        },
    });
    for (const step of spec.steps) {
        await recorder.replay(plugin, [step.event]);
        assert.deepEqual([...plugin.bindings.keys()].sort(), step.bindings);
        const titles = recorder
            .drain()
            .filter((m) => m.event === 'setTitle')
            .map((m) => m.context);
        assert.deepEqual(titles, step.render_context === null ? [] : [step.render_context]);
    }
    await plugin.close();
}
await appearanceLifecycleIncarnations();
for (const test of fixture.settings) {
    if (test.valid) assert.deepEqual(validate(fixture.fields, test.input), test.expected);
    else assert.throws(() => validate(fixture.fields, test.input));
}
for (const schema of fixture.invalid_schemas) assert.throws(() => validate(schema, {}));
for (const test of fixture.displays)
    assert.deepEqual(
        displayMessages('a', test.previous ?? undefined, test.desired, test.rich),
        test.expected,
        test.name,
    );
for (const test of fixture.retries)
    assert.equal(retryDelay(test.interval_ms, test.failures, test.jitter), test.expected_ms);
for (const test of fixture.paths)
    assert.deepEqual(extractPath(test.root, test.path), { value: test.value, found: test.found });
for (const path of fixture.invalid_paths) assert.throws(() => parsePath(path), path);
assert.equal(
    secret({ password: '${KEY}' }, 'password', () => 'secret+value'),
    'secret+value',
);
assert.throws(() => secret({ password: 'prefix-${KEY}' }, 'password', () => 'value'));
assert(
    !redact(
        Error('secret+value secret%2Bvalue https://user:pass@host'),
        { password: '${KEY}' },
        () => 'secret+value',
    ).message.includes('secret'),
);
assert(frameCapabilities({ version: 1 }).startsWith('SDMCAP1:'));
const resources = new Resources();
resources.watch('a', 'same', 1000);
resources.watch('b', 'same', 2000);
const generation = resources.begin('same');
resources.unwatch('a');
resources.unwatch('b');
resources.watch('a', 'same', 1000);
assert(!resources.complete('same', generation, 1, undefined, 0));
const directory = await mkdtemp(join(tmpdir(), 'sdm-js-'));
for (const test of fixture.state_reads.missing_paths) {
    const root = await mkdtemp(join(directory, 'paths-'));
    const blocker = join(root, 'blocked');
    await writeFile(blocker, 'keep');
    const path = join(root, test.path),
        store = new Store(path);
    if (test.error) {
        await assert.rejects(store.load(), undefined, test.name);
        // Exercise Windows' ambiguous ENOENT path even on Unix hosts
        await assert.rejects(missingParent(path), undefined, test.name);
    } else {
        await store.load();
        store.change({ saved: true });
        await store.close();
    }
    assert.equal(await readFile(blocker, 'utf8'), 'keep');
}
// Exercise directory link against the SDK contract
async function directoryLink(target, link) {
    try {
        await symlink(target, link, 'dir');
        console.log('directory alias fixture: symlink');
        return;
    } catch (error) {
        if (process.platform !== 'win32') throw error;
    }
    assert(!/[%!&|<>^"()\r\n]/.test(target + link), 'unsafe junction fixture temp path');
    const result = spawnSync('cmd.exe', ['/D', '/V:OFF', '/C', 'mklink', '/J', link, target], {
        timeout: 10_000,
        maxBuffer: 64 * 1024,
        encoding: 'utf8',
        windowsHide: true,
    });
    assert.ifError(result.error);
    assert.equal(
        result.status,
        0,
        'directory junction fixture failed: ' + result.stdout + result.stderr,
    );
    console.log('directory alias fixture: Windows junction (symlink privilege unavailable)');
}
{
    const root = await mkdtemp(join(directory, 'state-links-')),
        link = join(root, 'link'),
        target = join(root, 'target');
    await mkdir(target);
    await directoryLink(target, link);
    await rmdir(target);
    await assert.rejects(new Store(link).load());
    await assert.rejects(new Store(join(link, 'state.json')).load());
    const path = join(root, 'protected.json'),
        protectedStore = new Store(path);
    await writeFile(path, Buffer.alloc(MAXIMUM_STATE_BYTES + 1, 32));
    await assert.rejects(protectedStore.load());
    await rename(path, path + '.saved');
    await mkdir(target);
    await directoryLink(target, path);
    await rmdir(target);
    await assert.rejects(protectedStore.load());
    await rm(path);
    protectedStore.change({ pending: true });
    await assert.rejects(protectedStore.close(), /protected/);
    await protectedStore.load();
    await protectedStore.close();
    assert.equal((await stat(path + '.saved')).size, MAXIMUM_STATE_BYTES + 1);
    await rm(link);
    await mkdir(target);
    await directoryLink(target, link);
    const store = new Store(join(link, 'state.json'));
    await store.load();
    store.change({ saved: true });
    await store.close();
    assert.deepEqual(await new Store(join(target, 'state.json')).load(), { saved: true });
}
// Exercise state read limits against the SDK contract
async function stateReadLimits() {
    const contract = fixture.state_reads;
    assert.equal(MAXIMUM_STATE_BYTES, contract.maximum_bytes);
    let consumed = 0;
    // An unending sequence of short reads models a source growing after open
    // The reader must stop at the overflow byte without asking for EOF or stat
    await assert.rejects(
        readState({
            // Provide read behavior for this conformance fixture
            async read(buffer, offset, length) {
                const bytesRead = Math.min(997, length);
                buffer.fill(32, offset, offset + bytesRead);
                consumed += bytesRead;
                return { bytesRead };
            },
        }),
        /State exceeds limit/,
    );
    assert.equal(consumed, contract.maximum_bytes + contract.read_ahead_bytes);
    const source = Buffer.from('état');
    let position = 0;
    assert.deepEqual(
        await readState({
            // Provide read behavior for this conformance fixture
            async read(buffer, offset) {
                const bytesRead = position < source.length ? 1 : 0;
                if (bytesRead) buffer[offset] = source[position++];
                return { bytesRead };
            },
        }),
        source,
    );
    const failure = Error('read failed');
    await assert.rejects(
        readState({
            // Provide read behavior for this conformance fixture
            async read() {
                throw failure;
            },
        }),
        (error) => error === failure,
    );

    const path = join(directory, 'bounded-state.json');
    for (const test of contract.cases) {
        const raw = Buffer.from(
            JSON.stringify(
                test.version ? { version: test.version, data: contract.payload } : contract.payload,
            ),
        );
        const size = contract.maximum_bytes + test.extra_bytes;
        const file = await open(path, 'w');
        try {
            await file.writeFile(raw);
            const padding = Buffer.alloc(64 << 10, 32);
            let remaining = size - raw.length;
            while (remaining) {
                const chunk = Math.min(remaining, padding.length);
                await file.writeFile(padding.subarray(0, chunk));
                remaining -= chunk;
            }
        } finally {
            await file.close();
        }
        const store = new Store(path, test.version);
        store.value = { previous: true };
        if (test.valid) {
            assert.deepEqual(await store.load(), contract.payload, test.name);
        } else {
            await assert.rejects(store.load(), /State exceeds limit/, test.name);
            assert.deepEqual(store.value, { previous: true });
            assert.equal(store.dirty, false);
        }
        assert.equal((await stat(path)).size, size);
        if (!test.valid) {
            store.change({ replacement: true });
            await assert.rejects(store.close(), /saved state is protected/);
            assert.equal(store.dirty, true);
            assert.equal((await stat(path)).size, size);
            if (test.name === 'large_file') await rename(path, path + '.saved');
            else await writeFile(path, raw);
            const restored = await store.load();
            if (test.name !== 'large_file') assert.deepEqual(restored, contract.payload);
            store.change(contract.payload);
            await store.close();
            assert.deepEqual(await new Store(path, test.version).load(), contract.payload);
            if (test.name === 'large_file') assert.equal((await stat(path + '.saved')).size, size);
        }
    }
}
// Exercise state write limits against the SDK contract
async function stateWriteLimits() {
    for (let repetition = 0; repetition < 5; repetition++) {
        for (const test of fixture.state_writes.cases) {
            const path = join(directory, 'write-state.json');
            const store = new Store(path, test.version);
            store.change(test.value);
            const expected = JSON.stringify(
                test.version ? { version: test.version, data: test.value } : test.value,
            );
            const stringify = JSON.stringify;
            let calls = 0;
            try {
                JSON.stringify = (...args) => {
                    calls++;
                    return stringify(...args);
                };
                await store.close();
            } finally {
                JSON.stringify = stringify;
            }
            assert.equal(calls, fixture.state_writes.serialization_passes_per_flush);
            assert.equal(await readFile(path, 'utf8'), expected);
            assert.deepEqual(await new Store(path, test.version).load(), test.value);
            await rename(path, path + '.saved');
            await mkdir(path);
            store.change({ changed: true });
            await assert.rejects(store.close());
            assert(store.dirty);
            assert.equal(await readFile(path + '.saved', 'utf8'), expected);
            await rm(path, { recursive: true });
            await rename(path + '.saved', path);
            await store.close();
            assert.deepEqual(await new Store(path, test.version).load(), { changed: true });
        }
    }
}
// Exercise stream byte accounting against the SDK contract
function streamByteAccounting() {
    for (const asynchronous of [false, true]) {
        const text = 'é😀\ud800',
            size = new TextEncoder().encode(text).length,
            network = new NetworkPayload(),
            events = [];
        network.diagnostics.io = (direction, bytes) => events.push([direction, bytes]);
        const tracked = network.track({
            // Provide read behavior for this conformance fixture
            read: () => (asynchronous ? Promise.resolve(text) : text),
            // Provide write behavior for this conformance fixture
            write: () => (asynchronous ? Promise.resolve(3) : 3),
        });
        // Provide verify behavior for this conformance fixture
        const verify = async () => {
            assert.equal(await tracked.read(), text);
            assert.equal(await tracked.write(text), 3);
            assert.deepEqual(network.snapshot(), { sent: 3, received: size });
            assert.deepEqual(events, [
                ['read', size],
                ['write', 3],
            ]);
        };
        pendingAccounting.push(verify());
    }
}
const pendingAccounting = [];
streamByteAccounting();
await Promise.all(pendingAccounting);
assert.equal(
    redact(Error("p a!()*' p%20a%21%28%29%2A%27 p+a%21%28%29%2A%27"), { password: "p a!()*'" })
        .message,
    '[redacted] [redacted] [redacted]',
);

// Exercise worker input ownership against the SDK contract
async function workerInputOwnership() {
    const path = join(directory, 'owned-worker.mjs');
    await writeFile(
        path,
        `import {parentPort,workerData} from 'node:worker_threads';
if(workerData instanceof ArrayBuffer)parentPort.postMessage(new Uint8Array(workerData)[0]);
else if(Array.isArray(workerData))parentPort.postMessage({method:workerData.forEach,extra:workerData.extra===workerData[0].buffer,value:workerData[0][0]});
else if(workerData.resizable){workerData.buffer.resize(16);parentPort.postMessage({resizable:workerData.buffer.resizable,max:workerData.buffer.maxByteLength,size:workerData.buffer.byteLength,value:workerData.bytes[0]});}
else if(workerData.graph){workerData.bytes[0]=55;Atomics.add(new Int32Array(workerData.shared),0,1);parentPort.postMessage({cycle:workerData.self===workerData,view:workerData.view.buffer===workerData.bytes.buffer,map:workerData.map.get(workerData.bytes.buffer)===workerData.bytes,set:workerData.set.has(workerData.bytes),value:workerData.bytes[1],isolation:globalThis.ownedWorkerCalls=(globalThis.ownedWorkerCalls??0)+1});}
else setTimeout(()=>parentPort.postMessage(workerData.bytes[0]),workerData.delay);`,
    );
    const { pathToFileURL } = await import('node:url');
    const pool = new WorkerPool();
    try {
        const cancelled = new AbortController();
        cancelled.abort(Error('already cancelled'));
        await assert.rejects(
            pool.submit(
                pathToFileURL(path),
                {
                    // Provide value behavior for this conformance fixture
                    get value() {
                        throw Error('must not clone cancelled input');
                    },
                },
                cancelled.signal,
            ),
            /already cancelled/,
        );
        const source = new Uint8Array([17]);
        const jobs = Array.from({ length: 4 }, () =>
            pool.submit(pathToFileURL(path), { bytes: source, delay: 50 }),
        );
        const queued = pool.submit(pathToFileURL(path), { bytes: source, delay: 0 });
        source[0] = 99;
        assert.deepEqual(await Promise.all([...jobs, queued]), [17, 17, 17, 17, 17]);
        for (const bytes of [new Uint8Array([23, 17]), Buffer.from([23, 17])]) {
            const shared = new SharedArrayBuffer(4);
            const graph = {
                graph: true,
                bytes,
                view: new DataView(bytes.buffer),
                shared,
                map: new Map([[bytes.buffer, bytes]]),
                set: new Set([bytes]),
            };
            graph.self = graph;
            for (let i = 0; i < 2; i++)
                assert.deepEqual(await pool.submit(pathToFileURL(path), graph), {
                    cycle: true,
                    view: true,
                    map: true,
                    set: true,
                    value: 17,
                    isolation: 1,
                });
            assert.equal(bytes.byteLength, 2);
            assert.equal(bytes[0], 23);
            assert.equal(new Int32Array(shared)[0], 2);
            const direct = bytes.buffer.slice(
                bytes.byteOffset,
                bytes.byteOffset + bytes.byteLength,
            );
            assert.equal(await pool.submit(pathToFileURL(path), direct), 23);
            assert.equal(direct.byteLength, 2);
            assert.equal(bytes.byteLength, 2);
            assert.equal(bytes[0], 23);
        }
        const array = [new Uint8Array([41])];
        array.forEach = 17;
        array.extra = array[0].buffer;
        assert.deepEqual(await pool.submit(pathToFileURL(path), array), {
            method: 17,
            extra: true,
            value: 41,
        });
        assert.equal(array[0][0], 41);
        const buffer = new ArrayBuffer(8, { maxByteLength: 16 });
        if (buffer.resizable) {
            const bytes = new Uint8Array(buffer, 2, 2);
            bytes[0] = 29;
            assert.deepEqual(
                await pool.submit(pathToFileURL(path), { resizable: true, buffer, bytes }),
                { resizable: true, max: 16, size: 16, value: 29 },
            );
            assert.equal(buffer.byteLength, 8);
            assert.equal(bytes[0], 29);
            buffer.resize(12);
            assert.equal(buffer.byteLength, 12);
        }
    } finally {
        pool.close();
    }
}
try {
    await stateWriteLimits();
    await workerInputOwnership();
    await stateReadLimits();
    await incomingAdmission();
    const clock = new Clock(1000),
        store = new Store(join(directory, 'state.json'), 1, clock.now);
    store.change({ count: 2 });
    assert.equal(await store.flush(), false);
    clock.advance(250);
    assert(await store.flush());
    assert.deepEqual(await new Store(store.path, 1).load(), { count: 2 });
    assert.equal(
        await runProcess(process.env.STREAM_DOCK_MANAGER_NODE ?? process.execPath, [
            '-e',
            'process.stdout.write(process.argv[1])',
            'literal $HOME;`x`',
        ]),
        'literal $HOME;`x`',
    );
    await assert.rejects(
        runProcess(
            process.env.STREAM_DOCK_MANAGER_NODE ?? process.execPath,
            ['-e', 'process.stdout.write("x".repeat(1000))'],
            { outputLimit: 10 },
        ),
    );
    const path = join(directory, 'artifact');
    await writeFile(path, 'a');
    const key = fingerprint({ a: 'a' }, { target: 'test' });
    await remember(path, key);
    assert(await cached(path, key));
    await writeFile(path, 'b');
    assert(!(await cached(path, key)));
    const longPath = join(directory, 'a'.repeat(205));
    await writeFile(longPath, 'long artifact basename');
    await remember(longPath, key);
    assert(await cached(longPath, key));
    const server = createServer(
        // Handle createServer work for conformance
        (req, res) => {
            if (req.url === '/fail') {
                res.writeHead(429, { 'Retry-After': '3' }).end();
                return;
            }
            if (req.headers['if-none-match'] === '"x"') {
                res.writeHead(304).end();
                return;
            }
            res.writeHead(200, { 'Content-Type': 'application/json', ETag: '"x"' }).end(
                '{"value":3}',
            );
        },
    );
    await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
    try {
        const url = `http://127.0.0.1:${server.address().port}`;
        assert.deepEqual(await new Service().json('GET', url), { value: 3 });
        await assert.rejects(
            new Service().json('GET', url + '/fail'),
            (error) => error.status === 429 && error.retryAt > Date.now(),
        );
        const cache = new ValidatorCache();
        await request({ url, fields: { value: 'value' } }, { cache, automatic: true });
        assert(
            (await request({ url, fields: { value: 'value' } }, { cache, automatic: true }))
                .revalidated,
        );
        await assert.rejects(request({ url, method: 'POST' }, { automatic: true }));
    } finally {
        await new Promise((resolve) => server.close(resolve));
    }
    const recorder = new Recorder(
        new HostInfo({
            streamDockManager: {
                contract_version: 1,
                capabilities: ['plugin-protocol-v1', 'action-results-v1'],
            },
        }),
    );
    const plugin = new Plugin();
    plugin.register('test', {
        // Provide render behavior for this conformance fixture
        render: () => ({
            title: 'hello',
        }),
    });
    await recorder.replay(plugin, [
        { event: 'willAppear', context: 'a', action: 'test', payload: { settings: {} } },
    ]);
    assert(recorder.drain().some((x) => x.event === 'setTitle'));
    await recorder.replay(plugin, [{ event: 'willDisappear', context: 'a', action: 'test' }]);
    plugin.renderTargets(recorder.client);
    assert.equal(recorder.drain().length, 0);
    let reads = 0,
        commands = [];
    const connector = new Connector(
        {
            // Provide key behavior for this conformance fixture
            key: () => 'one',
            // Provide read behavior for this conformance fixture
            read: async () => {
                reads++;
                return 'sample';
            },
            // Provide command behavior for this conformance fixture
            command: async (_signal, action, _settings, ticks) => {
                commands.push([action, ticks]);
                return { message: 'delivered' };
            },
            // Provide discover behavior for this conformance fixture
            discover: async () => [],
            // Provide display behavior for this conformance fixture
            display: (value) => value,
            // Provide close behavior for this conformance fixture
            close: () => {},
        },
        { actions: ['status', 'volume'], interval: 10000, dialActions: ['volume'] },
    );
    await recorder.replay(connector, [
        { event: 'willAppear', context: 'a', action: 'status', payload: { settings: {} } },
        { event: 'willAppear', context: 'b', action: 'status', payload: { settings: {} } },
    ]);
    await recorder.poll(connector);
    for (let i = 0; i < 8; i++) {
        await new Promise((resolve) => setTimeout(resolve, 0));
        await recorder.poll(connector);
    }
    assert.equal(reads, 1);
    await recorder.replay(connector, [
        {
            event: 'sendToPlugin',
            context: 'inspector',
            action: 'status',
            payload: { op: 'discover', request_id: 'picker-1' },
        },
    ]);
    for (let i = 0; i < 8; i++) {
        await new Promise((resolve) => setTimeout(resolve, 0));
        await recorder.poll(connector);
    }
    assert.equal(commands.length, 0);
    assert(
        recorder
            .drain()
            .some(
                (e) =>
                    e.event === 'sendToPropertyInspector' &&
                    e.payload.request_id === 'picker-1' &&
                    e.payload.available === true,
            ),
    );
    connector.close();
    recorder.close();
} finally {
    await rm(directory, { recursive: true, force: true });
}
console.log(
    'JavaScript shared authoring, services, state, processes, cache and connector conformance passed',
);
const services = JSON.parse(
    await readFile(new URL('../../contracts/fixtures/services-v1.json', import.meta.url), 'utf8'),
);
for (const test of services.credentials) {
    // Provide call behavior for this conformance fixture
    const call = () => secret(test.settings, test.key, (key) => test.lookup[key]);
    if (test.invalid) assert.throws(call);
    else assert.equal(call(), test.value);
}
for (const test of services.fingerprints)
    assert.equal(fingerprint(test.inputs, test.configuration), test.expected);

const cacheDirectory = await mkdtemp(join(tmpdir(), 'sdk-cache-artifacts-'));
try {
    const pattern = services.cache_artifacts.byte_pattern;
    const chunk = Buffer.alloc(64 << 10);
    for (let i = 0; i < chunk.length; i++) chunk[i] = pattern[i % pattern.length];
    const path = join(cacheDirectory, 'artifact');
    for (const test of services.cache_artifacts.cases) {
        const output = await open(path, 'w');
        try {
            for (let remaining = test.bytes; remaining > 0;) {
                const count = Math.min(remaining, chunk.length);
                let written = 0;
                while (written < count) {
                    const result = await output.write(chunk, written, count - written);
                    written += result.bytesWritten;
                }
                remaining -= count;
            }
        } finally {
            await output.close();
        }
        await remember(path, 'fixture');
        assert(await cached(path, 'fixture'));
        assert(!(await cached(path, 'different')));
        const record = JSON.parse(await readFile(path + '.build.json', 'utf8'));
        assert.equal(record.output, test.sha256);
        const changed = await open(path, 'r+');
        try {
            await changed.write(Buffer.from([99]), 0, 1, Math.max(0, test.bytes - 1));
        } finally {
            await changed.close();
        }
        assert(!(await cached(path, 'fixture')));
        await rm(path);
        assert(!(await cached(path, 'fixture')));
        await assert.rejects(remember(path, 'fixture'));
    }
} finally {
    await rm(cacheDirectory, { recursive: true, force: true });
}

assert.deepEqual(
    redactPreview(fixture.redaction.input, fixture.redaction.secrets),
    fixture.redaction.expected,
);
const { serve, tls } = await import('../dist/src/peers.js');
const { connect: tlsConnect } = await import('node:tls');
const tlsPeer = await serve((socket) => socket.once('data', (data) => socket.end(data)), true);
try {
    await new Promise((resolve, reject) => {
        const socket = tlsConnect(
            { host: '127.0.0.1', port: tlsPeer.port, servername: 'localhost', ...tls().client },
            () => socket.write('ping'),
        );
        socket.once('error', reject);
        socket.once('data', (data) => {
            assert.equal(data.toString(), 'ping');
            socket.end();
            resolve();
        });
    });
} finally {
    await tlsPeer.close();
}

// Desktop acknowledgements correlate both the request and the binding context
for (const failed of [false, true]) {
    const { Client } = await import('../dist/src/runtime.js');
    const { HostInfo } = await import('../dist/src/protocol.js');
    const client = new Client(
        new HostInfo({
            streamDockManager: {
                contract_version: 1,
                capabilities: ['desktop-input-v1', 'action-results-v1'],
            },
        }),
    );
    const handler = {
        // Provide handle behavior for this conformance fixture
        handle(c, e) {
            if (e.event === 'keyUp') c.desktopInput('a', { op: 'copy', request_id: 'input' });
        },
    };
    await client.dispatch(handler, {
        event: 'keyUp',
        context: 'a',
        streamDockManager: { request_id: 'action' },
    });
    assert.equal(client.drain().filter((e) => e.event === 'actionResult').length, 0);
    await client.dispatch(handler, {
        event: 'desktopInputResult',
        context: 'other',
        payload: { request_id: 'input', error: false },
    });
    assert.equal(client.drain().length, 0);
    await client.dispatch(handler, {
        event: 'desktopInputResult',
        context: 'a',
        payload: { request_id: 'input', error: failed },
    });
    assert.deepEqual(client.drain().find((e) => e.event === 'actionResult').payload, {
        request_id: 'action',
        success: !failed,
    });
    client.close();
}

const values = await import('../dist/src/settings.js');
for (const test of services.typed_values) {
    assert.equal(values.number(test.input, 'n', 99), test.number);
    assert.equal(values.string(test.input, 's', 'fallback'), test.string);
    assert.equal(values.boolean(test.input, 'b'), test.boolean);
}
const { HTTPError } = await import('../dist/src/httpclient.js');
const failure = redact(new HTTPError(429, 1234), { password: 'secret' });
assert.equal(failure.status, 429);
assert.equal(failure.retryAt, 1234);
const { parseCapabilities } = await import('../dist/src/protocol.js');
assert.deepEqual(parseCapabilities(frameCapabilities({ version: 1, id: 'fixture' })), {
    version: 1,
    id: 'fixture',
});
{
    const commands = [];
    let active = 0,
        peak = 0;
    const driver = {
        // Provide key behavior for this conformance fixture
        key: () => 'one',
        // Provide read behavior for this conformance fixture
        read: async () => 'sample',
        // Provide command behavior for this conformance fixture
        command: async (_signal, _action, _settings, ticks) => {
            active++;
            peak = Math.max(peak, active);
            commands.push(ticks);
            await new Promise((resolve) => setTimeout(resolve, 5));
            active--;
            return { message: 'done' };
        },
        // Provide discover behavior for this conformance fixture
        discover: async () => [],
        // Provide display behavior for this conformance fixture
        display: (value) => value,
        // Provide close behavior for this conformance fixture
        close: () => {},
    };
    const connector = new Connector(driver, {
        actions: ['volume'],
        interval: 10000,
        dialActions: ['volume'],
    });
    const recorder = new Recorder();
    await recorder.replay(connector, [
        { event: 'willAppear', context: 'a', action: 'volume', payload: { settings: {} } },
    ]);
    for (const ticks of [2, 3, -1, 4])
        await recorder.replay(connector, [
            { event: 'dialRotate', context: 'a', action: 'volume', payload: { ticks } },
        ]);
    const deadline = Date.now() + 2000;
    while (commands.length < 4 || active) {
        await recorder.poll(connector);
        assert(Date.now() < deadline);
        await new Promise((resolve) => setTimeout(resolve, 2));
    }
    for (let n = 0; n < 5; n++) await recorder.poll(connector);
    assert.deepEqual(commands, [2, 3, -1, 4]);
    assert.equal(peak, 1);
    await recorder.replay(connector, [{ event: 'willDisappear', context: 'a', action: 'volume' }]);
    connector.close();
    recorder.close();
}

const protocol = JSON.parse(
    await readFile(new URL('../../contracts/fixtures/protocol-v1.json', import.meta.url), 'utf8'),
);
const { Client: ProtocolClient } = await import('../dist/src/runtime.js');
const { parseEvent: decodeEvent } = await import('../dist/src/protocol.js');
assert.deepEqual(
    Object.fromEntries(
        Object.keys(protocol.gates).map((event) => [
            event,
            new HostInfo(protocol.manager).supports(protocol.gates[event]),
        ]),
    ),
    Object.fromEntries(Object.keys(protocol.gates).map((event) => [event, true])),
);
for (const event of protocol.events) assert.deepEqual(decodeEvent(JSON.stringify(event)), event);
for (const raw of [null, []]) assert.throws(() => new HostInfo(raw));
const legacy = new ProtocolClient(new HostInfo(protocol.legacy), 'token');
legacy.feedback('a', {});
legacy.title('a', 'fallback');
legacy.toInspector('a', { settings: {} }, 'action');
assert.deepEqual(legacy.drain().at(-1), {
    event: 'sendToPropertyInspector',
    context: 'a',
    payload: { settings: {} },
    action: 'action',
});
legacy.close();

{
    const calls = [],
        plugin = new Plugin(),
        recorder = new Recorder();
    plugin.register('action', {
        // Provide handle behavior for this conformance fixture
        handle: () => calls.push('action'),
    });
    await recorder.replay(plugin, [
        { event: 'willAppear', context: 'a', action: 'action', payload: { settings: {} } },
    ]);
    const event = { event: 'sendToPlugin', context: 'a', action: 'action', payload: {} };
    await recorder.replay(plugin, [event]);
    assert.deepEqual(calls, ['action']);
    plugin.inspector = () => {
        calls.push('inspector');
    };
    await recorder.replay(plugin, [event]);
    assert.deepEqual(calls, ['action', 'inspector']);
    plugin.inspector = undefined;
    await recorder.replay(plugin, [event]);
    assert.deepEqual(calls, ['action', 'inspector', 'action']);
    plugin.close();
    recorder.close();
}

// Exercise typed log writers redact before framing and keep legacy fallback against the SDK contract
function typedLogWritersRedactBeforeFramingAndKeepLegacyFallback() {
    for (const typed of [false, true]) {
        const recorder = new Recorder(
            new HostInfo({
                streamDockManager: {
                    contract_version: 1,
                    capabilities: typed ? ['plugin-logs-v1'] : [],
                },
            }),
        );
        recorder.client.logRedactor = (text) => text.replaceAll('secret', '[redacted]');
        const text = '界'.repeat(9000) + 'secret';
        recorder.client.stderr().write(text);
        const events = recorder.drain();
        for (const event of events) {
            assert.equal(event.event, 'logMessage');
            assert.equal(event.payload.stream, typed ? 'stderr' : undefined);
        }
        assert.equal(
            events.map((event) => event.payload.message).join(''),
            text.replaceAll('secret', '[redacted]'),
        );
    }
}
typedLogWritersRedactBeforeFramingAndKeepLegacyFallback();

// Exercise logging completion and isolation against the SDK contract
async function loggingCompletionAndIsolation() {
    const protocol = JSON.parse(
        await readFile(
            new URL('../../contracts/fixtures/protocol-v1.json', import.meta.url),
            'utf8',
        ),
    );
    const recorder = new Recorder(new HostInfo(protocol.manager));
    let complete;
    const handler = {
        // Provide should log action behavior for this conformance fixture
        shouldLogAction: (event) => event.event === 'keyUp',
        // Provide handle behavior for this conformance fixture
        handle(client) {
            complete = client.deferAction();
        },
    };
    await recorder.replay(handler, [{ event: 'keyUp', action: 'counter', context: 'key' }]);
    assert.deepEqual(recorder.drain(), []);
    complete.finish(Error('failed'));
    complete.finish();
    const done = recorder.drain();
    assert.equal(done.length, 1);
    assert.equal(done[0].payload.stream, 'stderr');
    assert.match(done[0].payload.message, /failed or cancelled/);
    for (let i = 0; i < 200; i++) {
        try {
            recorder.client.log('flood');
        } catch {}
    }
    recorder.client.title('key', 'still responsive');
    const flood = recorder.drain();
    assert.equal(flood[0].event, 'setTitle');
    assert.equal(flood[1].payload.incomplete, true);
    assert.equal(flood.length, 130);
    const text = '界'.repeat(12000);
    recorder.client.log(text);
    const frames = recorder.drain();
    assert.equal(frames.map((x) => x.payload.message).join(''), text);
    assert.ok(frames.every((x) => Buffer.byteLength(x.payload.message) <= 16384));
    await recorder.client.dispatch(
        {
            // Provide handle behavior for this conformance fixture
            handle() {
                throw Error('controlled handler failure');
            },
        },
        {
            event: 'keyDown',
            action: 'counter',
            context: 'key',
            streamDockManager: { request_id: 'failed-action' },
        },
    );
    const failed = recorder.drain();
    assert.equal(failed.find((value) => value.event === 'actionResult').payload.success, false);
    assert.ok(failed.some((value) => value.event === 'showAlert'));
    recorder.client.title('key', 'still running');
    assert.equal(recorder.drain()[0].event, 'setTitle');
    recorder.close();
}
// Exercise logging network contract against the SDK contract
async function loggingNetworkContract() {
    const protocol = JSON.parse(
        await readFile(
            new URL('../../contracts/fixtures/protocol-v1.json', import.meta.url),
            'utf8',
        ),
    );
    const c = new ProtocolClient(new HostInfo(protocol.manager));
    const { request, response, forbidden, limits, preferences_event } = protocol.logging;
    assert.equal(startTrace(c.network, 'GET', request.url), undefined);
    await c.dispatch(
        {
            // Provide handle behavior for this conformance fixture
            handle() {},
        },
        preferences_event,
    );
    const trace = startTrace(c.network, request.method, request.url, request.headers);
    trace.request.write(Buffer.from(JSON.stringify(request.body)));
    trace.request.complete = true;
    trace.received(response.headers);
    trace.response.write(Buffer.from(JSON.stringify(response.body)));
    trace.response.complete = true;
    trace.finish(response.status);
    trace.finish(response.status);
    const values = c.drain();
    assert.equal(values.length, 1);
    for (const secret of forbidden) assert.ok(!JSON.stringify(values).includes(secret), secret);
    assert.equal(values[0].payload.source, 'network');
    assert.equal(values[0].payload.stream, 'stdout');
    assert.equal(JSON.parse(values[0].payload.network.request.text).value, 'retained');
    assert.equal(values[0].payload.network.response.complete, true);
    const stale = startTrace(c.network, 'GET', request.url);
    c.setNetworkDebug(false);
    c.setNetworkDebug(true);
    stale.finish(200);
    assert.deepEqual(c.drain(), []);
    const large = startTrace(c.network, 'GET', request.url);
    large.response.write(new Uint8Array(limits.body_bytes + 1));
    large.finish(200);
    const body = c.drain()[0].payload.network.response;
    assert.equal(body.truncated, true);
    assert.equal(body.text, '');
    c.logRedactor = (text) => text.replaceAll('application-secret', '[redacted]');
    const binary = startTrace(c.network, 'GET', request.url);
    binary.response.write(
        Buffer.concat([Buffer.from([255]), Buffer.from('application-secret\0payload')]),
    );
    binary.finish(200);
    const decoded = Buffer.from(c.drain()[0].payload.network.response.text, 'base64');
    assert.ok(!decoded.includes('application-secret'));
    assert.ok(decoded.includes('payload'));
    const malformed = startTrace(c.network, 'GET', request.url);
    malformed.received({ 'Content-Type': 'application/json' });
    malformed.response.write(Buffer.from(protocol.logging.partial_json));
    malformed.finish(200);
    const partial = c.drain()[0].payload.network.response;
    assert.equal(partial.text, '');
    assert.equal(partial.truncated, true);
    const server = createServer((_req, res) =>
        res
            .writeHead(200, { 'Content-Type': 'application/json', 'Content-Encoding': 'gzip' })
            .end(gzipSync('{"password":"compressed-secret","value":"retained"}')),
    );
    await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
    const service = new Service(1024, 2000, c.network);
    try {
        assert.equal(
            (await service.json('GET', `http://127.0.0.1:${server.address().port}`)).value,
            'retained',
        );
        const capture = c
            .drain()
            .find((value) => value.payload?.network?.response?.content_type === 'application/json')
            .payload.network.response;
        assert.deepEqual(JSON.parse(capture.text), { password: '[redacted]', value: 'retained' });
        assert.equal(capture.complete, true);
    } finally {
        service.close();
        await new Promise((resolve) => server.close(resolve));
    }
    const replacement = new ProtocolClient(new HostInfo(protocol.manager));
    replacement.setNetworkDebug(true);
    c.close();
    replacement.network.diagnostics.io('read', 1);
    assert.equal(replacement.drain().length, 1);
    replacement.close();
}
await loggingCompletionAndIsolation();
await loggingNetworkContract();

for (const host of [
    new HostInfo(),
    new HostInfo({ streamDockManager: { version: 1, capabilities: ['plugin-protocol-v1'] } }),
]) {
    const client = new Client(host);
    for (const prefix of ['', 'é😀', 'data:image/png;base64,']) {
        const base = new TextEncoder().encode(
            JSON.stringify({ event: 'setImage', context: 'a', payload: { image: prefix } }),
        ).length;
        for (const delta of [-1, 0, 1]) {
            const image = prefix + 'x'.repeat(MAX_MESSAGE - base + delta);
            if (delta > 0) assert.throws(() => client.send('setImage', 'a', { image }), /1048576/);
            else {
                client.send('setImage', 'a', { image });
                assert.equal(
                    new TextEncoder().encode(JSON.stringify(client.drain()[0])).length,
                    MAX_MESSAGE + delta,
                );
            }
        }
    }
    await client.close();
}

{
    const client = new Client();
    const base = new TextEncoder().encode(
        JSON.stringify({ event: 'setTitle', context: 'a', payload: { title: 'é' } }),
    ).length;
    const payload = { title: 'é' + 'x'.repeat(MAX_MESSAGE - base) };
    for (let i = 0; i < 8; i++) client.send('setTitle', 'a', payload);
    assert.throws(() => client.send('setTitle', 'a', {}), /queue/);
    client.logStream('stdout', 'independent');
    assert.equal(client.drain(MAX_MESSAGE).length, 1);
    client.send('setTitle', 'a', payload);
    assert.equal(client.drain().filter((x) => x.event === 'setTitle').length, 8);
    for (let i = 0; i < 128; i++) {
        try {
            client.logStream('stdout', 'x'.repeat(16384));
        } catch {}
    }
    client.send('setTitle', 'a', { title: 'logs retain normal capacity' });
    assert.equal(client.drain()[0].event, 'setTitle');
    await client.close();
}
await import('./transport.mjs');

// refreshPolicyAndIndependentDeadlines: identical fixture and behavior on Node.js
for (const c of fixture.refresh_policy.cases) {
    if (c.valid)
        assert.equal(resolveInterval(c.settings, fixture.refresh_policy.policy), c.expected_ms);
    else assert.throws(() => resolveInterval(c.settings, fixture.refresh_policy.policy));
}
{
    const r = new Resources();
    for (const [id, interval] of [
        ['fast', 1000],
        ['slow', 10000],
        ['off', 0],
    ])
        r.watch(id, 'shared', interval);
    r.complete('shared', r.begin('shared'), 1, undefined, 100000);
    r.complete('shared', r.begin('shared'), 2, undefined, 101000);
    assert.deepEqual(
        ['fast', 'slow', 'off'].map((id) => r.snapshot(id).lastValue),
        [2, 1, 1],
    );
    r.unwatch('fast');
    r.unwatch('slow');
    assert.deepEqual(r.due(3700000), []);
    r.states.get('shared').next = 0;
    r.complete('shared', r.begin('shared'), 3, undefined, 3700000);
    assert.equal(r.snapshot('off').lastValue, 3);
    r.states.get('shared').next = 0;
    r.watch('off', 'shared', 0);
    assert.deepEqual(r.due(3700000), ['shared']);
    r.complete('shared', r.begin('shared'), undefined, Error('cooldown'), 3700000, 0, 7300000);
    r.states.get('shared').next = 0;
    r.watch('new', 'shared', 1000);
    assert.deepEqual(r.due(3700000), []);
    assert.equal(r.deadline('shared'), 7300000);
    r.complete('shared', r.begin('shared'), 4, undefined, 7300000, 0, 7360000);
    r.states.get('shared').next = 0;
    assert.deepEqual(r.due(7300000), []);
}

await extensionsConformance();

for (const test of protocol.negotiation) {
    const host = new HostInfo(test.host);
    assert.equal(host.supports('plugin-protocol-v1'), test.supported, test.name);
    const client = new ProtocolClient(host);
    client.send('setTitle', 'context', { title: 'base' });
    client.send('setFeedback', 'context', { value: 1 });
    await client.close();
}

{
    const root = await mkdtemp(join(tmpdir(), 'sdk-current-state-'));
    try {
        const path = join(root, 'state.json');
        for (const raw of [
            '{"count":1}',
            '{"version":0,"data":{}}',
            '{"version":2,"data":{}}',
            '{"version":1}',
            '{',
        ]) {
            await writeFile(path, raw);
            const store = new Store(path);
            store.value = { previous: true };
            await assert.rejects(store.load());
            assert.deepEqual(store.value, { previous: true });
            store.change({ changed: true });
            await assert.rejects(store.close());
            assert.equal(await readFile(path, 'utf8'), raw);
        }
    } finally {
        await rm(root, { recursive: true, force: true });
    }
}

// Current Elgato core events/ui.ts declares inspector messages as JsonValue
{
    const { parseEvent } = await import('../dist/src/protocol.js');
    for (const payload of [null, [1, { unknown: true }], 'text', false, 0]) {
        const event = parseEvent(
            JSON.stringify({ event: 'sendToPlugin', context: 'action', payload }),
        );
        assert.deepEqual(event.payload, payload);
    }
}

// requiredTelemetryObserver: explicit observer flag, including polling=false
{
    const { parseEvent } = await import('../dist/src/protocol.js');
    const fixture = JSON.parse(
        await readFile(
            new URL('../../contracts/fixtures/protocol-v1.json', import.meta.url),
            'utf8',
        ),
    );
    for (const test of fixture.telemetry) {
        const event = { event: 'didReceiveTelemetry', payload: test.payload };
        if (test.valid)
            assert.equal(parseEvent(event).payload.audio_observer, test.payload.audio_observer);
        else assert.throws(() => parseEvent(event), undefined, test.name);
    }
}

// stateVersionBoundaries: runtime JS can mutate readonly TS fields; each I/O boundary checks v1
{
    const root = await mkdtemp(join(tmpdir(), 'sdk-state-version-'));
    try {
        for (const version of [0, 2, -1, 2 ** 32, true, 1.5]) {
            for (const exists of [false, true]) {
                const path = join(root, 'state.json');
                await rm(path, { force: true });
                const raw = '{"version":1,"data":{"count":7}}';
                if (exists) await writeFile(path, raw);
                // Provide unchanged behavior for this conformance fixture
                const unchanged = async () => {
                    if (exists) assert.equal(await readFile(path, 'utf8'), raw);
                    else await assert.rejects(stat(path), { code: 'ENOENT' });
                };
                assert.throws(() => new Store(path, version), /version/);
                for (const dirty of [false, true])
                    for (const force of [false, true]) {
                        const store = new Store(path, 1, () => 0);
                        store.version = version;
                        if (dirty) store.change({ count: 8 });
                        await assert.rejects(store.flush(force), /version/);
                        assert.equal(store.dirty, dirty);
                        await assert.rejects(store.close(), /version/);
                        await unchanged();
                    }
                const store = new Store(path);
                store.version = version;
                store.value = { count: 99 };
                await assert.rejects(store.load(), /version/);
                assert.deepEqual(store.value, { count: 99 });
                store.version = 1;
                store.change({ count: 7 });
                await assert.rejects(store.close(), /protected/);
                await unchanged();
                await store.load();
                store.change({ count: 7 });
                await store.close();
                assert.deepEqual(await new Store(path).load(), { count: 7 });
            }
        }
    } finally {
        await rm(root, { recursive: true, force: true });
    }
}

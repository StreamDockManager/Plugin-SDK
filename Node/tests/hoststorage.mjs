import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { Client, connect } from '../dist/src/runtime.js';
import { HostInfo, UnsupportedFeatureError, parseEvent } from '../dist/src/protocol.js';
import {
    HostStore,
    PortableSettings,
    RPCError,
    RPC_RESPONSE,
    STORAGE_PROTOCOL,
    SETTINGS_PROTOCOL,
} from '../dist/src/hoststorage.js';

// Exercise host storage conformance against the SDK contract
export async function hostStorageConformance() {
    const fixture = JSON.parse(
        await readFile(
            new URL('../../contracts/fixtures/storage-v1.json', import.meta.url),
            'utf8',
        ),
    );
    for (const metadata of fixture.malformed_manager)
        assert.equal(
            new HostInfo({ streamDockManager: metadata }).supports(STORAGE_PROTOCOL),
            false,
        );
    const base = new Client(new HostInfo(), 'opaque-session-token');
    base.getSettings('owned-context');
    base.settings('owned-context', fixture.value);
    base.getGlobalSettings();
    base.globalSettings(fixture.value);
    assert.deepEqual(base.drain(), fixture.base_requests);
    assert.equal(base.requestVsdCraftSettings(), undefined);
    assert.deepEqual(base.drain(), [{ event: 'getSettings', context: 'opaque-session-token' }]);
    for (const identity of ['', '  ']) {
        const unregistered = new Client(new HostInfo(), identity);
        assert.throws(() => unregistered.requestVsdCraftSettings(), /registration UUID/);
        assert.deepEqual(unregistered.drain(), []);
    }
    assert.throws(() => new HostStore(base, 'authoring'), UnsupportedFeatureError);
    const portable = new PortableSettings(base, 'action', 'owned-context');
    base.outputReady = () => {
        const [request] = base.drain();
        if (request.event === 'getSettings') {
            assert.deepEqual(request, { event: 'getSettings', context: 'owned-context' });
            assert.equal(
                base.control.observe({
                    event: 'didReceiveSettings',
                    context: 'owned-context',
                    payload: { settings: fixture.value },
                }),
                false,
            );
        }
    };
    assert.deepEqual(await portable.read(), {
        settings: fixture.value,
        backend: 'base-vsd',
        assurance: 'observed',
    });
    assert.equal((await portable.replace(fixture.value)).assurance, 'sent');
    for (const payload of fixture.invalid_base_payloads) {
        base.outputReady = () => {
            base.drain();
            base.control.observe(
                parseEvent(
                    JSON.stringify({
                        event: 'didReceiveSettings',
                        context: 'owned-context',
                        payload,
                    }),
                ),
            );
        };
        await assert.rejects(portable.read(), (error) => error.code === 'invalid_response');
    }
    const client = new Client(
        new HostInfo({
            streamDockManager: {
                contract_version: 1,
                capabilities: [STORAGE_PROTOCOL, SETTINGS_PROTOCOL],
            },
        }),
    );
    client.outputReady = () => {
        const [request] = client.drain();
        assert.equal(request.protocol_id, STORAGE_PROTOCOL);
        const result = { generation: fixture.generation, revision: '0' };
        if (request.method === 'get') {
            assert.deepEqual(request.payload, { namespace: 'authoring', key: fixture.key });
            Object.assign(result, { found: true, value: fixture.value });
        } else {
            assert.deepEqual(request.payload.mutations, [{ op: 'put', key: 'null', value: null }]);
            Object.assign(result, {
                committed: true,
                revision: '1',
                operation_id: fixture.operation_id,
            });
        }
        client.control.observe({
            event: RPC_RESPONSE,
            protocol_id: STORAGE_PROTOCOL,
            request_id: request.request_id,
            result,
        });
    };
    const store = new HostStore(client, 'authoring');
    const value = await store.get(fixture.key);
    assert.deepEqual(value.value, fixture.value);
    assert.equal(value.generation, fixture.generation);
    assert.equal(
        (await store.commit(value, fixture.operation_id, [{ op: 'put', key: 'null', value: null }]))
            .committed,
        true,
    );
    // Provide malformed behavior for this hoststorage fixture
    const malformed = (field, value) => {
        client.outputReady = () => {
            const [request] = client.drain();
            client.control.observe({
                event: RPC_RESPONSE,
                protocol_id: request.protocol_id,
                request_id: request.request_id,
                [field]: value,
            });
        };
    };
    for (const value of fixture.invalid_rpc_errors) {
        malformed('error', value);
        await assert.rejects(store.get('key'), (error) => error.code === 'invalid_response');
    }
    for (const revision of fixture.invalid_receipt_revisions) {
        malformed('result', {
            found: false,
            generation: fixture.generation,
            revision,
            operation_id: fixture.operation_id,
        });
        await assert.rejects(
            store.receipt(fixture.generation, fixture.operation_id),
            (error) => error.code === 'invalid_response',
        );
    }
    client.outputReady = () => {
        const [request] = client.drain();
        client.control.observe({
            event: RPC_RESPONSE,
            protocol_id: request.protocol_id,
            request_id: request.request_id,
            error: { code: 'storage_failure', message: 'storage unavailable' },
        });
    };
    await assert.rejects(
        new PortableSettings(client, 'global').replace({ secret: 'not logged' }),
        (error) => error instanceof RPCError && error.code === 'storage_failure',
    );
    assert.deepEqual(client.drain(), []);
    client.outputReady = () => {};
    const controller = new AbortController();
    const pending = store.get('cancelled', controller.signal);
    controller.abort();
    await assert.rejects(
        pending,
        (error) => error instanceof RPCError && error.code === 'indeterminate',
    );
    client.drain();
    const waits = Array.from({ length: 8 }, (_, i) => store.get(`pending-${i}`));
    await assert.rejects(
        store.get('overflow'),
        (error) => error instanceof RPCError && error.code === 'busy',
    );
    client.close();
    await Promise.all(
        waits.map((wait) => assert.rejects(wait, (error) => error.code === 'indeterminate')),
    );
    const NativeWebSocket = globalThis.WebSocket;
    class Socket extends EventTarget {
        static OPEN = 1;
        readyState = 1;
        bufferedAmount = 0;
        // Initialize Socket and its owned state
        constructor() {
            super();
            queueMicrotask(() => this.dispatchEvent(new Event('open')));
        }
        // Provide emit behavior for this hoststorage fixture
        emit(value) {
            queueMicrotask(() =>
                this.dispatchEvent(new MessageEvent('message', { data: JSON.stringify(value) })),
            );
        }
        // Provide send behavior for this hoststorage fixture
        send(raw) {
            const event = JSON.parse(raw);
            if (event.event === 'registerPlugin') this.emit({ event: 'keyDown', context: 'a' });
            else if (event.event === 'streamDockManagerRpcRequest') {
                this.emit({ event: 'keyUp', context: 'a' });
                this.emit({
                    event: RPC_RESPONSE,
                    protocol_id: STORAGE_PROTOCOL,
                    request_id: event.request_id,
                    result: { found: false, generation: '1', revision: '0' },
                });
            } else if (event.event === 'setTitle') this.emit({ event: 'shutdown' });
        }
        // Provide close behavior for this hoststorage fixture
        close() {
            if (this.readyState === 3) return;
            this.readyState = 3;
            queueMicrotask(() => this.dispatchEvent(new Event('close')));
        }
    }
    let active = false;
    const handled = [];
    globalThis.WebSocket = Socket;
    try {
        await connect(
            {
                // Provide next poll behavior for this hoststorage fixture
                nextPoll: () => undefined,
                // Provide should log action behavior for this hoststorage fixture
                shouldLogAction: () => false,
                // Provide handle behavior for this hoststorage fixture
                async handle(client, event) {
                    assert.equal(active, false, 'user handler reentered during RPC wait');
                    handled.push(event.event);
                    if (event.event === 'keyDown') {
                        active = true;
                        assert.equal(
                            (await new HostStore(client, 'fixture').get('missing')).found,
                            false,
                        );
                        active = false;
                        client.title('a', 'done');
                    }
                },
            },
            {
                port: 1234,
                identity: 'opaque',
                host: new HostInfo({
                    streamDockManager: { contract_version: 1, capabilities: [STORAGE_PROTOCOL] },
                }),
            },
        );
        assert.deepEqual(handled, ['keyDown', 'keyUp']);
    } finally {
        globalThis.WebSocket = NativeWebSocket;
    }
}

if (import.meta.main) await hostStorageConformance();

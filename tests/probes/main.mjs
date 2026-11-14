import { Service } from 'stream-dock-manager-plugin-sdk/httpclient';
import { THEME_EVENT } from 'stream-dock-manager-plugin-sdk';
import { shouldLogAction } from 'stream-dock-manager-plugin-sdk/runtime';
import { MAX_MESSAGE, fresh } from 'stream-dock-manager-plugin-sdk/protocol';
import { displayLayout } from 'stream-dock-manager-plugin-sdk/authoring';
import { HostStore, PortableSettings, RPCError } from 'stream-dock-manager-plugin-sdk/hoststorage';
export class Probe {
    binding = '';
    // Include key releases alongside standard runtime events in probe logs
    shouldLogAction(event) {
        return event.event === 'keyUp' || shouldLogAction(event);
    }
    // Exercise the host capabilities selected by each fixture event
    async handle(client, event) {
        const phase = event.payload?.settings?.storage_phase;
        if (phase) {
            if (event.event === 'willAppear') {
                this.binding = event.context;
                client.title(event.context, 'storage ready');
            } else if (event.event === 'keyUp') {
                await storageProbe(client, event.context, phase);
                client.title(event.context, 'storage ' + phase + ' passed');
            }
            return;
        }
        if (event.event === 'keyDown' && event.payload?.settings?.fixture_message_limit) {
            const prefix = 'é😀',
                base = new TextEncoder().encode(
                    JSON.stringify({
                        event: 'setFeedback',
                        context: event.context,
                        payload: { boundary: prefix },
                    }),
                ).length;
            for (const delta of [-1, 0, 1]) {
                let rejected = false;
                try {
                    client.send('setFeedback', event.context, {
                        boundary: prefix + 'x'.repeat(MAX_MESSAGE - base + delta),
                    });
                } catch (error) {
                    if (delta !== 1 || !String(error).includes('1048576')) throw error;
                    rejected = true;
                }
                if (delta === 1 && !rejected) throw Error('missing local message-limit rejection');
            }
            client.feedback(event.context, { boundary: null });
            client.title(event.context, 'message-limit:ok');
        } else if (event.event === 'keyDown' && event.payload?.settings?.fixture_endpoint) {
            const service = new Service(1024, 2000, client.network);
            try {
                await service.do('GET', event.payload.settings.fixture_endpoint, {
                    Authorization: 'Bearer fixture-private',
                });
            } finally {
                service.close();
            }
            client.logStream('stdout', 'SDK network request finished');
            client.title(event.context, 'network:done');
        } else if (event.event === 'willAppear') {
            client.logRedactor = (text) => text.replaceAll('fixture-private', '[redacted]');
            client.logStream('stdout', 'SDK stdout fixture-private');
            client.logStream('stderr', 'SDK stderr fixture-private');
            if (displayLayout().id !== 'plugin-display') throw Error('Packaged SDK layout missing');
            this.binding = event.context;
            client.settings(event.context, event.payload?.settings ?? {});
            client.globalSettings({ fixture: 'saved' });
            client.subscribeTelemetry(['cpu']);
        } else if (
            event.event === 'didReceiveGlobalSettings' &&
            this.binding &&
            event.payload?.settings?.fixture === 'saved'
        )
            client.title(this.binding, 'global:' + event.payload.settings.fixture);
        else if (event.event === THEME_EVENT && this.binding)
            client.title(this.binding, 'theme:' + event.payload.resolved);
        else if (event.event === 'didReceiveTelemetry') {
            const measurement = event.payload.cpu;
            if (this.binding && fresh(measurement, Date.now()))
                client.title(this.binding, 'telemetry:' + measurement.value);
        } else if (event.event === 'dialRotate')
            client.title(event.context, 'dial:' + event.payload.ticks);
        else if (event.event === 'touchTap') client.title(event.context, 'touch');
        else if (event.event === 'keyUp')
            client.desktopInput(event.context, {
                op: 'copy',
                request_id: event.streamDockManager.request_id + '-input',
            });
        else if (event.event === 'willDisappear') this.binding = '';
    }
}

// Verify private storage replay, conflicts and portable settings across restart phases
async function storageProbe(client, binding, phase) {
    // Fail the storage scenario with a precise contract assertion
    const check = (condition, message) => {
        if (!condition) throw Error(message);
    };
    // Compare nested fixture values without depending on object key order
    const equal = (a, b) => {
        if (a === b) return true;
        if (
            !a ||
            !b ||
            typeof a !== 'object' ||
            typeof b !== 'object' ||
            Array.isArray(a) !== Array.isArray(b)
        )
            return false;
        return (
            Object.keys(a).length === Object.keys(b).length &&
            Object.keys(a).every((key) => Object.hasOwn(b, key) && equal(a[key], b[key]))
        );
    };
    const operation = '12345678-1234-4234-8234-123456789abc';
    const expected = {
        unicode: 'é😀',
        unknown: { keep: true },
        false: false,
        zero: 0,
        null: null,
        empty: [],
        object: {},
    };
    const store = new HostStore(client, 'sdk-storage-v1');
    const current = await store.get('value');
    if (phase === 'write') {
        check(!current.found, 'new private value already exists');
        const mutations = [
            { op: 'put', key: 'value', value: expected },
            { op: 'put', key: 'null', value: null },
        ];
        const committed = await store.commit(current, operation, mutations);
        check(
            (await store.commit(current, operation, mutations)).revision === committed.revision,
            'idempotent commit mismatch',
        );
        let conflicted = false;
        try {
            await store.commit(current, '12345678-1234-4234-8234-123456789abd', mutations);
        } catch (error) {
            if (!(error instanceof RPCError) || error.code !== 'conflict') throw error;
            conflicted = true;
        }
        check(conflicted, 'stale revision accepted');
        for (const scope of ['action', 'global']) {
            const settings = new PortableSettings(client, scope, binding);
            const before = (await settings.read()).settings;
            await settings.update({ sdk_storage_probe: 'saved' });
            const after = (await settings.read()).settings;
            check(
                Object.keys(before).every(
                    (key) => Object.hasOwn(after, key) && equal(before[key], after[key]),
                ),
                'portable settings lost fields',
            );
            check(after.sdk_storage_probe === 'saved', 'portable update not observed');
        }
    } else if (phase === 'delete') {
        check(current.found, 'private value missing before delete');
        await store.commit(current, '12345678-1234-4234-8234-123456789abe', [
            { op: 'delete', key: 'value' },
            { op: 'delete', key: 'null' },
        ]);
        check(!(await store.get('value')).found, 'delete failed');
        return;
    } else if (phase !== 'read') throw Error('unknown storage phase');
    const value = await store.get('value');
    check(value.found && equal(value.value, expected), 'private value did not round trip');
    const nullValue = await store.get('null');
    check(nullValue.found && nullValue.value === null, 'null was treated as missing');
    check(!(await store.get('missing')).found, 'missing value appeared');
    check(equal((await store.listKeys()).keys, ['null', 'value']), 'list keys mismatch');
    const receipt = await store.receipt(value.generation, operation);
    check(receipt.found && receipt.committed, 'durable receipt missing');
}

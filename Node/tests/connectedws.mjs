import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { createInterface } from 'node:readline';
import { ServiceClient, Rejected, commandError } from '../dist/src/connectedws.js';
import { fileURLToPath } from 'node:url';
// Exercise service peer against the SDK contract
export async function servicePeer(secure = false) {
    const process_ = spawn(
        process.execPath,
        [
            fileURLToPath(new URL('../../tests/service-peer.mjs', import.meta.url)),
            ...(secure ? ['--tls'] : []),
        ],
        { stdio: ['pipe', 'pipe', 'inherit'] },
    );
    const lines = createInterface({ input: process_.stdout });
    const endpoint = await new Promise((resolve, reject) => {
        const timeout = setTimeout(() => {
            process_.kill();
            reject(Error('peer startup timed out'));
        }, 5000);
        lines.once('line', (line) => {
            clearTimeout(timeout);
            resolve(line);
        });
        process_.once('exit', (code) => {
            clearTimeout(timeout);
            reject(Error('peer exited ' + code));
        });
    });
    return {
        endpoint,
        // Retire connection work and release pending requests
        close: async () => {
            process_.stdin.end();
            await new Promise((resolve) => process_.once('exit', resolve));
            lines.close();
        },
    };
}
// Exercise connected sessions conformance against the SDK contract
export async function connectedSessionsConformance() {
    const peer = await servicePeer(),
        client = new ServiceClient();
    let auth = 0,
        waiting;
    // Provide handshake behavior for this connectedws fixture
    const handshake = async (channel) => {
        auth++;
        assert.deepEqual(await channel.receive(), { hello: true });
        await channel.send({ auth: 'fixture' });
        assert.deepEqual(await channel.receive(), { ready: true });
    };
    // Provide route behavior for this connectedws fixture
    const route = (value) => {
        if (value.event === 'waiting') waiting?.();
        return value.id;
    };
    try {
        const first = await client.connect('same', peer.endpoint, handshake, route);
        assert(first.created);
        const second = await client.connect('same', peer.endpoint, handshake, route);
        assert(!second.created);
        assert.equal(first.session, second.session);
        assert.equal(auth, 1);
        const session = first.session;
        const cancelled = new AbortController();
        cancelled.abort();
        await assert.rejects(
            client.connect('same', peer.endpoint, handshake, route, cancelled.signal),
        );
        await assert.rejects(client.connect('invalid', 'https://invalid', handshake, route));
        assert(session.alive);
        const responses = await Promise.all(
            ['one', 'two'].map((id) => session.request(id, { id })),
        );
        assert.deepEqual(
            responses.map((v) => v.id),
            ['one', 'two'],
        );
        const stop = new AbortController(),
            observed = new Promise((resolve) => (waiting = resolve));
        const pending = session.request('wait', { id: 'wait' }, stop.signal);
        await observed;
        await assert.rejects(session.request('wait', { id: 'wait' }), /Duplicate/);
        stop.abort();
        await assert.rejects(pending);
        assert(session.alive);
        await assert.rejects(
            session.request('oversized', { id: 'oversized', value: 'x'.repeat(65536) }),
            /oversized/,
        );
        // Invalid admission must not drop other service users
        assert(session.alive);
        assert.equal((await session.request('echo', { id: 'echo' })).id, 'echo');
        await assert.rejects(session.request('large', { id: 'large' }));
        assert(!session.alive);
        await client.connect('again', peer.endpoint, handshake, route);
        client.close();
        const opening = client.connect(
            'silent',
            peer.endpoint + '/silent',
            async (channel) => {
                await channel.receive();
            },
            route,
        );
        await new Promise((resolve) => setTimeout(resolve, 25));
        client.close();
        await assert.rejects(opening);
        const reconnected = await client.connect('final', peer.endpoint, handshake, route);
        assert(reconnected.created);
        assert(client.network.sent > 0 && client.network.received > 0);
        assert.equal(
            commandError(new Rejected('command')).message,
            'command was rejected; check permissions and the selected resource',
        );
        assert.match(commandError(Error('transport')).message, /uncertain/);
        await assert.rejects(
            client.connect('bad', peer.endpoint + '?secret=value', handshake, route),
            /without credentials/,
        );
    } finally {
        client.close();
        await peer.close();
    }
    const secure = await servicePeer(true),
        tlsClient = new ServiceClient();
    try {
        await assert.rejects(
            tlsClient.connect(
                'tls',
                secure.endpoint,
                () => assert.fail('untrusted TLS reached authentication'),
                () => undefined,
            ),
        );
    } finally {
        tlsClient.close();
        await secure.close();
    }
}

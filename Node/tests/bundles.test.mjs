import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, writeFile, readFile, rm, mkdir } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { resolve, join } from 'node:path';
import { execFileSync } from 'node:child_process';
import { createRequire } from 'node:module';
import { bundlePlugin } from '../tools/bundle.mjs';
const sdkRoot = resolve(import.meta.dirname, '..');
const budgets = JSON.parse(
    await readFile(new URL('./bundle-budgets.json', import.meta.url), 'utf8'),
);
// Allowances describe reviewed functionality against a specific historical
// baseline. Updating a baseline requires reviewing/removing its allowances
const allowances = Object.values(budgets.reviewed_allowances ?? {});
for (const allowance of allowances) {
    assert.equal(allowance.variant, 'full');
    assert.equal(allowance.metric, 'gzip_bytes');
    assert(Number.isSafeInteger(allowance.bytes) && allowance.bytes > 0);
    assert.equal(allowance.measured_after - allowance.measured_before, allowance.bytes);
    assert(allowance.reason && allowance.runtimes.length > 0);
    assert.equal(new Set(allowance.runtimes).size, allowance.runtimes.length);
    for (const runtime of allowance.runtimes)
        assert.equal(budgets[runtime]?.[allowance.variant]?.[allowance.metric], allowance.baseline);
}
for (const runtime of ['nodejs'])
    test(
        `tree shaking ${runtime}`,
        // Handle test work for bundles.test
        async () => {
            const project = await mkdtemp(join(tmpdir(), 'sdm-bundle-'));
            try {
                await writeFile(
                    join(project, 'package.json'),
                    '{"type":"module","name":"fixture","version":"0.1.0"}',
                );
                const variants = {
                    minimal: `import {Client} from 'stream-dock-manager-plugin-sdk';const client=new Client();client.title('a','hello');console.log(JSON.stringify(client.drain()));`,
                    counter: `import {Client} from 'stream-dock-manager-plugin-sdk';import {Plugin} from 'stream-dock-manager-plugin-sdk/authoring';const p=new Plugin();p.register('count',{render:()=>({title:'Count 0'})});const c=new Client();await p.handle(c,{event:'willAppear',context:'a',action:'count',payload:{settings:{}}});console.log(JSON.stringify(c.drain()));`,
                    http: `import {Connector} from 'stream-dock-manager-plugin-sdk/connectors';import {Service} from 'stream-dock-manager-plugin-sdk/httpclient';console.log(typeof Connector,typeof Service);`,
                    storage: `import {Store} from 'stream-dock-manager-plugin-sdk/storage';console.log(typeof Store);`,
                    hoststorage: `import {Client} from 'stream-dock-manager-plugin-sdk';import {PortableSettings,HostStore} from 'stream-dock-manager-plugin-sdk/hoststorage';const c=new Client(),s=new PortableSettings(c,'action','a');console.log((await s.replace({unknown:null})).assurance,typeof HostStore);`,
                    worker: `import {WorkerPool} from 'stream-dock-manager-plugin-sdk/workers';const pool=new WorkerPool();try{console.log(await pool.submit(new URL('./worker.mjs',import.meta.url),{value:21}));}finally{pool.close();}`,
                    full: `import {Client} from 'stream-dock-manager-plugin-sdk';import {Plugin} from 'stream-dock-manager-plugin-sdk/authoring';import {Connector} from 'stream-dock-manager-plugin-sdk/connectors';import {Service,request} from 'stream-dock-manager-plugin-sdk/httpclient';import {Store} from 'stream-dock-manager-plugin-sdk/storage';import {runProcess} from 'stream-dock-manager-plugin-sdk/process';import {WorkerPool} from 'stream-dock-manager-plugin-sdk/workers';import {Recorder,serve,tls} from 'stream-dock-manager-plugin-sdk/testkit';import {secret,safeError} from 'stream-dock-manager-plugin-sdk/credentials';import {fingerprint,cached,remember,parallel} from 'stream-dock-manager-plugin-sdk/packaging';import {Resources} from 'stream-dock-manager-plugin-sdk/polling';import {validate} from 'stream-dock-manager-plugin-sdk/settings';import {assets} from 'stream-dock-manager-plugin-sdk/inspectors';console.log([Client,Plugin,Connector,Service,request,Store,runProcess,WorkerPool,Recorder,serve,tls,secret,safeError,fingerprint,cached,remember,parallel,Resources,validate].map(value=>typeof value),Object.keys(assets()).length);`,
                };
                const executable = process.execPath;
                // Provide execute behavior for this bundles fixture
                const execute = (path) => execFileSync(executable, [path], { encoding: 'utf8' });
                await writeFile(
                    join(project, 'worker.mjs'),
                    `export async function run(input){if(input.delay)await new Promise(resolve=>setTimeout(resolve,input.delay));return input.value*2;}`,
                );
                const sizes = {},
                    reports = {};
                for (const [name, source] of Object.entries(variants)) {
                    await writeFile(join(project, 'main.mjs'), source);
                    const report = await bundlePlugin({
                        project,
                        runtime,
                        sdkRoot,
                        workers: name === 'worker' ? ['worker.mjs'] : [],
                        outdir: join(project, 'build', name),
                    });
                    if (name === 'minimal') {
                        const warm = await bundlePlugin({
                            project,
                            runtime,
                            sdkRoot,
                            outdir: join(project, 'build', name),
                        });
                        assert(warm.cache_hit);
                        await writeFile(join(project, 'NOTICE'), 'Fixture notice');
                        const notice = await bundlePlugin({
                            project,
                            runtime,
                            sdkRoot,
                            outdir: join(project, 'build', name),
                        });
                        assert(!notice.cache_hit);
                        assert(
                            (
                                await readFile(
                                    join(project, 'build', name, 'RUNTIME_LICENSES.txt'),
                                    'utf8',
                                )
                            ).includes('Fixture notice'),
                        );
                        await writeFile(
                            join(project, 'manifest.json'),
                            JSON.stringify({
                                StreamDockManager: { Capabilities: { version: 1, id: 'fixture' } },
                            }),
                        );
                        const framed = await bundlePlugin({
                            project,
                            runtime,
                            sdkRoot,
                            outdir: join(project, 'build', name),
                        });
                        assert(!framed.cache_hit);
                        assert(
                            (
                                await readFile(join(project, 'build', name, 'main.mjs'), 'utf8')
                            ).includes('SDMCAP1:'),
                        );
                        await rm(join(project, 'manifest.json'));
                        const unframed = await bundlePlugin({
                            project,
                            runtime,
                            sdkRoot,
                            outdir: join(project, 'build', name),
                        });
                        assert(!unframed.cache_hit);
                        await writeFile(join(project, 'build', name, 'main.mjs'), 'corrupt');
                        const repaired = await bundlePlugin({
                            project,
                            runtime,
                            sdkRoot,
                            outdir: join(project, 'build', name),
                        });
                        assert(!repaired.cache_hit);
                    }
                    const measured = { bytes: 0, gzip_bytes: 0 };
                    for (const output of Object.values(report.outputs)) {
                        measured.bytes += output.bytes;
                        measured.gzip_bytes += output.gzip_bytes;
                    }
                    sizes[name] = measured.bytes;
                    const emitted = Object.entries(report.metafile.outputs).flatMap(([, value]) =>
                        Object.entries(value.inputs)
                            .filter(([, input]) => input.bytesInOutput > 0)
                            .map(([key]) => key),
                    );
                    reports[name] = {
                        ...measured,
                        modules: emitted,
                        outputs: report.outputs,
                        settings: report.settings,
                    };
                    if (name !== 'worker')
                        assert(
                            !Object.keys(report.outputs).some((path) =>
                                path.endsWith('worker.mjs'),
                            ),
                        );
                    if (['minimal', 'counter'].includes(name))
                        for (const feature of [
                            'httpclient',
                            'diagnostics',
                            'storage',
                            'hoststorage',
                            'process',
                            'workers',
                            'testkit',
                            'connectors',
                            'inspector-assets',
                            'test-certificates',
                            'peers',
                        ])
                            assert(
                                !emitted.some((path) => path.endsWith('/' + feature + '.js')),
                                `${name} retains ${feature}`,
                            );
                    if (process.env.SDK_RECORD_BUDGETS) {
                        budgets[runtime][name] = measured;
                        await writeFile(
                            new URL('./bundle-budgets.json', import.meta.url),
                            JSON.stringify(budgets, null, 2) + '\n',
                        );
                    }
                    const baseline = budgets[runtime][name];
                    for (const metric of ['bytes', 'gzip_bytes']) {
                        const allowance = allowances
                            .filter(
                                (entry) =>
                                    entry.variant === name &&
                                    entry.metric === metric &&
                                    entry.runtimes.includes(runtime),
                            )
                            .reduce((total, entry) => total + entry.bytes, 0);
                        assert(
                            measured[metric] <=
                                baseline[metric] +
                                    Math.max(1024, baseline[metric] * 0.05) +
                                    allowance,
                            `${runtime}/${name}/${metric} ${measured[metric]} exceeds reviewed size budget`,
                        );
                    }
                    if (
                        [
                            'minimal',
                            'counter',
                            'http',
                            'storage',
                            'hoststorage',
                            'worker',
                            'full',
                        ].includes(name)
                    ) {
                        const release = execute(join(project, 'build', name, 'main.mjs'));
                        await bundlePlugin({
                            project,
                            runtime,
                            sdkRoot,
                            workers: name === 'worker' ? ['worker.mjs'] : [],
                            debug: true,
                            outdir: join(project, 'build', 'debug'),
                        });
                        assert.equal(execute(join(project, 'build/debug/main.mjs')), release);
                    }
                }
                await writeFile(
                    join(project, 'counter.mjs'),
                    await readFile(
                        new URL('../../examples/javascript/counter.mjs', import.meta.url),
                    ),
                );
                await writeFile(
                    join(project, 'main.mjs'),
                    `import {Client,HostInfo} from 'stream-dock-manager-plugin-sdk';import {counter} from './counter.mjs';
const p=counter(),c=new Client(new HostInfo(),'fixture');
await p.handle(c,{event:'willAppear',context:'a',action:'fixture.counter',payload:{settings:{count:-1,unknown:'keep'}}});c.drain();
await p.handle(c,{event:'keyUp',context:'a',action:'fixture.counter',payload:{}});const output=c.drain();
if(!output.some(e=>e.event==='setTitle'&&e.payload.title==='Count 1')||!output.some(e=>e.event==='setSettings'&&e.payload.unknown==='keep'))throw Error('Starter settings changed behavior');console.log('normalized');`,
                );
                await bundlePlugin({
                    project,
                    runtime,
                    sdkRoot,
                    outdir: join(project, 'build', 'starter'),
                });
                assert.equal(
                    execute(join(project, 'build', 'starter', 'main.mjs')).trim(),
                    'normalized',
                );
                await writeFile(
                    join(project, 'main.mjs'),
                    `import {WorkerPool} from 'stream-dock-manager-plugin-sdk/workers';
const pool=new WorkerPool(),url=new URL('./worker.mjs',import.meta.url);
const blockers=Array.from({length:4},()=>pool.submit(url,{value:1,delay:5000}));
const cancel=new AbortController();const queued=pool.submit(url,{value:2},cancel.signal);cancel.abort();
await queued.then(()=>{throw Error('Cancelled worker ran');},()=>{});pool.close();
await Promise.allSettled(blockers);console.log('cancelled');`,
                );
                await writeFile(
                    join(project, 'manifest.json'),
                    JSON.stringify({
                        StreamDockManager: { Capabilities: { version: 1, id: 'fixture' } },
                    }),
                );
                await bundlePlugin({
                    project,
                    runtime,
                    sdkRoot,
                    workers: ['worker.mjs'],
                    outdir: join(project, 'build', 'cancellation'),
                });
                assert(
                    (
                        await readFile(join(project, 'build', 'cancellation', 'main.mjs'), 'utf8')
                    ).includes('SDMCAP1:'),
                );
                assert(
                    !(
                        await readFile(join(project, 'build', 'cancellation', 'worker.mjs'), 'utf8')
                    ).includes('SDMCAP1:'),
                );
                assert.equal(
                    execute(join(project, 'build', 'cancellation', 'main.mjs')).trim(),
                    'cancelled',
                );
                await mkdir(join(sdkRoot, '../artifacts/tree-shaking'), { recursive: true });
                await writeFile(
                    join(sdkRoot, '../artifacts/tree-shaking', runtime + '.json'),
                    JSON.stringify(reports, null, 2) + '\n',
                );
                assert(sizes.minimal < sizes.full);
                assert(sizes.counter < sizes.full);
                for (const source of [
                    `import 'https://example.com/code.js';`,
                    `import 'missing-offline-dependency';`,
                    `await import('stream-dock-manager-plugin-sdk/'+process.argv[2]);`,
                ]) {
                    await writeFile(join(project, 'main.mjs'), source);
                    await assert.rejects(bundlePlugin({ project, runtime, sdkRoot }));
                }
                await writeFile(join(project, 'main.mjs'), 'console.log("fixture")');
                await assert.rejects(
                    bundlePlugin({ project, runtime, sdkRoot, workers: ['missing.mjs'] }),
                );
                await assert.rejects(
                    bundlePlugin({ project, runtime, sdkRoot, assets: ['missing.json'] }),
                );
                for (const adapter of ['deno', 'bun']) {
                    await writeFile(
                        join(project, 'main.mjs'),
                        `import {run} from 'stream-dock-manager-plugin-sdk/${adapter}';console.log(run);`,
                    );
                    await assert.rejects(bundlePlugin({ project, runtime, sdkRoot }));
                }
            } finally {
                await rm(project, { recursive: true, force: true });
            }
        },
    );

test('rejects retired runtime targets', async () => {
    for (const runtime of ['deno', 'bun'])
        await assert.rejects(
            bundlePlugin({ project: sdkRoot, sdkRoot, runtime }),
            /Only Node.js LTS is supported/,
        );
});

// Verify optional thumbnail worker bundles codecs, notices, and cancellation
test('optional thumbnail worker bundles codecs, notices, and cancellation', async () => {
    const project = await mkdtemp(join(tmpdir(), 'sdm-thumbnail-worker-'));
    try {
        const fixture = JSON.parse(
            await readFile(
                new URL('../../contracts/fixtures/extensions-v1.json', import.meta.url),
                'utf8',
            ),
        ).thumbnails;
        const sample = fixture[0];
        await writeFile(
            join(project, 'package.json'),
            JSON.stringify({ type: 'module', name: 'thumbnail-fixture', version: '0.1.0' }),
        );
        await writeFile(
            join(project, 'worker.mjs'),
            `import {decode} from 'stream-dock-manager-plugin-sdk/thumbnail';
import {PNG} from ${JSON.stringify(createRequire(import.meta.url).resolve('pngjs'))};
export function run({encoded,started,nativeCheckpoint}){
if(nativeCheckpoint){
    const state=new Int32Array(nativeCheckpoint), read=PNG.sync.read;
    PNG.sync.read=(...args)=>{
        Atomics.store(state,0,1);Atomics.notify(state,0);
        Atomics.wait(state,0,1);
        const result=read(...args);
        Atomics.store(state,0,3);Atomics.notify(state,0);
        return result;
    };
}
const image=decode(Buffer.from(encoded,'base64'),${sample.width},${sample.height});
if(started){const state=new Int32Array(started);
    // Cancel active JavaScript work after the PNG decoder has released its native zlib handles
    Atomics.store(state,0,1);Atomics.wait(state,0,1);
}
return {width:image.width,height:image.height,data:Array.from(image.data)};}`,
        );
        await writeFile(
            join(project, 'main.mjs'),
            `import {WorkerPool} from 'stream-dock-manager-plugin-sdk/workers';
const pool=new WorkerPool(),url=new URL('./worker.mjs',import.meta.url),encoded=${JSON.stringify(sample.data)};
try{const started=new SharedArrayBuffer(4),cancel=new AbortController();
const reason=new Error('cancel thumbnail fixture');
const paused=pool.submit(url,{encoded,started},cancel.signal);
const failed=paused.then(()=>{throw Error('Cancelled decode returned');},error=>{if(error!==reason)throw error;});
const waitFor=async(state,value)=>{const deadline=Date.now()+5000;while(Atomics.load(state,0)!==value){if(Date.now()>deadline)throw Error('Decoder worker did not reach checkpoint '+value);await new Promise(r=>setTimeout(r,5));}};
await waitFor(new Int32Array(started),1);
cancel.abort(reason);await failed;
const nativeCheckpoint=new SharedArrayBuffer(4),nativeState=new Int32Array(nativeCheckpoint);
const safeCancel=new AbortController();
const native=pool.submit(url,{encoded,nativeCheckpoint},safeCancel.signal);
const retired=native.then(()=>{throw Error('Retired PNG returned');},error=>{if(error!==reason)throw error;});
await waitFor(nativeState,1);
safeCancel.abort(reason);await retired;
if(Atomics.compareExchange(nativeState,0,1,2)!==1)throw Error('PNG exited before it was told to continue');
Atomics.notify(nativeState,0);
await waitFor(nativeState,3);
const image=await pool.submit(url,{encoded});console.log(JSON.stringify(image));}finally{pool.close();}`,
        );
        const outdir = join(project, 'build');
        await bundlePlugin({
            project,
            runtime: 'nodejs',
            sdkRoot,
            workers: ['worker.mjs'],
            outdir,
        });
        const result = JSON.parse(
            execFileSync(process.execPath, [join(outdir, 'main.mjs')], {
                encoding: 'utf8',
                timeout: 15000,
            }),
        );
        assert.deepEqual(result, {
            width: sample.width,
            height: sample.height,
            data: sample.rgba,
        });
        const notices = await readFile(join(outdir, 'RUNTIME_LICENSES.txt'), 'utf8');
        for (const dependency of ['jpeg-js', 'pngjs', 'omggif'])
            assert(notices.includes(dependency), `Missing ${dependency} notice`);
    } finally {
        await rm(project, { recursive: true, force: true });
    }
});

// Verify optional service WebSocket bundles run without node_modules
test('optional service WebSocket bundles run without node_modules', async () => {
    const { servicePeer } = await import('./connectedws.mjs');
    const peer = await servicePeer(),
        project = await mkdtemp(join(tmpdir(), 'sdm-service-bundle-'));
    try {
        await writeFile(
            join(project, 'package.json'),
            JSON.stringify({ type: 'module', name: 'service-fixture', version: '0.1.0' }),
        );
        await writeFile(
            join(project, 'main.mjs'),
            `import {ServiceClient} from 'stream-dock-manager-plugin-sdk/connectedws';
const client=new ServiceClient();try{const {session}=await client.connect('fixture',${JSON.stringify(peer.endpoint)},async channel=>{await channel.receive();await channel.send({auth:'fixture'});await channel.receive();},message=>message.id);const result=await session.request('echo',{id:'echo',value:7});console.log(JSON.stringify(result));}finally{client.close();}`,
        );
        const outdir = join(project, 'build');
        await bundlePlugin({ project, runtime: 'nodejs', sdkRoot, outdir });
        const output = JSON.parse(
            execFileSync(process.execPath, [join(outdir, 'main.mjs')], {
                encoding: 'utf8',
                timeout: 15000,
            }),
        );
        assert.equal(output.value, 7);
        assert((await readFile(join(outdir, 'RUNTIME_LICENSES.txt'), 'utf8')).includes('ws@'));
    } finally {
        await peer.close();
        await rm(project, { recursive: true, force: true });
    }
});

/// <reference lib="es2024.sharedmemory" />

import { getEnvironmentData, setEnvironmentData, Worker } from 'node:worker_threads';

const gateKey = 'app.streamdockmanager.sdk.worker-cancellation';
const runningCodec = 1;
const retired = 2;

// Snapshot a private cancellation gate into the worker without changing its input
export function createWorker(
    url: URL,
    input: unknown,
    transferList: ArrayBuffer[],
    gate: Int32Array<SharedArrayBuffer>,
): Worker {
    const previous = getEnvironmentData(gateKey);
    setEnvironmentData(gateKey, gate.buffer);
    try {
        return new Worker(url, { workerData: input, transferList });
    } finally {
        setEnvironmentData(gateKey, previous);
    }
}

// Prevent new native calls and wait only for one already in progress
export function retireAfterNativeCall(
    gate: Int32Array<SharedArrayBuffer>,
): Promise<unknown> | void {
    if ((Atomics.or(gate, 0, retired) & runningCodec) === 0) return;
    const wait = Atomics.waitAsync(gate, 0, retired | runningCodec);
    if (wait.async) return wait.value;
}

// Defer forced worker teardown while the synchronous PNG codec owns a native handle
export function protectPngCall<T>(run: () => T): T {
    const buffer = getEnvironmentData(gateKey);
    if (!(buffer instanceof SharedArrayBuffer)) return run();
    const gate = new Int32Array(buffer);
    if (Atomics.compareExchange(gate, 0, 0, runningCodec) !== 0) throw Error('Thumbnail cancelled');
    try {
        return run();
    } finally {
        Atomics.and(gate, 0, retired);
        Atomics.notify(gate, 0);
    }
}

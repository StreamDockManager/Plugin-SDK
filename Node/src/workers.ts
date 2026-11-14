import type { Worker } from 'node:worker_threads';
import { createWorker, retireAfterNativeCall } from './worker-cancellation.js';
export type WorkerFunction<Input, Result> = (input: Input) => Result | Promise<Result>;
interface Job {
    url: URL;
    input: unknown;
    finish: (error?: unknown, value?: unknown) => void;
    worker?: Worker;
    gate?: Int32Array<SharedArrayBuffer>;
}

// These buffers belong to structuredClone's snapshot, never the caller. Views,
// aliases, cycles and Map keys are visited once; shared memory remains shared
function ownedBuffers(input: unknown): ArrayBuffer[] {
    const seen = new Set<object>(),
        buffers: ArrayBuffer[] = [];
    // Validate values transferred to the worker boundary
    const visit = (value: any): void => {
        if (typeof value !== 'object' || !value) return;
        if (ArrayBuffer.isView(value)) value = value.buffer;
        if (seen.has(value)) return;
        seen.add(value);
        if (value instanceof ArrayBuffer) buffers.push(value);
        else if (value instanceof Map || value instanceof Set)
            value.forEach((item, key) => {
                visit(key);
                visit(item);
            });
        else Object.values(value).forEach(visit);
    };
    visit(input);
    return buffers;
}
/** Each job owns an isolated module worker; cancellation retires its result. */
export class WorkerPool {
    private pending: Job[] = [];
    private active = new Set<Job>();
    private closed = false;
    // Admit a bounded worker job with cancellation support
    submit<T>(url: URL, input: unknown, signal?: AbortSignal): Promise<T> {
        if (this.closed || this.pending.length >= 32)
            return Promise.reject(Error('Worker queue full or closed'));
        if (signal?.aborted) return Promise.reject(signal.reason ?? Error('Worker cancelled'));
        return new Promise((resolve, reject) => {
            let settled = false;
            // Cancel a queued or running worker job
            const abort = () => job.finish(signal?.reason ?? Error('Worker cancelled'));
            const job: Job = {
                url,
                input: structuredClone(input),
                // Settle the worker job and release its reservation
                finish: (error, value) => {
                    if (settled) return;
                    settled = true;
                    signal?.removeEventListener('abort', abort);
                    this.pending = this.pending.filter((candidate) => candidate !== job);
                    job.input = undefined;
                    if (error !== undefined) reject(error);
                    else resolve(value as T);
                    this.release(job);
                },
            };
            if (signal?.aborted) return abort();
            signal?.addEventListener('abort', abort, { once: true });
            this.pending.push(job);
            this.pump();
        });
    }
    // Release a slot only after any in-flight native codec call is safe to terminate
    private release(job: Job): void {
        const { worker, gate } = job;
        if (!worker || !gate || worker.threadId === -1) {
            this.active.delete(job);
            this.pump();
            return;
        }
        // Keep the worker counted against the pool's limit while the native call drains
        const waiting = retireAfterNativeCall(gate);
        let released = false;
        const complete = () => {
            if (released) return;
            released = true;
            worker.off('exit', complete);
            // Release the waiter if the worker exited without reaching the codec's finally block
            if (waiting) Atomics.notify(gate, 0);
            this.active.delete(job);
            if (worker.threadId !== -1) void worker.terminate();
            this.pump();
        };
        if (waiting) {
            worker.once('exit', complete);
            void waiting.then(complete);
        } else complete();
    }
    // Start queued jobs while worker capacity is available
    private pump(): void {
        while (!this.closed && this.active.size < 4 && this.pending.length) {
            const job = this.pending.shift()!;
            this.active.add(job);
            try {
                job.gate = new Int32Array(new SharedArrayBuffer(4));
                const worker = (job.worker = createWorker(
                    job.url,
                    job.input,
                    ownedBuffers(job.input),
                    job.gate,
                ));
                // The worker owns transferred buffers and cloned ordinary values
                // The parent snapshot is needed only while this job is queued
                job.input = undefined;
                worker.once('message', (value) => job.finish(undefined, value));
                worker.once('error', job.finish);
                worker.once('exit', (code) =>
                    job.finish(Error(`Worker exited before result (${code})`)),
                );
            } catch (error) {
                job.finish(error);
            }
        }
    }
    // Stop admission and retire queued worker jobs
    close(): void {
        this.closed = true;
        for (const job of [...this.pending, ...this.active])
            job.finish(Error('Worker pool closed'));
    }
}

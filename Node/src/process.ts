import { spawn } from 'node:child_process';
export interface ProcessOptions {
    timeout?: number;
    outputLimit?: number;
    signal?: AbortSignal;
    cwd?: string;
    env?: Record<string, string>;
}
/** Exact arguments, bounded output and bounded teardown; never invokes a shell. */
export async function runProcess(
    name: string,
    args: string[] = [],
    options: ProcessOptions = {},
): Promise<string> {
    options.signal?.throwIfAborted();
    const timeout = options.timeout ?? 8000,
        limit = options.outputLimit ?? 1 << 20;
    if (!Number.isFinite(timeout) || timeout <= 0 || !Number.isSafeInteger(limit) || limit < 1)
        throw Error('Invalid process limits');
    return new Promise((resolve, reject) => {
        const child = spawn(name, args, {
            shell: false,
            windowsHide: true,
            cwd: options.cwd,
            env: options.env,
            stdio: ['ignore', 'pipe', 'pipe'],
        });
        const chunks: Buffer[] = [];
        let size = 0,
            stderr = 0,
            settled = false,
            failure: unknown,
            drain: ReturnType<typeof setTimeout> | undefined;
        // Settle process completion and remove its listeners
        const finish = (error?: unknown) => {
            if (settled) return;
            settled = true;
            clearTimeout(deadline);
            if (drain) clearTimeout(drain);
            options.signal?.removeEventListener('abort', abort);
            child.stdout.destroy();
            child.stderr.destroy();
            if (error !== undefined) reject(error);
            else resolve(Buffer.concat(chunks).toString());
        };
        // Drain process output without retaining more than the byte limit
        const boundDrain = () => {
            drain ??= setTimeout(
                () => finish(failure ?? Error('Process output did not close')),
                1000,
            );
        };
        // Stop the child process when its owner retires
        const stop = (error: unknown) => {
            failure ??= error;
            child.kill('SIGKILL');
            boundDrain();
        };
        // Propagate cancellation to the child process
        const abort = () => stop(options.signal?.reason ?? Error('Process cancelled'));
        const deadline = setTimeout(() => stop(Error('Process deadline exceeded')), timeout);
        options.signal?.addEventListener('abort', abort, { once: true });
        if (options.signal?.aborted) abort();
        child.stdout.on('data', (data: Buffer) => {
            size += data.length;
            if (size > limit) stop(Error('Process response exceeds limit'));
            else chunks.push(data);
        });
        child.stderr.on('data', (data: Buffer) => {
            stderr += data.length;
            if (stderr > limit) stop(Error('Process response exceeds limit'));
        });
        child.once('error', (error) => finish(error));
        child.once('exit', () => boundDrain());
        child.once('close', (code) =>
            finish(
                failure ??
                    (code !== 0
                        ? Error(`${name} failed; check selection and operating-system permissions`)
                        : undefined),
            ),
        );
    });
}

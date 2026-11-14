import { byteLength } from './internal/bytes.js';
// Sort object keys recursively for stable build fingerprints
function canonical(value: any): any {
    return Array.isArray(value)
        ? value.map(canonical)
        : value && typeof value === 'object'
          ? Object.fromEntries(
                Object.keys(value)
                    .sort()
                    .map((key) => [key, canonical(value[key])]),
            )
          : value;
}
import { createHash } from 'node:crypto';
import { open, readFile } from 'node:fs/promises';
import { atomicWrite } from './internal/atomic-write.js';
// Hash file content without retaining the complete file
async function fileHash(path: string): Promise<string> {
    const file = await open(path, 'r');
    try {
        const hash = createHash('sha256');
        const buffer = Buffer.allocUnsafe(64 << 10);
        for (;;) {
            const { bytesRead } = await file.read(buffer, 0, buffer.length, null);
            if (!bytesRead) return hash.digest('hex');
            hash.update(buffer.subarray(0, bytesRead));
        }
    } finally {
        await file.close();
    }
}
// Hash ordered inputs and canonical build configuration
export function fingerprint(
    inputs: Record<string, Uint8Array | string>,
    configuration: unknown,
): string {
    const hash = createHash('sha256');
    hash.update(JSON.stringify(canonical(configuration)));
    for (const name of Object.keys(inputs).sort()) {
        const data = inputs[name];
        hash.update(`${name}\0${byteLength(data)}\0`);
        hash.update(data);
    }
    return hash.digest('hex');
}
// Verify the cached output still matches its recorded fingerprint
export async function cached(path: string, key: string): Promise<boolean> {
    try {
        const record = JSON.parse(await readFile(path + '.build.json', 'utf8'));
        return record.input === key && record.output === (await fileHash(path));
    } catch {
        return false;
    }
}
// Record the verified input and output fingerprints
export async function remember(path: string, key: string): Promise<void> {
    const record = {
        input: key,
        output: await fileHash(path),
    };
    return atomicWrite(path + '.build.json', JSON.stringify(record));
}
// Run work with the requested concurrency bound
export async function parallel<T>(
    items: T[],
    jobs: number,
    work: (item: T) => Promise<void>,
    signal?: AbortSignal,
): Promise<void> {
    if (!Number.isInteger(jobs) || jobs < 1 || jobs > 128)
        throw Error('Jobs must be between 1 and 128');
    let index = 0;
    const errors: unknown[] = [];
    await Promise.all(
        Array.from({ length: Math.min(jobs, items.length) }, async () => {
            while (index < items.length) {
                const item = items[index++];
                try {
                    signal?.throwIfAborted();
                    await work(item);
                } catch (error) {
                    errors.push(error);
                }
            }
        }),
    );
    if (errors.length) throw errors[0];
}

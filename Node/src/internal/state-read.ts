// Shared by the Node.js storage entry points. Do not use file size
// metadata as the read limit: an already-open file can continue to grow
import { lstat, stat } from 'node:fs/promises';
import { dirname } from 'node:path';
export const MAXIMUM_STATE_BYTES = 8 << 20;
export class StateLimitError extends Error {
    // Report that incremental state reading exceeded the byte ceiling
    constructor() {
        super('State exceeds limit');
    }
}

// Windows ENOENT can describe a blocked parent, not just an absent state file
export async function missingParent(path: string): Promise<void> {
    for (let parent = path; ; parent = dirname(parent)) {
        try {
            await lstat(parent);
        } catch (error) {
            if ((error as NodeJS.ErrnoException).code === 'ENOENT' && dirname(parent) !== parent)
                continue;
            throw error;
        }
        if (parent === path || !(await stat(parent)).isDirectory())
            throw Error('State path is unavailable');
        return;
    }
}

interface StateReader {
    // Read a chunk from the already-open state handle
    read(
        buffer: Buffer,
        offset: number,
        length: number,
        position: null,
    ): Promise<{ bytesRead: number }>;
}

// Read state incrementally while enforcing the byte ceiling
export async function readState(reader: StateReader): Promise<Buffer> {
    const chunks: Buffer[] = [];
    let total = 0;
    for (;;) {
        const chunk = Buffer.allocUnsafe(Math.min(64 << 10, MAXIMUM_STATE_BYTES + 1 - total));
        let offset = 0;
        while (offset < chunk.length) {
            const { bytesRead } = await reader.read(chunk, offset, chunk.length - offset, null);
            if (!bytesRead) {
                chunks.push(chunk.subarray(0, offset));
                return Buffer.concat(chunks, total);
            }
            total += bytesRead;
            if (total > MAXIMUM_STATE_BYTES) throw new StateLimitError();
            offset += bytesRead;
        }
        chunks.push(chunk);
    }
}

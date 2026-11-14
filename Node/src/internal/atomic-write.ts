import { open, rename, unlink } from 'node:fs/promises';
import { randomUUID } from 'node:crypto';
import { dirname, join } from 'node:path';

// The caller owns encoding and directory creation. State writes request fsync;
// disposable build-cache records retain their existing non-fsync policy
export async function atomicWrite(path: string, data: string, sync?: boolean): Promise<void> {
    const temporary = join(dirname(path), randomUUID() + '.tmp');
    try {
        const file = await open(temporary, 'wx', 0o600);
        try {
            await file.writeFile(data);
            if (sync) await file.sync();
        } finally {
            await file.close();
        }
        await rename(temporary, path);
    } finally {
        await unlink(temporary).catch(() => {});
    }
}

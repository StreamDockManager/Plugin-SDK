import { lstat, rm } from 'node:fs/promises';
const output = new URL('../dist/', import.meta.url);
try {
    if ((await lstat(output)).isSymbolicLink()) throw Error('Refusing symlink SDK output cleanup');
    await rm(output, { recursive: true, force: true });
} catch (error) {
    if (error.code !== 'ENOENT') throw error;
}

import { readFile, realpath } from 'node:fs/promises';
import { createHash } from 'node:crypto';
import path from 'node:path';
const { resolve, join } = path;

// Calculate the digest used to verify bundled output
const digest = (data) => createHash('sha256').update(data).digest('hex');

// Windows relative paths across drives can still be absolute
export function insideBundleRoot(root, candidate, pathApi = path) {
    const relative = pathApi.relative(root, candidate);
    return (
        !pathApi.isAbsolute(relative) &&
        relative !== '..' &&
        !relative.startsWith('../') &&
        !relative.startsWith('..\\')
    );
}

// Offline cache validation, also exercised by Python packaging's shared fixtures
// Input names may be absolute esbuild dependencies; output names must stay inside
export async function verifyBundle(report, { project, outdir, assetRoot, sdkRoot }) {
    if (!report.settings.treeShaking) return false;
    for (const [name, expected] of Object.entries(report.input_sha256)) {
        const source = name.startsWith('sdk:')
            ? join(sdkRoot, name.slice(4))
            : name.startsWith('asset:')
              ? join(assetRoot, name.slice(6))
              : resolve(project, name);
        try {
            if (digest(await readFile(source)) !== expected) return false;
        } catch (error) {
            if (['ENOENT', 'ENOTDIR'].includes(error.code)) {
                if (expected === null) continue;
                return false;
            }
            throw error;
        }
    }
    const root = await realpath(outdir);
    for (const [name, expected] of Object.entries(report.output_sha256)) {
        try {
            const source = await realpath(resolve(outdir, name));
            if (!insideBundleRoot(root, source)) throw Error('JavaScript output escapes bundle');
            if (digest(await readFile(source)) !== expected) return false;
        } catch (error) {
            if (['ENOENT', 'ENOTDIR'].includes(error.code)) return false;
            throw error;
        }
    }
    return true;
}

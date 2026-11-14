import { readFile, readdir, writeFile } from 'node:fs/promises';
import { createHash } from 'node:crypto';
import { join, relative } from 'node:path';
// Hash source bytes for build provenance
const hash = (data) => createHash('sha256').update(data).digest('hex');
// Enumerate the source files used by this build
async function inventory(root, directory, result = {}) {
    for (const entry of await readdir(directory, { withFileTypes: true })) {
        const path = join(directory, entry.name);
        if (entry.isSymbolicLink()) throw Error('SDK build inputs must be regular files');
        if (entry.isDirectory()) await inventory(root, path, result);
        else result[relative(root, path).replaceAll('\\', '/')] = hash(await readFile(path));
    }
    return result;
}
// Collect the current SDK source fingerprints
async function sources(root) {
    const result = {};
    for (const name of ['src', 'tools']) await inventory(root, join(root, name), result);
    for (const name of ['package.json', 'tsconfig.json', 'package-lock.json'])
        result[name] = hash(await readFile(join(root, name)));
    return result;
}
// Record the provenance of the generated SDK output
export async function recordSDKBuild(root) {
    const source = await sources(root),
        output = await inventory(root, join(root, 'dist'));
    await writeFile(
        join(root, 'dist/build-info.json'),
        JSON.stringify({ version: 1, source, output }, null, 2) + '\n',
    );
}
// Reject SDK output that no longer matches its source inputs
export async function verifySDKBuild(root) {
    const info = JSON.parse(await readFile(join(root, 'dist/build-info.json'), 'utf8'));
    let local = false;
    try {
        await readdir(join(root, 'src'));
        local = true;
    } catch (error) {
        if (error.code !== 'ENOENT') throw error;
    }
    if (local) {
        const current = await sources(root);
        if (JSON.stringify(current) !== JSON.stringify(info.source))
            throw Error(
                'SDK sources changed; run npm run build in the Node.js SDK before packaging',
            );
    }
    for (const [name, expected] of Object.entries(info.output))
        if (hash(await readFile(join(root, name))) !== expected)
            throw Error('SDK compiled output is stale or corrupt; rebuild/reinstall the SDK');
}

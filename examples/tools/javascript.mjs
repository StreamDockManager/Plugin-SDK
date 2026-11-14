import { pathToFileURL } from 'node:url';
import { join, resolve } from 'node:path';
import { readFile, access } from 'node:fs/promises';
const [project, runtime] = process.argv.slice(2);
if (!project || !runtime) throw Error('Usage: node javascript.mjs PROJECT RUNTIME');
const root = process.env.STREAM_DOCK_MANAGER_SDK_JS_ROOT;
// Prefer the explicitly selected SDK checkout when developing the starter locally
const { bundlePlugin } = await import(
    root
        ? pathToFileURL(join(root, 'tools/bundle.mjs')).href
        : 'stream-dock-manager-plugin-sdk/build'
);
// Read optional project bundling options without requiring a project descriptor
let metadata = {};
try {
    metadata =
        JSON.parse(await readFile(join(project, 'plugin-project.json'), 'utf8')).javascript ?? {};
} catch {}
const source = resolve(project);
// Use project assets when present and otherwise reuse the shared starter assets
let assetRoot = join(source, 'assets');
try {
    await access(assetRoot);
} catch {
    assetRoot = resolve(source, '../assets');
}
// Bundle the starter for the requested runtime with its resolved asset root
await bundlePlugin({
    project: source,
    runtime,
    assetRoot,
    force: process.argv.includes('--force'),
    ...metadata,
});

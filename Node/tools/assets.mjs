import { runtimeAssetSource } from './runtime-assets.mjs';
import { content } from '../dist/src/inspector-assets.js';
import { cp, mkdir, writeFile } from 'node:fs/promises';
await mkdir(new URL('../dist/web/', import.meta.url), { recursive: true });
await cp(new URL('../../web/', import.meta.url), new URL('../dist/web/', import.meta.url), {
    recursive: true,
});
// Keep embedded runtime helpers compact while preserving full comments in canonical source
const runtimeContent = Object.fromEntries(
    await Promise.all(
        Object.entries(content).map(async ([name, source]) => [
            name,
            name.endsWith('.js') ? await runtimeAssetSource(source) : source,
        ]),
    ),
);
await writeFile(
    new URL('../dist/src/inspector-assets.js', import.meta.url),
    '// Generated runtime assets; edit canonical web sources\nexport const content = ' +
        JSON.stringify(runtimeContent) +
        ';\n',
);
import { recordSDKBuild } from './build-state.mjs';
import { fileURLToPath } from 'node:url';
await recordSDKBuild(fileURLToPath(new URL('../', import.meta.url)));

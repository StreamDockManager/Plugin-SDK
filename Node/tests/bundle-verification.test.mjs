import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, writeFile, readFile, unlink, symlink, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join, posix, win32 } from 'node:path';
import { createHash } from 'node:crypto';
import { insideBundleRoot, verifyBundle } from '../tools/bundle-verify.mjs';

test('bundle roots reject paths on other Windows drives and shares', () => {
    assert(insideBundleRoot('D:/sdk', 'D:/sdk/dist/main.mjs', win32));
    assert(!insideBundleRoot('D:/sdk', 'C:/temp/plugin/main.mjs', win32));
    assert(!insideBundleRoot('D:/sdk', 'D:/sdk-other/main.mjs', win32));
    assert(!insideBundleRoot('//host/sdk', '//other/share/src.mjs', win32));
    assert(insideBundleRoot('/sdk', '/sdk/dist/main.mjs', posix));
    assert(!insideBundleRoot('/sdk', '/sdk-other/main.mjs', posix));
});

const contract = JSON.parse(
    await readFile(new URL('../../contracts/bundle-verification.v1.json', import.meta.url)),
);
for (const item of contract.cases) {
    test(
        `bundle verification: ${item.name}`,
        // Handle test work for bundle verification.test
        async () => {
            const project = await mkdtemp(join(tmpdir(), 'sdk-bundle-contract-'));
            try {
                const outdir = join(project, 'build/javascript');
                const sdkRoot = join(project, 'sdk');
                const assetRoot = join(project, 'assets');
                await Promise.all(
                    [outdir, sdkRoot, assetRoot].map((path) => mkdir(path, { recursive: true })),
                );
                const paths = {
                    input: join(project, 'main.mjs'),
                    sdk: join(sdkRoot, 'index.js'),
                    asset: join(assetRoot, 'icon.svg'),
                    output: join(outdir, 'main.mjs'),
                };
                await Promise.all(Object.values(paths).map((path) => writeFile(path, 'fixture')));
                const digest = createHash('sha256').update('fixture').digest('hex');
                const report = {
                    settings: { treeShaking: true },
                    input_sha256: {
                        'main.mjs': digest,
                        'sdk:index.js': digest,
                        'asset:icon.svg': digest,
                    },
                    output_sha256: { 'main.mjs': digest },
                };
                const mutation = item.mutation;
                if (mutation.startsWith('optional-')) {
                    report.input_sha256['optional.json'] = null;
                    if (mutation === 'optional-appeared')
                        await writeFile(join(project, 'optional.json'), 'new');
                } else if (mutation in paths) await writeFile(paths[mutation], 'changed');
                else if (mutation.startsWith('missing-')) await unlink(paths[mutation.slice(8)]);
                else if (mutation === 'tree-shaking') report.settings.treeShaking = false;
                else if (mutation === 'escape') {
                    await writeFile(join(project, 'build/escape.mjs'), 'fixture');
                    report.output_sha256 = { '../escape.mjs': digest };
                } else if (mutation === 'symlink') {
                    await unlink(paths.output);
                    await symlink(paths.input, paths.output);
                }
                let accepted;
                try {
                    accepted = await verifyBundle(report, { project, outdir, sdkRoot, assetRoot });
                } catch (error) {
                    assert.match(error.message, /JavaScript output escapes bundle/);
                    accepted = false;
                }
                assert.equal(accepted, item.accept);
            } finally {
                await rm(project, { recursive: true, force: true });
            }
        },
    );
}

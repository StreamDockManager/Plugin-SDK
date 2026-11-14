import { insideBundleRoot, verifyBundle } from './bundle-verify.mjs';
import { verifySDKBuild } from './build-state.mjs';
import { build, version as esbuildVersion } from 'esbuild';
import { readFile, writeFile, mkdir, copyFile } from 'node:fs/promises';
import { createHash } from 'node:crypto';
import { dirname, resolve, relative, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { createRequire, isBuiltin } from 'node:module';
import { gzipSync } from 'node:zlib';
// Calculate a stable digest for bundle verification
const digest = (value) => createHash('sha256').update(value).digest('hex');
// Build a portable plugin bundle and its verified metadata
export async function bundlePlugin({
    project,
    runtime,
    entry = 'main.mjs',
    workers = [],
    assets = [],
    outdir,
    debug = false,
    force = false,
    assetRoot,
    sdkRoot = process.env.STREAM_DOCK_MANAGER_SDK_JS_ROOT,
}) {
    project = resolve(project);
    outdir = resolve(outdir ?? join(project, 'build/javascript'));
    if (runtime !== 'nodejs') throw Error('Only Node.js LTS is supported');
    if (!sdkRoot) sdkRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..');
    else sdkRoot = resolve(sdkRoot);
    await verifySDKBuild(sdkRoot);
    const pkg = JSON.parse(await readFile(join(sdkRoot, 'package.json'), 'utf8'));
    const alias = Object.fromEntries(
        Object.entries(pkg.exports)
            .filter(([key]) => key !== './build')
            .map(([key, value]) => [
                'stream-dock-manager-plugin-sdk' + (key === '.' ? '' : key.slice(1)),
                resolve(sdkRoot, value.import),
            ]),
    );
    assetRoot = resolve(assetRoot ?? join(project, 'assets'));
    const assetInputs = {};
    // Validate a package-relative resource path
    const safe = (name) => {
        const path = resolve(project, name);
        if (!insideBundleRoot(project, path) || name.includes('\\') || name.includes('\0'))
            throw Error('Entry/asset escapes project');
        return path;
    };
    const entryPoints = { main: safe(entry) };
    for (const name of workers) {
        if (!/^[A-Za-z0-9_./-]+\.[cm]?[jt]s$/.test(name)) throw Error('Invalid worker path');
        const source = safe(name);
        await readFile(source); // Missing declarations fail before emitting a wrapper
        const wrapperPath = join(outdir, '.sdk-worker-entries', name.replace(/\.[^.]+$/, '.mjs'));
        await mkdir(dirname(wrapperPath), { recursive: true });
        const modulePath = relative(dirname(wrapperPath), source).replaceAll('\\', '/');
        await writeFile(
            wrapperPath,
            `import {parentPort,workerData} from 'node:worker_threads';\nimport {run} from ${JSON.stringify(modulePath.startsWith('.') ? modulePath : './' + modulePath)};\nif(!parentPort)throw Error('SDK worker entry requires a worker');\nparentPort.postMessage(await run(workerData));\n`,
        );
        entryPoints[name.replace(/\.[^.]+$/, '')] = wrapperPath;
    }
    let capabilities = null;
    try {
        capabilities =
            JSON.parse(await readFile(join(project, 'manifest.json'), 'utf8')).StreamDockManager
                ?.Capabilities ?? null;
    } catch (error) {
        if (error.code !== 'ENOENT') throw error;
    }
    const descriptor = capabilities
        ? (() => {
              const data = Buffer.from(JSON.stringify(capabilities)).toString('base64');
              if (Buffer.byteLength(JSON.stringify(capabilities)) > 65536)
                  throw Error('Descriptor exceeds limit');
              return `SDMCAP1:${data.length.toString(16).padStart(8, '0')}:${data}:ENDSDMCAP1`;
          })()
        : undefined;
    const settings = {
        runtime,
        entry,
        workers,
        assets,
        debug,
        esbuild: esbuildVersion,
        typescript: JSON.parse(
            await readFile(
                createRequire(import.meta.url).resolve('typescript/package.json'),
                'utf8',
            ),
        ).version,
        target: 'es2022',
        treeShaking: true,
        minify: !debug,
    };
    if (!force) {
        try {
            const prior = JSON.parse(await readFile(join(outdir, 'bundle-report.json'), 'utf8'));
            if (JSON.stringify(prior.settings) === JSON.stringify(settings)) {
                if (await verifyBundle(prior, { project, outdir, assetRoot, sdkRoot }))
                    return { ...prior, cache_hit: true };
            }
        } catch (error) {
            if (
                !['ENOENT', 'ENOTDIR'].includes(error.code) &&
                !(error instanceof SyntaxError) &&
                !(error instanceof TypeError)
            )
                throw error;
        }
    }
    const result = await build({
        absWorkingDir: project,
        entryPoints,
        outdir,
        bundle: true,
        define: {
            __STREAM_DOCK_MANAGER_CAPABILITIES__: JSON.stringify(capabilities),
            // Keep optional ws accelerators out of portable offline packages
            'process.env.WS_NO_BUFFER_UTIL': 'true',
            'process.env.WS_NO_UTF_8_VALIDATE': 'true',
        },
        format: 'esm',
        platform: 'node',
        target: 'es2022',
        treeShaking: true,
        minify: !debug,
        metafile: true,
        write: false,
        alias,
        logOverride: {
            'unsupported-dynamic-import': 'error',
            'unsupported-require-call': 'error',
            'ignored-dynamic-import': 'error',
            'require-resolve-not-external': 'error',
        },
        legalComments: 'eof',
        outExtension: { '.js': '.mjs' },
        plugins: [
            {
                name: 'offline-code',
                // Register the bundler hooks for dependency inspection
                setup(builder) {
                    // Optional codecs use CommonJS builtin imports. Expose only
                    // known Node builtins through static ESM adapters, so bundles
                    // need neither node_modules nor a runtime require shim
                    builder.onResolve(
                        { filter: /^[A-Za-z_][A-Za-z0-9_:/-]*$/ },
                        // Translate CommonJS builtin imports into static adapters without remote resolution
                        (args) => {
                            if (!isBuiltin(args.path)) return;
                            const path = args.path.startsWith('node:')
                                ? args.path
                                : 'node:' + args.path;
                            return args.kind === 'require-call'
                                ? { path, namespace: 'sdk-node-builtin' }
                                : { path, external: true };
                        },
                    );
                    builder.onLoad({ filter: /.*/, namespace: 'sdk-node-builtin' }, (args) => ({
                        contents: `import * as builtin from ${JSON.stringify(args.path)};module.exports=builtin.default ?? builtin;`,
                        loader: 'js',
                    }));
                    builder.onResolve({ filter: /^(https?:|npm:|jsr:|data:)/ }, (args) => ({
                        errors: [{ text: 'Remote code imports are not packaged: ' + args.path }],
                    }));
                },
            },
        ],
    });
    for (const input of Object.values(result.metafile.inputs)) {
        for (const imported of input.imports)
            if (
                imported.external &&
                !imported.path.startsWith('node:') &&
                imported.path !== '<define:__STREAM_DOCK_MANAGER_CAPABILITIES__>'
            )
                throw Error('Unresolved runtime import: ' + imported.path);
    }
    for (const output of Object.values(result.metafile.outputs))
        for (const imported of output.imports)
            if (
                imported.external &&
                !imported.path.startsWith('node:') &&
                imported.path !== '<define:__STREAM_DOCK_MANAGER_CAPABILITIES__>'
            )
                throw Error('Unresolved runtime import: ' + imported.path);
    // Only the launch entry needs a static descriptor. CPU workers must not
    // retain plugin metadata just because they share the application build
    if (descriptor) {
        const main = result.outputFiles.find(
            (output) => relative(outdir, output.path) === 'main.mjs',
        );
        if (!main) throw Error('Missing launch bundle');
        main.contents = Buffer.concat([Buffer.from('/* ' + descriptor + ' */\n'), main.contents]);
        for (const [name, output] of Object.entries(result.metafile.outputs))
            if (relative(outdir, resolve(project, name)) === 'main.mjs')
                output.bytes = main.contents.length;
    }
    await mkdir(outdir, { recursive: true });
    const files = {};
    for (const output of result.outputFiles) {
        await writeFile(output.path, output.contents);
        files[relative(outdir, output.path).replaceAll('\\', '/')] = digest(output.contents);
    }
    for (const name of assets) {
        const source = resolve(assetRoot, name);
        if (!insideBundleRoot(assetRoot, source)) throw Error('Asset escapes project');
        if (files[name] || ['bundle-report.json', 'RUNTIME_LICENSES.txt'].includes(name))
            throw Error('Declared asset collides with a bundle output: ' + name);
        assetInputs['asset:' + name] = digest(await readFile(source));
        await mkdir(dirname(join(outdir, name)), { recursive: true });
        await copyFile(source, join(outdir, name));
        files[name] = digest(await readFile(source));
    }
    // Normalize build metadata for deterministic fingerprints
    const canonical = (name) => {
        if (name.startsWith('<define:') || name.startsWith('sdk-node-builtin:')) return name;
        if (name.startsWith('asset:') || name.startsWith('sdk:')) return name;
        const full = resolve(project, name);
        return insideBundleRoot(sdkRoot, full)
            ? 'sdk:' + relative(sdkRoot, full).replaceAll('\\', '/')
            : insideBundleRoot(project, full)
              ? relative(project, full).replaceAll('\\', '/')
              : name;
    };
    const inputs = {
        ...assetInputs,
        'sdk:package.json': digest(await readFile(join(sdkRoot, 'package.json'))),
    };
    for (const name of Object.keys(result.metafile.inputs)
        .filter((name) => !name.startsWith('<define:') && !name.startsWith('sdk-node-builtin:'))
        .sort())
        inputs[name] = digest(await readFile(resolve(project, name)));
    for (const name of [
        'tools/bundle.mjs',
        'tools/bundle-verify.mjs',
        'tools/build-state.mjs',
        'package-lock.json',
    ]) {
        try {
            inputs['sdk:' + name] = digest(await readFile(join(sdkRoot, name)));
        } catch (error) {
            if (error.code !== 'ENOENT') throw error;
        }
    }
    for (const name of [
        'package.json',
        'package-lock.json',
        'dependencies.lock.json',
        'plugin-project.json',
        'manifest.json',
        'tsconfig.json',
        'jsconfig.json',
    ]) {
        try {
            inputs[name] = digest(await readFile(join(project, name)));
        } catch (error) {
            if (error.code !== 'ENOENT') throw error;
            inputs[name] = null;
        }
    }
    const notices = [],
        dependencies = [];
    const contributing = new Set(
        Object.values(result.metafile.outputs).flatMap((output) =>
            Object.entries(output.inputs)
                .filter(([, value]) => value.bytesInOutput > 0)
                .map(([name]) => name),
        ),
    );
    const roots = new Set();
    // Resolution and side-effect metadata affect the graph even when a module
    // currently contributes no emitted bytes. Notices still follow retained code
    for (const name of Object.keys(result.metafile.inputs).filter(
        (name) => !name.startsWith('<define:') && !name.startsWith('sdk-node-builtin:'),
    )) {
        let directory = dirname(resolve(project, name));
        for (let depth = 0; depth < 128; depth++) {
            try {
                const metadata = await readFile(join(directory, 'package.json'));
                inputs[relative(project, join(directory, 'package.json'))] = digest(metadata);
                if (contributing.has(name)) roots.add(directory);
                break;
            } catch (error) {
                if (error.code !== 'ENOENT') throw error;
            }
            const parent = dirname(directory);
            if (parent === directory) break;
            directory = parent;
        }
    }
    for (const root of roots) {
        let metadata;
        try {
            const data = await readFile(join(root, 'package.json'), 'utf8');
            inputs[relative(project, join(root, 'package.json'))] = digest(data);
            metadata = JSON.parse(data);
        } catch {
            continue;
        }
        let found = false;
        const licenses = {};
        for (const name of [
            'LICENSE',
            'LICENSE.md',
            'LICENSE.txt',
            'COPYING',
            'NOTICE',
            'THIRD_PARTY_NOTICES.md',
        ]) {
            try {
                const text = await readFile(join(root, name), 'utf8');
                inputs[relative(project, join(root, name))] = digest(text);
                notices.push(`${metadata.name}@${metadata.version}: ${name}\n${text}`);
                licenses[name] = digest(text);
                found = true;
            } catch (error) {
                if (error.code !== 'ENOENT') throw error;
                inputs[relative(project, join(root, name))] = null;
            }
        }
        // omggif 1.0.10 ships its complete MIT notice only as a source header
        // Match the reviewed notice before retaining it in the distributable
        if (!found && metadata.name === 'omggif' && metadata.version === '1.0.10') {
            const source = await readFile(join(root, 'omggif.js'), 'utf8');
            const header =
                source
                    .split('// omggif is')[0]
                    .split('\n')
                    .map((line) => line.replace(/^\/\/ ?/, ''))
                    .join('\n')
                    .trim() + '\n';
            const noticePath = 'tools/notices/omggif-1.0.10.txt';
            const notice = await readFile(join(sdkRoot, noticePath), 'utf8');
            if (header !== notice)
                throw Error('omggif source license differs from the reviewed notice');
            inputs['sdk:' + noticePath] = digest(notice);
            inputs[relative(project, join(root, 'omggif.js'))] = digest(source);
            notices.push(
                `${metadata.name}@${metadata.version}: omggif.js license header\n${notice}`,
            );
            licenses['omggif.js license header'] = digest(notice);
            found = true;
        }
        if (root !== project && !found)
            throw Error('Bundled dependency license missing: ' + metadata.name);
        dependencies.push({
            name: metadata.name,
            version: metadata.version,
            licenses,
            inputs: Object.fromEntries(
                Object.entries(inputs)
                    .filter(
                        ([name]) =>
                            !name.startsWith('asset:') &&
                            !name.startsWith('sdk:') &&
                            insideBundleRoot(root, resolve(project, name)),
                    )
                    .map(([name, value]) => [canonical(name), value]),
            ),
        });
    }
    await writeFile(join(outdir, 'RUNTIME_LICENSES.txt'), notices.join('\n\n'));
    files['RUNTIME_LICENSES.txt'] = digest(await readFile(join(outdir, 'RUNTIME_LICENSES.txt')));
    const normalizedInputs = Object.fromEntries(
        Object.entries(inputs).map(([name, value]) => [canonical(name), value]),
    );
    const cleanMeta = {
        inputs: Object.fromEntries(
            Object.entries(result.metafile.inputs).map(([name, value]) => [
                canonical(name),
                {
                    ...value,
                    imports: value.imports.map((item) => ({
                        ...item,
                        path: item.external ? item.path : canonical(item.path),
                    })),
                },
            ]),
        ),
        outputs: Object.fromEntries(
            Object.entries(result.metafile.outputs).map(([name, value]) => [
                relative(outdir, resolve(project, name)).replaceAll('\\', '/'),
                {
                    ...value,
                    ...(value.entryPoint ? { entryPoint: canonical(value.entryPoint) } : {}),
                    inputs: Object.fromEntries(
                        Object.entries(value.inputs).map(([name, value]) => [
                            canonical(name),
                            value,
                        ]),
                    ),
                },
            ]),
        ),
    };
    const report = {
        version: 1,
        cache_hit: false,
        settings,
        dependencies,
        input_sha256: normalizedInputs,
        output_sha256: files,
        fingerprint: digest(JSON.stringify({ inputs: normalizedInputs, settings })),
        outputs: Object.fromEntries(
            result.outputFiles.map((output) => [
                relative(outdir, output.path),
                { bytes: output.contents.length, gzip_bytes: gzipSync(output.contents).length },
            ]),
        ),
        metafile: cleanMeta,
    };
    await writeFile(join(outdir, 'bundle-report.json'), JSON.stringify(report, null, 2) + '\n');
    return report;
}
if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
    const [project, runtime] = process.argv.slice(2);
    if (!project || !runtime) throw Error('Usage: node bundle.mjs PROJECT RUNTIME');
    const config =
        JSON.parse(await readFile(join(resolve(project), 'plugin-project.json'), 'utf8'))
            .javascript ?? {};
    await bundlePlugin({ project, runtime, ...config });
}

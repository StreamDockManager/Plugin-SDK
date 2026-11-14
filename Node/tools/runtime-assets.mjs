import { transform } from 'esbuild';

// Strip ordinary prose from shipped JavaScript without changing canonical source assets
export async function runtimeAssetSource(source) {
    const { code } = await transform(source, {
        loader: 'js',
        target: 'esnext',
        minifyIdentifiers: false,
        minifySyntax: false,
        minifyWhitespace: false,
        legalComments: 'inline',
        charset: 'utf8',
    });
    // SPDX headers are license metadata even though esbuild does not classify them as legal comments
    const spdx = source.match(/^\s*\/\/\s*SPDX-License-Identifier:[^\r\n]+/gm) || [];
    return (
        spdx
            .filter((header) => !code.includes(header.trim()))
            .map((header) => header.trim() + '\n')
            .join('') + code
    );
}

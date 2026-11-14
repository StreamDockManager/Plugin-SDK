import test from 'node:test';
import assert from 'node:assert/strict';
import { Script } from 'node:vm';
import { runtimeAssetSource } from '../tools/runtime-assets.mjs';

// Preserve executable directives and legal notices while removing non-runtime prose
test('runtime assets retain directives and license metadata without ordinary comments', async () => {
    const input =
        '// SPDX-License-Identifier: MIT\n/*! @license Example */\n"use strict";\n// Explain the helper\nglobalThis.result = 3;\n';
    const result = await runtimeAssetSource(input);
    assert.match(result, /SPDX-License-Identifier: MIT/);
    assert.match(result, /@license Example/);
    assert.match(result, /"use strict"/);
    assert.doesNotMatch(result, /Explain the helper/);
    const scope = {};
    new Script(result).runInNewContext(scope);
    assert.equal(scope.result, 3);
});

// Do not mistake comment-shaped string or regular-expression content for source comments
test('runtime assets preserve literal comment text and regular expression behavior', async () => {
    const input = 'globalThis.result = ["// literal /* text */", /a\\/\\/b/.test("a//b")];';
    const result = await runtimeAssetSource(input);
    const scope = {};
    new Script(result).runInNewContext(scope);
    assert.deepEqual(Array.from(scope.result), ['// literal /* text */', true]);
});

import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { runInNewContext } from 'node:vm';
import { test } from 'node:test';
import { validate } from '../dist/src/settings.js';
const window = {};
runInNewContext(readFileSync(new URL('../../web/inspector-fields.js', import.meta.url), 'utf8'), {
    window,
    structuredClone,
});
const browser = window.StreamDockManagerFields.validate;
// Provide error behavior for this settings core fixture
const error = (fn, fields, value) => {
    try {
        fn(fields, value);
    } catch (e) {
        return e.message;
    }
};

// Verify settings core preserves Node/browser error precedence and compatibility
test('settings core preserves Node/browser error precedence and compatibility', () => {
    const cases = [
        [
            [{ key: '', type: 'number', sensitive: true }],
            {},
            'Empty or duplicate settings field',
            ': sensitive requires a string field',
        ],
        [
            [{ key: 7, type: 'text' }],
            { 7: 'value' },
            undefined,
            'Empty or duplicate settings field',
        ],
        [
            [{ key: 'x', type: 'unknown', sensitive: true }],
            {},
            'Unknown field type',
            'x: unknown field type unknown',
        ],
        [
            [{ key: 'x', type: 'number', sensitive: true, path_picker: 'file' }],
            {},
            'Invalid path picker',
            'x: sensitive requires a string field',
        ],
        [
            [{ key: 'x', type: 'path', path_picker: 'invalid' }],
            {},
            'Invalid path picker',
            'x: invalid path picker',
        ],
        [[{ key: 'x', type: 'number', min: 5, max: 2 }], {}, 'Invalid bounds', 'x: invalid bounds'],
        [[{ key: 'x', type: 'text', required: true }], {}, 'Required field', 'x: required'],
        [
            [{ key: 'x', type: 'text', required: true }],
            { x: '' },
            'x: invalid text value',
            'x: invalid text value',
        ],
        [
            [{ key: 'x', type: 'select', options: ['a'], default: 'b' }],
            {},
            'x: invalid select value',
            'x: invalid select value',
        ],
        [
            [{ key: 'x', type: 'pairs' }],
            { x: { a: 1 } },
            'x: invalid pairs value',
            'x: invalid pairs value',
        ],
        [[], [], 'Settings must be an object', 'Settings must be an object'],
    ];
    for (const [fields, settings, nodeError, browserError] of cases) {
        assert.equal(error(validate, fields, settings), nodeError);
        assert.equal(error(browser, fields, settings), browserError);
    }
});
// Verify settings core clones unknown values and creates prototype-safe defaults
test('settings core clones unknown values and creates prototype-safe defaults', () => {
    const fields = [
        { key: '__proto__', type: 'pairs', default: { a: 'b' } },
        { key: 'n', type: 'number', min: 0, max: 3, default: 2 },
        { key: 'c', type: 'checkbox', default: false },
    ];
    for (const fn of [validate, browser]) {
        const original = { unknown: { a: [1] } };
        const value = fn(fields, original);
        assert.equal(Object.hasOwn(value, '__proto__'), true);
        assert.deepEqual(value.__proto__, { a: 'b' });
        value.unknown.a.push(2);
        assert.deepEqual(original, { unknown: { a: [1] } });
        assert.equal(value.c, false);
        assert.equal(value.n, 2);
        assert.throws(
            () =>
                fn(
                    Array.from({ length: 257 }, (_, i) => ({ key: `x${i}`, type: 'text' })),
                    {},
                ),
            /Too many/,
        );
    }
});

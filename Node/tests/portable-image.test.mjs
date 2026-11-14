import test from 'node:test';
import assert from 'node:assert/strict';
import { imageDataURI, MAX_PORTABLE_IMAGE_BYTES } from '../dist/src/index.js';

// Verify portable image data URI
test('portable image data URI', () => {
    for (const mime of ['image/png', 'image/svg+xml']) {
        const data = Uint8Array.of(0, 1, 254, 255);
        assert.equal(imageDataURI(mime, data), `data:${mime};base64,AAH+/w==`);
        assert.deepEqual([...data], [0, 1, 254, 255]);
        const maximum = Buffer.alloc(MAX_PORTABLE_IMAGE_BYTES);
        assert.deepEqual(Buffer.from(imageDataURI(mime, maximum).split(',')[1], 'base64'), maximum);
        assert.throws(() => imageDataURI(mime, Buffer.alloc(0)));
        assert.throws(() => imageDataURI(mime, Buffer.alloc(MAX_PORTABLE_IMAGE_BYTES + 1)));
    }
    for (const mime of [
        'image/jpeg',
        'IMAGE/PNG',
        'image/svg+xml;charset=utf-8',
        'file:///tmp/icon.png',
    ])
        assert.throws(() => imageDataURI(mime, Uint8Array.of(1)));
    assert.throws(() => imageDataURI('image/png', 'icons/even.png'), TypeError);
});

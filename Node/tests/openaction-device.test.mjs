import test from 'node:test';
import assert from 'node:assert/strict';
import { OpenActionDeviceProvider } from '../dist/src/openaction_device.js';

// Verify explicit OpenDeck envelopes preserve host payload shapes
test('explicit OpenDeck envelopes preserve host payload shapes', () => {
    const p = new OpenActionDeviceProvider('xy');
    const info = { id: 'xy-1', name: 'Virtual', rows: 2, columns: 3, encoders: 1, type: 0 };
    const registered = p.registerDevice(info);
    assert.deepEqual(registered, {
        event: 'registerDevice',
        payload: { ...info, touchpoints: 0, infobars: 0 },
    });
    registered.payload.name = 'Changed';
    assert.equal(info.name, 'Virtual');
    for (const event of ['keyDown', 'keyUp', 'encoderDown', 'encoderUp'])
        assert.deepEqual(p[event]('xy-1', 255), {
            event,
            payload: { device: 'xy-1', position: 255 },
        });
    for (const event of ['deregisterDevice', 'rerenderImages'])
        assert.deepEqual(p[event]('xy-1'), { event, payload: 'xy-1' });
    assert.equal(p.encoderChange('xy-1', 0, -32768).payload.ticks, -32768);
    assert.deepEqual(p.touchscreenPress('xy-1', 0, 65535, 0).payload, {
        device: 'xy-1',
        position: 0,
        x: 65535,
        y: 0,
        hold: false,
    });
});
// Verify namespace and Rust numeric widths fail closed
test('namespace and Rust numeric widths fail closed', () => {
    for (const ns of ['', 'a', 'abc', 'éx', ' x', '\x00x'])
        assert.throws(() => new OpenActionDeviceProvider(ns));
    const p = new OpenActionDeviceProvider('xy');
    for (const n of [-1, 256, true, 1.5, NaN, Infinity]) assert.throws(() => p.keyDown('xy-1', n));
    assert.throws(() => p.keyDown('ab-1', 0));
    assert.throws(() => p.keyDown('xy' + 'é'.repeat(128), 0));
    assert.throws(() =>
        p.registerDevice({ id: 'xy-1', name: '', rows: 1, columns: 1, encoders: 0, type: 0 }),
    );
    assert.throws(() => p.encoderChange('xy-1', 0, 32768));
    assert.throws(() => p.touchscreenPress('xy-1', 0, 65536, 0));
    assert.throws(() => p.touchscreenPress('xy-1', 0, 0, 0, 1));
});
// Verify provider responses are scoped, bounded, and opaque
test('provider responses are scoped, bounded, and opaque', () => {
    const p = new OpenActionDeviceProvider('xy');
    for (const event of [
        { event: 'setBrightness', device: 'xy-1', brightness: 255 },
        { event: 'setImage', device: 'xy-1', controller: null, position: null, image: null },
        {
            event: 'setImage',
            device: 'xy-1',
            controller: 'Encoder',
            position: 0,
            image: 'data:image/jpeg;base64,opaque',
        },
    ])
        assert.deepEqual(p.parseResponse(JSON.stringify(event)), event);
    for (const event of [
        { event: 'setBrightness', device: 'ab-1', brightness: 0 },
        { event: 'setBrightness', device: 'xy-1', brightness: true },
        { event: 'setImage', device: 'xy-1', position: 0 },
        { event: 'setImage', device: 'xy-1', controller: 'Keypad', position: 0, image: 42 },
        { event: 'setImage', device: 'xy-1', payload: {} },
    ])
        assert.throws(() => p.parseResponse(JSON.stringify(event)));
    assert.throws(() => p.parseResponse(' '.repeat(1048577)));
});

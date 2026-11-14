import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import vm from 'node:vm';
const callbacks = new Map();
let timer = 0;
const context = {
    window: {},
    structuredClone,
    TextEncoder,
    // Retain timer callbacks so the test controls expiration deterministically
    setTimeout(callback) {
        callbacks.set(++timer, callback);
        return timer;
    },
    // Remove the cancelled timer without advancing the test clock
    clearTimeout(id) {
        callbacks.delete(id);
    },
};
vm.runInNewContext(
    readFileSync(new URL('../web/inspector-workflows.js', import.meta.url), 'utf8'),
    context,
);
const { RequestLifecycle, DiscoveryController } = context.window.StreamDockManagerWorkflows;
const lifecycle = new RequestLifecycle();
let expired = 0;
lifecycle.start('one', 100, () => expired++);
const stale = callbacks.get(timer);
lifecycle.start('two', 100, () => expired++);
stale();
assert.equal(expired, 0);
assert.equal(lifecycle.accepts('one'), false);
assert.equal(lifecycle.accepts('two'), true);
lifecycle.finish();
assert.equal(callbacks.size, 0);
assert.equal(lifecycle.accepts('two'), false);
lifecycle.start('two', 100, () => expired++);
stale();
assert.equal(expired, 0);
callbacks.get(timer)();
assert.equal(expired, 1);
assert.equal(lifecycle.accepts('two'), false);
const sent = [],
    phases = [];
const controller = new DiscoveryController({
    send: (payload) => sent.push(payload),
    changed: (phase) => phases.push(phase),
    cancelOp: 'cancel',
});
const id = controller.start({ op: 'discover' });
callbacks.get(timer)();
assert.equal(controller.busy, false);
assert.equal(JSON.stringify(sent.at(-1)), JSON.stringify({ op: 'cancel', request_id: id }));
assert.deepEqual(phases, ['loading', 'cancelled', 'timeout']);
assert.equal(controller.receive({ request_id: id }), false);
controller.close();
assert.throws(() => controller.start({}), /closed/);
console.log(
    'Request lifecycle stale timer, matching, retirement, timeout and cancellation checks passed.',
);

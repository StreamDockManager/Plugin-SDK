import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { runInNewContext } from 'node:vm';
// Test the packaged source passed by each SDK, using a deterministic timer
const timers = new Map();
let serial = 0;
const sandbox = {
    window: {},
    structuredClone,
    TextEncoder,
    // Retain timer callbacks so the test controls expiration deterministically
    setTimeout(fn) {
        timers.set(++serial, fn);
        return serial;
    },
    // Remove the cancelled timer without advancing the test clock
    clearTimeout(id) {
        timers.delete(id);
    },
};
runInNewContext(readFileSync(process.argv[2] === '-' ? 0 : process.argv[2], 'utf8'), sandbox);
const { DiscoveryController } = sandbox.window.StreamDockManagerWorkflows;
const sent = [],
    phases = [];
const picker = new DiscoveryController({
    send: (p) => sent.push(p),
    cancelOp: 'cancel',
    changed: (p) => phases.push(p),
});
const first = picker.start({ op: 'discover' }),
    second = picker.start({ op: 'discover' });
assert.notEqual(first, second);
assert.equal(sent[1].op, 'cancel');
assert.equal(picker.receive({ request_id: first }, [{ id: 'stale' }]), false);
assert(picker.receive({ request_id: second }, [{ id: 'one', name: 'One' }]));
assert.equal(picker.selection('saved')[0].id, 'saved');
assert.equal(picker.choices.length, 1);
const third = picker.start({ op: 'discover' });
for (const fn of [...timers.values()]) fn();
assert.equal(picker.phase, 'timeout');
assert.equal(picker.receive({ request_id: third }, []), false);
assert.equal(picker.choices.length, 1);
const fourth = picker.start({ op: 'discover' });
assert(picker.receive({ request_id: fourth, available: false }, []));
assert.equal(picker.phase, 'error');
assert.equal(picker.choices.length, 1);
const errorID = picker.start({});
picker.receive({ request_id: errorID, error: 'unavailable' }, []);
assert.equal(picker.phase, 'error');
assert.equal(picker.choices.length, 1);
const fifth = picker.start({ op: 'discover' });
assert.throws(() => picker.receive({ request_id: fifth }, Array(8193).fill({})));
assert.equal(picker.busy, false);
assert.equal(picker.choices.length, 1);
picker.close();
assert.throws(() => picker.start({}));
const failed = new DiscoveryController({ send: () => false });
assert.equal(failed.start({}), undefined);
assert.equal(failed.busy, false);
assert.equal(failed.phase, 'error');
assert.equal(timers.size, 0);
console.log('discovery correlation, cancellation, timeout, retention and limits passed');

import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { retryAfter } from '../dist/src/polling.js';
import { project } from '../dist/src/settings.js';
const fixture = JSON.parse(
    readFileSync(new URL('../../contracts/fixtures/retry-after-v1.json', import.meta.url)),
);
for (const c of fixture.cases)
    assert.equal(
        retryAfter(c.value, fixture.now_ms),
        c.delay_ms === null ? undefined : fixture.now_ms + c.delay_ms,
        c.name,
    );
const global = { shared: { v: [1] }, global: [2] },
    action = { shared: { v: [3] }, missing: true, local: [4] };
const value = project(global, action, ['shared', 'missing']);
assert.equal(Object.hasOwn(value, 'missing'), false);
assert.deepEqual(value.shared, { v: [1] });
value.shared.v.push(9);
value.global.push(9);
value.local.push(9);
assert.deepEqual(global, { shared: { v: [1] }, global: [2] });
assert.deepEqual(action.local, [4]);
assert.deepEqual(project(global, action).shared, action.shared);

// Exercise both connector call sites, including plugin-scoped inspector drafts
const { Connector } = await import('../dist/src/connectors.js');
const connector = new Connector(
    {},
    { actions: ['status'], interval: 1000, sharedKeys: ['shared', 'missing'] },
);
connector.globals = { shared: { v: [1] }, global: [2] };
connector.bindings.set('binding', {
    action: 'test.status',
    settings: { shared: { v: [3] }, missing: true, local: [4] },
});
const bound = connector.bindingSettings('binding');
assert.equal(bound._action, 'status');
assert.deepEqual(bound.shared, { v: [1] });
assert.equal(Object.hasOwn(bound, 'missing'), false);
bound.shared.v.push(8);
assert.deepEqual(connector.globals.shared, { v: [1] });
let captured;
connector.enqueue = (_client, request) => {
    captured = request.settings;
};
const client = {
    // Provide defer action behavior for this shared primitives fixture
    deferAction: () => ({}),
};
for (const scope of ['action', 'plugin']) {
    const draft = { shared: { v: [3] }, missing: true, local: [4] };
    connector.inspect(client, { payload: { op: 'discover', scope, settings: draft } });
    assert.deepEqual(captured.shared, { v: [scope === 'plugin' ? 3 : 1] });
    assert.equal(Object.hasOwn(captured, 'missing'), scope === 'plugin');
    captured.shared.v.push(8);
    captured.global.push(8);
    captured.local.push(8);
    assert.deepEqual(connector.globals, { shared: { v: [1] }, global: [2] });
    assert.deepEqual(draft.local, [4]);
    assert.deepEqual(draft.shared, { v: [3] });
}

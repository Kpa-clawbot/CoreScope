/**
 * Unit tests for computeNodesWithoutScope (public/analytics.js), the pure
 * computation behind the Scopes tab's "Nodes Not Using Any Scope" section —
 * the flip side of "Nodes Running This Region": every node with no
 * default_scope configured at all.
 */
'use strict';

const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

let passed = 0, failed = 0;
function test(name, fn) {
  try {
    fn();
    passed++;
    console.log(`  ✅ ${name}`);
  } catch (e) {
    failed++;
    console.log(`  ❌ ${name}: ${e.message}`);
  }
}

// Same proven sandbox as test-analytics-foreign-traffic-tab.js — rolling a
// fresh one by hand tends to miss a global roles.js/app.js touch eagerly at
// load time (window.addEventListener, fetch, etc.).
function makeSandbox() {
  const ctx = {
    window: { addEventListener: () => {}, dispatchEvent: () => {} },
    document: {
      readyState: 'complete',
      createElement: () => ({ id: '', textContent: '', innerHTML: '' }),
      head: { appendChild: () => {} },
      getElementById: () => null,
      addEventListener: () => {},
      querySelectorAll: () => [],
      querySelector: () => null,
    },
    console, Date, Infinity, Math, Array, Object, String, Number, JSON, RegExp,
    Error, TypeError, parseInt, parseFloat, isNaN, isFinite,
    encodeURIComponent, decodeURIComponent,
    setTimeout: () => {}, clearTimeout: () => {}, setInterval: () => {}, clearInterval: () => {},
    fetch: () => Promise.resolve({ json: () => Promise.resolve({}) }),
    performance: { now: () => Date.now() },
    localStorage: (() => { const s = {}; return { getItem: k => s[k] || null, setItem: (k, v) => { s[k] = String(v); }, removeItem: k => { delete s[k]; } }; })(),
    location: { hash: '' },
    getHashParams: function() { return new URLSearchParams((ctx.location.hash.split('?')[1] || '')); },
    CustomEvent: class CustomEvent {},
    Map, Promise, URLSearchParams,
    addEventListener: () => {},
    dispatchEvent: () => {},
    requestAnimationFrame: (cb) => setTimeout(cb, 0),
  };
  vm.createContext(ctx);
  return ctx;
}

function loadInCtx(ctx, file) {
  if (!ctx.__payloadLabelsLoaded && file !== 'public/payload-labels.js') {
    ctx.__payloadLabelsLoaded = true;
    vm.runInContext(fs.readFileSync('public/payload-labels.js', 'utf8'), ctx);
  }
  vm.runInContext(fs.readFileSync(file, 'utf8'), ctx);
  for (const k of Object.keys(ctx.window)) ctx[k] = ctx.window[k];
}

function makeAnalyticsSandbox() {
  const ctx = makeSandbox();
  ctx.getComputedStyle = () => ({ getPropertyValue: () => '' });
  ctx.registerPage = () => {};
  ctx.api = () => Promise.resolve({});
  ctx.timeAgo = (iso) => iso ? 'x ago' : '—';
  ctx.RegionFilter = { init: () => {}, onChange: () => {}, regionQueryString: () => '' };
  ctx.onWS = () => {};
  ctx.offWS = () => {};
  ctx.connectWS = () => {};
  ctx.invalidateApiCache = () => {};
  ctx.makeColumnsResizable = () => {};
  ctx.initTabBar = () => {};
  ctx.IATA_COORDS_GEO = {};
  loadInCtx(ctx, 'public/roles.js');
  loadInCtx(ctx, 'public/app.js');
  ctx.fetchAllNodes = async () => ({ nodes: [] });
  try { loadInCtx(ctx, 'public/analytics.js'); } catch (e) {
    for (const k of Object.keys(ctx.window)) ctx[k] = ctx.window[k];
  }
  return ctx;
}

const ctx = makeAnalyticsSandbox();
const computeNodesWithoutScope = ctx.window._analyticsComputeNodesWithoutScope;
const computeRepeatersNeverRelayingScope = ctx.window._analyticsComputeRepeatersNeverRelayingScope;

console.log('\n=== analytics.js: computeNodesWithoutScope ===');

test('is exported for testing', () => {
  assert.strictEqual(typeof computeNodesWithoutScope, 'function');
});

test('filters to only nodes with a falsy default_scope', () => {
  const nodes = [
    { public_key: 'pk1', name: 'Scoped', role: 'repeater', default_scope: '#dk' },
    { public_key: 'pk2', name: 'Unscoped1', role: 'repeater', default_scope: null },
    { public_key: 'pk3', name: 'Unscoped2', role: 'companion', default_scope: '' },
  ];
  const result = computeNodesWithoutScope(nodes, 100);
  assert.strictEqual(result.total, 2, 'only the two nodes without default_scope should count');
  const names = result.sortedCapped.map(n => n.name);
  assert.ok(names.includes('Unscoped1') && names.includes('Unscoped2'), 'both unscoped nodes should be present');
  assert.ok(!names.includes('Scoped'), 'the scoped node must not appear');
});

test('tallies roleSummary by role, most common first', () => {
  const nodes = [
    { public_key: 'pk1', role: 'repeater', default_scope: null },
    { public_key: 'pk2', role: 'repeater', default_scope: null },
    { public_key: 'pk3', role: 'repeater', default_scope: null },
    { public_key: 'pk4', role: 'companion', default_scope: null },
    { public_key: 'pk5', role: 'room', default_scope: null },
    { public_key: 'pk6', role: 'room', default_scope: null },
  ];
  const result = computeNodesWithoutScope(nodes, 100);
  // JSON comparison, not assert.deepStrictEqual: result.roleSummary objects
  // were constructed inside the vm sandbox's own realm, so Node's strict
  // prototype-identity check treats them as unequal to host-realm object
  // literals even when structurally identical.
  assert.strictEqual(JSON.stringify(result.roleSummary), JSON.stringify([
    { role: 'repeater', count: 3 },
    { role: 'room', count: 2 },
    { role: 'companion', count: 1 },
  ]));
});

test('nodes with a missing role are tallied under "unknown"', () => {
  const nodes = [{ public_key: 'pk1', default_scope: null }];
  const result = computeNodesWithoutScope(nodes, 100);
  assert.strictEqual(JSON.stringify(result.roleSummary), JSON.stringify([{ role: 'unknown', count: 1 }]));
});

test('sorts most-recently-active (last_seen) first', () => {
  const nodes = [
    { public_key: 'pkOld', name: 'Old', role: 'repeater', default_scope: null, last_seen: '2026-01-01T00:00:00Z' },
    { public_key: 'pkNew', name: 'New', role: 'repeater', default_scope: null, last_seen: '2026-07-19T00:00:00Z' },
    { public_key: 'pkMid', name: 'Mid', role: 'repeater', default_scope: null, last_seen: '2026-04-01T00:00:00Z' },
  ];
  const result = computeNodesWithoutScope(nodes, 100);
  assert.deepStrictEqual(result.sortedCapped.map(n => n.name), ['New', 'Mid', 'Old']);
});

test('caps sortedCapped at `cap` and sets truncated accordingly', () => {
  const nodes = [];
  for (let i = 0; i < 5; i++) {
    nodes.push({ public_key: 'pk' + i, name: 'N' + i, role: 'repeater', default_scope: null, last_seen: '2026-07-1' + i + 'T00:00:00Z' });
  }
  const capped = computeNodesWithoutScope(nodes, 3);
  assert.strictEqual(capped.sortedCapped.length, 3, 'sortedCapped should respect the cap');
  assert.strictEqual(capped.total, 5, 'total should still reflect the full uncapped count');
  assert.strictEqual(capped.truncated, true, 'truncated should be true when more nodes exist than the cap');

  const uncapped = computeNodesWithoutScope(nodes, 10);
  assert.strictEqual(uncapped.truncated, false, 'truncated should be false when the cap exceeds the total');
});

test('returns zero total and empty rows when every node has a scope', () => {
  const nodes = [
    { public_key: 'pk1', role: 'repeater', default_scope: '#dk' },
    { public_key: 'pk2', role: 'repeater', default_scope: '#dk-oj' },
  ];
  const result = computeNodesWithoutScope(nodes, 100);
  assert.strictEqual(result.total, 0);
  assert.deepStrictEqual(result.sortedCapped, []);
  assert.deepStrictEqual(result.roleSummary, []);
});

test('opts.role narrows sortedCapped to that role but leaves total/roleSummary at the full count', () => {
  const nodes = [
    { public_key: 'pk1', name: 'Rep1', role: 'repeater', default_scope: null },
    { public_key: 'pk2', name: 'Comp1', role: 'companion', default_scope: null },
    { public_key: 'pk3', name: 'Comp2', role: 'companion', default_scope: '' },
  ];
  const result = computeNodesWithoutScope(nodes, 100, { role: 'companion' });
  assert.strictEqual(result.total, 3, 'total stays the full unfiltered count');
  assert.strictEqual(result.filteredTotal, 2, 'filteredTotal reflects the role filter');
  assert.deepStrictEqual(result.sortedCapped.map(n => n.name).sort(), ['Comp1', 'Comp2']);
  assert.strictEqual(result.roleSummary.length, 2, 'roleSummary is unaffected by the filter, still shows both roles');
});

test('opts.q matches case-insensitively against name or public_key', () => {
  const nodes = [
    { public_key: 'pkABC123', name: 'SkyMesh Node', role: 'repeater', default_scope: null },
    { public_key: 'pkXYZ999', name: 'Other Repeater', role: 'repeater', default_scope: null },
  ];
  const byName = computeNodesWithoutScope(nodes, 100, { q: 'skymesh' });
  assert.strictEqual(byName.filteredTotal, 1);
  assert.strictEqual(byName.sortedCapped[0].name, 'SkyMesh Node');

  const byKey = computeNodesWithoutScope(nodes, 100, { q: 'xyz999' });
  assert.strictEqual(byKey.filteredTotal, 1);
  assert.strictEqual(byKey.sortedCapped[0].name, 'Other Repeater');
});

test('opts.role and opts.q combine (AND, not OR)', () => {
  const nodes = [
    { public_key: 'pk1', name: 'Alpha', role: 'companion', default_scope: null },
    { public_key: 'pk2', name: 'Alpha', role: 'repeater', default_scope: null },
  ];
  const result = computeNodesWithoutScope(nodes, 100, { role: 'companion', q: 'alpha' });
  assert.strictEqual(result.filteredTotal, 1);
  assert.strictEqual(result.sortedCapped[0].role, 'companion');
});

test('no opts (or empty opts) behaves exactly as before — filteredTotal equals total', () => {
  const nodes = [
    { public_key: 'pk1', name: 'A', role: 'repeater', default_scope: null },
    { public_key: 'pk2', name: 'B', role: 'repeater', default_scope: null },
  ];
  const result = computeNodesWithoutScope(nodes, 100);
  assert.strictEqual(result.filteredTotal, result.total);
});

test('opts.geo narrows to domestic/foreign via window.MC_GEO_FILTER, same box the Nodes tab uses', () => {
  // Denmark-ish box: lat 54-58, lon 8-13. A node outside it is "foreign".
  ctx.window.MC_GEO_FILTER = { latMin: 54, latMax: 58, lonMin: 8, lonMax: 13 };
  try {
    const nodes = [
      { public_key: 'pk1', name: 'Domestic1', role: 'repeater', default_scope: null, lat: 56.0, lon: 10.0 },
      { public_key: 'pk2', name: 'Foreign1', role: 'repeater', default_scope: null, lat: 40.0, lon: 20.0 },
    ];
    const domestic = computeNodesWithoutScope(nodes, 100, { geo: 'domestic' });
    assert.strictEqual(domestic.filteredTotal, 1);
    assert.strictEqual(domestic.sortedCapped[0].name, 'Domestic1');

    const foreign = computeNodesWithoutScope(nodes, 100, { geo: 'foreign' });
    assert.strictEqual(foreign.filteredTotal, 1);
    assert.strictEqual(foreign.sortedCapped[0].name, 'Foreign1');

    const all = computeNodesWithoutScope(nodes, 100);
    assert.strictEqual(all.filteredTotal, 2, 'no geo opt leaves both nodes');
  } finally {
    ctx.window.MC_GEO_FILTER = null;
  }
});

console.log('\n=== analytics.js: computeRepeatersNeverRelayingScope ===');

test('is exported for testing', () => {
  assert.strictEqual(typeof computeRepeatersNeverRelayingScope, 'function');
});

test('includes only repeater/room nodes with no transported_scopes, excludes other roles entirely', () => {
  const nodes = [
    { public_key: 'pk1', name: 'NeverRelays', role: 'repeater', transported_scopes: null },
    { public_key: 'pk2', name: 'RelaysDK', role: 'repeater', transported_scopes: ['#dk'] },
    { public_key: 'pk3', name: 'RoomNeverRelays', role: 'room', transported_scopes: [] },
    { public_key: 'pk4', name: 'CompanionNeverRelays', role: 'companion', transported_scopes: null },
  ];
  const result = computeRepeatersNeverRelayingScope(nodes, 100);
  const names = result.sortedCapped.map(n => n.name);
  assert.strictEqual(result.total, 2, 'only NeverRelays and RoomNeverRelays should count');
  assert.ok(names.includes('NeverRelays') && names.includes('RoomNeverRelays'));
  assert.ok(!names.includes('RelaysDK'), 'a repeater that has relayed a scope must not appear');
  assert.ok(!names.includes('CompanionNeverRelays'), 'a companion can never relay at all — must not appear regardless of transported_scopes');
});

test('a node missing default_scope but WITH transported_scopes is correctly excluded (the two signals differ)', () => {
  // Real-world case found on stg.meshview.dk: a repeater's hashRegions
  // config can let it relay for others even when its own adverts never
  // carry a matching transport code (no default_scope).
  const nodes = [
    { public_key: 'pk1', name: 'RelaysForOthers', role: 'repeater', default_scope: null, transported_scopes: ['#dk', '#dk3'] },
  ];
  const result = computeRepeatersNeverRelayingScope(nodes, 100);
  assert.strictEqual(result.total, 0, 'a repeater with transported_scopes must not appear here even without its own default_scope');
});

test('sorts by relay_count_24h descending — the busiest unconfigured repeaters first', () => {
  const nodes = [
    { public_key: 'pkQuiet', name: 'Quiet', role: 'repeater', relay_count_24h: 2, transported_scopes: null },
    { public_key: 'pkBusy', name: 'Busy', role: 'repeater', relay_count_24h: 500, transported_scopes: null },
    { public_key: 'pkMid', name: 'Mid', role: 'room', relay_count_24h: 40, transported_scopes: [] },
  ];
  const result = computeRepeatersNeverRelayingScope(nodes, 100);
  assert.deepStrictEqual(result.sortedCapped.map(n => n.name), ['Busy', 'Mid', 'Quiet']);
});

test('caps sortedCapped at `cap` and sets truncated accordingly', () => {
  const nodes = [];
  for (let i = 0; i < 5; i++) {
    nodes.push({ public_key: 'pk' + i, name: 'N' + i, role: 'repeater', relay_count_24h: i, transported_scopes: null });
  }
  const capped = computeRepeatersNeverRelayingScope(nodes, 3);
  assert.strictEqual(capped.sortedCapped.length, 3);
  assert.strictEqual(capped.total, 5);
  assert.strictEqual(capped.truncated, true);

  const uncapped = computeRepeatersNeverRelayingScope(nodes, 10);
  assert.strictEqual(uncapped.truncated, false);
});

test('returns zero total when every repeater/room has relayed at least one scope', () => {
  const nodes = [
    { public_key: 'pk1', role: 'repeater', transported_scopes: ['#dk'] },
    { public_key: 'pk2', role: 'room', transported_scopes: ['#dk-oj'] },
  ];
  const result = computeRepeatersNeverRelayingScope(nodes, 100);
  assert.strictEqual(result.total, 0);
  assert.deepStrictEqual(result.sortedCapped, []);
});

test('opts.geo narrows to domestic/foreign, leaving total (unfiltered) unchanged', () => {
  ctx.window.MC_GEO_FILTER = { latMin: 54, latMax: 58, lonMin: 8, lonMax: 13 };
  try {
    const nodes = [
      { public_key: 'pk1', name: 'DomesticRepeater', role: 'repeater', transported_scopes: null, lat: 56.0, lon: 10.0 },
      { public_key: 'pk2', name: 'ForeignRepeater', role: 'repeater', transported_scopes: null, lat: 40.0, lon: 20.0 },
    ];
    const domestic = computeRepeatersNeverRelayingScope(nodes, 100, { geo: 'domestic' });
    assert.strictEqual(domestic.total, 2, 'total stays the full unfiltered count');
    assert.strictEqual(domestic.filteredTotal, 1);
    assert.strictEqual(domestic.sortedCapped[0].name, 'DomesticRepeater');

    const foreign = computeRepeatersNeverRelayingScope(nodes, 100, { geo: 'foreign' });
    assert.strictEqual(foreign.filteredTotal, 1);
    assert.strictEqual(foreign.sortedCapped[0].name, 'ForeignRepeater');
  } finally {
    ctx.window.MC_GEO_FILTER = null;
  }
});

console.log('\n════════════════════════════════════════');
console.log(`  Nodes Without Scope: ${passed} passed, ${failed} failed`);
console.log('════════════════════════════════════════');
process.exit(failed === 0 ? 0 : 1);

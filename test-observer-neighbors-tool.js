// test-observer-neighbors-tool.js — vm.createContext sandbox tests for
// public/observer-neighbors-tool.js (Tools > Observer Neighbors page).
'use strict';
const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

let passed = 0, failed = 0;
async function test(name, fn) {
  try {
    await fn();
    passed++;
    console.log(`  ✅ ${name}`);
  } catch (e) {
    failed++;
    console.log(`  ❌ ${name}: ${e.message}`);
  }
}

function makeRow(overrides) {
  return Object.assign({
    observerId: 'obs1',
    observerName: 'Observer One',
    observerIata: 'SJC',
    neighborPubkey: 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
    neighborName: 'Neighbor A',
    neighborRole: 'repeater',
    scopes: '#dk',
    status: 'responded',
    seenViaPackets: true,
    reportedAt: '2026-07-28T10:00:00Z',
  }, overrides);
}

function createSandbox(rows, unknownScopesFixture) {
  const docStore = {};
  const listeners = {};
  function fakeEl(id) {
    if (!docStore[id]) {
      docStore[id] = {
        id: id,
        innerHTML: '',
        textContent: '',
        value: '',
        dataset: {},
        _listeners: {},
        addEventListener: function (evt, fn) { this._listeners[evt] = fn; },
        querySelectorAll: function () { return []; },
        querySelector: function () { return null; },
      };
    }
    return docStore[id];
  }

  const sandbox = {
    window: {},
    document: {
      getElementById: (id) => fakeEl(id),
      querySelectorAll: () => [],
      querySelector: () => null,
    },
    location: { hash: '#/tools/observer-neighbors' },
    fetch: () => Promise.resolve({ ok: true, json: () => Promise.resolve({ neighbors: rows, unknownScopes: unknownScopesFixture || [] }) }),
    URLSearchParams: URLSearchParams,
    registerPage: function () {},
    timeAgo: (iso) => 'TIME_AGO(' + iso + ')',
    encodeURIComponent: encodeURIComponent,
    console: console,
    __docStore: docStore,
  };
  sandbox.self = sandbox;
  sandbox.globalThis = sandbox;
  const ctx = vm.createContext(sandbox);
  const src = fs.readFileSync(__dirname + '/public/observer-neighbors-tool.js', 'utf8');
  vm.runInContext(src, ctx);
  return sandbox;
}

// init() builds real DOM via template literals assigned to a fake
// container's innerHTML, then queries document.getElementById for the
// sub-elements it just described. Our fakeEl() stub doesn't parse HTML,
// so getElementById always returns a *fresh* stub the first time it's
// asked for a given id, regardless of what init() wrote into innerHTML.
// That's fine for these tests: we only need to observe what renderTable()
// assigns to '#obs-nb-table-wrap'.innerHTML and '#obs-nb-status'.textContent
// after fetch resolves, and that async load() runs inside init().
function initWith(rows, unknownScopesFixture) {
  const sb = createSandbox(rows, unknownScopesFixture);
  const container = { innerHTML: '' };
  sb.window.ObserverNeighborsTool.init(container);
  return sb;
}

function waitForLoad() {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

(async () => {
  console.log('\n=== observer-neighbors-tool.js: Observer Neighbors page ===');

  await test('window.ObserverNeighborsTool exists with init/destroy/sortValue', () => {
    const sb = createSandbox([]);
    assert.strictEqual(typeof sb.window.ObserverNeighborsTool.init, 'function');
    assert.strictEqual(typeof sb.window.ObserverNeighborsTool.destroy, 'function');
    assert.strictEqual(typeof sb.window.ObserverNeighborsTool.sortValue, 'function');
  });

  await test('empty result set shows a neutral "no observer has reported" message, not an error', async () => {
    const sb = initWith([]);
    await waitForLoad();
    const wrap = sb.__docStore['obs-nb-table-wrap'];
    assert.ok(wrap.innerHTML.includes('No observer has reported any direct neighbors yet'), `got: ${wrap.innerHTML}`);
  });

  await test('renders a row with observer link, neighbor link, scope badge, and packet-evidence label', async () => {
    const sb = initWith([makeRow()]);
    await waitForLoad();
    const wrap = sb.__docStore['obs-nb-table-wrap'];
    assert.ok(wrap.innerHTML.includes('href="#/observers/obs1"'), 'expected observer link');
    assert.ok(wrap.innerHTML.includes('Observer One'), 'expected observer display name');
    assert.ok(wrap.innerHTML.includes('href="#/nodes/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"'), 'expected neighbor node link');
    assert.ok(wrap.innerHTML.includes('Neighbor A'), 'expected neighbor display name');
    assert.ok(wrap.innerHTML.includes('#dk'), 'expected the scope badge');
    assert.ok(wrap.innerHTML.includes('confirmed'), 'expected packet-evidence "confirmed" label');
    assert.ok(wrap.innerHTML.includes('class="col-scope-list"'), 'expected the Configured Scope cell to reuse the col-scope-list wrap fix');
  });

  await test('an unresolved neighbor pubkey renders truncated and unlinked, and "no reply" for a timeout with no scope', async () => {
    const sb = initWith([makeRow({ neighborName: null, neighborRole: null, scopes: null, status: 'timeout', seenViaPackets: false })]);
    await waitForLoad();
    const wrap = sb.__docStore['obs-nb-table-wrap'];
    assert.ok(!/href="#\/nodes\//.test(wrap.innerHTML), 'must not link an unresolved neighbor pubkey');
    assert.ok(wrap.innerHTML.includes('aaaaaaaaaaaa'), 'expected a truncated pubkey');
    assert.ok(wrap.innerHTML.includes('no reply'), 'expected the "no reply" label for a timeout with no scope');
    assert.ok(wrap.innerHTML.includes('not seen yet'), 'expected the "not seen yet" packet-evidence label');
  });

  await test('status line shows the row count', async () => {
    const sb = initWith([makeRow(), makeRow({ observerId: 'obs2', observerName: 'Observer Two' })]);
    await waitForLoad();
    const status = sb.__docStore['obs-nb-status'];
    assert.ok(status.textContent.includes('2 of 2 neighbor pairs'), `got: ${status.textContent}`);
  });

  await test('renders the Unknown Scopes panel with scope, count, and example neighbors', async () => {
    const sb = initWith([makeRow()], [
      { scope: '#dk-storkbh', count: 3, examples: ['Neighbor A', 'Neighbor B', 'Neighbor C'] },
    ]);
    await waitForLoad();
    const wrap = sb.__docStore['obs-nb-unknown-scopes-wrap'];
    assert.ok(wrap.innerHTML.includes('Scopes CoreScope Doesn\'t Know About Yet (1)'), `got: ${wrap.innerHTML}`);
    assert.ok(wrap.innerHTML.includes('#dk-storkbh'), 'expected the unknown scope name');
    assert.ok(wrap.innerHTML.includes('Neighbor A, Neighbor B, Neighbor C'), 'expected the example neighbors joined');
  });

  await test('Unknown Scopes panel renders nothing when there are no unknown scopes', async () => {
    const sb = initWith([makeRow()], []);
    await waitForLoad();
    const wrap = sb.__docStore['obs-nb-unknown-scopes-wrap'];
    assert.strictEqual(wrap.innerHTML, '', 'panel should be empty, not an empty-state message -- absence of unknown scopes is the normal case');
  });

  await test('sortValue: observer/neighbor fall back to id/pubkey when unresolved, lowercased', () => {
    const sb = createSandbox([]);
    const sv = sb.window.ObserverNeighborsTool.sortValue;
    assert.strictEqual(sv({ observerName: 'Observer One' }, 'observer'), 'observer one');
    assert.strictEqual(sv({ observerId: 'obs1' }, 'observer'), 'obs1');
    assert.strictEqual(sv({ neighborName: 'Neighbor A' }, 'neighbor'), 'neighbor a');
    assert.strictEqual(sv({ neighborPubkey: 'AABB' }, 'neighbor'), 'aabb');
  });

  await test('the default-sorted column (Observer) header carries sort-active', async () => {
    const sb = initWith([makeRow(), makeRow({ observerId: 'obs2', observerName: 'Observer Two' })]);
    await waitForLoad();
    const wrap = sb.__docStore['obs-nb-table-wrap'];
    assert.ok(/data-sort-col="observer"[^>]*class="sortable sort-active"|class="sortable sort-active"[^>]*data-sort-col="observer"/.test(wrap.innerHTML),
      `expected Observer header to carry sort-active by default; got: ${wrap.innerHTML.slice(0, 400)}`);
    assert.ok(!/data-sort-col="neighbor"[^>]*sort-active/.test(wrap.innerHTML), 'Neighbor header should not be marked active');
  });

  await test('sortValue: evidence sorts booleans as 1/0', () => {
    const sb = createSandbox([]);
    const sv = sb.window.ObserverNeighborsTool.sortValue;
    assert.strictEqual(sv({ seenViaPackets: true }, 'evidence'), 1);
    assert.strictEqual(sv({ seenViaPackets: false }, 'evidence'), 0);
  });

  console.log('\n════════════════════════════════════════');
  console.log(`  Observer Neighbors tool: ${passed} passed, ${failed} failed`);
  console.log('════════════════════════════════════════');
  process.exit(failed === 0 ? 0 : 1);
})();

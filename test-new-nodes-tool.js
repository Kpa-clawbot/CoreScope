// test-new-nodes-tool.js — vm.createContext sandbox tests for
// public/new-nodes.js (Tools > New Nodes page).
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
    publicKey: 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
    name: 'NewRepeater',
    role: 'repeater',
    lat: 56.0,
    lon: 10.0,
    firstSeen: '2026-07-29T10:00:00Z',
    areas: ['Aarhus by'],
    foreign: false,
  }, overrides);
}

function createSandbox(newNodesFixture) {
  const docStore = {};
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
    location: { hash: '#/tools/new-nodes' },
    api: (path) => Promise.resolve({ newNodes: newNodesFixture }),
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
  const src = fs.readFileSync(__dirname + '/public/new-nodes.js', 'utf8');
  vm.runInContext(src, ctx);
  return sandbox;
}

function initWith(rows) {
  const sb = createSandbox(rows);
  const container = { innerHTML: '' };
  sb.window.NewNodesTool.init(container);
  return sb;
}

function waitForLoad() {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

(async () => {
  console.log('\n=== new-nodes.js: New Nodes tool page ===');

  await test('window.NewNodesTool exists with init/destroy/sortValue/roleLabel', () => {
    const sb = createSandbox([]);
    assert.strictEqual(typeof sb.window.NewNodesTool.init, 'function');
    assert.strictEqual(typeof sb.window.NewNodesTool.destroy, 'function');
    assert.strictEqual(typeof sb.window.NewNodesTool.sortValue, 'function');
    assert.strictEqual(typeof sb.window.NewNodesTool.roleLabel, 'function');
  });

  await test('empty result set shows a neutral message, not an error', async () => {
    const sb = initWith([]);
    await waitForLoad();
    const wrap = sb.__docStore['new-nodes-table-wrap'];
    assert.ok(wrap.innerHTML.includes('No new nodes yet'), `got: ${wrap.innerHTML}`);
  });

  await test('renders a row with node link, role, area badge, and first-seen', async () => {
    const sb = initWith([makeRow()]);
    await waitForLoad();
    const wrap = sb.__docStore['new-nodes-table-wrap'];
    assert.ok(wrap.innerHTML.includes('href="#/nodes/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"'), 'expected node link');
    assert.ok(wrap.innerHTML.includes('NewRepeater'), 'expected node display name');
    assert.ok(wrap.innerHTML.includes('Repeater'), 'expected human-readable role label');
    assert.ok(wrap.innerHTML.includes('Aarhus by'), 'expected the area badge');
    assert.ok(wrap.innerHTML.includes('TIME_AGO(2026-07-29T10:00:00Z)'), 'expected timeAgo formatting for firstSeen');
  });

  await test('a node with no known name falls back to truncated pubkey, unlinked text stays a link to the node', async () => {
    const sb = initWith([makeRow({ name: null })]);
    await waitForLoad();
    const wrap = sb.__docStore['new-nodes-table-wrap'];
    assert.ok(wrap.innerHTML.includes('aaaaaaaa'), 'expected a truncated pubkey fallback for the name');
    assert.ok(wrap.innerHTML.includes('href="#/nodes/'), 'should still link to the node even without a resolved name');
  });

  await test('a node with no areas shows a dash, not an empty cell', async () => {
    const sb = initWith([makeRow({ areas: [] })]);
    await waitForLoad();
    const wrap = sb.__docStore['new-nodes-table-wrap'];
    assert.ok(wrap.innerHTML.includes('col-scope-list'), 'expected the area cell to reuse the col-scope-list wrap fix');
  });

  await test('status line shows the row count', async () => {
    const sb = initWith([makeRow(), makeRow({ publicKey: 'bb'.repeat(32), name: 'Second' })]);
    await waitForLoad();
    const status = sb.__docStore['new-nodes-status'];
    assert.ok(status.textContent.includes('2 of 2 new nodes'), `got: ${status.textContent}`);
  });

  await test('filter matches on name, pubkey, role, and area', async () => {
    const sb = initWith([
      makeRow({ publicKey: 'aa'.repeat(32), name: 'RepeaterOne', role: 'repeater', areas: ['Aarhus by'] }),
      makeRow({ publicKey: 'bb'.repeat(32), name: 'RoomTwo', role: 'room', areas: ['Bornholm'] }),
    ]);
    await waitForLoad();
    const filterInput = sb.__docStore['new-nodes-filter'];
    filterInput.value = 'bornholm';
    filterInput._listeners.input();
    const wrap = sb.__docStore['new-nodes-table-wrap'];
    assert.ok(wrap.innerHTML.includes('RoomTwo'), 'expected the Bornholm row to match');
    assert.ok(!wrap.innerHTML.includes('RepeaterOne'), 'expected the Aarhus row to be filtered out');
  });

  // #1868-followup — All/Domestic/Foreign origin toggle, reusing
  // nodes.foreign_advert (#730) rather than reimplementing geo classification.
  await test('origin toggle: Domestic shows only non-foreign, Foreign shows only foreign', async () => {
    const sb = initWith([
      makeRow({ publicKey: 'aa'.repeat(32), name: 'HomeNode', foreign: false }),
      makeRow({ publicKey: 'bb'.repeat(32), name: 'BridgedNode', foreign: true }),
    ]);
    await waitForLoad();
    const tabsWrap = sb.__docStore['new-nodes-origin-tabs'];
    const wrap = sb.__docStore['new-nodes-table-wrap'];

    // All (default): both rows present.
    assert.ok(wrap.innerHTML.includes('HomeNode') && wrap.innerHTML.includes('BridgedNode'), 'expected both rows under All');

    // Domestic: only the non-foreign row.
    tabsWrap._listeners.click({ target: { dataset: { origin: 'domestic' }, closest: function () { return this; } } });
    assert.ok(wrap.innerHTML.includes('HomeNode'), 'expected HomeNode under Domestic');
    assert.ok(!wrap.innerHTML.includes('BridgedNode'), 'expected BridgedNode excluded under Domestic');

    // Foreign: only the foreign row.
    tabsWrap._listeners.click({ target: { dataset: { origin: 'foreign' }, closest: function () { return this; } } });
    assert.ok(wrap.innerHTML.includes('BridgedNode'), 'expected BridgedNode under Foreign');
    assert.ok(!wrap.innerHTML.includes('HomeNode'), 'expected HomeNode excluded under Foreign');
  });

  await test('origin toggle: clicking the already-active tab is a no-op', async () => {
    const sb = initWith([makeRow({ foreign: false })]);
    await waitForLoad();
    const tabsWrap = sb.__docStore['new-nodes-origin-tabs'];
    const wrap = sb.__docStore['new-nodes-table-wrap'];
    const before = wrap.innerHTML;
    tabsWrap._listeners.click({ target: { dataset: { origin: 'all' }, closest: function () { return this; } } });
    assert.strictEqual(wrap.innerHTML, before, 'clicking the already-active "All" tab should not change the rendered table');
  });

  await test('roleLabel maps known roles to display names and passes through unknown ones', () => {
    const sb = createSandbox([]);
    const rl = sb.window.NewNodesTool.roleLabel;
    assert.strictEqual(rl('repeater'), 'Repeater');
    assert.strictEqual(rl('room'), 'Room Server');
    assert.strictEqual(rl('companion'), 'Companion');
    assert.strictEqual(rl('sensor'), 'Sensor');
    assert.strictEqual(rl('mystery-role'), 'mystery-role');
    assert.strictEqual(rl(''), '');
  });

  await test('sortValue: name falls back to pubkey when unresolved, lowercased', () => {
    const sb = createSandbox([]);
    const sv = sb.window.NewNodesTool.sortValue;
    assert.strictEqual(sv({ name: 'NewRepeater' }, 'name'), 'newrepeater');
    assert.strictEqual(sv({ publicKey: 'AABBCC' }, 'name'), 'aabbcc');
    assert.strictEqual(sv({ areas: ['Aarhus by', 'Djursland'] }, 'areas'), 'aarhus by, djursland');
  });

  await test('the default-sorted column (First Seen) header carries sort-active', async () => {
    const sb = initWith([makeRow(), makeRow({ publicKey: 'cc'.repeat(32), name: 'Other' })]);
    await waitForLoad();
    const wrap = sb.__docStore['new-nodes-table-wrap'];
    assert.ok(/data-sort-col="firstSeen"[^>]*class="sortable sort-active"|class="sortable sort-active"[^>]*data-sort-col="firstSeen"/.test(wrap.innerHTML),
      `expected First Seen header to carry sort-active by default; got: ${wrap.innerHTML.slice(0, 400)}`);
  });

  console.log('\n════════════════════════════════════════');
  console.log(`  New Nodes tool: ${passed} passed, ${failed} failed`);
  console.log('════════════════════════════════════════');
  process.exit(failed === 0 ? 0 : 1);
})();

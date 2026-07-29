// test-node-changes-tool.js — vm.createContext sandbox tests for
// public/node-changes.js (Tools > Node Changes page).
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
    id: 1,
    publicKey: 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
    name: 'ChangedNode',
    role: 'repeater',
    foreign: false,
    changeType: 'role',
    oldValue: 'companion',
    newValue: 'repeater',
    detectedAt: '2026-07-29T10:00:00Z',
  }, overrides);
}

function createSandbox(nodeChangesFixture) {
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
    location: { hash: '#/tools/node-changes' },
    api: (path) => Promise.resolve({ nodeChanges: nodeChangesFixture }),
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
  const src = fs.readFileSync(__dirname + '/public/node-changes.js', 'utf8');
  vm.runInContext(src, ctx);
  return sandbox;
}

function initWith(rows) {
  const sb = createSandbox(rows);
  const container = { innerHTML: '' };
  sb.window.NodeChangesTool.init(container);
  sb.__container = container;
  return sb;
}

function waitForLoad() {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

(async () => {
  console.log('\n=== node-changes.js: Node Changes tool page ===');

  await test('window.NodeChangesTool exists with init/destroy/sortValue/typeLabel/roleLabel', () => {
    const sb = createSandbox([]);
    assert.strictEqual(typeof sb.window.NodeChangesTool.init, 'function');
    assert.strictEqual(typeof sb.window.NodeChangesTool.destroy, 'function');
    assert.strictEqual(typeof sb.window.NodeChangesTool.sortValue, 'function');
    assert.strictEqual(typeof sb.window.NodeChangesTool.typeLabel, 'function');
    assert.strictEqual(typeof sb.window.NodeChangesTool.roleLabel, 'function');
  });

  await test('empty result set shows a neutral message, not an error', async () => {
    const sb = initWith([]);
    await waitForLoad();
    const wrap = sb.__docStore['node-changes-table-wrap'];
    assert.ok(wrap.innerHTML.includes('No node changes recorded yet'), `got: ${wrap.innerHTML}`);
  });

  await test('renders a role-change row with node link, type badge, and old->new detail', async () => {
    const sb = initWith([makeRow()]);
    await waitForLoad();
    const wrap = sb.__docStore['node-changes-table-wrap'];
    assert.ok(wrap.innerHTML.includes('href="#/nodes/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"'), 'expected node link');
    assert.ok(wrap.innerHTML.includes('ChangedNode'), 'expected node display name');
    assert.ok(wrap.innerHTML.includes('Role'), 'expected the Role type badge');
    assert.ok(wrap.innerHTML.includes('companion'), 'expected old value');
    assert.ok(wrap.innerHTML.includes('repeater'), 'expected new value');
    assert.ok(wrap.innerHTML.includes('TIME_AGO(2026-07-29T10:00:00Z)'), 'expected timeAgo formatting for detectedAt');
  });

  await test('renders a position-change row with distance instead of old->new', async () => {
    const sb = initWith([makeRow({ changeType: 'position', oldValue: '56.0,10.0', newValue: '56.1,10.0', distanceKm: 11.2 })]);
    await waitForLoad();
    const wrap = sb.__docStore['node-changes-table-wrap'];
    assert.ok(wrap.innerHTML.includes('Moved'), 'expected "Moved" wording for a position change');
    assert.ok(wrap.innerHTML.includes('11.2 km'), 'expected the formatted distance');
    assert.ok(!wrap.innerHTML.includes('56.0,10.0'), 'should not show the raw lat,lon old_value, got: ' + wrap.innerHTML);
  });

  await test('renders a resurrected row with "Returned" and last-seen wording', async () => {
    const sb = initWith([makeRow({ changeType: 'resurrected', oldValue: '2026-06-01T00:00:00Z', newValue: '' })]);
    await waitForLoad();
    const wrap = sb.__docStore['node-changes-table-wrap'];
    assert.ok(wrap.innerHTML.includes('Returned'), 'expected "Returned" wording');
    assert.ok(wrap.innerHTML.includes('TIME_AGO(2026-06-01T00:00:00Z)'), 'expected the last-seen timeAgo, got: ' + wrap.innerHTML);
  });

  await test('a node with no known name falls back to truncated pubkey', async () => {
    const sb = initWith([makeRow({ name: null })]);
    await waitForLoad();
    const wrap = sb.__docStore['node-changes-table-wrap'];
    assert.ok(wrap.innerHTML.includes('aaaaaaaa'), 'expected a truncated pubkey fallback for the name');
    assert.ok(wrap.innerHTML.includes('href="#/nodes/'), 'should still link to the node even without a resolved name');
  });

  await test('status line shows the row count', async () => {
    const sb = initWith([makeRow(), makeRow({ id: 2, publicKey: 'bb'.repeat(32), name: 'Second' })]);
    await waitForLoad();
    const status = sb.__docStore['node-changes-status'];
    assert.ok(status.textContent.includes('2 of 2 changes'), `got: ${status.textContent}`);
  });

  await test('filter matches on name and pubkey', async () => {
    const sb = initWith([
      makeRow({ publicKey: 'aa'.repeat(32), name: 'RoleChanger' }),
      makeRow({ publicKey: 'bb'.repeat(32), name: 'NameChanger', changeType: 'name' }),
    ]);
    await waitForLoad();
    const filterInput = sb.__docStore['node-changes-filter'];
    filterInput.value = 'namechanger';
    filterInput._listeners.input();
    const wrap = sb.__docStore['node-changes-table-wrap'];
    assert.ok(wrap.innerHTML.includes('NameChanger'), 'expected the NameChanger row to match');
    assert.ok(!wrap.innerHTML.includes('RoleChanger'), 'expected the RoleChanger row to be filtered out');
  });

  // Type checkboxes — built from whatever change types are actually
  // present in the loaded data, all checked by default.
  await test('type checkboxes render one per distinct changeType present, all checked by default', async () => {
    const sb = initWith([
      makeRow({ publicKey: 'aa'.repeat(32), changeType: 'role' }),
      makeRow({ publicKey: 'bb'.repeat(32), changeType: 'position' }),
    ]);
    await waitForLoad();
    const typeWrap = sb.__docStore['node-changes-type-filters'];
    assert.ok(typeWrap.innerHTML.includes('data-type="role"'), `expected a role checkbox, got: ${typeWrap.innerHTML}`);
    assert.ok(typeWrap.innerHTML.includes('data-type="position"'), `expected a position checkbox, got: ${typeWrap.innerHTML}`);
  });

  await test('unchecking a type checkbox hides matching rows without touching others', async () => {
    const sb = initWith([
      makeRow({ publicKey: 'aa'.repeat(32), name: 'RoleRow', changeType: 'role' }),
      makeRow({ publicKey: 'bb'.repeat(32), name: 'PositionRow', changeType: 'position', distanceKm: 5 }),
    ]);
    await waitForLoad();
    const typeWrap = sb.__docStore['node-changes-type-filters'];
    const wrap = sb.__docStore['node-changes-table-wrap'];
    assert.ok(wrap.innerHTML.includes('RoleRow') && wrap.innerHTML.includes('PositionRow'), 'expected both rows before unchecking');

    typeWrap._listeners.change({ target: { dataset: { type: 'position' }, checked: false } });
    assert.ok(wrap.innerHTML.includes('RoleRow'), 'expected the role row to remain');
    assert.ok(!wrap.innerHTML.includes('PositionRow'), 'expected the position row to be hidden after unchecking Position');

    const status = sb.__docStore['node-changes-status'];
    assert.ok(status.textContent.includes('(filtered)'), `expected the status line to note filtering, got: ${status.textContent}`);
  });

  // Origin tabs — same All/Domestic/Foreign vocabulary as New Nodes'
  // toggle, filtering client-side on row.foreign.
  await test('origin tabs render with All active by default', async () => {
    const sb = initWith([makeRow()]);
    await waitForLoad();
    const html = sb.__container.innerHTML;
    assert.ok(html.includes('data-origin="all"'), `expected an All tab, got: ${html}`);
    assert.ok(/class="tab-btn active"[^>]*data-origin="all"|data-origin="all"[^>]*class="tab-btn active"/.test(html),
      `expected the All tab active by default, got: ${html}`);
  });

  await test('clicking Domestic hides foreign rows, clicking Foreign hides domestic rows', async () => {
    const sb = initWith([
      makeRow({ publicKey: 'aa'.repeat(32), name: 'DomesticRow', foreign: false }),
      makeRow({ publicKey: 'bb'.repeat(32), name: 'ForeignRow', foreign: true }),
    ]);
    await waitForLoad();
    const originWrap = sb.__docStore['node-changes-origin-tabs'];
    const wrap = sb.__docStore['node-changes-table-wrap'];
    assert.ok(wrap.innerHTML.includes('DomesticRow') && wrap.innerHTML.includes('ForeignRow'), 'expected both rows under All');

    originWrap._listeners.click({ target: { closest: () => ({ dataset: { origin: 'domestic' } }) } });
    assert.ok(wrap.innerHTML.includes('DomesticRow'), 'expected DomesticRow under Domestic');
    assert.ok(!wrap.innerHTML.includes('ForeignRow'), 'expected ForeignRow hidden under Domestic');

    originWrap._listeners.click({ target: { closest: () => ({ dataset: { origin: 'foreign' } }) } });
    assert.ok(!wrap.innerHTML.includes('DomesticRow'), 'expected DomesticRow hidden under Foreign');
    assert.ok(wrap.innerHTML.includes('ForeignRow'), 'expected ForeignRow under Foreign');
  });

  await test('clicking the already-active origin tab does not reset the render', async () => {
    const sb = initWith([makeRow()]);
    await waitForLoad();
    const originWrap = sb.__docStore['node-changes-origin-tabs'];
    const before = originWrap.innerHTML;
    originWrap._listeners.click({ target: { closest: () => ({ dataset: { origin: 'all' } }) } });
    assert.strictEqual(originWrap.innerHTML, before, 'expected no re-render for a no-op origin click');
  });

  // Role checkboxes — same "built from what's present, all checked by
  // default" convention as New Nodes' role filter.
  await test('role checkboxes render one per distinct role present, all checked by default', async () => {
    const sb = initWith([
      makeRow({ publicKey: 'aa'.repeat(32), role: 'repeater' }),
      makeRow({ publicKey: 'bb'.repeat(32), role: 'companion' }),
    ]);
    await waitForLoad();
    const roleWrap = sb.__docStore['node-changes-role-filters'];
    assert.ok(roleWrap.innerHTML.includes('data-role="repeater"'), `expected a repeater checkbox, got: ${roleWrap.innerHTML}`);
    assert.ok(roleWrap.innerHTML.includes('data-role="companion"'), `expected a companion checkbox, got: ${roleWrap.innerHTML}`);
  });

  await test('unchecking a role checkbox hides matching rows without touching others', async () => {
    const sb = initWith([
      makeRow({ publicKey: 'aa'.repeat(32), name: 'RepeaterRow', role: 'repeater' }),
      makeRow({ publicKey: 'bb'.repeat(32), name: 'RoomRow', role: 'room' }),
    ]);
    await waitForLoad();
    const roleWrap = sb.__docStore['node-changes-role-filters'];
    const wrap = sb.__docStore['node-changes-table-wrap'];
    assert.ok(wrap.innerHTML.includes('RepeaterRow') && wrap.innerHTML.includes('RoomRow'), 'expected both rows before unchecking');

    roleWrap._listeners.change({ target: { dataset: { role: 'room' }, checked: false } });
    assert.ok(wrap.innerHTML.includes('RepeaterRow'), 'expected the repeater row to remain');
    assert.ok(!wrap.innerHTML.includes('RoomRow'), 'expected the room row to be hidden after unchecking Room Server');

    const status = sb.__docStore['node-changes-status'];
    assert.ok(status.textContent.includes('(filtered)'), `expected the status line to note filtering, got: ${status.textContent}`);
  });

  await test('the table shows a Role column with the resolved role label', async () => {
    const sb = initWith([makeRow({ role: 'companion' })]);
    await waitForLoad();
    const wrap = sb.__docStore['node-changes-table-wrap'];
    assert.ok(wrap.innerHTML.includes('Companion'), `expected the Companion role label in the table, got: ${wrap.innerHTML}`);
  });

  await test('roleLabel maps known roles to display names and passes through unknown ones', () => {
    const sb = createSandbox([]);
    const rl = sb.window.NodeChangesTool.roleLabel;
    assert.strictEqual(rl('repeater'), 'Repeater');
    assert.strictEqual(rl('room'), 'Room Server');
    assert.strictEqual(rl('companion'), 'Companion');
    assert.strictEqual(rl('mystery-role'), 'mystery-role');
  });

  await test('sortValue: role is lowercased', () => {
    const sb = createSandbox([]);
    const sv = sb.window.NodeChangesTool.sortValue;
    assert.strictEqual(sv({ role: 'Repeater' }, 'role'), 'repeater');
  });

  await test('typeLabel maps known types to display names and passes through unknown ones', () => {
    const sb = createSandbox([]);
    const tl = sb.window.NodeChangesTool.typeLabel;
    assert.strictEqual(tl('role'), 'Role');
    assert.strictEqual(tl('name'), 'Name');
    assert.strictEqual(tl('position'), 'Position');
    assert.strictEqual(tl('resurrected'), 'Returned');
    assert.strictEqual(tl('mystery-type'), 'mystery-type');
  });

  await test('sortValue: name falls back to pubkey when unresolved, lowercased', () => {
    const sb = createSandbox([]);
    const sv = sb.window.NodeChangesTool.sortValue;
    assert.strictEqual(sv({ name: 'ChangedNode' }, 'name'), 'changednode');
    assert.strictEqual(sv({ publicKey: 'AABBCC' }, 'name'), 'aabbcc');
  });

  await test('the default-sorted column (Detected) header carries sort-active', async () => {
    const sb = initWith([makeRow(), makeRow({ id: 2, publicKey: 'cc'.repeat(32), name: 'Other' })]);
    await waitForLoad();
    const wrap = sb.__docStore['node-changes-table-wrap'];
    assert.ok(/data-sort-col="detectedAt"[^>]*class="sortable sort-active"|class="sortable sort-active"[^>]*data-sort-col="detectedAt"/.test(wrap.innerHTML),
      `expected Detected header to carry sort-active by default; got: ${wrap.innerHTML.slice(0, 400)}`);
  });

  console.log('\n════════════════════════════════════════');
  console.log(`  Node Changes tool: ${passed} passed, ${failed} failed`);
  console.log('════════════════════════════════════════');
  process.exit(failed === 0 ? 0 : 1);
})();

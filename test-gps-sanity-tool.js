// test-gps-sanity-tool.js — vm.createContext sandbox tests for
// public/gps-sanity.js (Tools > Suspicious GPS Positions page).
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

function makeFlaggedNode(overrides) {
  return Object.assign({
    publicKey: 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
    name: 'BadGPSNode',
    lat: 60.0, lon: 20.0,
    clusterLat: 55.0, clusterLon: 10.0,
    distanceKm: 700.5,
    clusterSpreadKm: 11.1,
    clusterSize: 2,
  }, overrides);
}

function makeGPSSanityResponse(overrides) {
  return Object.assign({
    nodes: [makeFlaggedNode()],
    totalRealGps: 100,
    evaluated: 60,
  }, overrides);
}

function createSandbox(fixture) {
  const docStore = {};
  const resizeCalls = [];
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
    location: { hash: '#/tools/gps-sanity' },
    api: (path) => Promise.resolve(typeof fixture === 'function' ? fixture(path) : fixture),
    registerPage: function () {},
    console: console,
    encodeURIComponent: encodeURIComponent,
    __docStore: docStore,
    __resizeCalls: resizeCalls,
    makeColumnsResizable: function (selector, storageKey) { resizeCalls.push({ selector, storageKey }); },
  };
  sandbox.self = sandbox;
  sandbox.globalThis = sandbox;
  const ctx = vm.createContext(sandbox);
  const src = fs.readFileSync(__dirname + '/public/gps-sanity.js', 'utf8');
  vm.runInContext(src, ctx);
  return sandbox;
}

function initWith(fixture) {
  const sb = createSandbox(fixture);
  const container = { innerHTML: '' };
  sb.window.GPSSanityTool.init(container);
  sb.__container = container;
  return sb;
}

function waitForLoad() {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

(async () => {
  console.log('\n=== gps-sanity.js: Suspicious GPS Positions tool page ===');

  await test('window.GPSSanityTool exists with init/destroy/sortValue', () => {
    const sb = createSandbox(makeGPSSanityResponse());
    assert.strictEqual(typeof sb.window.GPSSanityTool.init, 'function');
    assert.strictEqual(typeof sb.window.GPSSanityTool.destroy, 'function');
    assert.strictEqual(typeof sb.window.GPSSanityTool.sortValue, 'function');
  });

  await test('shows a neutral message when no nodes are flagged', async () => {
    const sb = initWith(makeGPSSanityResponse({ nodes: [] }));
    await waitForLoad();
    const wrap = sb.__docStore['gps-sanity-table-wrap'];
    assert.ok(wrap.innerHTML.includes('No suspicious GPS positions found'), `got: ${wrap.innerHTML}`);
  });

  await test('status line reports total real-GPS and evaluated counts', async () => {
    const sb = initWith(makeGPSSanityResponse({ totalRealGps: 250, evaluated: 140 }));
    await waitForLoad();
    const status = sb.__docStore['gps-sanity-status'];
    assert.ok(status.textContent.includes('250'), `got: ${status.textContent}`);
    assert.ok(status.textContent.includes('140'), `got: ${status.textContent}`);
  });

  await test('renders a flagged row with node link, own position, cluster consensus, distance, and cluster size', async () => {
    const sb = initWith(makeGPSSanityResponse({
      nodes: [makeFlaggedNode({ name: 'AirBorn Mesh 2', lat: 50.0672, lon: 18.5077, clusterLat: 56.1185, clusterLon: 14.5156, distanceKm: 723.5, clusterSize: 2 })],
    }));
    await waitForLoad();
    const wrap = sb.__docStore['gps-sanity-table-wrap'];
    assert.ok(wrap.innerHTML.includes('href="#/nodes/' + 'aa'.repeat(32) + '"'), 'expected a node link');
    assert.ok(wrap.innerHTML.includes('AirBorn Mesh 2'), 'expected the node name');
    assert.ok(wrap.innerHTML.includes('50.0672, 18.5077'), 'expected the node\'s own reported position');
    assert.ok(wrap.innerHTML.includes('56.1185, 14.5156'), 'expected the cluster centroid position');
    assert.ok(wrap.innerHTML.includes('723.5 km'), `expected the distance, got: ${wrap.innerHTML}`);
    assert.ok(wrap.innerHTML.includes('>2<'), 'expected the cluster size');
  });

  await test('a node with no known name falls back to truncated pubkey', async () => {
    const sb = initWith(makeGPSSanityResponse({ nodes: [makeFlaggedNode({ name: '' })] }));
    await waitForLoad();
    const wrap = sb.__docStore['gps-sanity-table-wrap'];
    assert.ok(wrap.innerHTML.includes('aaaaaaaa'), 'expected a truncated pubkey fallback');
  });

  await test('the default-sorted column (Distance) header carries sort-active', async () => {
    const sb = initWith(makeGPSSanityResponse({ nodes: [makeFlaggedNode(), makeFlaggedNode({ publicKey: 'bb'.repeat(32) })] }));
    await waitForLoad();
    const wrap = sb.__docStore['gps-sanity-table-wrap'];
    assert.ok(/data-sort-col="distanceKm"[^>]*class="sortable sort-active"|class="sortable sort-active"[^>]*data-sort-col="distanceKm"/.test(wrap.innerHTML),
      `expected Distance header to carry sort-active by default; got: ${wrap.innerHTML.slice(0, 400)}`);
  });

  await test('filter matches on node name and pubkey', async () => {
    const sb = initWith(makeGPSSanityResponse({
      nodes: [
        makeFlaggedNode({ publicKey: 'aa'.repeat(32), name: 'FirstNode' }),
        makeFlaggedNode({ publicKey: 'bb'.repeat(32), name: 'SecondNode' }),
      ],
    }));
    await waitForLoad();
    const filterInput = sb.__docStore['gps-sanity-filter'];
    filterInput.value = 'secondnode';
    filterInput._listeners.input();
    const wrap = sb.__docStore['gps-sanity-table-wrap'];
    assert.ok(wrap.innerHTML.includes('SecondNode'), 'expected SecondNode to match');
    assert.ok(!wrap.innerHTML.includes('FirstNode'), 'expected FirstNode to be filtered out');
  });

  await test('filtering updates the table-level count without clobbering the top-level summary', async () => {
    const sb = initWith(makeGPSSanityResponse({
      totalRealGps: 250, evaluated: 140,
      nodes: [
        makeFlaggedNode({ publicKey: 'aa'.repeat(32), name: 'FirstNode' }),
        makeFlaggedNode({ publicKey: 'bb'.repeat(32), name: 'SecondNode' }),
      ],
    }));
    await waitForLoad();
    const filterInput = sb.__docStore['gps-sanity-filter'];
    filterInput.value = 'secondnode';
    filterInput._listeners.input();
    const tableStatus = sb.__docStore['gps-sanity-table-status'];
    const topStatus = sb.__docStore['gps-sanity-status'];
    assert.ok(tableStatus.textContent.includes('1 of 2'), `expected the table-level filtered count, got: ${tableStatus.textContent}`);
    assert.ok(topStatus.textContent.includes('250') && topStatus.textContent.includes('140'), `expected the top-level summary to survive filtering, got: ${topStatus.textContent}`);
  });

  await test('sortValue: name falls back to pubkey when unresolved, lowercased', () => {
    const sb = createSandbox(makeGPSSanityResponse());
    const sv = sb.window.GPSSanityTool.sortValue;
    assert.strictEqual(sv({ name: 'BadGPSNode' }, 'name'), 'badgpsnode');
    assert.strictEqual(sv({ publicKey: 'AABBCC' }, 'name'), 'aabbcc');
  });

  await test('wires makeColumnsResizable with the table selector and a dedicated storage key', async () => {
    const sb = initWith(makeGPSSanityResponse());
    await waitForLoad();
    assert.strictEqual(sb.__resizeCalls.length, 1, `expected exactly one makeColumnsResizable call, got: ${JSON.stringify(sb.__resizeCalls)}`);
    assert.strictEqual(sb.__resizeCalls[0].selector, '#gps-sanity-table');
    assert.ok(sb.__resizeCalls[0].storageKey && sb.__resizeCalls[0].storageKey.indexOf('gps-sanity') !== -1,
      `expected a gps-sanity-specific storage key, got: ${sb.__resizeCalls[0].storageKey}`);
  });

  await test('a failed load shows an error message, not a stuck loading state', async () => {
    const sb = initWith(() => Promise.reject(new Error('boom')));
    await waitForLoad();
    const status = sb.__docStore['gps-sanity-status'];
    const content = sb.__docStore['gps-sanity-content'];
    assert.ok(status.textContent.includes('Failed to load'), `expected an error message, got: ${status.textContent}`);
    assert.ok(!content.innerHTML.includes('Loading'), 'should not still show the loading placeholder after failure');
  });

  console.log('\n════════════════════════════════════════');
  console.log(`  Suspicious GPS Positions tool: ${passed} passed, ${failed} failed`);
  console.log('════════════════════════════════════════');
  process.exit(failed === 0 ? 0 : 1);
})();

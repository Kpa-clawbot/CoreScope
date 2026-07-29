// test-position-gaps-tool.js — vm.createContext sandbox tests for
// public/position-gaps.js (Tools > Position-Fix Coverage Gaps page).
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

function makeAreaGap(overrides) {
  return Object.assign({ areaKey: 'AREAA', label: 'Area A', realFix: 8, approximated: 2 }, overrides);
}

function makeEstimatedNode(overrides) {
  return Object.assign({
    publicKey: 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
    name: 'EstimatedNode',
    areaKey: 'AREAA',
    label: 'Area A',
    lat: 56.0,
    lon: 10.0,
    contributorCount: 3,
    spreadKm: 1.2,
  }, overrides);
}

function makeAreasResponse(overrides) {
  return Object.assign({
    density: [],
    bridgeNodes: [],
    positionGaps: [makeAreaGap()],
    unpositionedTotal: 5,
    unpositionedNoNeighborFix: 1,
    estimatedNodes: [makeEstimatedNode()],
  }, overrides);
}

function createSandbox(areasFixture) {
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
    location: { hash: '#/tools/position-gaps' },
    api: (path) => Promise.resolve(typeof areasFixture === 'function' ? areasFixture(path) : areasFixture),
    URLSearchParams: URLSearchParams,
    registerPage: function () {},
    timeAgo: (iso) => 'TIME_AGO(' + iso + ')',
    encodeURIComponent: encodeURIComponent,
    console: console,
    __docStore: docStore,
    __resizeCalls: resizeCalls,
    makeColumnsResizable: function (selector, storageKey) { resizeCalls.push({ selector, storageKey }); },
  };
  sandbox.self = sandbox;
  sandbox.globalThis = sandbox;
  const ctx = vm.createContext(sandbox);
  const src = fs.readFileSync(__dirname + '/public/position-gaps.js', 'utf8');
  vm.runInContext(src, ctx);
  return sandbox;
}

function initWith(areasFixture) {
  const sb = createSandbox(areasFixture);
  const container = { innerHTML: '' };
  sb.window.PositionGapsTool.init(container);
  sb.__container = container;
  return sb;
}

function waitForLoad() {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

(async () => {
  console.log('\n=== position-gaps.js: Position-Fix Coverage Gaps tool page ===');

  await test('window.PositionGapsTool exists with init/destroy/sortValue', () => {
    const sb = createSandbox(makeAreasResponse());
    assert.strictEqual(typeof sb.window.PositionGapsTool.init, 'function');
    assert.strictEqual(typeof sb.window.PositionGapsTool.destroy, 'function');
    assert.strictEqual(typeof sb.window.PositionGapsTool.sortValue, 'function');
  });

  await test('shows a neutral message when no areas are configured', async () => {
    const sb = initWith(makeAreasResponse({ positionGaps: [], estimatedNodes: [] }));
    await waitForLoad();
    const content = sb.__docStore['position-gaps-content'];
    assert.ok(content.innerHTML.includes('No Areas are configured'), `got: ${content.innerHTML}`);
  });

  await test('renders the area breakdown table with real/estimated counts and %', async () => {
    const sb = initWith(makeAreasResponse({ positionGaps: [makeAreaGap({ label: 'Area A', realFix: 8, approximated: 2 })] }));
    await waitForLoad();
    const content = sb.__docStore['position-gaps-content'];
    assert.ok(content.innerHTML.includes('Area A'), 'expected the area label');
    assert.ok(content.innerHTML.includes('20.0%'), `expected 20.0% estimated, got: ${content.innerHTML}`);
  });

  await test('area breakdown sorts worst (highest %% estimated) first', async () => {
    const sb = initWith(makeAreasResponse({
      positionGaps: [
        makeAreaGap({ label: 'MostlyReal', realFix: 19, approximated: 1 }),
        makeAreaGap({ label: 'MostlyGuessed', realFix: 1, approximated: 9 }),
      ],
    }));
    await waitForLoad();
    const content = sb.__docStore['position-gaps-content'];
    const iGuessed = content.innerHTML.indexOf('MostlyGuessed');
    const iReal = content.innerHTML.indexOf('MostlyReal');
    assert.ok(iGuessed !== -1 && iReal !== -1 && iGuessed < iReal, `expected MostlyGuessed (higher %% estimated) to render first, got: ${content.innerHTML}`);
  });

  await test('collapses the area table to 10 with a "Show all" toggle when there are more', async () => {
    const many = Array.from({ length: 13 }, (_, i) => makeAreaGap({ areaKey: 'A' + i, label: 'Area ' + i, realFix: 1, approximated: i }));
    const sb = initWith(makeAreasResponse({ positionGaps: many }));
    await waitForLoad();
    const content = sb.__docStore['position-gaps-content'];
    assert.ok(content.innerHTML.includes('Show all 13 areas'), `expected a "Show all 13 areas" toggle, got: ${content.innerHTML}`);
  });

  await test('clicking "Show all" on the area table expands it', async () => {
    const many = Array.from({ length: 13 }, (_, i) => makeAreaGap({ areaKey: 'A' + i, label: 'Area ' + i, realFix: 1, approximated: 13 - i }));
    const sb = initWith(makeAreasResponse({ positionGaps: many }));
    await waitForLoad();
    const content = sb.__docStore['position-gaps-content'];
    assert.ok(!content.innerHTML.includes('Area 12'), 'expected Area 12 collapsed away initially');
    content._listeners.click({ target: { closest: (sel) => (sel === '[data-area-toggle]' ? { dataset: {} } : null) } });
    const areaTable = sb.__docStore['position-gaps-area-table'];
    assert.ok(areaTable.innerHTML.includes('Area 12'), `expected Area 12 to appear after expanding, got: ${areaTable.innerHTML}`);
  });

  await test('renders a "View on Map" link to #/map?estimatedNodes=1 with the estimated count', async () => {
    const sb = initWith(makeAreasResponse({ estimatedNodes: [makeEstimatedNode(), makeEstimatedNode({ publicKey: 'bb'.repeat(32) })] }));
    await waitForLoad();
    const content = sb.__docStore['position-gaps-content'];
    assert.ok(content.innerHTML.includes('href="#/map?estimatedNodes=1"'), `expected a map link, got: ${content.innerHTML}`);
    assert.ok(content.innerHTML.includes('View on Map (2)'), `expected the count in the link label, got: ${content.innerHTML}`);
  });

  await test('omits the map link when there are no estimated nodes', async () => {
    const sb = initWith(makeAreasResponse({ estimatedNodes: [] }));
    await waitForLoad();
    const content = sb.__docStore['position-gaps-content'];
    assert.ok(!content.innerHTML.includes('estimatedNodes=1'), `expected no map link, got: ${content.innerHTML}`);
  });

  await test('renders an estimated-node row with node link, area, contributors, and spread', async () => {
    const sb = initWith(makeAreasResponse({ estimatedNodes: [makeEstimatedNode({ name: 'GuessedNode', contributorCount: 4, spreadKm: 2.345 })] }));
    await waitForLoad();
    const wrap = sb.__docStore['position-gaps-est-table-wrap'];
    assert.ok(wrap.innerHTML.includes('href="#/nodes/' + 'aa'.repeat(32) + '"'), 'expected a node link');
    assert.ok(wrap.innerHTML.includes('GuessedNode'), 'expected the node name');
    assert.ok(wrap.innerHTML.includes('Area A'), 'expected the area badge');
    assert.ok(wrap.innerHTML.includes('>4<'), `expected the contributor count, got: ${wrap.innerHTML}`);
    assert.ok(wrap.innerHTML.includes('2.3 km'), `expected the rounded spread, got: ${wrap.innerHTML}`);
  });

  await test('a node with no known name falls back to truncated pubkey', async () => {
    const sb = initWith(makeAreasResponse({ estimatedNodes: [makeEstimatedNode({ name: '' })] }));
    await waitForLoad();
    const wrap = sb.__docStore['position-gaps-est-table-wrap'];
    assert.ok(wrap.innerHTML.includes('aaaaaaaa'), 'expected a truncated pubkey fallback');
  });

  await test('empty estimated-nodes list shows a neutral message', async () => {
    const sb = initWith(makeAreasResponse({ estimatedNodes: [] }));
    await waitForLoad();
    const wrap = sb.__docStore['position-gaps-est-table-wrap'];
    assert.ok(wrap.innerHTML.includes('No node currently needs'), `got: ${wrap.innerHTML}`);
  });

  await test('status line reports unpositioned totals, including the no-neighbor-fix caveat', async () => {
    const sb = initWith(makeAreasResponse({ unpositionedTotal: 7, unpositionedNoNeighborFix: 3 }));
    await waitForLoad();
    const status = sb.__docStore['position-gaps-status'];
    assert.ok(status.textContent.includes('7 nodes'), `got: ${status.textContent}`);
    assert.ok(status.textContent.includes('3 also have no positioned neighbor'), `got: ${status.textContent}`);
  });

  await test('status line omits the no-neighbor-fix caveat when it is zero', async () => {
    const sb = initWith(makeAreasResponse({ unpositionedTotal: 4, unpositionedNoNeighborFix: 0 }));
    await waitForLoad();
    const status = sb.__docStore['position-gaps-status'];
    assert.ok(!status.textContent.includes('also have no positioned neighbor'), `got: ${status.textContent}`);
  });

  await test('filter matches on node name, pubkey, and area', async () => {
    const sb = initWith(makeAreasResponse({
      estimatedNodes: [
        makeEstimatedNode({ publicKey: 'aa'.repeat(32), name: 'FirstNode', label: 'Area A' }),
        makeEstimatedNode({ publicKey: 'bb'.repeat(32), name: 'SecondNode', label: 'Area B' }),
      ],
    }));
    await waitForLoad();
    const filterInput = sb.__docStore['position-gaps-filter'];
    filterInput.value = 'secondnode';
    filterInput._listeners.input();
    const wrap = sb.__docStore['position-gaps-est-table-wrap'];
    assert.ok(wrap.innerHTML.includes('SecondNode'), 'expected SecondNode to match');
    assert.ok(!wrap.innerHTML.includes('FirstNode'), 'expected FirstNode to be filtered out');
  });

  await test('sortValue: name falls back to pubkey when unresolved, lowercased', () => {
    const sb = createSandbox(makeAreasResponse());
    const sv = sb.window.PositionGapsTool.sortValue;
    assert.strictEqual(sv({ name: 'GuessedNode' }, 'name'), 'guessednode');
    assert.strictEqual(sv({ publicKey: 'AABBCC' }, 'name'), 'aabbcc');
  });

  await test('the default-sorted column (Spread) header carries sort-active', async () => {
    const sb = initWith(makeAreasResponse({ estimatedNodes: [makeEstimatedNode(), makeEstimatedNode({ publicKey: 'cc'.repeat(32) })] }));
    await waitForLoad();
    const wrap = sb.__docStore['position-gaps-est-table-wrap'];
    assert.ok(/data-sort-col="spreadKm"[^>]*class="sortable sort-active"|class="sortable sort-active"[^>]*data-sort-col="spreadKm"/.test(wrap.innerHTML),
      `expected Spread header to carry sort-active by default; got: ${wrap.innerHTML.slice(0, 400)}`);
  });

  await test('wires makeColumnsResizable with the table selector and a dedicated storage key', async () => {
    const sb = initWith(makeAreasResponse());
    await waitForLoad();
    assert.strictEqual(sb.__resizeCalls.length, 1, `expected exactly one makeColumnsResizable call, got: ${JSON.stringify(sb.__resizeCalls)}`);
    assert.strictEqual(sb.__resizeCalls[0].selector, '#position-gaps-est-table');
    assert.ok(sb.__resizeCalls[0].storageKey && sb.__resizeCalls[0].storageKey.indexOf('position-gaps') !== -1,
      `expected a position-gaps-specific storage key, got: ${sb.__resizeCalls[0].storageKey}`);
  });

  await test('a failed load shows an error message, not a stuck loading state', async () => {
    const sb = initWith(() => Promise.reject(new Error('boom')));
    await waitForLoad();
    const status = sb.__docStore['position-gaps-status'];
    const content = sb.__docStore['position-gaps-content'];
    assert.ok(status.textContent.includes('Failed to load'), `expected an error message, got: ${status.textContent}`);
    assert.ok(!content.innerHTML.includes('Loading'), 'should not still show the loading placeholder after failure');
  });

  console.log('\n════════════════════════════════════════');
  console.log(`  Position-Fix Coverage Gaps tool: ${passed} passed, ${failed} failed`);
  console.log('════════════════════════════════════════');
  process.exit(failed === 0 ? 0 : 1);
})();

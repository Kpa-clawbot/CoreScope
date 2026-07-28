/**
 * DOM-rendering tests for the "Areas" Analytics tab
 * (renderAreasTab, public/analytics.js).
 *
 * Drives the real render function against a stubbed api() that returns a
 * fixed /api/analytics/areas response, asserting rendered tables, empty
 * states, and the 60s auto-refresh timer lifecycle — same harness style
 * as test-analytics-wardriving-tab.js.
 */
'use strict';

const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

let passed = 0, failed = 0;
async function testAsync(name, fn) {
  try {
    await fn();
    passed++;
    console.log(`  ✅ ${name}`);
  } catch (e) {
    failed++;
    console.log(`  ❌ ${name}: ${e.message}`);
  }
}

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
    setTimeout: () => {}, clearTimeout: () => {},
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
  // Spies (not just no-ops) so the timer-lifecycle test can verify a
  // real interval got registered AND really cleared.
  let nextIntervalId = 1;
  const liveIntervalIds = new Set();
  const clearedIntervalIds = [];
  ctx.__liveIntervalIds = liveIntervalIds;
  ctx.__clearedIntervalIds = clearedIntervalIds;
  ctx.setInterval = function () {
    const id = nextIntervalId++;
    liveIntervalIds.add(id);
    return id;
  };
  ctx.clearInterval = function (id) {
    liveIntervalIds.delete(id);
    clearedIntervalIds.push(id);
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

function makeAnalyticsSandbox(apiStub) {
  const ctx = makeSandbox();
  ctx.getComputedStyle = () => ({ getPropertyValue: () => '' });
  ctx.registerPage = () => {};
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
  ctx.api = apiStub || (() => Promise.resolve({}));
  try { loadInCtx(ctx, 'public/analytics.js'); } catch (e) {
    for (const k of Object.keys(ctx.window)) ctx[k] = ctx.window[k];
  }
  return ctx;
}

function fakeEl() {
  return { innerHTML: '', querySelector: () => null, querySelectorAll: () => [] };
}

function makeAreasResponse(overrides) {
  return Object.assign({
    density: [
      { areaKey: 'ODE', label: 'Odense by', total: 5, active: 3, degraded: 1, silent: 1, roleCounts: { repeater: 2, client: 3 } },
      { areaKey: 'DK', label: 'Danmark (alle)', total: 8, active: 5, degraded: 2, silent: 1, roleCounts: { repeater: 3, client: 5 } },
    ],
    bridgeNodes: [
      { publicKey: 'pkbridge01', name: 'BridgeNode', areaKey: 'ODE', label: 'Odense by', edgeCount: 4, otherAreaCount: 2, otherAreas: ['Danmark (alle)', 'Jylland'] },
    ],
    positionGaps: [
      { areaKey: 'ODE', label: 'Odense by', realFix: 4, approximated: 1 },
    ],
    unpositionedTotal: 3,
    unpositionedNoNeighborFix: 1,
    estimatedNodes: [
      { publicKey: 'pkest01', name: 'EstimatedNode1', areaKey: 'ODE', label: 'Odense by', lat: 55.4, lon: 10.4, contributorCount: 3, spreadKm: 1.2 },
    ],
  }, overrides);
}

function makeApiStub(resp) {
  return function (path) {
    if (path.indexOf('/analytics/areas') === 0) return Promise.resolve(resp);
    return Promise.resolve({});
  };
}

(async () => {
  console.log('\n=== analytics.js: renderAreasTab ===');

  await testAsync('renders the Node Density & Health table from the API response', async () => {
    const ctx = makeAnalyticsSandbox(makeApiStub(makeAreasResponse()));
    const el = fakeEl();
    await ctx.window._analyticsRenderAreasTab(el);
    assert.ok(el.innerHTML.includes('Node Density &amp; Health by Area'), 'density section heading should render');
    assert.ok(el.innerHTML.includes('Odense by'), 'ODE area label should render');
    assert.ok(el.innerHTML.includes('Danmark (alle)'), 'DK area label should render');
    assert.ok(el.innerHTML.includes('client: 3, repeater: 2'), 'role mix should render sorted alphabetically');
  });

  await testAsync('renders the Cross-Area Bridge Nodes table with a node link and other-areas list', async () => {
    const ctx = makeAnalyticsSandbox(makeApiStub(makeAreasResponse()));
    const el = fakeEl();
    await ctx.window._analyticsRenderAreasTab(el);
    assert.ok(el.innerHTML.includes('Cross-Area Bridge Nodes'), 'bridge section heading should render');
    assert.ok(el.innerHTML.includes('href="#/nodes/pkbridge01"'), 'bridge node should link to its node detail page');
    assert.ok(el.innerHTML.includes('BridgeNode'), 'bridge node display name should render');
    assert.ok(el.innerHTML.includes('Danmark (alle), Jylland'), 'other areas reached should be listed');
  });

  await testAsync('Node Density & Health sorts worst-health-first, not the API\'s Total-desc order', async () => {
    const ctx = makeAnalyticsSandbox(makeApiStub(makeAreasResponse({
      density: [
        { areaKey: 'BIG', label: 'BigHealthy', total: 100, active: 100, degraded: 0, silent: 0, roleCounts: {} },   // 0% unhealthy, biggest
        { areaKey: 'SMALL', label: 'SmallSick', total: 4, active: 1, degraded: 1, silent: 2, roleCounts: {} },      // 75% unhealthy, smallest
        { areaKey: 'MID', label: 'MidSick', total: 10, active: 5, degraded: 3, silent: 2, roleCounts: {} },         // 50% unhealthy
      ],
    })));
    const el = fakeEl();
    await ctx.window._analyticsRenderAreasTab(el);
    const startIdx = el.innerHTML.indexOf('id="areasDensity"');
    const section = el.innerHTML.slice(startIdx);
    const idxSmall = section.indexOf('SmallSick');
    const idxMid = section.indexOf('MidSick');
    const idxBig = section.indexOf('BigHealthy');
    assert.ok(idxSmall > -1 && idxMid > -1 && idxBig > -1, 'all three areas should render');
    assert.ok(idxSmall < idxMid && idxMid < idxBig, 'rows should be ordered worst-health -> best-health, not by Total');
  });

  await testAsync('Node Density & Health collapses to top 10 with a "Show all" toggle', async () => {
    const manyAreas = [];
    for (let i = 0; i < 15; i++) manyAreas.push({ areaKey: 'A' + i, label: 'Area' + i, total: 10, active: 10 - i, degraded: 0, silent: i, roleCounts: {} });
    const ctx = makeAnalyticsSandbox(makeApiStub(makeAreasResponse({ density: manyAreas })));
    const el = fakeEl();
    await ctx.window._analyticsRenderAreasTab(el);
    const startIdx = el.innerHTML.indexOf('id="areasDensity"');
    const section = el.innerHTML.slice(startIdx, el.innerHTML.indexOf('id="areasBridgeNodes"'));
    const tbody = section.slice(section.indexOf('<tbody>'), section.indexOf('</tbody>'));
    const rowCount = (tbody.match(/<tr>/g) || []).length;
    assert.strictEqual(rowCount, 10, 'only 10 rows should render by default');
    assert.ok(section.includes('Show all 15 areas'), 'a "Show all" toggle should appear when there are more than 10 areas');
  });

  await testAsync('Cross-Area Bridge Nodes keeps the API\'s otherAreaCount-desc order and collapses to top 10', async () => {
    const manyBridges = [];
    for (let i = 0; i < 12; i++) manyBridges.push({ publicKey: 'pk' + i, name: 'Bridge' + i, areaKey: 'A', label: 'AreaA', edgeCount: 5, otherAreaCount: 12 - i, otherAreas: ['AreaB'] });
    const ctx = makeAnalyticsSandbox(makeApiStub(makeAreasResponse({ bridgeNodes: manyBridges })));
    const el = fakeEl();
    await ctx.window._analyticsRenderAreasTab(el);
    const startIdx = el.innerHTML.indexOf('id="areasBridgeNodes"');
    const section = el.innerHTML.slice(startIdx, el.innerHTML.indexOf('id="areasPositionGaps"'));
    const idxFirst = section.indexOf('Bridge0');
    const idxLast = section.indexOf('Bridge9');
    assert.ok(idxFirst > -1 && idxFirst < idxLast, 'highest otherAreaCount (Bridge0) should render before a lower one (Bridge9)');
    const tbody = section.slice(section.indexOf('<tbody>'), section.indexOf('</tbody>'));
    const rowCount = (tbody.match(/<tr>/g) || []).length;
    assert.strictEqual(rowCount, 10, 'only 10 rows should render by default');
    assert.ok(section.includes('Show all 12 nodes'), 'a "Show all" toggle should appear when there are more than 10 bridge nodes');
  });

  await testAsync('numeric column headers are right-aligned to match their right-aligned data cells, in all three tables', async () => {
    // dborup: numbers didn't line up under their headers -- td cells had
    // text-align:right but the matching th headers didn't, so header text
    // sat flush-left while the numbers under it sat flush-right.
    const ctx = makeAnalyticsSandbox(makeApiStub(makeAreasResponse()));
    const el = fakeEl();
    await ctx.window._analyticsRenderAreasTab(el);

    function countRightAligned(html, startMarker, endMarker) {
      const start = html.indexOf(startMarker);
      const end = endMarker ? html.indexOf(endMarker) : html.length;
      const section = html.slice(start, end);
      const thead = section.slice(section.indexOf('<thead>'), section.indexOf('</thead>'));
      const tbodyStart = section.indexOf('<tbody>');
      const firstRow = section.slice(tbodyStart, section.indexOf('</tr>', tbodyStart));
      const theadRight = (thead.match(/text-align:right/g) || []).length;
      const rowRight = (firstRow.match(/text-align:right/g) || []).length;
      return { theadRight, rowRight };
    }

    const density = countRightAligned(el.innerHTML, 'id="areasDensity"', 'id="areasBridgeNodes"');
    assert.strictEqual(density.theadRight, density.rowRight, `Density: ${density.theadRight} right-aligned headers vs ${density.rowRight} right-aligned cells in the first row`);

    const bridge = countRightAligned(el.innerHTML, 'id="areasBridgeNodes"', 'id="areasPositionGaps"');
    assert.strictEqual(bridge.theadRight, bridge.rowRight, `Bridge Nodes: ${bridge.theadRight} right-aligned headers vs ${bridge.rowRight} right-aligned cells in the first row`);

    const gaps = countRightAligned(el.innerHTML, 'id="areasPositionGaps"', null);
    assert.strictEqual(gaps.theadRight, gaps.rowRight, `Position Gaps: ${gaps.theadRight} right-aligned headers vs ${gaps.rowRight} right-aligned cells in the first row`);
  });

  await testAsync('renders the Position-Fix Coverage Gaps table with a computed percentage', async () => {
    const ctx = makeAnalyticsSandbox(makeApiStub(makeAreasResponse()));
    const el = fakeEl();
    await ctx.window._analyticsRenderAreasTab(el);
    assert.ok(el.innerHTML.includes('Position-Fix Coverage Gaps by Area'), 'position gaps section heading should render');
    // 1 approximated of (4 real + 1 approximated) = 20.0%
    assert.ok(el.innerHTML.includes('20.0%'), 'ODE row should show 20.0% estimated');
  });

  await testAsync('all three tables mark scalar columns sortable with data-sort-col, and leave free-text columns (Role Mix, Which Areas) unsortable', async () => {
    const ctx = makeAnalyticsSandbox(makeApiStub(makeAreasResponse()));
    const el = fakeEl();
    await ctx.window._analyticsRenderAreasTab(el);

    const density = el.innerHTML.slice(el.innerHTML.indexOf('id="areasDensity"'), el.innerHTML.indexOf('id="areasBridgeNodes"'));
    for (const col of ['area', 'total', 'active', 'degraded', 'silent']) {
      assert.ok(density.includes('data-sort-col="' + col + '"'), `Density should have a sortable "${col}" column`);
    }
    const densityThead = density.slice(density.indexOf('<thead>'), density.indexOf('</thead>'));
    assert.ok(!densityThead.includes('data-sort-col="roleMix"'), 'Density Role Mix header should not be sortable (free-text breakdown)');

    const bridge = el.innerHTML.slice(el.innerHTML.indexOf('id="areasBridgeNodes"'), el.innerHTML.indexOf('id="areasPositionGaps"'));
    for (const col of ['node', 'homeArea', 'edgeCount', 'otherAreaCount']) {
      assert.ok(bridge.includes('data-sort-col="' + col + '"'), `Bridge Nodes should have a sortable "${col}" column`);
    }
    const bridgeThead = bridge.slice(bridge.indexOf('<thead>'), bridge.indexOf('</thead>'));
    assert.ok(!bridgeThead.includes('data-sort-col="whichAreas"'), 'Bridge Nodes Which Areas header should not be sortable (free-text list)');

    const gaps = el.innerHTML.slice(el.innerHTML.indexOf('id="areasPositionGaps"'));
    for (const col of ['area', 'realFix', 'approximated', 'pctEstimated']) {
      assert.ok(gaps.includes('data-sort-col="' + col + '"'), `Position Gaps should have a sortable "${col}" column`);
    }
  });

  await testAsync('the pre-click default sort highlights a real header (sort-active + down arrow) on Bridge Nodes and Position Gaps, but no header on Density (composite default)', async () => {
    const ctx = makeAnalyticsSandbox(makeApiStub(makeAreasResponse()));
    const el = fakeEl();
    await ctx.window._analyticsRenderAreasTab(el);

    const density = el.innerHTML.slice(el.innerHTML.indexOf('id="areasDensity"'), el.innerHTML.indexOf('id="areasBridgeNodes"'));
    assert.ok(!density.includes('sort-active'), 'Density has no single default column (worst-health is a composite), so no header should start highlighted');

    const bridge = el.innerHTML.slice(el.innerHTML.indexOf('id="areasBridgeNodes"'), el.innerHTML.indexOf('id="areasPositionGaps"'));
    const otherAreaCountTh = bridge.slice(bridge.indexOf('data-sort-col="otherAreaCount"') - 80, bridge.indexOf('data-sort-col="otherAreaCount"') + 200);
    assert.ok(otherAreaCountTh.includes('sort-active'), 'Bridge Nodes\' default sort column (otherAreaCount) should start highlighted');
    assert.ok(otherAreaCountTh.includes('↓'), 'Bridge Nodes\' default direction (desc) should show a down arrow');

    const gaps = el.innerHTML.slice(el.innerHTML.indexOf('id="areasPositionGaps"'));
    const pctTh = gaps.slice(gaps.indexOf('data-sort-col="pctEstimated"') - 80, gaps.indexOf('data-sort-col="pctEstimated"') + 200);
    assert.ok(pctTh.includes('sort-active'), 'Position Gaps\' default sort column (pctEstimated) should start highlighted');
    assert.ok(pctTh.includes('↓'), 'Position Gaps\' default direction (desc) should show a down arrow');
  });

  await testAsync('Position-Fix Coverage Gaps sorts worst-coverage-first, not the API\'s realFix-desc order', async () => {
    const ctx = makeAnalyticsSandbox(makeApiStub(makeAreasResponse({
      positionGaps: [
        { areaKey: 'FULL', label: 'FullyMapped', realFix: 100, approximated: 0 },   // 0% estimated, biggest realFix
        { areaKey: 'WORST', label: 'WorstCoverage', realFix: 2, approximated: 8 },   // 80% estimated, smallest realFix
        { areaKey: 'MID', label: 'MidCoverage', realFix: 10, approximated: 5 },      // ~33% estimated
      ],
    })));
    const el = fakeEl();
    await ctx.window._analyticsRenderAreasTab(el);
    const startIdx = el.innerHTML.indexOf('id="areasPositionGaps"');
    const section = el.innerHTML.slice(startIdx);
    const idxWorst = section.indexOf('WorstCoverage');
    const idxMid = section.indexOf('MidCoverage');
    const idxFull = section.indexOf('FullyMapped');
    assert.ok(idxWorst > -1 && idxMid > -1 && idxFull > -1, 'all three areas should render');
    assert.ok(idxWorst < idxMid && idxMid < idxFull, 'rows should be ordered worst-% -> best-%, not by realFix');
  });

  await testAsync('Position-Fix Coverage Gaps collapses to top 10 with a "Show all" toggle', async () => {
    const manyGaps = [];
    for (let i = 0; i < 15; i++) manyGaps.push({ areaKey: 'A' + i, label: 'Area' + i, realFix: 10, approximated: i });
    const ctx = makeAnalyticsSandbox(makeApiStub(makeAreasResponse({ positionGaps: manyGaps })));
    const el = fakeEl();
    await ctx.window._analyticsRenderAreasTab(el);
    const startIdx = el.innerHTML.indexOf('id="areasPositionGaps"');
    const section = el.innerHTML.slice(startIdx, el.innerHTML.indexOf('nodes network-wide have no real GPS fix'));
    const tbody = section.slice(section.indexOf('<tbody>'), section.indexOf('</tbody>'));
    const rowCount = (tbody.match(/<tr>/g) || []).length;
    assert.strictEqual(rowCount, 10, 'only 10 rows should render by default');
    assert.ok(section.includes('Show all 15 areas'), 'a "Show all" toggle should appear when there are more than 10 areas');
  });

  await testAsync('no "Show all" toggle on the Position-Fix Coverage Gaps table when there are 10 or fewer areas', async () => {
    const ctx = makeAnalyticsSandbox(makeApiStub(makeAreasResponse()));
    const el = fakeEl();
    await ctx.window._analyticsRenderAreasTab(el);
    assert.ok(!el.innerHTML.includes('Show all'), 'no toggle should render with only 1 area in positionGaps');
  });

  await testAsync('renders a "View Estimated Nodes on Map" LINK (not a JS-click button) with the estimated-node count, pointing at a shareable/bookmarkable URL', async () => {
    const ctx = makeAnalyticsSandbox(makeApiStub(makeAreasResponse()));
    const el = fakeEl();
    await ctx.window._analyticsRenderAreasTab(el);
    assert.ok(el.innerHTML.includes('id="areasViewEstimatedNodes"'), 'the View Estimated Nodes link should render');
    assert.ok(el.innerHTML.includes('View Estimated Nodes on Map (1)'), 'the link label should show the estimated-node count');
    // Must be a real <a href> so right-click "Copy Link Address" works and
    // the URL survives a reload -- dborup asked for this after the first
    // version (sessionStorage + a plain button) only ever led to a bare
    // "#/map" with no shareable state.
    assert.ok(el.innerHTML.includes('<a href="#/map?estimatedNodes=1" id="areasViewEstimatedNodes"'), 'expected a real anchor with the estimatedNodes=1 deep-link query param');
  });

  await testAsync('does not render the "View Estimated Nodes on Map" link when estimatedNodes is empty', async () => {
    const ctx = makeAnalyticsSandbox(makeApiStub(makeAreasResponse({ estimatedNodes: [] })));
    const el = fakeEl();
    await ctx.window._analyticsRenderAreasTab(el);
    assert.ok(!el.innerHTML.includes('id="areasViewEstimatedNodes"'), 'the link should not render when there are no estimated nodes to show');
  });

  await testAsync('renders the unpositioned-nodes summary note including the no-neighbor-fix subset', async () => {
    const ctx = makeAnalyticsSandbox(makeApiStub(makeAreasResponse()));
    const el = fakeEl();
    await ctx.window._analyticsRenderAreasTab(el);
    assert.ok(el.innerHTML.includes('3 nodes network-wide have no real GPS fix'), 'unpositioned total should render');
    assert.ok(el.innerHTML.includes('of which 1 also have no positioned neighbor'), 'no-neighbor-fix subset should render');
  });

  await testAsync('shows a neutral empty state when no Areas are configured', async () => {
    const ctx = makeAnalyticsSandbox(makeApiStub({ density: [], bridgeNodes: [], positionGaps: [], unpositionedTotal: 0, unpositionedNoNeighborFix: 0 }));
    const el = fakeEl();
    await ctx.window._analyticsRenderAreasTab(el);
    assert.ok(el.innerHTML.includes('No Areas are configured'), 'should show the no-areas-configured empty state');
  });

  await testAsync('shows a neutral message when bridgeNodes is empty but other sections have data', async () => {
    const ctx = makeAnalyticsSandbox(makeApiStub(makeAreasResponse({ bridgeNodes: [] })));
    const el = fakeEl();
    await ctx.window._analyticsRenderAreasTab(el);
    assert.ok(el.innerHTML.includes('No packet-derived neighbor edges cross between two different areas yet'), 'should show the bridge-nodes empty state');
  });

  await testAsync('shows a friendly message on API failure instead of throwing', async () => {
    const ctx = makeAnalyticsSandbox(function () { return Promise.reject(new Error('network down')); });
    const el = fakeEl();
    await ctx.window._analyticsRenderAreasTab(el);
    assert.ok(el.innerHTML.includes('Failed to load area analytics'), 'should show a failure message');
    assert.ok(el.innerHTML.includes('network down'), 'should include the underlying error');
  });

  await testAsync('rendering registers a real interval, and stop() actually clears it (not a no-op)', async () => {
    const ctx = makeAnalyticsSandbox(makeApiStub(makeAreasResponse()));
    const stop = ctx.window._analyticsStopAreasRefresh;
    assert.strictEqual(typeof stop, 'function', '_stopAreasRefresh must be exported for testing/cleanup');

    stop(); // must not throw when no timer is registered yet
    const el = fakeEl();
    await ctx.window._analyticsRenderAreasTab(el);

    assert.strictEqual(ctx.__liveIntervalIds.size, 1, 'rendering should register exactly one live interval');
    stop();
    assert.strictEqual(ctx.__liveIntervalIds.size, 0, 'stop() should clear the registered interval');
    assert.ok(ctx.__clearedIntervalIds.length >= 1, 'clearInterval should have actually been called');
  });

  console.log('\n════════════════════════════════════════');
  console.log(`  Areas tab: ${passed} passed, ${failed} failed`);
  console.log('════════════════════════════════════════');
  process.exit(failed === 0 ? 0 : 1);
})();

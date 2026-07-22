/**
 * DOM-rendering tests for the "Foreign Traffic" Analytics tab
 * (renderForeignTrafficTab, public/analytics.js).
 *
 * Per bot review on PR #1852 (comment 5012304813): the tab shipped with
 * zero tests, repeating the same TDD-policy violation flagged on a prior
 * round. This seeds a fetchAllNodes() stub with a mix of
 * unscoped_relay_count_24h values and asserts the rendered row order,
 * exclusion rules, and the foreignCount summary note — plus that the
 * 60s auto-refresh timer the bot also flagged as missing now exists and
 * is safely stoppable.
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

// Faithful copy of test-frontend-helpers.js's proven sandbox (same file
// this suite's makeAnalyticsSandbox() pattern is borrowed from) — rolling
// a fresh one by hand tends to miss a global roles.js/app.js touch eagerly
// at load time (window.addEventListener, fetch, etc.).
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
  // Spies (not just no-ops) so tests can verify a timer that was really
  // registered gets really cleared, instead of only asserting stop()
  // doesn't throw — a `function stop(){}` no-op would pass that alone.
  // Defined as closures (not object methods) so they work under the
  // sandboxed code's 'use strict', where a bare `setInterval(...)` call
  // has `this === undefined`.
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

// Roughly Denmark — same box used by test-geo-filter.js / test-nodes-geo-scope-filter.js.
// Foreign-flagged fixture nodes below all use lat/lon OUTSIDE this box: the
// tab classifies foreign live from a node's own lat/lon (nodePassesGeoFilter)
// rather than trusting the stored `foreign` flag, which only reflects nodes
// classified at ADVERT-ingest time and never un-flags — see isForeignNode's
// doc comment in analytics.js.
const GEO_BOX = { latMin: 53, latMax: 59, lonMin: 6, lonMax: 15 };

function makeAnalyticsSandbox(nodesFixture) {
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
  // Override fetchAllNodes (loaded from app.js) with a stub that hands
  // back a fixed node list, instead of exercising its real pagination
  // loop against the stubbed api().
  ctx.fetchAllNodes = async () => ({ nodes: nodesFixture });
  ctx.window.MC_GEO_FILTER = GEO_BOX;
  try { loadInCtx(ctx, 'public/analytics.js'); } catch (e) {
    for (const k of Object.keys(ctx.window)) ctx[k] = ctx.window[k];
  }
  return ctx;
}

function fakeEl() {
  return { innerHTML: '' };
}

(async () => {
  console.log('\n=== analytics.js: renderForeignTrafficTab ===');

  await testAsync('sorts repeaters by unscoped_relay_count_24h descending', async () => {
    const ctx = makeAnalyticsSandbox([
      { public_key: 'pkA', name: 'RepeaterLow', role: 'repeater', unscoped_relay_count_24h: 10, relay_count_24h: 20 },
      { public_key: 'pkB', name: 'RepeaterHigh', role: 'repeater', unscoped_relay_count_24h: 50, relay_count_24h: 50 },
      { public_key: 'pkC', name: 'RoomMid', role: 'room', unscoped_relay_count_24h: 25, relay_count_24h: 30 },
    ]);
    const el = fakeEl();
    await ctx.window._analyticsRenderForeignTrafficTab(el);
    const idxHigh = el.innerHTML.indexOf('RepeaterHigh');
    const idxMid = el.innerHTML.indexOf('RoomMid');
    const idxLow = el.innerHTML.indexOf('RepeaterLow');
    assert.ok(idxHigh > -1 && idxMid > -1 && idxLow > -1, 'all three repeaters should be rendered');
    assert.ok(idxHigh < idxMid && idxMid < idxLow, 'rows should be sorted by unscoped_relay_count_24h descending');
  });

  await testAsync('excludes non-repeater/room roles and zero-unscoped repeaters', async () => {
    const ctx = makeAnalyticsSandbox([
      { public_key: 'pkClient', name: 'NoisyClient', role: 'client', unscoped_relay_count_24h: 999, relay_count_24h: 999 },
      { public_key: 'pkClean', name: 'CleanRepeater', role: 'repeater', unscoped_relay_count_24h: 0, relay_count_24h: 40 },
      { public_key: 'pkDirty', name: 'DirtyRepeater', role: 'repeater', unscoped_relay_count_24h: 5, relay_count_24h: 40 },
    ]);
    const el = fakeEl();
    await ctx.window._analyticsRenderForeignTrafficTab(el);
    assert.ok(!el.innerHTML.includes('NoisyClient'), 'a client-role node must never appear, regardless of its unscoped count');
    assert.ok(!el.innerHTML.includes('CleanRepeater'), 'a repeater with zero unscoped relays must not appear');
    assert.ok(el.innerHTML.includes('DirtyRepeater'), 'a repeater with unscoped relays > 0 must appear');
  });

  await testAsync('shows the empty-state message when no repeater has unscoped relays', async () => {
    const ctx = makeAnalyticsSandbox([
      { public_key: 'pkClean', name: 'CleanRepeater', role: 'repeater', unscoped_relay_count_24h: 0, relay_count_24h: 40 },
    ]);
    const el = fakeEl();
    await ctx.window._analyticsRenderForeignTrafficTab(el);
    assert.ok(el.innerHTML.includes('No repeater has relayed an unscoped flood packet'), 'empty state message should be shown');
  });

  await testAsync('foreignCount note reflects the number of foreign-flagged nodes', async () => {
    const ctx = makeAnalyticsSandbox([
      { public_key: 'pkA', name: 'RepeaterA', role: 'repeater', unscoped_relay_count_24h: 5, relay_count_24h: 5 },
      { public_key: 'pkForeign1', name: 'ForeignNode1', role: 'client', unscoped_relay_count_24h: 0, relay_count_24h: 0, lat: 40.7, lon: -74.0 },
      { public_key: 'pkForeign2', name: 'ForeignNode2', role: 'client', unscoped_relay_count_24h: 0, relay_count_24h: 0, lat: 40.7, lon: -74.0 },
    ]);
    const el = fakeEl();
    await ctx.window._analyticsRenderForeignTrafficTab(el);
    assert.ok(el.innerHTML.includes('2 node(s)'), 'note should mention the count of foreign-flagged nodes (2)');
  });

  await testAsync('foreignCount note falls back to the no-foreign-yet message when none are flagged', async () => {
    const ctx = makeAnalyticsSandbox([
      { public_key: 'pkA', name: 'RepeaterA', role: 'repeater', unscoped_relay_count_24h: 5, relay_count_24h: 5 },
    ]);
    const el = fakeEl();
    await ctx.window._analyticsRenderForeignTrafficTab(el);
    assert.ok(el.innerHTML.includes('no foreign-origin node has advertised'), 'should show the zero-foreign fallback message');
  });

  await testAsync('foreign-flagged nodes list renders every foreign node, sorted newest-heard first', async () => {
    const ctx = makeAnalyticsSandbox([
      { public_key: 'pkA', name: 'RepeaterA', role: 'repeater', unscoped_relay_count_24h: 5, relay_count_24h: 5 },
      { public_key: 'pkOld', name: 'OldForeign', role: 'companion', lat: 44.4, lon: 26.1, first_seen: '2026-07-01T00:00:00Z', last_seen: '2026-07-01T00:00:00Z' },
      { public_key: 'pkNew', name: 'NewForeign', role: 'client', lat: 52.4, lon: 10.8, first_seen: '2026-07-18T00:00:00Z', last_seen: '2026-07-18T12:00:00Z' },
    ]);
    const el = fakeEl();
    await ctx.window._analyticsRenderForeignTrafficTab(el);
    assert.ok(el.innerHTML.includes('Foreign-Flagged Nodes (2)'), 'heading should show the correct count');
    const startIdx = el.innerHTML.indexOf('Foreign-Flagged Nodes');
    const endIdx = el.innerHTML.indexOf('Repeaters Relaying Unscoped Traffic');
    const foreignSectionHtml = el.innerHTML.slice(startIdx, endIdx > -1 ? endIdx : undefined);
    assert.ok(!foreignSectionHtml.includes('RepeaterA'), 'a non-foreign node must not appear in the foreign-nodes section');
    const idxNew = el.innerHTML.indexOf('NewForeign');
    const idxOld = el.innerHTML.indexOf('OldForeign');
    assert.ok(idxNew > -1 && idxOld > -1, 'both foreign nodes should be listed');
    assert.ok(idxNew < idxOld, 'more recently heard foreign node should be listed first');
  });

  await testAsync('foreign-flagged nodes list shows a placeholder when none are flagged yet', async () => {
    const ctx = makeAnalyticsSandbox([
      { public_key: 'pkA', name: 'RepeaterA', role: 'repeater', unscoped_relay_count_24h: 5, relay_count_24h: 5 },
    ]);
    const el = fakeEl();
    await ctx.window._analyticsRenderForeignTrafficTab(el);
    assert.ok(el.innerHTML.includes('Foreign-Flagged Nodes (0)'), 'heading should show a zero count');
    assert.ok(el.innerHTML.includes('None yet'), 'placeholder message should be shown when no node is foreign-flagged');
  });

  await testAsync('classifies live from lat/lon, ignoring a stale `foreign` DB flag either direction (Bornholm regression)', async () => {
    // Real case found on stg.meshview.dk: a Bornholm repeater at the
    // real coordinates below stayed flagged foreign=true forever because
    // the ingestor's foreign_advert flag is written once from an ADVERT
    // and never cleared (cmd/ingestor/db.go MarkNodeForeign) — even after
    // the geo_filter box widened to include it and it sent fresh
    // adverts. The tab must classify from the node's CURRENT lat/lon,
    // not that stored flag, in both directions: a stale foreign=true
    // inside the box must NOT show as foreign, and a node with no flag
    // at all but real out-of-box coordinates MUST show as foreign.
    const ctx = makeAnalyticsSandbox([
      { public_key: 'pkStaleFlag', name: 'DK_BORNHOLMERTAARNET', role: 'repeater', lat: 55.00182, lon: 15.07351, foreign: true, last_seen: '2026-07-20T00:00:00Z' },
      { public_key: 'pkUnflaggedForeign', name: 'ActuallyForeign', role: 'repeater', lat: 40.7, lon: -74.0, last_seen: '2026-07-20T00:00:00Z' },
    ]);
    // Real production box (matches stg.meshview.dk at the time this bug
    // was found) — wider than the shared GEO_BOX above, since
    // Bornholm's real lon (~15.07) needed the actual configured
    // lonMax=15.25 to be correctly in-box.
    ctx.window.MC_GEO_FILTER = { latMin: 54.5, latMax: 57.8, lonMin: 8, lonMax: 15.25 };
    const el = fakeEl();
    await ctx.window._analyticsRenderForeignTrafficTab(el);
    assert.ok(el.innerHTML.includes('Foreign-Flagged Nodes (1)'), 'exactly 1 of 2 nodes should classify as foreign — the in-box one must not, regardless of its stale flag');
    assert.ok(!el.innerHTML.includes('DK_BORNHOLMERTAARNET'), 'a node inside the geo_filter box must not appear as foreign, even with a stale foreign=true flag');
    assert.ok(el.innerHTML.includes('ActuallyForeign'), 'a node outside the geo_filter box must appear as foreign, even with no stored flag at all');
  });

  await testAsync('rendering registers a real interval, and stop() actually clears it (not a no-op)', async () => {
    const ctx = makeAnalyticsSandbox([]);
    const stop = ctx.window._analyticsStopForeignTrafficRefresh;
    assert.strictEqual(typeof stop, 'function', '_stopForeignTrafficRefresh must be exported for testing/cleanup');

    // Calling stop() before any render must not throw (no timer registered yet).
    stop();
    assert.strictEqual(ctx.__clearedIntervalIds.length, 0, 'stop() before any render should not call clearInterval at all');

    await ctx.window._analyticsRenderForeignTrafficTab(fakeEl());
    assert.strictEqual(ctx.__liveIntervalIds.size, 1, 'render should register exactly one live interval');
    const [registeredId] = ctx.__liveIntervalIds;

    stop();
    assert.strictEqual(ctx.__liveIntervalIds.size, 0, 'stop() should leave zero live intervals');
    assert.deepStrictEqual(ctx.__clearedIntervalIds, [registeredId], 'stop() should call clearInterval with the exact id that render registered');

    // Idempotent: calling stop() again with nothing live must not clear anything a second time.
    stop();
    assert.deepStrictEqual(ctx.__clearedIntervalIds, [registeredId], 'a second stop() call must not clear anything again — the timer reference should already be null');
  });

  await testAsync('Foreign-Flagged Nodes collapses to top 10 with a "Show all" toggle', async () => {
    const fixture = [];
    for (let i = 0; i < 15; i++) {
      // Newest-heard-first sort means index 0 is the most recent -- give
      // decreasing last_seen so Foreign0..Foreign9 are exactly the shown set.
      fixture.push({
        public_key: 'pkForeign' + i, name: 'Foreign' + i, role: 'client',
        lat: 40.7, lon: -74.0,
        first_seen: '2026-07-01T00:00:00Z',
        last_seen: '2026-07-' + String(20 - i).padStart(2, '0') + 'T00:00:00Z',
      });
    }
    const ctx = makeAnalyticsSandbox(fixture);
    const el = fakeEl();
    await ctx.window._analyticsRenderForeignTrafficTab(el);
    const startIdx = el.innerHTML.indexOf('id="foreignTrafficFlaggedNodes"');
    const endIdx = el.innerHTML.indexOf('Repeaters Relaying Unscoped Traffic');
    const section = el.innerHTML.slice(startIdx, endIdx);
    assert.ok(section.includes('Foreign9'), 'the 10th foreign node (index 9) should be shown');
    assert.ok(!section.includes('Foreign10'), 'the 11th foreign node should be collapsed away by default');
    assert.ok(section.includes('Show all 15 nodes'), 'a "Show all" toggle should appear when there are more than 10 foreign nodes');
  });

  await testAsync('Repeaters Relaying Unscoped Traffic collapses to top 10 with a "Show all" toggle', async () => {
    const fixture = [];
    for (let i = 0; i < 15; i++) {
      fixture.push({
        public_key: 'pkRelay' + i, name: 'Relay' + i, role: 'repeater',
        unscoped_relay_count_24h: 15 - i, relay_count_24h: 20,
      });
    }
    const ctx = makeAnalyticsSandbox(fixture);
    const el = fakeEl();
    await ctx.window._analyticsRenderForeignTrafficTab(el);
    const section = el.innerHTML.slice(el.innerHTML.indexOf('id="foreignTrafficRelays"'));
    assert.ok(section.includes('Relay9'), 'the 10th relay (index 9, unscoped_relay_count_24h=6) should be shown');
    assert.ok(!section.includes('Relay10'), 'the 11th relay should be collapsed away by default');
    assert.ok(section.includes('Show all 15 repeaters'), 'a "Show all" toggle should appear when there are more than 10 relaying repeaters');
  });

  await testAsync('no "Show all" toggle when a list has 10 or fewer entries', async () => {
    const ctx = makeAnalyticsSandbox([
      { public_key: 'pkA', name: 'RepeaterA', role: 'repeater', unscoped_relay_count_24h: 5, relay_count_24h: 5 },
      { public_key: 'pkForeign', name: 'ForeignNode', role: 'client', lat: 40.7, lon: -74.0 },
    ]);
    const el = fakeEl();
    await ctx.window._analyticsRenderForeignTrafficTab(el);
    assert.ok(!el.innerHTML.includes('Show all'), 'no toggle should render when neither list exceeds 10 entries');
  });

  console.log('\n════════════════════════════════════════');
  console.log(`  Foreign Traffic tab: ${passed} passed, ${failed} failed`);
  console.log('════════════════════════════════════════');
  process.exit(failed === 0 ? 0 : 1);
})();

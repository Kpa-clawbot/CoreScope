/**
 * DOM-rendering tests for the "Wardriving" Analytics tab
 * (renderWardrivingTab, public/analytics.js).
 *
 * Drives the real render function against a stubbed api() that returns a
 * fixed /api/analytics/wardriving response (and /api/resolve-hops for the
 * Entry Points section), asserting rendered stat cards, table rows, sort
 * order, empty states, and the 60s auto-refresh timer lifecycle — same
 * harness style as test-analytics-foreign-traffic-tab.js.
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

// Minimal but representative /api/analytics/wardriving fixture: 2 senders,
// 2 entry-point prefixes (one unique_prefix-resolvable, one ambiguous),
// 2 observers (one with known coordinates, one without).
function makeWardrivingResponse(overrides) {
  return Object.assign({
    window: '24h',
    channel: '#wardriving',
    totalMessages: 3,
    timeSeries: [{ t: '2026-07-20T08:00:00Z', count: 1 }, { t: '2026-07-20T09:00:00Z', count: 2 }],
    topSenders: [
      { sender: 'Alice', count: 2 },
      { sender: 'Bob', count: 1 },
    ],
    entryPoints: [
      { prefix: 'AAAA', observationCount: 3, messageCount: 2 },
      { prefix: 'CCCC', observationCount: 1, messageCount: 1 },
    ],
    observers: [
      { observerId: '1', observerName: 'SeattleObs', iata: 'SEA', lat: 47.4502, lon: -122.3088, observationCount: 3, messageCount: 3 },
      { observerId: '2', observerName: 'UnknownObs', iata: 'ZZZ', observationCount: 1, messageCount: 1 },
    ],
  }, overrides);
}

function makeApiStub(wardrivingResp, resolveHopsResp) {
  return function (path) {
    if (path.indexOf('/analytics/wardriving') === 0) return Promise.resolve(wardrivingResp);
    if (path.indexOf('/resolve-hops') === 0) return Promise.resolve(resolveHopsResp || { resolved: {} });
    return Promise.resolve({});
  };
}

(async () => {
  console.log('\n=== analytics.js: renderWardrivingTab ===');

  await testAsync('renders stat cards from the API response', async () => {
    const ctx = makeAnalyticsSandbox(makeApiStub(makeWardrivingResponse(), {
      resolved: {
        AAAA: { name: 'GatewayRepeater', pubkey: 'pkGateway', confidence: 'unique_prefix' },
      },
    }));
    const el = fakeEl();
    await ctx.window._analyticsRenderWardrivingTab(el);
    assert.ok(el.innerHTML.includes('>3<'), 'total messages (3) should appear in a stat card');
    assert.ok(el.innerHTML.includes('Active Senders'), 'Active Senders card label should render');
    assert.ok(el.innerHTML.includes('Entry-Point Repeaters'), 'Entry-Point Repeaters card label should render');
    assert.ok(el.innerHTML.includes('Observers Reached'), 'Observers Reached card label should render');
  });

  await testAsync('Top Senders table is sorted by count and shows % of total', async () => {
    const ctx = makeAnalyticsSandbox(makeApiStub(makeWardrivingResponse()));
    const el = fakeEl();
    await ctx.window._analyticsRenderWardrivingTab(el);
    const idxAlice = el.innerHTML.indexOf('Alice');
    const idxBob = el.innerHTML.indexOf('Bob');
    assert.ok(idxAlice > -1 && idxBob > -1, 'both senders should be listed');
    assert.ok(idxAlice < idxBob, 'Alice (2 messages) should be listed before Bob (1 message)');
    // Alice: 2 of 3 total = 66.7%
    assert.ok(el.innerHTML.includes('66.7%'), 'Alice row should show 66.7% of total messages');
  });

  await testAsync('Entry Points resolves unique_prefix repeaters and folds ambiguous into one bucket', async () => {
    const ctx = makeAnalyticsSandbox(makeApiStub(makeWardrivingResponse(), {
      resolved: {
        AAAA: { name: 'GatewayRepeater', pubkey: 'pkGateway', confidence: 'unique_prefix' },
        CCCC: { name: 'BestGuessRepeater', pubkey: 'pkGuess', confidence: 'gps_preference' },
      },
    }));
    const el = fakeEl();
    await ctx.window._analyticsRenderWardrivingTab(el);
    const startIdx = el.innerHTML.indexOf('Entry Points');
    const endIdx = el.innerHTML.indexOf('Coverage by Observer');
    const section = el.innerHTML.slice(startIdx, endIdx);
    assert.ok(section.includes('GatewayRepeater'), 'unique_prefix resolution should show the real repeater name');
    assert.ok(!section.includes('BestGuessRepeater'), 'a non-unique_prefix resolution must not be shown as a specific named repeater');
    assert.ok(section.includes('Ambiguous'), 'the ambiguous prefix should be folded into an explicit Ambiguous bucket');
    // AAAA: 3 of 4 total observations = 75.0%
    assert.ok(section.includes('75.0%'), 'GatewayRepeater should show 75.0% of observations (3 of 4)');
  });

  await testAsync('Coverage by Observer shows resolved coordinates and "—" for an unknown IATA', async () => {
    const ctx = makeAnalyticsSandbox(makeApiStub(makeWardrivingResponse()));
    const el = fakeEl();
    await ctx.window._analyticsRenderWardrivingTab(el);
    const startIdx = el.innerHTML.indexOf('Coverage by Observer');
    const section = el.innerHTML.slice(startIdx);
    assert.ok(section.includes('SeattleObs'), 'observer with known coordinates should be listed');
    assert.ok(section.includes('47.45, -122.31'), 'SeattleObs should show its resolved lat/lon');
    assert.ok(section.includes('UnknownObs'), 'observer without known coordinates should still be listed');
    const idxUnknown = section.indexOf('UnknownObs');
    const unknownRow = section.slice(idxUnknown, section.indexOf('</tr>', idxUnknown));
    assert.ok(unknownRow.includes('—'), 'UnknownObs (no resolvable IATA) should show a dash for its location, not blank/null');
  });

  await testAsync('shows empty-state messages when the window has no wardriving activity', async () => {
    const ctx = makeAnalyticsSandbox(makeApiStub(makeWardrivingResponse({
      totalMessages: 0, topSenders: [], entryPoints: [], observers: [], timeSeries: [],
    })));
    const el = fakeEl();
    await ctx.window._analyticsRenderWardrivingTab(el);
    assert.ok(el.innerHTML.includes('No wardriving messages in this window'), 'senders empty state should show');
    assert.ok(el.innerHTML.includes('No wardriving messages with a relay path'), 'entry points empty state should show');
    assert.ok(el.innerHTML.includes('No observer has heard wardriving traffic'), 'observers empty state should show');
  });

  await testAsync('rendering registers a real interval, and stop() actually clears it (not a no-op)', async () => {
    const ctx = makeAnalyticsSandbox(makeApiStub(makeWardrivingResponse()));
    const stop = ctx.window._analyticsStopWardrivingRefresh;
    assert.strictEqual(typeof stop, 'function', '_stopWardrivingRefresh must be exported for testing/cleanup');

    stop(); // must not throw when no timer is registered yet
    const el = fakeEl();
    await ctx.window._analyticsRenderWardrivingTab(el);

    assert.strictEqual(ctx.__liveIntervalIds.size, 1, 'rendering should register exactly one live interval');
    stop();
    assert.strictEqual(ctx.__liveIntervalIds.size, 0, 'stop() should clear the registered interval');
    assert.ok(ctx.__clearedIntervalIds.length >= 1, 'clearInterval should have actually been called');
  });

  console.log('\n════════════════════════════════════════');
  console.log(`  Wardriving tab: ${passed} passed, ${failed} failed`);
  console.log('════════════════════════════════════════');
  process.exit(failed === 0 ? 0 : 1);
})();

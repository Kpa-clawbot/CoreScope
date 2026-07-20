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
// 2 observers (one with known coordinates, one without), and a 2-point
// signal-quality time series.
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
    signalTimeSeries: [
      { t: '2026-07-20T08:00:00Z', avgSnr: 4.0, avgRssi: -80.0, observationCount: 1 },
      { t: '2026-07-20T09:00:00Z', avgSnr: 6.0, avgRssi: -70.0, observationCount: 3 },
    ],
    avgSnr: 5.5,
    avgRssi: -72.5,
    sessions: [
      { sender: 'Alice', startTime: '2026-07-20T09:00:00Z', endTime: '2026-07-20T09:00:00Z', durationMinutes: 0, messageCount: 1, entryPointCount: 1, observerCount: 1 },
      { sender: 'Bob', startTime: '2026-07-20T08:30:00Z', endTime: '2026-07-20T08:30:00Z', durationMinutes: 0, messageCount: 1, entryPointCount: 1, observerCount: 1 },
      { sender: 'Alice', startTime: '2026-07-20T08:00:00Z', endTime: '2026-07-20T08:05:00Z', durationMinutes: 5, messageCount: 1, entryPointCount: 2, observerCount: 1 },
    ],
    gpsShares: [
      { sender: 'SiriusNet-mobile', lat: 55.59743, lon: 13.00128, messageCount: 8, lastSeen: '2026-07-20T09:00:00Z' },
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
    assert.ok(el.innerHTML.includes('5.5 dB'), 'Avg SNR card should show the API-provided average');
    assert.ok(el.innerHTML.includes('-72.5 dBm'), 'Avg RSSI card should show the API-provided average');
    assert.ok(el.innerHTML.includes('<div class="stat-value">1</div><div class="stat-label">GPS Shared</div>'), 'GPS Shared card should show the sharing-sender count');
  });

  await testAsync('GPS Sharing table lists senders who shared a position, with a map link', async () => {
    const ctx = makeAnalyticsSandbox(makeApiStub(makeWardrivingResponse()));
    const el = fakeEl();
    await ctx.window._analyticsRenderWardrivingTab(el);
    const startIdx = el.innerHTML.indexOf('id="wardrivingGPSShares"');
    assert.ok(startIdx > -1, 'GPS Sharing section should render');
    const section = el.innerHTML.slice(startIdx);
    assert.ok(section.includes('SiriusNet-mobile'), 'the sharing sender should be listed');
    assert.ok(section.includes('55.59743, 13.00128'), 'the shared position should render as plain coordinates');
    assert.ok(section.includes('href="#/map?lat=55.59743&lon=13.00128&zoom=15"'), 'the position should link to the live map centered on it');
  });

  await testAsync('GPS Sharing shows a neutral message when nobody has shared a position', async () => {
    const ctx = makeAnalyticsSandbox(makeApiStub(makeWardrivingResponse({ gpsShares: [] })));
    const el = fakeEl();
    await ctx.window._analyticsRenderWardrivingTab(el);
    const startIdx = el.innerHTML.indexOf('id="wardrivingGPSShares"');
    const section = el.innerHTML.slice(startIdx);
    assert.ok(section.includes('No sender has shared an explicit position'), 'should show a neutral empty state');
  });

  await testAsync('Signal Quality Trends renders both SNR and RSSI charts', async () => {
    const ctx = makeAnalyticsSandbox(makeApiStub(makeWardrivingResponse()));
    const el = fakeEl();
    await ctx.window._analyticsRenderWardrivingTab(el);
    const startIdx = el.innerHTML.indexOf('Signal Quality Trends');
    assert.ok(startIdx > -1, 'Signal Quality Trends heading should render');
    const section = el.innerHTML.slice(startIdx);
    assert.ok(section.includes('Avg SNR (dB)'), 'SNR chart label should render');
    assert.ok(section.includes('Avg RSSI (dBm)'), 'RSSI chart label should render');
    assert.ok(section.includes('<svg'), 'at least one SVG chart should render for the 2-point signal series');
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

  await testAsync('Top Senders names are clickable drill-down triggers (whole-window, no since/until)', async () => {
    const ctx = makeAnalyticsSandbox(makeApiStub(makeWardrivingResponse()));
    const el = fakeEl();
    await ctx.window._analyticsRenderWardrivingTab(el);
    const startIdx = el.innerHTML.indexOf('id="wardrivingSenders"');
    const endIdx = el.innerHTML.indexOf('id="wardrivingSessions"');
    const section = el.innerHTML.slice(startIdx, endIdx);
    assert.ok(section.includes('data-wd-sender="Alice"'), 'Alice should be a drill-down trigger');
    assert.ok(!section.includes('data-wd-since'), 'Top Senders triggers should not scope to a since/until range');
  });

  await testAsync('Sessions sender names are clickable drill-down triggers scoped to that session\'s exact range', async () => {
    const ctx = makeAnalyticsSandbox(makeApiStub(makeWardrivingResponse()));
    const el = fakeEl();
    await ctx.window._analyticsRenderWardrivingTab(el);
    const startIdx = el.innerHTML.indexOf('id="wardrivingSessions"');
    const endIdx = el.innerHTML.indexOf('id="wardrivingEntryPoints"');
    const section = el.innerHTML.slice(startIdx, endIdx);
    assert.ok(section.includes('data-wd-since="2026-07-20T08:00:00Z"'), 'the 5-minute Alice session should carry its own startTime as data-wd-since');
    assert.ok(section.includes('data-wd-until="2026-07-20T08:05:00Z"'), 'the 5-minute Alice session should carry its own endTime as data-wd-until');
  });

  await testAsync('Sessions table lists each run with duration, entry points, and observers', async () => {
    const ctx = makeAnalyticsSandbox(makeApiStub(makeWardrivingResponse()));
    const el = fakeEl();
    await ctx.window._analyticsRenderWardrivingTab(el);
    const startIdx = el.innerHTML.indexOf('id="wardrivingSessions"');
    const endIdx = el.innerHTML.indexOf('id="wardrivingEntryPoints"');
    const section = el.innerHTML.slice(startIdx, endIdx);
    // 3 sessions in the fixture: two Alice rows, one Bob row. Sender names
    // render both as visible text and as a data-wd-sender attribute value,
    // so match on the visible-text form specifically.
    assert.strictEqual((section.match(/>Alice</g) || []).length, 2, 'both Alice sessions should render as separate rows');
    assert.ok(section.includes('Bob'), 'Bob session should render');
    assert.ok(section.includes('5m'), 'the 5-minute session should show its duration');
    assert.ok(el.innerHTML.includes('<div class="stat-value">3</div><div class="stat-label">Sessions</div>'), 'the Sessions stat card should show the session count (3)');
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
    const startIdx = el.innerHTML.indexOf('id="wardrivingEntryPoints"');
    const endIdx = el.innerHTML.indexOf('id="wardrivingObservers"');
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
      signalTimeSeries: [], avgSnr: null, avgRssi: null, sessions: [],
      gpsShares: [],
    })));
    const el = fakeEl();
    await ctx.window._analyticsRenderWardrivingTab(el);
    assert.ok(el.innerHTML.includes('No wardriving messages in this window'), 'senders empty state should show');
    assert.ok(el.innerHTML.includes('No wardriving sessions in this window'), 'sessions empty state should show');
    assert.ok(el.innerHTML.includes('No wardriving messages with a relay path'), 'entry points empty state should show');
    assert.ok(el.innerHTML.includes('No observer has heard wardriving traffic'), 'observers empty state should show');
    assert.ok(el.innerHTML.includes('Insufficient data points to chart'), 'signal chart empty state should show');
    assert.ok(el.innerHTML.includes('<div class="stat-value">—</div><div class="stat-label">Avg SNR</div>'), 'Avg SNR card should show a dash when there is no signal data');
    assert.ok(el.innerHTML.includes('<div class="stat-value">—</div><div class="stat-label">Avg RSSI</div>'), 'Avg RSSI card should show a dash when there is no signal data');
    assert.ok(el.innerHTML.includes('No sender has shared an explicit position'), 'GPS sharing empty state should show');
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

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
      { public_key: 'pkA', name: 'RepeaterLow', role: 'repeater', unscoped_relay_count_24h: 10, relay_count_24h: 20, foreign: false },
      { public_key: 'pkB', name: 'RepeaterHigh', role: 'repeater', unscoped_relay_count_24h: 50, relay_count_24h: 50, foreign: false },
      { public_key: 'pkC', name: 'RoomMid', role: 'room', unscoped_relay_count_24h: 25, relay_count_24h: 30, foreign: false },
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
      { public_key: 'pkClient', name: 'NoisyClient', role: 'client', unscoped_relay_count_24h: 999, relay_count_24h: 999, foreign: false },
      { public_key: 'pkClean', name: 'CleanRepeater', role: 'repeater', unscoped_relay_count_24h: 0, relay_count_24h: 40, foreign: false },
      { public_key: 'pkDirty', name: 'DirtyRepeater', role: 'repeater', unscoped_relay_count_24h: 5, relay_count_24h: 40, foreign: false },
    ]);
    const el = fakeEl();
    await ctx.window._analyticsRenderForeignTrafficTab(el);
    assert.ok(!el.innerHTML.includes('NoisyClient'), 'a client-role node must never appear, regardless of its unscoped count');
    assert.ok(!el.innerHTML.includes('CleanRepeater'), 'a repeater with zero unscoped relays must not appear');
    assert.ok(el.innerHTML.includes('DirtyRepeater'), 'a repeater with unscoped relays > 0 must appear');
  });

  await testAsync('shows the empty-state message when no repeater has unscoped relays', async () => {
    const ctx = makeAnalyticsSandbox([
      { public_key: 'pkClean', name: 'CleanRepeater', role: 'repeater', unscoped_relay_count_24h: 0, relay_count_24h: 40, foreign: false },
    ]);
    const el = fakeEl();
    await ctx.window._analyticsRenderForeignTrafficTab(el);
    assert.ok(el.innerHTML.includes('No repeater has relayed an unscoped flood packet'), 'empty state message should be shown');
  });

  await testAsync('foreignCount note reflects the number of foreign-flagged nodes', async () => {
    const ctx = makeAnalyticsSandbox([
      { public_key: 'pkA', name: 'RepeaterA', role: 'repeater', unscoped_relay_count_24h: 5, relay_count_24h: 5, foreign: false },
      { public_key: 'pkForeign1', name: 'ForeignNode1', role: 'client', unscoped_relay_count_24h: 0, relay_count_24h: 0, foreign: true },
      { public_key: 'pkForeign2', name: 'ForeignNode2', role: 'client', unscoped_relay_count_24h: 0, relay_count_24h: 0, foreign: true },
    ]);
    const el = fakeEl();
    await ctx.window._analyticsRenderForeignTrafficTab(el);
    assert.ok(el.innerHTML.includes('2 node(s)'), 'note should mention the count of foreign-flagged nodes (2)');
  });

  await testAsync('foreignCount note falls back to the no-foreign-yet message when none are flagged', async () => {
    const ctx = makeAnalyticsSandbox([
      { public_key: 'pkA', name: 'RepeaterA', role: 'repeater', unscoped_relay_count_24h: 5, relay_count_24h: 5, foreign: false },
    ]);
    const el = fakeEl();
    await ctx.window._analyticsRenderForeignTrafficTab(el);
    assert.ok(el.innerHTML.includes('no foreign-origin node has advertised'), 'should show the zero-foreign fallback message');
  });

  await testAsync('foreign-flagged nodes list renders every foreign node, sorted newest-heard first', async () => {
    const ctx = makeAnalyticsSandbox([
      { public_key: 'pkA', name: 'RepeaterA', role: 'repeater', unscoped_relay_count_24h: 5, relay_count_24h: 5, foreign: false },
      { public_key: 'pkOld', name: 'OldForeign', role: 'companion', lat: 44.4, lon: 26.1, first_seen: '2026-07-01T00:00:00Z', last_seen: '2026-07-01T00:00:00Z', foreign: true },
      { public_key: 'pkNew', name: 'NewForeign', role: 'client', lat: 52.4, lon: 10.8, first_seen: '2026-07-18T00:00:00Z', last_seen: '2026-07-18T12:00:00Z', foreign: true },
    ]);
    const el = fakeEl();
    await ctx.window._analyticsRenderForeignTrafficTab(el);
    assert.ok(el.innerHTML.includes('Foreign-Flagged Nodes (2)'), 'heading should show the correct count');
    const foreignSectionHtml = el.innerHTML.slice(el.innerHTML.indexOf('Foreign-Flagged Nodes'));
    assert.ok(!foreignSectionHtml.includes('RepeaterA'), 'a non-foreign node must not appear in the foreign-nodes section');
    const idxNew = el.innerHTML.indexOf('NewForeign');
    const idxOld = el.innerHTML.indexOf('OldForeign');
    assert.ok(idxNew > -1 && idxOld > -1, 'both foreign nodes should be listed');
    assert.ok(idxNew < idxOld, 'more recently heard foreign node should be listed first');
  });

  await testAsync('foreign-flagged nodes list shows a placeholder when none are flagged yet', async () => {
    const ctx = makeAnalyticsSandbox([
      { public_key: 'pkA', name: 'RepeaterA', role: 'repeater', unscoped_relay_count_24h: 5, relay_count_24h: 5, foreign: false },
    ]);
    const el = fakeEl();
    await ctx.window._analyticsRenderForeignTrafficTab(el);
    assert.ok(el.innerHTML.includes('Foreign-Flagged Nodes (0)'), 'heading should show a zero count');
    assert.ok(el.innerHTML.includes('None yet'), 'placeholder message should be shown when no node is foreign-flagged');
  });

  await testAsync('a stop-refresh hook is exported and safe to call repeatedly (idempotent)', async () => {
    const ctx = makeAnalyticsSandbox([]);
    const stop = ctx.window._analyticsStopForeignTrafficRefresh;
    assert.strictEqual(typeof stop, 'function', '_stopForeignTrafficRefresh must be exported for testing/cleanup');
    // Before any render, and after — must not throw either way.
    stop();
    await ctx.window._analyticsRenderForeignTrafficTab(fakeEl());
    stop();
    stop();
  });

  console.log('\n════════════════════════════════════════');
  console.log(`  Foreign Traffic tab: ${passed} passed, ${failed} failed`);
  console.log('════════════════════════════════════════');
  process.exit(failed === 0 ? 0 : 1);
})();

/**
 * Tests for the two "lad os lave dem begge" extensions built on top of
 * /api/analytics/hop-depth (GetHopDepthAnalytics, cmd/server/db.go):
 *
 *  1. Scopes tab Overview: renderHopDepthSectionHtml / hopDepthBucketStats
 *     — the scoped-vs-unscoped hop-depth comparison (does region scoping
 *     actually contain flood propagation to fewer hops than unscoped
 *     traffic).
 *  2. Foreign Traffic tab: hopDepthLookupByPubkey — the per-repeater
 *     min/median/max hop-depth enrichment joined onto the existing
 *     "Repeaters Relaying Unscoped Traffic" table by public key.
 *
 * Pure-function unit tests plus one DOM-rendering integration test for
 * the Foreign Traffic table join, following the same vm-sandbox pattern
 * as test-analytics-foreign-traffic-tab.js / test-node-analytics-hop-chart.js.
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
    fetch: () => Promise.resolve({ ok: true, json: () => Promise.resolve({}) }),
    performance: { now: () => Date.now() },
    localStorage: (() => { const s = {}; return { getItem: k => s[k] || null, setItem: (k, v) => { s[k] = String(v); }, removeItem: k => { delete s[k]; } }; })(),
    location: { hash: '' },
    getHashParams: function() { return new URLSearchParams((ctx.location.hash.split('?')[1] || '')); },
    CustomEvent: class CustomEvent {},
    Map, Promise, URLSearchParams,
    addEventListener: () => {},
    dispatchEvent: () => {},
    requestAnimationFrame: (cb) => setTimeout(cb, 0),
    setInterval: () => 1,
    clearInterval: () => {},
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

const GEO_BOX = { latMin: 53, latMax: 59, lonMin: 6, lonMax: 15 };

function makeAnalyticsSandbox(nodesFixture, opts) {
  opts = opts || {};
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
  // fetch is what app.js's real api() ultimately calls -- routes by URL so
  // the hop-depth endpoint can be stubbed independently of every other
  // in-flight api() call (e.g. roles.js's own config fetch on load).
  if (opts.hopDepthResponse !== undefined) {
    ctx.fetch = (url) => {
      if (String(url).indexOf('/analytics/hop-depth') !== -1) {
        return Promise.resolve({ ok: true, json: () => Promise.resolve(opts.hopDepthResponse) });
      }
      return Promise.resolve({ ok: true, json: () => Promise.resolve({}) });
    };
  }
  loadInCtx(ctx, 'public/roles.js');
  loadInCtx(ctx, 'public/app.js');
  ctx.fetchAllNodes = async () => ({ nodes: nodesFixture || [] });
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
  console.log('\n=== analytics.js: hopDepthBucketStats ===');

  await testAsync('empty/missing buckets return total 0 and null median', async () => {
    const ctx = makeAnalyticsSandbox([]);
    [[], null, undefined].forEach((input) => {
      const stats = ctx.window._analyticsHopDepthBucketStats(input);
      assert.strictEqual(stats.total, 0);
      assert.strictEqual(stats.median, null);
    });
  });

  await testAsync('single bucket -> median is that bucket\'s hop value', async () => {
    const ctx = makeAnalyticsSandbox([]);
    const stats = ctx.window._analyticsHopDepthBucketStats([{ hops: 3, count: 7 }]);
    assert.strictEqual(stats.total, 7);
    assert.strictEqual(stats.median, 3);
  });

  await testAsync('odd total -> median is the middle bucket by cumulative count', async () => {
    const ctx = makeAnalyticsSandbox([]);
    // hops 0..4 counts 1 -> total 5, cumulative >= 2.5 first at hops=2.
    const stats = ctx.window._analyticsHopDepthBucketStats([
      { hops: 0, count: 1 }, { hops: 1, count: 1 }, { hops: 2, count: 1 },
      { hops: 3, count: 1 }, { hops: 4, count: 1 },
    ]);
    assert.strictEqual(stats.total, 5);
    assert.strictEqual(stats.median, 2);
  });

  await testAsync('median is unaffected by input bucket order (sorts internally)', async () => {
    const ctx = makeAnalyticsSandbox([]);
    const stats = ctx.window._analyticsHopDepthBucketStats([
      { hops: 4, count: 1 }, { hops: 0, count: 1 }, { hops: 2, count: 1 },
      { hops: 1, count: 1 }, { hops: 3, count: 1 },
    ]);
    assert.strictEqual(stats.median, 2);
  });

  console.log('\n=== analytics.js: renderHopDepthSectionHtml ===');

  await testAsync('null/empty hopData renders nothing', async () => {
    const ctx = makeAnalyticsSandbox([]);
    assert.strictEqual(ctx.window._analyticsRenderHopDepthSectionHtml(null), '');
    assert.strictEqual(ctx.window._analyticsRenderHopDepthSectionHtml({}), '');
  });

  await testAsync('present-but-empty buckets renders the no-data message', async () => {
    const ctx = makeAnalyticsSandbox([]);
    const html = ctx.window._analyticsRenderHopDepthSectionHtml({ scopedHopDepth: [], unscopedHopDepth: [] });
    assert.ok(html.includes('No relay-hop data in this window'));
  });

  await testAsync('renders scoped/unscoped median stat cards and per-hop bar rows', async () => {
    const ctx = makeAnalyticsSandbox([]);
    const html = ctx.window._analyticsRenderHopDepthSectionHtml({
      scopedHopDepth: [{ hops: 0, count: 10 }],
      unscopedHopDepth: [{ hops: 0, count: 4 }, { hops: 1, count: 4 }, { hops: 2, count: 2 }],
    });
    assert.ok(html.includes('Scoped Median Hop'), 'should show a scoped median stat card');
    assert.ok(html.includes('Unscoped Median Hop'), 'should show an unscoped median stat card');
    assert.ok(html.includes('10 samples'), 'scoped sample size should be 10');
    assert.ok(html.includes('0 hops'), 'should have a hop=0 row');
    assert.ok(html.includes('2 hops'), 'should have a hop=2 row');
  });

  console.log('\n=== analytics.js: hopDepthLookupByPubkey ===');

  await testAsync('builds a publicKey -> entry map', async () => {
    const ctx = makeAnalyticsSandbox([]);
    const entries = [
      { publicKey: 'pkA', name: 'A', count: 5, minHops: 1, medianHops: 2, maxHops: 4 },
      { publicKey: 'pkB', name: 'B', count: 2, minHops: 0, medianHops: 0.5, maxHops: 1 },
    ];
    const map = ctx.window._analyticsHopDepthLookupByPubkey(entries);
    assert.strictEqual(map.pkA.count, 5);
    assert.strictEqual(map.pkB.maxHops, 1);
  });

  await testAsync('empty/null input returns an empty map, not a throw', async () => {
    const ctx = makeAnalyticsSandbox([]);
    [[], null, undefined].forEach((input) => {
      const map = ctx.window._analyticsHopDepthLookupByPubkey(input);
      assert.strictEqual(Object.keys(map).length, 0);
    });
  });

  console.log('\n=== analytics.js: Foreign Traffic tab hop-depth enrichment ===');

  await testAsync('relay table shows min/median/max hop columns joined by public key', async () => {
    const ctx = makeAnalyticsSandbox([
      { public_key: 'pkA', name: 'RepeaterA', role: 'repeater', unscoped_relay_count_24h: 10, relay_count_24h: 20 },
    ], {
      hopDepthResponse: {
        window: '24h',
        scopedHopDepth: [],
        unscopedHopDepth: [],
        unscopedByRepeater: [
          { publicKey: 'pkA', name: 'RepeaterA', count: 10, minHops: 1, medianHops: 2.5, maxHops: 6 },
        ],
      },
    });
    const el = fakeEl();
    await ctx.window._analyticsRenderForeignTrafficTab(el);
    assert.ok(el.innerHTML.includes('Min Hops'), 'table header should include Min Hops');
    assert.ok(el.innerHTML.includes('Median Hops'), 'table header should include Median Hops');
    assert.ok(el.innerHTML.includes('Max Hops'), 'table header should include Max Hops');
    // Row cells: min=1, median=2.5, max=6 for pkA.
    const rowStart = el.innerHTML.indexOf('RepeaterA');
    const rowSection = el.innerHTML.slice(rowStart, rowStart + 500);
    assert.ok(rowSection.includes('>1<'), 'min hops cell should show 1');
    assert.ok(rowSection.includes('>2.5<'), 'median hops cell should show 2.5');
    assert.ok(rowSection.includes('>6<'), 'max hops cell should show 6');
  });

  await testAsync('a repeater with no hop-depth entry shows placeholders instead of throwing', async () => {
    const ctx = makeAnalyticsSandbox([
      { public_key: 'pkNoHops', name: 'QuietRepeater', role: 'repeater', unscoped_relay_count_24h: 3, relay_count_24h: 10 },
    ], {
      hopDepthResponse: { window: '24h', scopedHopDepth: [], unscopedHopDepth: [], unscopedByRepeater: [] },
    });
    const el = fakeEl();
    await ctx.window._analyticsRenderForeignTrafficTab(el);
    assert.ok(el.innerHTML.includes('QuietRepeater'));
    const rowStart = el.innerHTML.indexOf('QuietRepeater');
    const rowSection = el.innerHTML.slice(rowStart, rowStart + 500);
    assert.ok(rowSection.includes('>—<'), 'missing hop-depth data should render as an em dash placeholder');
  });

  await testAsync('a failing hop-depth fetch degrades to placeholders, not a broken tab', async () => {
    const ctx = makeAnalyticsSandbox([
      { public_key: 'pkA', name: 'RepeaterA', role: 'repeater', unscoped_relay_count_24h: 5, relay_count_24h: 5 },
    ]);
    // No hopDepthResponse opt given -> default sandbox fetch mock returns
    // { ok:true, json: () => ({}) } for every path, including hop-depth,
    // so unscopedByRepeater is simply undefined/absent -- exercises the
    // "no data at all" branch through the same code path a real network
    // error's catch(() => null) would also produce.
    const el = fakeEl();
    await ctx.window._analyticsRenderForeignTrafficTab(el);
    assert.ok(el.innerHTML.includes('RepeaterA'), 'tab should still render the repeater row');
    assert.ok(el.innerHTML.includes('Min Hops'), 'table structure should still include the hop columns');
  });

  console.log('\n════════════════════════════════════════');
  console.log(`  Hop-Depth Analytics UI: ${passed} passed, ${failed} failed`);
  console.log('════════════════════════════════════════');
  if (failed > 0) process.exit(1);
})();

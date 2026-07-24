/* Regression test for the Nodes page's Domestic/Foreign geo-scope filter.
 *
 * The unfiltered node list can be dominated by out-of-region nodes on
 * deployments with heavy cross-border traffic, making it hard to see
 * which nodes are actually local. This adds an "All / Domestic / Foreign"
 * filter-group (public/nodes.js, matching the existing Status filter's
 * pattern) that narrows the client-side `nodes` array by classifying each
 * node's own lat/lon against the configured geo_filter box/polygon
 * (nodePassesGeoFilter, public/app.js) — NOT the `foreign` flag, which
 * only reflects nodes classified at ADVERT-ingest time and badly
 * undercounts on real data (see commit 6012ed0).
 *
 * Drives the real loadNodes() pipeline (via pageMod().init()) rather than
 * re-implementing the filter predicate, so this actually exercises the
 * production code path — same harness style as test-issue-1606-pagination.js.
 * nodePassesGeoFilter itself is stubbed here with a simplified bbox-only
 * version (see makeNodesEnv below) — this file tests that nodes.js wires
 * the geo-scope filter correctly, not the geo-math predicate itself; that
 * lives in test-geo-filter.js, cross-checked against the real Go
 * internal/geofilter package.
 */
'use strict';
const vm = require('vm');
const fs = require('fs');
const path = require('path');
const assert = require('assert');

let passed = 0, failed = 0;
function test(name, fn) {
  const out = fn();
  if (out && typeof out.then === 'function') {
    return out.then(() => { passed++; console.log('  ✅ ' + name); })
      .catch(e => { failed++; console.log('  ❌ ' + name + ': ' + e.message); });
  }
  try {
    passed++; console.log('  ✅ ' + name);
  } catch (e) {
    failed++; console.log('  ❌ ' + name + ': ' + e.message);
  }
  return Promise.resolve();
}

function loadInCtx(ctx, file) {
  vm.runInContext(fs.readFileSync(file, 'utf8'), ctx);
  for (const k of Object.keys(ctx.window)) ctx[k] = ctx.window[k];
}

function makeSandbox() {
  const ctx = {
    window: { addEventListener: () => {}, dispatchEvent: () => {} },
    document: {
      readyState: 'complete',
      createElement: () => ({ id: '', textContent: '', innerHTML: '', style: {}, classList: { add(){}, remove(){}, toggle(){}, contains(){return false;} }, appendChild(){}, addEventListener(){} }),
      head: { appendChild: () => {} },
      getElementById: () => null,
      addEventListener: () => {},
      removeEventListener: () => {},
      querySelectorAll: () => [],
      querySelector: () => null,
    },
    console, Date, Infinity, Math, Array, Object, String, Number, JSON, RegExp, Error, TypeError,
    parseInt, parseFloat, isNaN, isFinite, encodeURIComponent, decodeURIComponent,
    setTimeout: (fn) => { fn(); return 0; }, clearTimeout: () => {},
    setInterval: () => 0, clearInterval: () => {},
    Promise, Map, Set, URLSearchParams,
    fetch: () => Promise.resolve({ json: () => Promise.resolve({}) }),
    performance: { now: () => Date.now() },
    localStorage: (() => {
      const store = {};
      return { getItem: k => store[k] || null, setItem: (k, v) => { store[k] = String(v); }, removeItem: k => { delete store[k]; } };
    })(),
    location: { hash: '' },
    getHashParams: function () { return new URLSearchParams((ctx.location.hash.split('?')[1] || '')); },
    CustomEvent: class CustomEvent {},
  };
  vm.createContext(ctx);
  return ctx;
}

// Fixture: a handful of nodes with lat/lon either inside or outside the
// configured geo_filter box (set as ctx.window.MC_GEO_FILTER below). All
// have a 64-char pubkey and a name so the loadNodes() defensive filter
// keeps them.
const GEO_BOX = { latMin: 53, latMax: 59, lonMin: 6, lonMax: 15 }; // roughly Denmark

function makeFixture() {
  const nodes = [];
  for (let i = 0; i < 5; i++) {
    nodes.push({
      public_key: 'domestic' + i.toString(16).padStart(56, '0'),
      name: 'Domestic' + i,
      role: 'repeater',
      advert_count: 1,
      lat: 55.0, lon: 10.0, // inside GEO_BOX
      last_seen: new Date(Date.now() - i * 1000).toISOString(),
    });
  }
  for (let i = 0; i < 3; i++) {
    nodes.push({
      public_key: 'foreign' + i.toString(16).padStart(57, '0'),
      name: 'Foreign' + i,
      role: 'companion',
      advert_count: 1,
      lat: 40.7, lon: -74.0, // outside GEO_BOX (New York)
      last_seen: new Date(Date.now() - i * 1000).toISOString(),
    });
  }
  return nodes;
}

function makeNodesEnv(fixture) {
  const ctx = makeSandbox();
  const domElements = {};
  function getEl(id) {
    if (!domElements[id]) {
      domElements[id] = {
        id, innerHTML: '', textContent: '', value: '', scrollTop: 0,
        style: {}, dataset: {},
        classList: { add(){}, remove(){}, toggle(){}, contains(){return false;} },
        addEventListener() {}, querySelectorAll() { return []; }, querySelector() { return null; },
        getAttribute() { return null; }, setAttribute() {}, appendChild() {},
      };
    }
    return domElements[id];
  }
  ctx.document.getElementById = getEl;

  ctx.api = function (url) {
    const q = url.indexOf('?') >= 0 ? url.slice(url.indexOf('?') + 1) : '';
    const params = new URLSearchParams(q);
    const offset = parseInt(params.get('offset') || '0', 10);
    const limit = parseInt(params.get('limit') || '500', 10);
    const page = fixture.slice(offset, offset + limit);
    return Promise.resolve({
      nodes: page,
      total: fixture.length,
      counts: { repeaters: fixture.length },
    });
  };
  ctx.invalidateApiCache = () => {};

  // Simplified bbox-only stand-in for the real nodePassesGeoFilter
  // (public/app.js) — this file tests that nodes.js correctly WIRES the
  // geo-scope filter into its loadNodes() pipeline, not the geo-math
  // itself (see test-geo-filter.js for that, cross-checked against the
  // real Go internal/geofilter package).
  ctx.nodePassesGeoFilter = function (lat, lon, gf) {
    if (!gf) return true;
    if (typeof lat !== 'number' || typeof lon !== 'number') return true;
    if (lat === 0 && lon === 0) return true;
    return lat >= gf.latMin && lat <= gf.latMax && lon >= gf.lonMin && lon <= gf.lonMax;
  };
  ctx.window.MC_GEO_FILTER = GEO_BOX;

  ctx.ROLE_COLORS = { repeater: '#0', room: '#0', companion: '#0', sensor: '#0' };
  ctx.ROLE_STYLE = {};
  ctx.TYPE_COLORS = {};
  ctx.getNodeStatus = () => 'active';
  ctx.getHealthThresholds = () => ({ staleMs: 1, degradedMs: 1, silentMs: 1 });
  ctx.timeAgo = () => '';
  ctx.truncate = (s) => s;
  ctx.escapeHtml = (s) => String(s || '');
  ctx.payloadTypeName = () => '';
  ctx.payloadTypeColor = () => '';
  ctx.debounce = (fn) => fn;
  ctx.initTabBar = () => {};
  ctx.getFavorites = () => [];
  ctx.favStar = () => '';
  ctx.bindFavStars = () => {};
  ctx.makeColumnsResizable = () => {};
  ctx.CLIENT_TTL = { nodeList: 0, nodeDetail: 0, nodeHealth: 0 };
  ctx.RegionFilter = { init(){}, onChange(){ return () => {}; }, offChange(){}, getRegionParam(){ return ''; } };
  ctx.AreaFilter = { init(){}, onChange(){ return () => {}; }, offChange(){}, getAreaParam(){ return ''; } };
  ctx.getFleetSkew = () => Promise.resolve({});
  ctx.onWS = () => {};
  ctx.offWS = () => {};
  ctx.debouncedOnWS = () => () => {};
  let pageMod = null;
  ctx.registerPage = (name, handlers) => { pageMod = handlers; };

  loadInCtx(ctx, path.join(__dirname, 'public/nodes.js'));

  return { ctx, pageMod: () => pageMod };
}

async function settle(ctx) {
  let lastLen = -1, stable = 0;
  for (let i = 0; i < 50; i++) {
    await new Promise(r => setImmediate(r));
    const f = ctx.window._nodesGetFiltered();
    const cur = Array.isArray(f) ? f.length : -1;
    if (cur === lastLen) { stable++; if (stable > 3) break; } else { stable = 0; lastLen = cur; }
  }
}

console.log('=== nodes.js: Domestic/Foreign geo-scope filter ===');

(async () => {
  await test('default geoScope ("all") includes both domestic and foreign nodes', async () => {
    const env = makeNodesEnv(makeFixture());
    const appEl = env.ctx.document.getElementById('page');
    env.pageMod().init(appEl);
    await settle(env.ctx);
    const filtered = env.ctx.window._nodesGetFiltered();
    assert.strictEqual(env.ctx.window._nodesGetGeoScope(), 'all', 'geoScope should default to all');
    assert.strictEqual(filtered.length, 8, 'all 8 nodes (5 domestic + 3 foreign) should be present by default');
  });

  await test('geoScope "domestic" excludes nodes outside the geo_filter box', async () => {
    const env = makeNodesEnv(makeFixture());
    const appEl = env.ctx.document.getElementById('page');
    env.ctx.window._nodesSetGeoScope('domestic');
    env.pageMod().init(appEl);
    await settle(env.ctx);
    const filtered = env.ctx.window._nodesGetFiltered();
    assert.strictEqual(filtered.length, 5, 'only the 5 in-box nodes should remain');
    assert.ok(filtered.every(n => n.name.startsWith('Domestic')), 'no out-of-box node should be present');
  });

  await test('geoScope "foreign" includes only nodes outside the geo_filter box', async () => {
    const env = makeNodesEnv(makeFixture());
    const appEl = env.ctx.document.getElementById('page');
    env.ctx.window._nodesSetGeoScope('foreign');
    env.pageMod().init(appEl);
    await settle(env.ctx);
    const filtered = env.ctx.window._nodesGetFiltered();
    assert.strictEqual(filtered.length, 3, 'only the 3 out-of-box nodes should remain');
    assert.ok(filtered.every(n => n.name.startsWith('Foreign')), 'every remaining node should be out of the box');
  });

  await test('geoScope "domestic" counts a node with no GPS at all as domestic', async () => {
    const fixture = makeFixture();
    fixture.push({
      public_key: 'nogps00' + '0'.repeat(57),
      name: 'NoGpsNode',
      role: 'repeater',
      advert_count: 1,
      // no lat/lon at all
      last_seen: new Date().toISOString(),
    });
    const env = makeNodesEnv(fixture);
    const appEl = env.ctx.document.getElementById('page');
    env.ctx.window._nodesSetGeoScope('domestic');
    env.pageMod().init(appEl);
    await settle(env.ctx);
    const filtered = env.ctx.window._nodesGetFiltered();
    assert.ok(filtered.some(n => n.name === 'NoGpsNode'), 'a node with no GPS should be treated as domestic, not hidden');
  });

  await test('geoScope combines with an existing role tab filter (AND, not OR)', async () => {
    const env = makeNodesEnv(makeFixture());
    const appEl = env.ctx.document.getElementById('page');
    env.ctx.window._nodesSetGeoScope('foreign');
    env.pageMod().init(appEl);
    await settle(env.ctx);
    // All 3 foreign fixture nodes are role=companion; narrowing further to
    // role=repeater (none of which are foreign) should yield zero rows,
    // not fall back to ignoring the geo filter.
    env.ctx.location.hash = '#/nodes?tab=repeater';
    env.pageMod().init(appEl);
    await settle(env.ctx);
    const filtered = env.ctx.window._nodesGetFiltered();
    assert.strictEqual(filtered.length, 0, 'foreign+repeater should be empty — all foreign fixture nodes are companions');
  });

  await test('loadNodes() awaits window.MeshConfigReady before geo-scope filtering (cold-load race regression)', async () => {
    // On a real cold page load, roles.js kicks off /api/config/client and
    // only sets window.MC_GEO_FILTER once that promise resolves — which
    // can still be in flight when nodes.js's loadNodes() runs. Without an
    // explicit await, the filter would silently run against `undefined`
    // and nodePassesGeoFilter's "no config = always passes" fallback
    // would misclassify every foreign node as domestic. This simulates
    // that exact timing with a promise under manual control.
    const env = makeNodesEnv(makeFixture());
    const appEl = env.ctx.document.getElementById('page');

    delete env.ctx.window.MC_GEO_FILTER; // config not loaded yet
    let resolveConfig;
    env.ctx.window.MeshConfigReady = new Promise((r) => { resolveConfig = r; });

    env.ctx.window._nodesSetGeoScope('foreign');
    env.pageMod().init(appEl);

    // Plenty of ticks for the node fetch + everything up to the await to
    // run — loadNodes() must still be BLOCKED on MeshConfigReady, proving
    // the await is really there and not a no-op.
    for (let i = 0; i < 20; i++) await new Promise((r) => setImmediate(r));
    assert.strictEqual(
      env.ctx.window._nodesGetFiltered().length, 0,
      'loadNodes() should still be blocked awaiting MeshConfigReady, not have finished filtering with a null config yet'
    );

    // Resolve late, mirroring roles.js's real .then() timing (MC_GEO_FILTER
    // is set immediately before the promise it's derived from resolves).
    env.ctx.window.MC_GEO_FILTER = GEO_BOX;
    resolveConfig();
    await settle(env.ctx);

    const filtered = env.ctx.window._nodesGetFiltered();
    assert.strictEqual(
      filtered.length, 3,
      'once MeshConfigReady resolves, the foreign filter should correctly show the 3 out-of-box nodes — not have locked in an "all domestic" result from before config arrived'
    );
  });

  console.log('\n════════════════════════════════════════');
  console.log(`  Nodes geo-scope filter: ${passed} passed, ${failed} failed`);
  console.log('════════════════════════════════════════');
  process.exit(failed === 0 ? 0 : 1);
})();

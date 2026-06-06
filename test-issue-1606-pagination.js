/* Regression test for issue #1606 — frontend nodes.js must paginate /api/nodes.
 *
 * Bug: v3.8.3 clamped /api/nodes ?limit to 500. public/nodes.js:1117 still
 * hard-codes limit=5000 and treats the single response as the full set, so
 * deployments with >500 nodes silently see only the top 500 by last_seen DESC.
 *
 * This test drives loadNodes() against a mocked api() that exposes a fixture
 * of 1200 nodes total, returning at most 500 per call (the new server cap).
 * After loadNodes() completes, _allNodes.length must equal data.total (1200).
 *
 * With the pre-fix single-fetch code, _allNodes.length == 500 and the
 * assertion below fails. With the pagination loop in place, it passes.
 */
'use strict';
const vm = require('vm');
const fs = require('fs');
const path = require('path');
const assert = require('assert');

let passed = 0, failed = 0;
const pending = [];
function test(name, fn) {
  try {
    const out = fn();
    if (out && typeof out.then === 'function') {
      pending.push(out.then(() => { passed++; console.log('  ✅ ' + name); })
        .catch(e => { failed++; console.log('  ❌ ' + name + ': ' + e.message); }));
      return;
    }
    passed++; console.log('  ✅ ' + name);
  } catch (e) {
    failed++; console.log('  ❌ ' + name + ': ' + e.message);
  }
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

function makeNodesEnv(totalNodes, serverCap) {
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

  // Fixture: `totalNodes` distinct nodes; api() returns at most `serverCap`
  // per request, honoring `offset` and `limit` query params.
  const fixture = [];
  for (let i = 0; i < totalNodes; i++) {
    fixture.push({
      public_key: i.toString(16).padStart(62, '0') + 'a0',
      name: 'Node' + i,
      role: 'repeater',
      advert_count: 1,
      last_seen: new Date(Date.now() - i * 1000).toISOString(),
    });
  }
  const apiCalls = [];
  ctx.api = function (url) {
    apiCalls.push(url);
    const q = url.indexOf('?') >= 0 ? url.slice(url.indexOf('?') + 1) : '';
    const params = new URLSearchParams(q);
    const offset = parseInt(params.get('offset') || '0', 10);
    const reqLimit = parseInt(params.get('limit') || '500', 10);
    const limit = Math.min(reqLimit, serverCap);
    const page = fixture.slice(offset, offset + limit);
    return Promise.resolve({
      nodes: page,
      total: fixture.length,
      counts: { repeaters: fixture.length },
    });
  };
  ctx.invalidateApiCache = () => {};

  // Stubs that nodes.js touches at module load and during loadNodes
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

  const repoRoot = path.resolve(__dirname);
  loadInCtx(ctx, path.join(repoRoot, 'public/nodes.js'));

  return { ctx, pageMod: () => pageMod, apiCalls, fixtureTotal: fixture.length };
}

console.log('=== issue #1606: nodes.js pagination ===');

test('loadNodes paginates and loads all 1200 nodes when server caps at 500', async () => {
  const env = makeNodesEnv(1200, 500);
  // pageMod.init() calls loadNodes() internally. Await one tick of microtasks
  // by reaching into the test hook and waiting on a fresh fetch.
  const appEl = env.ctx.document.getElementById('page');
  env.pageMod().init(appEl);
  // Give all queued promises a chance to settle. loadNodes is async; the init
  // path kicks it off but doesn't return its promise, so we loop until the
  // mocked api stops being called or a generous cap is hit.
  let lastCount = -1, stable = 0;
  for (let i = 0; i < 50; i++) {
    await new Promise(r => setImmediate(r));
    const n = env.ctx.window._nodesGetAllNodes();
    const cur = Array.isArray(n) ? n.length : -1;
    if (cur === lastCount) { stable++; if (stable > 3) break; } else { stable = 0; lastCount = cur; }
  }
  const all = env.ctx.window._nodesGetAllNodes();
  assert.ok(Array.isArray(all), '_allNodes must be an array, got ' + typeof all);
  assert.strictEqual(all.length, env.fixtureTotal,
    'expected _allNodes.length === total (' + env.fixtureTotal + '), got ' + all.length +
    ' — frontend silently truncated to server cap (issue #1606)');
});

// --- B1: partial-load deadlock (api throws on page 2) ---
test('B1: after api() throws mid-pagination, _allNodes resets so next call refetches', async () => {
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

  const fixture = [];
  for (let i = 0; i < 1200; i++) {
    fixture.push({ public_key: i.toString(16).padStart(62, '0') + 'b0', name: 'N' + i, role: 'repeater', advert_count: 1, last_seen: new Date().toISOString() });
  }
  let callCount = 0;
  let shouldThrow = true;
  ctx.api = function(url) {
    callCount++;
    const params = new URLSearchParams(url.split('?')[1] || '');
    const offset = parseInt(params.get('offset') || '0', 10);
    if (shouldThrow && offset >= 500) return Promise.reject(new Error('network error on page 2'));
    const page = fixture.slice(offset, offset + 500);
    return Promise.resolve({ nodes: page, total: 1200, counts: { repeaters: 1200 } });
  };
  ctx.invalidateApiCache = () => {};
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

  // First call — will fail on page 2
  pageMod.init(ctx.document.getElementById('page'));
  for (let i = 0; i < 50; i++) await new Promise(r => setImmediate(r));

  // _allNodes must be null/undefined after failure (not [] which is truthy and blocks refetch)
  const afterFail = ctx.window._nodesGetAllNodes();
  assert.ok(!afterFail || afterFail === null,
    'B1: after api() throws mid-pagination, _allNodes should be null/falsy so next call refetches, got: ' + JSON.stringify(afterFail && afterFail.length));
});

// --- M1: total instability — loop must use length-based exit, not total ---
test('M1: pagination fetches all pages even when total is understated (filter instability)', async () => {
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

  // Server reports total:50 (filtered count) but actually sends full pages
  const fixture = [];
  for (let i = 0; i < 1200; i++) {
    fixture.push({ public_key: i.toString(16).padStart(62, '0') + 'c0', name: 'M' + i, role: 'repeater', advert_count: 1, last_seen: new Date().toISOString() });
  }
  ctx.api = function(url) {
    const params = new URLSearchParams(url.split('?')[1] || '');
    const offset = parseInt(params.get('offset') || '0', 10);
    const page = fixture.slice(offset, offset + 500);
    // total is deliberately wrong (mimics routes.go:1357 area filter overwrite)
    return Promise.resolve({ nodes: page, total: 50, counts: { repeaters: 50 } });
  };
  ctx.invalidateApiCache = () => {};
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

  pageMod.init(ctx.document.getElementById('page'));
  for (let i = 0; i < 50; i++) await new Promise(r => setImmediate(r));

  const all = ctx.window._nodesGetAllNodes();
  assert.ok(Array.isArray(all), 'M1: _allNodes should be array');
  // With total-driven loop (current code), it stops at 50 nodes. Correct behavior: fetch all 1200.
  assert.strictEqual(all.length, 1200,
    'M1: expected 1200 nodes (length-based exit) but got ' + all.length + ' (total-driven exit stopped early)');
});

// --- M2: progress feedback during multi-page load ---
test('M2: progress feedback is shown between page fetches', async () => {
  const ctx = makeSandbox();
  const domElements = {};
  const domUpdates = [];
  function getEl(id) {
    if (!domElements[id]) {
      domElements[id] = {
        id, innerHTML: '', textContent: '', value: '', scrollTop: 0,
        style: {}, dataset: {},
        classList: { add(){}, remove(){}, toggle(){}, contains(){return false;} },
        addEventListener() {}, querySelectorAll() { return []; }, querySelector() { return null; },
        getAttribute() { return null; }, setAttribute() {}, appendChild() {},
      };
      // Track innerHTML/textContent changes for progress detection
      const orig = domElements[id];
      Object.defineProperty(orig, 'innerHTML', {
        get() { return orig._html || ''; },
        set(v) { orig._html = v; if (id === 'nodesBody' && v && v.includes('Loading')) domUpdates.push(v); }
      });
      Object.defineProperty(orig, 'textContent', {
        get() { return orig._text || ''; },
        set(v) { orig._text = v; if (id === 'nodesBody' && v && v.includes('Loading')) domUpdates.push(v); }
      });
    }
    return domElements[id];
  }
  ctx.document.getElementById = getEl;

  const fixture = [];
  for (let i = 0; i < 1200; i++) {
    fixture.push({ public_key: i.toString(16).padStart(62, '0') + 'd0', name: 'P' + i, role: 'repeater', advert_count: 1, last_seen: new Date().toISOString() });
  }
  ctx.api = function(url) {
    const params = new URLSearchParams(url.split('?')[1] || '');
    const offset = parseInt(params.get('offset') || '0', 10);
    const page = fixture.slice(offset, offset + 500);
    return Promise.resolve({ nodes: page, total: 1200, counts: { repeaters: 1200 } });
  };
  ctx.invalidateApiCache = () => {};
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

  pageMod.init(ctx.document.getElementById('page'));
  for (let i = 0; i < 50; i++) await new Promise(r => setImmediate(r));

  // At least one intermediate progress update should have been written to DOM
  assert.ok(domUpdates.length > 0,
    'M2: expected progress feedback in #nodesBody during pagination, got 0 updates');
});

Promise.allSettled(pending).then(() => {
  console.log('\n  Issue #1606: ' + passed + ' passed, ' + failed + ' failed\n');
  if (failed > 0) process.exit(1);
});

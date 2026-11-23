/* Teardown races on the full-page maps (map, live, planner).
 *
 * The race stress suite (tests/e2e/test-map-race-stress-e2e.js) found three
 * ways a page could throw after the user had already navigated away:
 *
 *   1. Leaflet 1.9.4's _animateZoom() schedules
 *      setTimeout(_onZoomTransitionEnd, 250) and keeps no handle, and
 *      map.remove() does not cancel it. Removing the map inside that window
 *      left the callback to run _move() on the deleted _mapPane:
 *      "Cannot read properties of undefined (reading '_leaflet_pos')".
 *      window.MC_safeRemoveMap (roles.js) is the shared fix.
 *   2. live.js init() kept going after `await loadNodes()` when the page had
 *      been destroyed meanwhile, and called map.on() on the nulled map.
 *   3. markercluster's chunkedLoading keeps adding markers in setTimeout
 *      chunks it gives no handle for; once map.js tore the map down the next
 *      chunk threw in this._map.getMinZoom().
 *
 * Everything runs the real files (Leaflet, markercluster, roles.js, map.js,
 * live.js) in a jsdom window with a hand-driven clock, so each race is
 * replayed deterministically instead of by timing.
 */
'use strict';
/* global setImmediate */
const fs = require('fs');
const path = require('path');
const assert = require('assert');
const { JSDOM } = require('jsdom');
const { loadApiBase } = require('../unit/api-base-sandbox');

const ROOT = path.join(__dirname, '..', '..');
const read = (p) => fs.readFileSync(path.join(ROOT, p), 'utf8');
const LEAFLET_SRC = read('public/vendor/leaflet-1.9.4/leaflet.js');
const CLUSTER_SRC = read('public/vendor/leaflet.markercluster.js');
const ROLES_SRC = read('public/roles.js');
const MAP_SRC = read('public/map.js');
const AREA_SRC = read('public/area-filter.js');
const REGION_SRC = read('public/region-filter.js');
const LIVE_SRC = read('public/live.js');
const PAYLOAD_LABELS_SRC = read('public/payload-labels.js');

let passed = 0, failed = 0;
async function test(name, fn) {
  try { await fn(); passed++; console.log('  ✅ ' + name); }
  catch (e) { failed++; console.log('  ❌ ' + name + ': ' + (e && e.message !== undefined ? e.message : e)); if (process.env.TEARDOWN_DEBUG) console.log(e && e.stack); }
}

// A clock the test advances by hand. Timers and frames that throw are
// collected instead of escaping, so a test can assert "nothing threw".
function installClock(win) {
  const timers = new Map();
  let seq = 0;
  const clock = { now: 0, errors: [] };
  function add(fn, ms, every) {
    const id = ++seq;
    timers.set(id, { fn, at: clock.now + Math.max(0, Number(ms) || 0), every });
    return id;
  }
  win.setTimeout = (fn, ms) => add(fn, ms, 0);
  win.clearTimeout = (id) => { timers.delete(id); };
  win.setInterval = (fn, ms) => add(fn, ms, Math.max(1, Number(ms) || 1));
  win.clearInterval = (id) => { timers.delete(id); };
  win.requestAnimationFrame = (fn) => add(() => fn(clock.now), 16, 0);
  win.cancelAnimationFrame = (id) => { timers.delete(id); };
  const RealDate = win.Date;
  // markercluster times its chunks with (new Date).getTime().
  win.Date = class extends RealDate {
    constructor(...a) { if (a.length) super(...a); else super(clock.now); }
    static now() { return clock.now; }
  };
  Object.defineProperty(win, 'performance', { value: { now: () => clock.now }, configurable: true, writable: true });
  clock.pending = () => timers.size;
  clock.advance = (ms) => {
    const until = clock.now + ms;
    for (;;) {
      let nextId = null, next = null;
      for (const [id, t] of timers) if (t.at <= until && (!next || t.at < next.at)) { nextId = id; next = t; }
      if (!next) break;
      clock.now = Math.max(clock.now, next.at);
      if (next.every) next.at = clock.now + next.every; else timers.delete(nextId);
      try { next.fn(); } catch (e) { clock.errors.push(e); }
    }
    clock.now = until;
  };
  return clock;
}

// A jsdom window with the real Leaflet + markercluster loaded.
function makeWindow(opts) {
  opts = opts || {};
  const dom = new JSDOM('<!doctype html><html><body><div id="app"></div></body></html>', {
    url: 'http://localhost/', runScripts: 'outside-only', pretendToBeVisual: false,
  });
  const win = dom.window;
  const clock = installClock(win);
  const consoleErrors = [];
  win.console = {
    log() {}, info() {}, debug() {}, warn() {},
    error: (...a) => consoleErrors.push(a.map(x => (x && x.stack) || String(x)).join(' ')),
  };
  win.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {} });
  win.fetch = opts.fetch || (() => Promise.resolve({ ok: true, json: () => Promise.resolve({}) }));
  // jsdom has no canvas backend; live.js draws its animations on one.
  const noop = () => {};
  win.HTMLCanvasElement.prototype.getContext = () => ({
    setTransform: noop, clearRect: noop, fillRect: noop, beginPath: noop, arc: noop, fill: noop, stroke: noop,
    moveTo: noop, lineTo: noop, scale: noop, fillText: noop, save: noop, restore: noop, measureText: () => ({ width: 0 }),
  });
  const ctx = dom.getInternalVMContext();
  win.eval(LEAFLET_SRC);
  win.eval(CLUSTER_SRC);
  loadApiBase(ctx);
  win.eval(ROLES_SRC);
  return { dom, win, clock, consoleErrors };
}

function newMap(win) {
  const el = win.document.createElement('div');
  win.document.body.appendChild(el);
  return win.L.map(el, { zoomAnimation: true }).setView([51, 4], 9);
}

// Stands in for map-tile-providers.js: a base layer is what gives the map its
// maxZoom, which markercluster requires.
function attachTiles(map) {
  const layer = map.addLayer ? window_L(map).gridLayer({ maxZoom: 19 }).addTo(map) : null;
  return { group: layer, sync() {}, destroy() { if (layer && layer._map) layer.remove(); } };
}
function window_L(map) { return map._container.ownerDocument.defaultView.L; }

const isLeafletPos = (e) => /_leaflet_pos/.test(String(e && e.message));

// Lets pending promise callbacks run without touching the fake clock.
const settle = async (n) => { for (let i = 0; i < (n || 20); i++) await new Promise(r => setImmediate(r)); };

// An AreaFilter whose subscriber list is observable (same semantics as
// public/area-filter.js: onChange pushes, offChange removes by identity).
function countingAreaFilter() {
  let listeners = [];
  return {
    init() {}, areaQueryString: () => '',
    onChange(fn) { listeners.push(fn); return fn; },
    offChange(fn) { listeners = listeners.filter(f => f !== fn); },
    count: () => listeners.length,
  };
}

function deferred() {
  let resolve;
  const promise = new Promise(r => { resolve = r; });
  return { promise, resolve };
}

(async () => {
  console.log('\n=== MC_safeRemoveMap: Leaflet zoom-transition timer ===');

  await test('fixture: plain off()+remove() inside a zoom animation throws _leaflet_pos (the Leaflet bug)', () => {
    const { win, clock } = makeWindow();
    const map = newMap(win);
    map._animateZoom(win.L.latLng(51, 4), 10, true);
    map.off(); map.remove();
    clock.advance(300);
    assert.ok(clock.errors.some(isLeafletPos), 'the fixture no longer reproduces the race: ' + clock.errors.map(String).join(' | '));
  });

  await test('MC_safeRemoveMap inside a zoom animation: the pending transition end is a no-op', () => {
    const { win, clock } = makeWindow();
    assert.strictEqual(typeof win.MC_safeRemoveMap, 'function', 'roles.js does not define window.MC_safeRemoveMap');
    const map = newMap(win);
    let zoomEnds = 0, moves = 0;
    map.on('zoomend', () => zoomEnds++);
    map.on('move moveend', () => moves++);
    map._animateZoom(win.L.latLng(51, 4), 10, true);
    assert.strictEqual(map._animatingZoom, true, 'fixture did not start a zoom animation');
    const before = { zoomEnds, moves };
    win.MC_safeRemoveMap(map);
    clock.advance(1000);
    assert.deepStrictEqual(clock.errors.map(String), []);
    assert.strictEqual(map._animatingZoom, false);
    assert.deepStrictEqual({ zoomEnds, moves }, before, 'a listener ran after the map was removed');
    assert.ok(!map._mapPane, 'map was not removed');
  });

  await test('MC_safeRemoveMap with a queued animated-zoom frame and a pan animation does not throw', () => {
    const { win, clock } = makeWindow();
    const map = newMap(win);
    // jsdom has no 3D transforms, so force the animated path Leaflet takes in a browser.
    map._zoomAnimated = true;
    map._nothingToAnimate = () => false;
    map.setZoom(10, { animate: true });
    map.panBy([50, 50], { animate: true, duration: 1 });
    win.MC_safeRemoveMap(map);
    clock.advance(2000);
    assert.deepStrictEqual(clock.errors.map(String), []);
  });

  await test('MC_safeRemoveMap is idempotent and null-safe', () => {
    const { win, clock } = makeWindow();
    const map = newMap(win);
    win.MC_safeRemoveMap(map);
    win.MC_safeRemoveMap(map);
    win.MC_safeRemoveMap(null);
    win.MC_safeRemoveMap(undefined);
    clock.advance(500);
    assert.deepStrictEqual(clock.errors.map(String), []);
  });

  await test('MC_safeRemoveMap swallows a failing remove() and still detaches listeners', () => {
    const { win } = makeWindow();
    let offCalls = 0;
    const fake = { off() { offCalls++; return this; }, remove() { throw new Error('container reused'); } };
    win.MC_safeRemoveMap(fake);
    assert.strictEqual(offCalls, 1);
  });

  await test('MC_safeRemoveMap fires unload (controls detach) and drops listeners afterwards', () => {
    const { win, clock } = makeWindow();
    const map = newMap(win);
    let unloads = 0, moves = 0, removedControls = 0;
    map.on('unload', () => unloads++);
    map.on('moveend', () => moves++);
    const ctl = win.L.control({ position: 'topleft' });
    ctl.onAdd = () => win.document.createElement('div');
    ctl.onRemove = () => { removedControls++; };
    ctl.addTo(map);
    win.MC_safeRemoveMap(map);
    assert.strictEqual(unloads, 1, 'unload listeners must run (Leaflet and disposal watchers rely on it)');
    assert.strictEqual(removedControls, 1, 'controls detach on unload');
    assert.ok(!map._events || Object.keys(map._events).every(k => !map._events[k] || map._events[k].length === 0),
      'listeners remain after removal');
    map.fire('moveend');
    clock.advance(500);
    assert.strictEqual(moves, 0);
    assert.deepStrictEqual(clock.errors.map(String), []);
  });

  // ── map.js ──────────────────────────────────────────────────────────────

  function mapPageWindow(opts) {
    const env = makeWindow(opts);
    const { win } = env;
    let page = null;
    win.registerPage = (name, h) => { if (name === 'map') page = h; };
    win.escapeHtml = (s) => String(s == null ? '' : s).replace(/[&<>"']/g, c => '&#' + c.charCodeAt(0) + ';');
    win.timeAgo = () => 'now';
    win.truncate = (s) => s;
    win.onWS = () => {}; win.offWS = () => {};
    win.debouncedOnWS = (fn) => fn;
    win.CLIENT_TTL = { nodeList: 0, observers: 0, analyticsRF: 0 };
    win.AreaFilter = { init() {}, onChange() {}, offChange() {}, areaQueryString: () => '' };
    win.api = opts.api || ((p) => Promise.resolve(p === '/observers' ? { observers: [] } : {}));
    win.fetchAllNodes = opts.fetchAllNodes || (() => Promise.resolve({ nodes: [], counts: {} }));
    win.MC_attachAutoTiles = attachTiles;
    win.eval(MAP_SRC);
    env.page = () => page;
    env.mount = () => {
      const app = win.document.getElementById('app');
      return page.init(app);
    };
    return env;
  }

  function nodesAround(n) {
    const out = [];
    for (let i = 0; i < n; i++) {
      out.push({ public_key: 'k' + i, name: 'n' + i, role: 'repeater', lat: 51 + (i % 50) / 100, lon: 4 + Math.floor(i / 50) / 100, last_seen: new Date(0).toISOString() });
    }
    return out;
  }

  console.log('\n=== map.js teardown ===');

  await test('map.js: navigating away during the /api/config/map fetch never builds a map', async () => {
    const cfg = deferred();
    const env = mapPageWindow({
      fetch: (url) => String(url).indexOf('/api/config/map') >= 0 ? cfg.promise : Promise.resolve({ ok: true, json: () => Promise.resolve({}) }),
    });
    const created = [];
    const realMap = env.win.L.map;
    env.win.L.map = function () { const m = realMap.apply(this, arguments); created.push(m); return m; };
    const done = env.mount();
    await settle();
    env.page().destroy();
    cfg.resolve({ ok: true, json: () => Promise.resolve({ center: [51, 4], zoom: 9 }) });
    await done;
    await settle();
    env.clock.advance(2000);
    assert.strictEqual(created.length, 0, 'init built a map after the page was destroyed');
    assert.deepStrictEqual(env.clock.errors.map(String), []);
  });

  await test('map.js: destroy while markercluster is still adding chunks — no getMinZoom of null', async () => {
    const env = mapPageWindow({ fetchAllNodes: () => Promise.resolve({ nodes: nodesAround(600), counts: {} }) });
    // Make every marker cost 1 ms so the 100 ms chunk budget runs out and the
    // rest of the markers are left to a pending setTimeout chunk.
    const proto = env.win.L.MarkerClusterGroup.prototype;
    const realAdd = proto._addLayer;
    proto._addLayer = function () { env.clock.now += 1; return realAdd.apply(this, arguments); };
    await env.mount();
    await settle();
    const leaflet = env.win.document.getElementById('leaflet-map');
    assert.strictEqual(leaflet.getAttribute('data-loaded'), 'true', 'loadNodes did not finish');
    assert.ok(env.clock.pending() > 0, 'fixture did not leave a chunk pending');
    env.page().destroy();
    env.clock.advance(5000);
    assert.deepStrictEqual(env.clock.errors.map(String), []);
  });

  await test('map.js: destroy with a markercluster animation step queued — the queued step never runs', async () => {
    const env = mapPageWindow({ fetchAllNodes: () => Promise.resolve({ nodes: nodesAround(50), counts: {} }) });
    await env.mount();
    await settle();
    const map = env.win.__mc_map;
    let group = null;
    map.eachLayer((l) => { if (l instanceof env.win.L.MarkerClusterGroup) group = l; });
    assert.ok(group && group._topClusterLevel, 'cluster group not on the map');
    // The shape of markercluster's _animationZoomIn tail: a step queued with
    // _enqueue (300 ms timer) that walks the cluster tree.
    let ran = 0;
    group._enqueue(function () {
      ran++;
      this._topClusterLevel._recursively(map.getBounds(), 0, 1, function () {});
    });
    env.page().destroy();
    env.clock.advance(1000);
    assert.deepStrictEqual(env.clock.errors.map(String), []);
    assert.strictEqual(ran, 0, 'queued cluster animation step ran after destroy');
  });

  await test('map.js: destroy inside a zoom animation does not throw _leaflet_pos', async () => {
    const env = mapPageWindow({});
    await env.mount();
    await settle();
    const map = env.win.__mc_map;
    assert.ok(map && map._mapPane, 'map not built');
    map._animateZoom(env.win.L.latLng(51, 4), 10, true);
    env.page().destroy();
    env.clock.advance(1000);
    assert.deepStrictEqual(env.clock.errors.map(String), []);
  });

  await test('map.js: visit/destroy x10 leaves one AreaFilter handler, none after the last destroy', async () => {
    const env = mapPageWindow({});
    const af = env.win.AreaFilter = countingAreaFilter();
    for (let i = 0; i < 10; i++) {
      await env.mount();
      await settle();
      assert.strictEqual(af.count(), 1, 'visit ' + (i + 1));
      env.page().destroy();
      assert.strictEqual(af.count(), 0, 'after destroy ' + (i + 1));
    }
  });

  // ── live.js ─────────────────────────────────────────────────────────────

  function livePageWindow(opts) {
    const env = makeWindow(opts);
    const { win } = env;
    let page = null;
    win.registerPage = (name, h) => { if (name === 'live') page = h; };
    win.getParsedDecoded = () => ({});
    win.getParsedPath = () => [];
    win.getHashParams = () => new win.URLSearchParams('');
    win.debounce = (fn, ms) => { let t; return (...a) => { win.clearTimeout(t); t = win.setTimeout(() => fn(...a), ms); }; };
    win.escapeHtml = (s) => String(s == null ? '' : s);
    win.timeAgo = () => 'now';
    win.onWS = () => {}; win.offWS = () => {}; win.connectWS = () => {};
    win.debouncedOnWS = (fn) => fn;
    win.api = () => Promise.resolve({});
    win.invalidateApiCache = () => {};
    win.HopResolver = { init() {}, resolve: () => ({}), ready: () => false, setAffinity() {} };
    win.RegionFilter = { init() {}, getSelected: () => null, onChange: () => 1, offChange() {}, regionQueryString: () => '', nodesRegionQueryString: () => '' };
    win.AreaFilter = { init() {}, onChange() {}, offChange() {}, areaQueryString: () => '' };
    win.MC_attachAutoTiles = attachTiles;
    win.fetchAllNodes = opts.fetchAllNodes;
    win.eval(PAYLOAD_LABELS_SRC);
    win.eval(LIVE_SRC);
    env.page = () => page;
    env.mount = () => page.init(win.document.getElementById('app'));
    return env;
  }

  console.log('\n=== live.js teardown ===');

  await test('live.js: navigating away during loadNodes() — init stops: no map.on, no affinity fetch/refresh on the dead page', async () => {
    const nodesReq = deferred();
    const fetched = [];
    const env = livePageWindow({
      fetchAllNodes: () => nodesReq.promise,
      fetch: (url) => { fetched.push(String(url)); return Promise.resolve({ ok: true, json: () => Promise.resolve({}) }); },
    });
    const maps = [];
    const realMap = env.win.L.map;
    env.win.L.map = function () { const m = realMap.apply(this, arguments); maps.push(m); return m; };
    const done = env.mount();
    await settle();
    assert.strictEqual(maps.length, 1, 'init did not reach the map before loadNodes');
    const liveMap = maps[0];
    const onCalls = [];
    const realOn = liveMap.on;
    liveMap.on = function (types) { onCalls.push(types); return realOn.apply(this, arguments); };
    env.page().destroy();
    fetched.length = 0;
    nodesReq.resolve({ nodes: nodesAround(20) });
    let initError = null;
    try { await done; } catch (e) { initError = e; }
    await settle();
    env.clock.advance(5000);
    assert.strictEqual(initError, null, 'init rejected: ' + (initError && initError.message));
    assert.deepStrictEqual(onCalls, [], 'init wired map listeners after destroy');
    assert.deepStrictEqual(fetched.filter(u => /neighbor-graph/.test(u)), [], 'loadNodes went on to fetch affinity for a destroyed page');
    assert.deepStrictEqual(env.clock.errors.map(String), []);
    assert.deepStrictEqual(env.consoleErrors, []);
  });

  await test('live.js: navigating away during the /api/config/map fetch never builds a map', async () => {
    const cfg = deferred();
    const env = livePageWindow({
      fetchAllNodes: () => Promise.resolve({ nodes: [] }),
      fetch: (url) => String(url).indexOf('/api/config/map') >= 0 ? cfg.promise : Promise.resolve({ ok: true, json: () => Promise.resolve({}) }),
    });
    const maps = [];
    const realMap = env.win.L.map;
    env.win.L.map = function () { const m = realMap.apply(this, arguments); maps.push(m); return m; };
    const done = env.mount();
    await settle();
    env.page().destroy();
    cfg.resolve({ ok: true, json: () => Promise.resolve({ center: [51, 4], zoom: 9 }) });
    let initError = null;
    try { await done; } catch (e) { initError = e; }
    await settle();
    env.clock.advance(2000);
    assert.strictEqual(initError, null, 'init rejected: ' + (initError && initError.message));
    assert.strictEqual(maps.length, 0, 'init built a map after the page was destroyed');
    assert.deepStrictEqual(env.clock.errors.map(String), []);
  });

  await test('live.js: destroy inside a zoom animation does not throw _leaflet_pos', async () => {
    const env = livePageWindow({ fetchAllNodes: () => Promise.resolve({ nodes: nodesAround(5) }) });
    const maps = [];
    const realMap = env.win.L.map;
    env.win.L.map = function () { const m = realMap.apply(this, arguments); maps.push(m); return m; };
    await env.mount();
    await settle();
    maps[0]._animateZoom(env.win.L.latLng(51, 4), 10, true);
    env.page().destroy();
    env.clock.advance(1000);
    assert.deepStrictEqual(env.clock.errors.map(String), []);
  });

  await test('live.js: visit/destroy x10 leaves one AreaFilter handler, none after the last destroy', async () => {
    const env = livePageWindow({ fetchAllNodes: () => Promise.resolve({ nodes: nodesAround(5) }) });
    const af = env.win.AreaFilter = countingAreaFilter();
    for (let i = 0; i < 10; i++) {
      await env.mount();
      await settle();
      assert.strictEqual(af.count(), 1, 'visit ' + (i + 1));
      env.page().destroy();
      assert.strictEqual(af.count(), 0, 'after destroy ' + (i + 1));
    }
  });

  // ── document click-capture listener leak (area/region filter) ──────────
  // The real filters add a capture-phase click listener on document that
  // closes over the page's container; a page that never releases it pins the
  // whole detached controls DOM on every visit.
  function realFilters(env) {
    const { win } = env;
    const json = (d) => ({ json: () => Promise.resolve(d) });
    win.CSApi = { fetch: (u) => Promise.resolve(json(
      u.indexOf('areas') >= 0 ? [{ key: 'a', label: 'A' }, { key: 'b', label: 'B' }]
        : u.indexOf('quick-picks') >= 0 ? [] : { r1: 'One', r2: 'Two' })) };
    win.eval(AREA_SRC);
    win.eval(REGION_SRC);
    const live = new Set();
    let added = 0, removed = 0;
    const add = win.document.addEventListener.bind(win.document);
    const rem = win.document.removeEventListener.bind(win.document);
    win.document.addEventListener = (t, fn, o) => {
      if (t === 'click' && o === true) { added++; live.add(fn); }
      return add(t, fn, o);
    };
    win.document.removeEventListener = (t, fn, o) => {
      if (t === 'click' && o === true) { removed++; live.delete(fn); }
      return rem(t, fn, o);
    };
    return { stats: () => ({ added, removed, live: live.size }) };
  }

  async function visits(env, probe) {
    for (let i = 0; i < 10; i++) {
      await env.mount();
      await settle();
      env.page().destroy();
    }
    const st = probe.stats();
    assert.ok(st.added >= 10, 'listeners were never added: ' + JSON.stringify(st));
    assert.strictEqual(st.live, 0, 'leaked click-capture listeners: ' + JSON.stringify(st));
    assert.strictEqual(st.added, st.removed);
  }

  await test('map.js: real AreaFilter leaves no document click-capture listener after 10 visits', async () => {
    const env = mapPageWindow({});
    const probe = realFilters(env);
    await visits(env, probe);
    assert.strictEqual(env.win.__areaContainerAfterDestroy, undefined);
  });

  await test('live.js: real Area+RegionFilter leave no document click-capture listener after 10 visits', async () => {
    const env = livePageWindow({ fetchAllNodes: () => Promise.resolve({ nodes: nodesAround(5) }) });
    const probe = realFilters(env);
    await visits(env, probe);
  });

  await test('AreaFilter/RegionFilter.render on a new container releases the previous one', async () => {
    const env = mapPageWindow({});
    const probe = realFilters(env);
    const { win } = env;
    for (const F of [win.AreaFilter, win.RegionFilter]) {
      const mk = () => { const d = win.document.createElement('div'); win.document.body.appendChild(d); return d; };
      const a = mk(), b = mk();
      await F.init(a, { dropdown: true }); await F.init(b, { dropdown: true });
      assert.strictEqual(probe.stats().live, 1, 'one live listener after switching containers');
      F.destroy();
      assert.strictEqual(probe.stats().live, 0);
      F.destroy();
    }
  });

  await test('AreaFilter.init that resolves after destroy does not re-add the listener', async () => {
    const env = mapPageWindow({});
    const probe = realFilters(env);
    const d = env.win.document.createElement('div');
    env.win.document.body.appendChild(d);
    const p = env.win.AreaFilter.init(d);
    env.win.AreaFilter.destroy();
    await p;
    assert.strictEqual(probe.stats().live, 0);
  });

  console.log('\n' + passed + ' passed, ' + failed + ' failed');
  process.exit(failed === 0 ? 0 : 1);
})();

/* test-issue-2444-secondary-map-tile-sync.js — the Home mini-map and the
 * Analytics route map follow the base-map pick like the other picker-less
 * maps (#2444 review): a pick in this tab, a pick saved in another tab (a
 * 'storage' event), and a theme flip each swap the layer in place, and every
 * teardown drops the map from the shared tile-sync registry first.
 *
 * Real code throughout, in one vm sandbox per case:
 *   - public/map-tile-providers.js and public/roles.js, loaded whole
 *   - _applyTilesToNodeMap from public/nodes.js, and loadHealth / renderHome /
 *     showChooser / destroy (home.js), renderSubpathDetail /
 *     loadSubpathDetail / renderTab / destroy (analytics.js) with their
 *     map-removal helpers, extracted by brace counting (their page closures
 *     make them otherwise unreachable)
 * Leaflet is a mock that records which layers each map holds.
 *
 * Runs via: node tests/unit/test-issue-2444-secondary-map-tile-sync.js
 */
'use strict';
const vm = require('vm');
const fs = require('fs');
const path = require('path');
const assert = require('assert');
const ROOT = path.resolve(__dirname, '..', '..');
const { loadApiBase } = require('./api-base-sandbox');

let passed = 0, failed = 0;
async function test(name, fn) {
  try { await fn(); passed++; console.log('  ✅ ' + name); }
  catch (e) { failed++; console.log('  ❌ ' + name + ': ' + e.message); }
}

const read = (file) => fs.readFileSync(path.join(ROOT, 'public', file), 'utf8');
// The source of one function, or '' when the file has none by that name.
function extract(file, re) {
  const src = read(file);
  const m = src.match(re);
  if (!m) return '';
  let depth = 0, i = src.indexOf('{', m.index + m[0].length - 1);
  for (; i < src.length; i++) {
    if (src[i] === '{') depth++;
    else if (src[i] === '}' && --depth === 0) { i++; break; }
  }
  return src.slice(m.index, i);
}
function must(file, re) {
  const s = extract(file, re);
  if (!s) throw new Error(re + ' not found in ' + file);
  return s;
}

const HELPER = must('nodes.js', /function _applyTilesToNodeMap\s*\(map\)\s*\{/);
const HOME = [
  must('home.js', /async function loadHealth\s*\(pubkey\)\s*\{/),
  must('home.js', /function destroy\s*\(\)\s*\{/),
  must('home.js', /function renderHome\s*\(container\)\s*\{/),
  must('home.js', /function showChooser\s*\(container\)\s*\{/),
  extract('home.js', /function removeMiniMap\s*\(\)\s*\{/),
].join('\n');
const ANALYTICS = [
  must('analytics.js', /function renderSubpathDetail\s*\(panel, data\)\s*\{/),
  must('analytics.js', /async function loadSubpathDetail\s*\(hopsStr\)\s*\{/),
  must('analytics.js', /function destroy\s*\(\)\s*\{/),
  must('analytics.js', /async function renderTab\s*\(tab\)\s*\{/),
  extract('analytics.js', /function removeSubpathMap\s*\(\)\s*\{/),
].join('\n');

// Leaflet mock: a map is the list of layers on it. remove() notes whether the
// tile-sync registry still held the map at that moment.
function makeL(ctx) {
  const maps = [];
  const mkLayer = (extra) => {
    const l = Object.assign({ addTo(map) { map.layers.push(l); return l; } }, extra);
    return l;
  };
  const L = {
    map() {
      const pane = { style: { filter: '' } };
      const map = {
        layers: [], pane, removed: 0, invalidated: 0, registeredAtRemove: null,
        container: { isConnected: true },
        getPane: () => pane, getContainer: () => map.container,
        setView() { return map; }, fitBounds() {}, invalidateSize() { map.invalidated++; }, off() {},
        removeLayer(l) { const i = map.layers.indexOf(l); if (i >= 0) map.layers.splice(i, 1); },
        remove() { map.removed++; map.registeredAtRemove = registered(ctx, map); },
      };
      maps.push(map);
      return map;
    },
    tileLayer: (url, opts) => mkLayer({ url, opts: opts || {} }),
    layerGroup: (layers) => mkLayer({ group: layers }),
    circleMarker: () => ({ bindTooltip() { return this; }, addTo() {} }),
    marker: () => ({ addTo() {} }),
    polyline: () => ({ addTo() {} }),
    latLngBounds: () => ({ pad: () => ({}) }),
  };
  return { L, maps };
}
function registered(ctx, map) {
  const st = ctx.__mcNodeTileSync;
  return !!(st && st.maps.some((r) => r.map === map));
}
// The registry holds exactly these maps (identity; vm arrays are cross-realm).
function holds(s, want) {
  const got = s.registry();
  assert.ok(got.length === want.length && want.every((m, i) => got[i] === m),
    'registry holds ' + got.length + ' map(s), expected ' + want.length);
}
// The one base layer on a map (fails when layers stacked up).
function base(map) {
  const tiles = map.layers.filter((l) => l.url || l.group);
  assert.strictEqual(tiles.length, 1, tiles.length + ' base layers on the map');
  return tiles[0].group ? tiles[0].group[0] : tiles[0];
}

const PROVIDERS = { osmfr: { enabled: true }, opentopomap: { enabled: true }, usgs: { enabled: true } };

function makeSandbox(opts) {
  opts = opts || {};
  const listeners = {};
  const observers = [];
  const store = {};
  const state = { theme: 'light' };
  const timers = [];
  const ctx = {
    console: { log: console.log, error: console.error, warn() {} },
    JSON, Date, Math, Object, Array, String, Number, Boolean, Promise, Set, Map, isFinite, encodeURIComponent,
    setTimeout: (fn) => timers.push(fn), clearTimeout() {}, // queued; run by flushTimers
    fetch: () => new Promise(() => {}), // roles.js's config fetch never lands: MC_MAP_CFG below stays
    localStorage: {
      getItem: (k) => (Object.prototype.hasOwnProperty.call(store, k) ? store[k] : null),
      setItem(k, v) { store[k] = String(v); },
      removeItem(k) { delete store[k]; },
    },
    addEventListener(type, fn) { (listeners[type] = listeners[type] || []).push(fn); },
    removeEventListener() {},
    dispatchEvent(ev) { (listeners[ev.type] || []).slice().forEach((fn) => fn(ev)); return true; },
    matchMedia: () => ({ matches: false, addEventListener() {} }),
    CustomEvent: function (type, init) { this.type = type; this.detail = (init && init.detail) || null; },
    MutationObserver: function (cb) { this.observe = () => { observers.push(cb); }; this.disconnect = () => {}; },
  };
  ctx.window = ctx;
  ctx.globalThis = ctx;
  ctx.document = {
    documentElement: {
      getAttribute: (k) => (k === 'data-theme' ? state.theme : null),
      style: { getPropertyValue: () => '' },
    },
    querySelector: () => null, querySelectorAll: () => [], getElementById: () => null,
    createElement: () => ({ style: {}, appendChild() {}, setAttribute() {}, addEventListener() {} }),
    addEventListener() {}, body: { appendChild() {}, style: {} }, head: { appendChild() {} },
    readyState: 'complete',
  };
  const leaflet = makeL(ctx);
  ctx.L = leaflet.L;
  vm.createContext(ctx);
  loadApiBase(ctx);
  ctx.MC_MAP_CFG = { tiles: { providers: PROVIDERS } };
  if (!opts.noTileModule) vm.runInContext(read('map-tile-providers.js'), ctx, { filename: 'map-tile-providers.js' });
  vm.runInContext(read('roles.js'), ctx, { filename: 'roles.js' });
  if (!opts.noHelper) vm.runInContext(HELPER + '\nwindow._applyTilesToNodeMap = _applyTilesToNodeMap;', ctx);
  return {
    ctx, maps: leaflet.maps, store,
    pick(id) { ctx.MC_setPinnedTileProvider(id); },
    // Another tab saved a pick: storage already holds it, then 'storage' arrives.
    otherTabPick(id) {
      if (id) store['mc-tile-pinned'] = id; else delete store['mc-tile-pinned'];
      ctx.dispatchEvent({ type: 'storage', key: 'mc-tile-pinned', newValue: id, oldValue: null, storageArea: ctx.localStorage });
    },
    setTheme(t) { state.theme = t; observers.slice().forEach((cb) => cb([])); },
    // Run every queued setTimeout callback; one that throws fails the test.
    flushTimers() { timers.splice(0).forEach((fn) => { if (typeof fn === 'function') fn(); }); },
    registry: () => (ctx.__mcNodeTileSync ? ctx.__mcNodeTileSync.maps.map((r) => r.map) : []),
  };
}

// ── Home: the health card's mini-map ────────────────────────────────────────
function home(opts) {
  const s = makeSandbox(opts);
  const card = {
    innerHTML: '', classList: { add() {}, remove() {} },
    querySelector: () => null, querySelectorAll: () => [], scrollIntoView() {},
  };
  let node = { lat: 10, lon: 20, name: 'n', role: 'repeater' };
  Object.assign(s.ctx, {
    // _healthGen: the load counter #2461 adds (harmless without it).
    CLIENT_TTL: {}, miniMap: null, _healthGen: 0, searchTimeout: null, searchAbort: null, _themeRefreshHandler: null,
    api: async () => ({ node, stats: {}, recentPackets: [], observers: [] }),
    isMyNode: () => true, getHealthStatus: () => 'healthy', timeAgo: () => 'now', truncate: String,
    snrQuality: () => ({ label: 'n/a', color: 'var(--text-muted)' }),
    escapeHtml: String, escapeAttr: String, addMyNode() {}, loadMyNodes() {}, showJourney() {},
    isExperienced: () => true, getMyNodes: () => [], checklist: () => '', setupSearch() {}, loadStats() {}, setLevel() {},
    __page: { innerHTML: '', querySelectorAll: () => [] },
  });
  const other = { addEventListener() {}, classList: { add() {}, remove() {} } };
  s.ctx.document.getElementById = (id) => (id === 'homeHealth' ? card : id === 'healthMap' ? {} : other);
  vm.runInContext(HOME, s.ctx);
  // The page content replaced under the map: the theme refresh, a level change.
  s.rerenders = { 'renderHome (theme refresh)': 'renderHome(__page)', 'showChooser': 'showChooser(__page)' };
  s.card = card;
  s.show = async (n) => {
    if (n) node = n;
    await vm.runInContext('loadHealth("abc")', s.ctx);
    assert.ok(!/Failed to load/.test(card.innerHTML), 'loadHealth failed: ' + card.innerHTML.slice(0, 80));
    return s.maps[s.maps.length - 1];
  };
  s.destroy = () => vm.runInContext('destroy()', s.ctx);
  return s;
}

// ── Analytics: the route (subpath) detail map ───────────────────────────────
const ROUTE = {
  nodes: [{ name: 'a', lat: 1, lon: 2 }, { name: 'b', lat: 3, lon: 4 }],
  hops: ['aa', 'bb'], hourDistribution: new Array(24).fill(1), signal: {}, observers: [],
  parentPaths: [], firstSeen: null, lastSeen: null, totalMatches: 1,
};
function analytics(opts) {
  const s = makeSandbox(opts);
  const panel = { innerHTML: '', classList: { add() {}, remove() {} } };
  let data = ROUTE;
  Object.assign(s.ctx, {
    // _subpathGen: the load counter #2461 adds (harmless without it).
    _subpathMap: null, _subpathGen: 0, CLIENT_TTL: {}, esc: String, formatDistance: String,
    statusGreen: () => 'g', statusRed: () => 'r', statusYellow: () => 'y',
    api: async () => data,
    _stopRolesRefresh() {}, _stopScopesRefresh() {}, _stopDistanceRetry() {},
    _analyticsData: {}, _loadError: null, _regionChangeHandler: null, _areaChangeHandler: null,
    _channelData: null, _ngState: null, _themeRefreshHandler: null,
    BULK_TABS: new Set(['overview']), loadErrorHtml: String,
  });
  // The tab content replaced under the map: a tab click, the theme refresh.
  s.rerenders = { 'renderTab (tab change, theme refresh)': 'renderTab("overview")' };
  s.ctx.document.getElementById = (id) => (id === 'subpathDetail' ? panel : { innerHTML: '' });
  vm.runInContext(ANALYTICS, s.ctx);
  s.panel = panel;
  s.show = async (d) => {
    if (d) data = d;
    const before = s.maps.length;
    await vm.runInContext('loadSubpathDetail("aa,bb")', s.ctx);
    assert.ok(!/Error:/.test(panel.innerHTML), 'loadSubpathDetail failed: ' + panel.innerHTML.slice(0, 80));
    return s.maps.length > before ? s.maps[s.maps.length - 1] : null;
  };
  s.destroy = () => vm.runInContext('destroy()', s.ctx);
  return s;
}

const NO_LOCATION = {
  home: { lat: null, lon: null, name: 'n', role: 'repeater' },
  analytics: Object.assign({}, ROUTE, { nodes: [{ name: 'a' }, { name: 'b' }] }),
};

async function suite(label, key, make) {
  console.log('── ' + label + ' ──');

  await test('is built for the current pick, raster, with the provider\'s own maxZoom', async () => {
    const s = make();
    s.pick('opentopomap');
    const map = await s.show();
    assert.ok(/opentopomap/.test(base(map).url), base(map).url);
    assert.strictEqual(base(map).opts.maxZoom, 17, 'OpenTopoMap stops at 17');
    holds(s, [map]);
  });

  await test('a pick in this tab swaps the layer in place', async () => {
    const s = make();
    const map = await s.show();
    assert.ok(/cartocdn/.test(base(map).url), 'starts on the theme map: ' + base(map).url);
    s.pick('osmfr-hot');
    assert.ok(/openstreetmap\.fr\/hot/.test(base(map).url), base(map).url);
    s.pick('usgs-topo');
    assert.ok(/USGSTopo/.test(base(map).url), base(map).url);
    assert.strictEqual(base(map).opts.maxZoom, 16, 'USGS stops at 16');
    s.pick(null);
    assert.ok(/cartocdn/.test(base(map).url), 'Auto returns the theme map: ' + base(map).url);
    assert.strictEqual(s.maps.length, 1, 'the map itself was not rebuilt');
  });

  await test('a pick saved in another tab (storage event) swaps the layer, and its Auto swaps back', async () => {
    const s = make();
    const map = await s.show();
    s.otherTabPick('osmfr-hot');
    assert.ok(/openstreetmap\.fr\/hot/.test(base(map).url), base(map).url);
    s.otherTabPick(null);
    assert.ok(/cartocdn/.test(base(map).url), base(map).url);
  });

  await test('a theme flip swaps the layer and sets the dark filter', async () => {
    const s = make();
    const map = await s.show();
    const light = base(map).url;
    s.ctx.MC_setDarkTileProvider('carto-voyager-dark');
    assert.strictEqual(base(map).url, light, 'a pick for the other theme changes nothing');
    s.setTheme('dark');
    assert.ok(/voyager/.test(base(map).url), base(map).url);
    assert.ok(/invert\(/.test(map.pane.style.filter), 'filter: ' + map.pane.style.filter);
    s.setTheme('light');
    assert.strictEqual(base(map).url, light);
    assert.strictEqual(map.pane.style.filter, '', 'filter cleared in light');
  });

  await test('leaving the page unregisters the map before removing it', async () => {
    const s = make();
    const map = await s.show();
    s.destroy();
    assert.strictEqual(map.removed, 1, 'map removed');
    assert.strictEqual(map.registeredAtRemove, false, 'still registered when Leaflet removed it');
    holds(s, []);
    const n = map.layers.length;
    s.pick('osmfr-hot');
    s.setTheme('dark');
    assert.strictEqual(map.layers.length, n, 'a removed map got a new layer');
  });

  await test('showing another one replaces the map and its registration', async () => {
    const s = make();
    const first = await s.show();
    const second = await s.show();
    assert.notStrictEqual(first, second);
    assert.strictEqual(first.removed, 1, 'first map removed');
    assert.strictEqual(first.registeredAtRemove, false, 'still registered when Leaflet removed it');
    holds(s, [second]);
    s.pick('osmfr-hot');
    assert.ok(/openstreetmap\.fr\/hot/.test(base(second).url));
  });

  await test('showing one without a location removes the map and its registration', async () => {
    const s = make();
    const map = await s.show();
    await s.show(NO_LOCATION[key]);
    assert.strictEqual(map.removed, 1, 'map removed');
    assert.strictEqual(map.registeredAtRemove, false, 'still registered when Leaflet removed it');
    holds(s, []);
  });

  await test('re-rendering the content around it removes the map and its registration', async () => {
    for (const how of Object.keys(make().rerenders)) {
      const s = make();
      const map = await s.show();
      await vm.runInContext(s.rerenders[how], s.ctx);
      assert.strictEqual(map.removed, 1, how + ': map not removed');
      assert.strictEqual(map.registeredAtRemove, false, how + ': still registered when Leaflet removed it');
      holds(s, []);
    }
  });

  await test('without the nodes.js helper the map still gets one plain layer', async () => {
    const s = make({ noHelper: true });
    const map = await s.show();
    assert.ok(/^https:\/\//.test(base(map).url), base(map).url);
    s.destroy();
    assert.strictEqual(map.removed, 1, 'and is still removed on leave');
  });
}

// The health card's own races: the 100 ms invalidateSize timer against a map
// that is gone by then, and a second loadHealth started before the first lands.
async function homeRaces() {
  console.log('── Home mini-map: timers and overlapping loads ──');

  // Each api() call waits here until the test settles it.
  function pendingApi(s) {
    const calls = [];
    const body = { node: { lat: 10, lon: 20, name: 'n', role: 'repeater' }, stats: {}, recentPackets: [], observers: [] };
    s.ctx.api = () => new Promise((resolve, reject) => {
      calls.push({ land: () => resolve(body), fail: () => reject(new Error('down')) });
    });
    return calls;
  }
  const load = (s) => vm.runInContext('loadHealth("abc")', s.ctx);

  await test('the size timer of a shown map resizes it', async () => {
    const s = home();
    const map = await s.show();
    s.flushTimers();
    assert.strictEqual(map.invalidated, 1);
  });

  await test('the size timer of a map removed meanwhile does nothing (no throw)', async () => {
    const s = home();
    const map = await s.show();
    s.destroy();
    s.flushTimers(); // threw "Cannot read properties of null" before the fix
    assert.strictEqual(map.invalidated, 0, 'a removed map was resized');
  });

  await test('the size timer of a map replaced meanwhile leaves both alone; the new one has its own', async () => {
    const s = home();
    const first = await s.show();
    const second = await s.show();
    s.flushTimers();
    assert.strictEqual(first.invalidated, 0, 'a removed map was resized');
    assert.strictEqual(second.invalidated, 1, 'the new map is resized once, by its own timer');
  });

  await test('two loads started before either lands: the first map is removed, only the second stays registered', async () => {
    const s = home();
    const calls = pendingApi(s);
    const p1 = load(s), p2 = load(s);
    assert.strictEqual(calls.length, 2, 'both loads are waiting');
    calls[0].land(); await p1;
    const first = s.maps[s.maps.length - 1];
    holds(s, [first]);
    calls[1].land(); await p2;
    assert.strictEqual(s.maps.length, 2, 'the second load drew its own map');
    const second = s.maps[1];
    assert.strictEqual(first.removed, 1, 'first map removed');
    assert.strictEqual(first.registeredAtRemove, false, 'still registered when Leaflet removed it');
    assert.strictEqual(second.removed, 0);
    holds(s, [second]);
  });

  await test('a load that fails after another one drew a map removes and unregisters that map', async () => {
    const s = home();
    const calls = pendingApi(s);
    const p1 = load(s), p2 = load(s);
    calls[0].land(); await p1;
    const map = s.maps[s.maps.length - 1];
    holds(s, [map]);
    calls[1].fail(); await p2;
    assert.ok(/Failed to load node health/.test(s.card.innerHTML), 'the card shows the error');
    assert.strictEqual(map.removed, 1, 'map removed with the card content');
    assert.strictEqual(map.registeredAtRemove, false, 'still registered when Leaflet removed it');
    holds(s, []);
    s.flushTimers();
    assert.strictEqual(map.invalidated, 0, 'a removed map was resized');
  });
}

(async () => {
  await suite('Home mini-map', 'home', home);
  await homeRaces();
  await suite('Analytics route map', 'analytics', analytics);
  console.log('\n#2444 secondary map tile sync: ' + passed + ' passed, ' + failed + ' failed');
  process.exit(failed === 0 ? 0 : 1);
})();

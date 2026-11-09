/* test-tile-resolver.js — shared tile resolution contract (#1420/#1470 followup,
 * node-map light-tiles refactor).
 *
 * Bug being pinned: roles.js getTileUrl() resolved light mode from the frozen
 * TILE_LIGHT scalar — every consumer that is not map.js/live.js (node detail
 * inset map and its reusers, analytics minimap) shipped the wrong basemap in
 * light mode: the user's Light Mode Provider pick and the operator's
 * map.tiles.lightDefault were ignored, and a map created before
 * /api/config/client resolved kept a keyless Carto URL (watermark) forever,
 * because only map.js and live.js listen for mc-tile-provider-changed.
 *
 * Contract under test (public/map-tile-providers.js):
 *   window.MC_resolveTile(isDark)  → { type, id, url, attribution, maxZoom,
 *                                      refUrl, invertFilter, override }
 *     - url is ALWAYS a string (function-typed providers are invoked, #1614)
 *     - light + dark pick the registry entry for the theme
 *       (localStorage pick > server lightDefault/darkDefault > shipped default)
 *     - explicit operator overrides (MC_LEGACY_TILE_OVERRIDE + TILE_* scalar)
 *       beat the user's provider pick
 *     - provider attribution/maxZoom/refUrl/invertFilter are carried through
 *     - resolution is lazy: re-running MC_initTileRegistry after async config
 *       changes the next resolve's output (Carto key picked up without reload)
 *   window.MC_isDarkTheme()        → data-theme attr, else prefers-color-scheme
 *   nodes.js _applyTilesToNodeMap: light pick honoured; re-sync on
 *     mc-tile-provider-changed swaps layers in place (never stacks); maps gone
 *     from the DOM are pruned; late config is picked up.
 *
 * Pure vm sandbox, no browser or dependencies — mirrors the harness used by
 * test-issue-1420-tile-providers.js / test-issue-1470-node-tile-helper.js.
 * Runs via: node tests/unit/test-tile-resolver.js
 */
'use strict';
const REPO_ROOT = require('path').resolve(__dirname, '..', '..');
const vm   = require('vm');
const fs   = require('fs');
const path = require('path');
const assert = require('assert');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log('  ✅ ' + name); }
  catch (e) { failed++; console.log('  ❌ ' + name + ': ' + e.message); }
}

function makeStorage() {
  const store = {};
  return {
    getItem(k)    { return Object.prototype.hasOwnProperty.call(store, k) ? store[k] : null; },
    setItem(k, v) { store[k] = String(v); },
    removeItem(k) { delete store[k]; },
    clear()       { for (const k of Object.keys(store)) delete store[k]; },
  };
}

/** Sandbox where documentElement's data-theme is MUTABLE (theme flips). */
function makeSandbox(opts) {
  opts = opts || {};
  let theme = opts.theme || 'dark';
  const listeners = {};
  const events = [];
  const ctx = {
    console,
    setTimeout, clearTimeout,
    JSON, Date, Math, Object, Array, String, Number, Boolean, Set, Map,
    fetch: () => Promise.resolve({ ok: false, json: () => Promise.resolve({}) }),
    localStorage: makeStorage(),
    document: {
      documentElement: {
        getAttribute: (k) => k === 'data-theme' ? theme : null,
        setAttribute: (k, v) => { if (k === 'data-theme') theme = v; },
        style: { getPropertyValue: () => '' },
      },
      querySelector: () => null,
      querySelectorAll: () => [],
      getElementById: () => null,
      createElement: () => ({ style: {}, appendChild: () => {}, setAttribute: () => {}, addEventListener: () => {} }),
      addEventListener: () => {},
      body: { appendChild: () => {}, style: {} },
      head: { appendChild: () => {} },
      readyState: 'complete',
    },
    window: {
      addEventListener: (type, fn) => { (listeners[type] = listeners[type] || []).push(fn); },
      removeEventListener: (type, fn) => {
        const l = listeners[type] || [];
        const i = l.indexOf(fn);
        if (i >= 0) l.splice(i, 1);
      },
      dispatchEvent: (ev) => { events.push(ev); return true; },
      matchMedia: () => ({ matches: opts.prefersDark === true, addEventListener: () => {} }),
    },
    CustomEvent: function (type, init) { this.type = type; this.detail = (init && init.detail) || null; },
  };
  ctx.window.localStorage = ctx.localStorage;
  ctx.window.document = ctx.document;
  ctx.globalThis = ctx;
  vm.createContext(ctx);
  ctx.setTheme = (t) => { theme = t; };
  ctx.listeners = listeners;
  ctx.fire = (type, ev) => { (listeners[type] || []).forEach(fn => fn(ev)); };
  return ctx;
}

function loadInto(ctx, relPath) {
  const src = fs.readFileSync(path.join(REPO_ROOT, relPath), 'utf8');
  vm.runInContext(src, ctx, { filename: relPath });
  for (const k of Object.keys(ctx.window)) {
    if (!(k in ctx)) ctx[k] = ctx.window[k];
  }
}

/** Leaflet mock recording tileLayer/addTo/removeLayer with setUrl support. */
function makeL() {
  const layers = [];
  let layerId = 0;
  const L = {
    tileLayer(url, opts) {
      const layer = { id: ++layerId, url, opts, _removed: false,
        addTo(map) { map._layers.push(this); layers.push({ layer: this, map }); return this; } };
      return layer;
    },
    map() { return { _layers: [], getPane: () => null }; },
  };
  return {
    L, layers,
    newMap() {
      const map = { _layers: [] };
      map.getPane = (n) => n === 'tilePane' ? (map._pane = map._pane || { style: { filter: '' } }) : null;
      map.getContainer = () => ({ isConnected: map._connected === false ? false : true });
      map.removeLayer = (layer) => {
        layer._removed = true;
        const i = map._layers.indexOf(layer);
        if (i >= 0) map._layers.splice(i, 1);
      };
      return map;
    },
  };
}

function extractApplyTilesHelper() {
  const nodesSrc = fs.readFileSync(path.join(REPO_ROOT, 'public', 'nodes.js'), 'utf8');
  const startMatch = nodesSrc.match(/function\s+_applyTilesToNodeMap\s*\(map\)\s*\{/);
  if (!startMatch) throw new Error('_applyTilesToNodeMap definition not found in public/nodes.js');
  const start = startMatch.index;
  let depth = 0, i = start;
  for (; i < nodesSrc.length; i++) {
    const ch = nodesSrc[i];
    if (ch === '{') depth++;
    else if (ch === '}') { depth--; if (depth === 0) { i++; break; } }
  }
  return nodesSrc.slice(start, i);
}

console.log('── MC_resolveTile / MC_isDarkTheme / node-map tile sync ──');

function loadResolver(ctx) {
  loadInto(ctx, 'public/map-tile-providers.js');
  loadInto(ctx, 'public/roles.js');
}

// ── Resolver basics ─────────────────────────────────────────────────────────

test('resolve(light, no pick) → Carto light_all, no key when none configured', () => {
  const ctx = makeSandbox({ theme: 'light' });
  loadResolver(ctx);
  const r = ctx.window.MC_resolveTile(false);
  assert.strictEqual(r.type, 'light');
  assert.strictEqual(r.id, 'carto-light');
  assert.ok(/light_all/.test(r.url), 'light_all URL — got ' + r.url);
  assert.ok(!/\?key=/.test(r.url), 'no key when unconfigured — got ' + r.url);
  assert.strictEqual(r.maxZoom, 19);
  assert.strictEqual(r.invertFilter, null);
  assert.strictEqual(r.refUrl, null);
  assert.ok(r.attribution && /Carto/.test(r.attribution), 'Carto attribution — got ' + r.attribution);
});

test('resolve(light) carries the configured Carto key WITHOUT config-patching (lazy #1614)', () => {
  const ctx = makeSandbox({ theme: 'light' });
  ctx.window.MC_MAP_CFG = { tiles: { providers: { carto: { enabled: true, key: 'SECRET-KEY' } } } };
  loadResolver(ctx);
  const r = ctx.window.MC_resolveTile(false);
  assert.ok(/\?key=SECRET-KEY/.test(r.url), 'keyed URL — got ' + r.url);
});

test('resolve(light, pick=carto-voyager) → voyager tiles (the node-map light bug)', () => {
  const ctx = makeSandbox({ theme: 'light' });
  loadResolver(ctx);
  ctx.window.MC_setLightTileProvider('carto-voyager');
  const r = ctx.window.MC_resolveTile(false);
  assert.strictEqual(r.id, 'carto-voyager');
  assert.ok(/voyager/.test(r.url), 'voyager URL — got ' + r.url);
});

test('resolve(light, stored pick disabled by config) → falls back cleanly', () => {
  const ctx = makeSandbox({ theme: 'light' });
  ctx.window.MC_MAP_CFG = { tiles: { providers: { carto: { enabled: false } } } };
  loadResolver(ctx);
  ctx.window.localStorage.setItem('mc-light-tile-provider', 'carto-voyager'); // no longer in registry
  const r = ctx.window.MC_resolveTile(false);
  assert.ok(r.url, 'still resolves a URL — got ' + r.url);
  assert.ok(!/voyager/.test(r.url), 'disabled pick not used — got ' + r.url);
  assert.strictEqual(r.override, false);
});

test('resolve(light, server lightDefault=carto-voyager, no user pick) → voyager', () => {
  const ctx = makeSandbox({ theme: 'light' });
  ctx.window.MC_MAP_CFG = { tiles: { providers: { carto: { enabled: true } }, lightDefault: 'carto-voyager' } };
  loadResolver(ctx);
  const r = ctx.window.MC_resolveTile(false);
  assert.ok(/voyager/.test(r.url), 'operator lightDefault honoured — got ' + r.url);
});

test('resolve(dark, pick=carto-voyager-dark) → invertFilter + attrs carried', () => {
  const ctx = makeSandbox({ theme: 'dark' });
  loadResolver(ctx);
  ctx.window.MC_setDarkTileProvider('carto-voyager-dark');
  const r = ctx.window.MC_resolveTile(true);
  assert.strictEqual(r.id, 'carto-voyager-dark');
  assert.ok(/voyager/.test(r.url), 'voyager URL — got ' + r.url);
  assert.ok(/invert\(/.test(r.invertFilter), 'invertFilter — got ' + JSON.stringify(r.invertFilter));
  assert.strictEqual(r.maxZoom, 19);
});

test('resolve(maxZoom passthrough): usgs-topo pick → maxZoom 16', () => {
  const ctx = makeSandbox({ theme: 'light' });
  ctx.window.MC_MAP_CFG = { tiles: { providers: { usgs: { enabled: true } } } };
  loadResolver(ctx);
  ctx.window.MC_setLightTileProvider('usgs-topo');
  const r = ctx.window.MC_resolveTile(false);
  assert.ok(/USGSTopo|basemap\.nationalmap/.test(r.url), 'usgs URL — got ' + r.url);
  assert.strictEqual(r.maxZoom, 16, 'operator maxZoom carried — got ' + r.maxZoom);
});

test('explicit operator override (MC_LEGACY_TILE_OVERRIDE) beats the user pick', () => {
  const ctx = makeSandbox({ theme: 'light' });
  loadResolver(ctx);
  // TILE_LIGHT is read as a bare global by the resolver (in the browser it IS
  // window.TILE_LIGHT); mirror the window-stub write onto the vm global.
  ctx.window.TILE_LIGHT = 'https://tiles.operator.example/light/{z}/{x}/{y}.png';
  ctx.TILE_LIGHT = ctx.window.TILE_LIGHT;
  ctx.window.MC_LEGACY_TILE_OVERRIDE = { dark: false, light: true };
  ctx.window.MC_setLightTileProvider('carto-voyager'); // user pick loses to the override
  const r = ctx.window.MC_resolveTile(false);
  assert.strictEqual(r.url, 'https://tiles.operator.example/light/{z}/{x}/{y}.png', 'override URL wins');
  assert.strictEqual(r.override, true);
});

test('resolver invokes function-typed urls and never returns a function (#1614)', () => {
  const ctx = makeSandbox({ theme: 'dark' });
  loadResolver(ctx);
  ctx.window.MC_TILE_PROVIDERS = {
    'fn-provider': { provider: 'carto', label: 'Fn',
      url: () => 'https://{s}.tiles.example.com/dark/{z}/{x}/{y}.png',
      attribution: 'x', type: 'dark' },
  };
  ctx.window.MC_getDarkTileProvider = () => 'fn-provider';
  const r = ctx.window.MC_resolveTile(true);
  assert.strictEqual(typeof r.url, 'string');
  assert.ok(/\{z\}/.test(r.url) && /\{x\}/.test(r.url) && /\{y\}/.test(r.url));
});

test('lazy: url built at call time picks up a Carto key arriving after load (config race)', () => {
  const ctx = makeSandbox({ theme: 'light' });
  loadResolver(ctx);
  const before = ctx.window.MC_resolveTile(false).url;
  assert.ok(!/\?key=/.test(before), 'pre-config: no key — got ' + before);
  // Async config arrives: registry re-init (exactly what roles.js does on
  // MeshConfigReady) → next resolve carries the key without any reload.
  ctx.window.MC_MAP_CFG = { tiles: { providers: { carto: { enabled: true, key: 'KEY-LATER' } } } };
  ctx.window.MC_initTileRegistry(true);
  const after = ctx.window.MC_resolveTile(false).url;
  assert.ok(/\?key=KEY-LATER/.test(after), 'post-config: keyed — got ' + after);
});

// ── MC_isDarkTheme + getTileUrl wrapper ─────────────────────────────────────

test('MC_isDarkTheme follows data-theme attr; falls back to OS when unset', () => {
  const ctx = makeSandbox({ theme: 'light' });
  loadResolver(ctx);
  assert.strictEqual(ctx.window.MC_isDarkTheme(), false);
  ctx.setTheme('dark');
  assert.strictEqual(ctx.window.MC_isDarkTheme(), true);
  // attr absent
  ctx.document.documentElement.getAttribute = (k) => k === 'data-theme' ? null : null;
  // prefer-color-scheme dark
  ctx.window.matchMedia = (q) => ({ matches: /dark/.test(q), addEventListener: () => {} });
  assert.strictEqual(ctx.window.MC_isDarkTheme(), true, 'OS preference used when no attr');
  ctx.window.matchMedia = () => ({ matches: false, addEventListener: () => {} });
  assert.strictEqual(ctx.window.MC_isDarkTheme(), false);
});

test('getTileUrl() light returns the stored light pick (wrapper integration)', () => {
  const ctx = makeSandbox({ theme: 'light' });
  loadResolver(ctx);
  ctx.window.MC_setLightTileProvider('carto-voyager');
  const url = ctx.window.getTileUrl();
  assert.ok(/voyager/.test(url), 'getTileUrl honours light pick — got ' + url);
});

test('getTileUrl() dark still returns a string with mocked registry (1614 harness shape)', () => {
  const ctx = makeSandbox({ theme: 'dark' });
  loadResolver(ctx);
  ctx.window.MC_TILE_PROVIDERS = {
    'str-provider': { provider: 'carto', label: 'Str',
      url: 'https://tiles.example.com/dark/{z}/{x}/{y}.png', attribution: 'x', type: 'dark' },
  };
  ctx.window.MC_getDarkTileProvider = () => 'str-provider';
  assert.strictEqual(ctx.window.getTileUrl(), 'https://tiles.example.com/dark/{z}/{x}/{y}.png');
});

// ── nodes.js _applyTilesToNodeMap ───────────────────────────────────────────

function loadNodeHelper(ctx, makeMap, L) {
  ctx.L = L;
  const map = makeMap();
  ctx._map = map;
  vm.runInContext(extractApplyTilesHelper(), ctx, { filename: 'nodes.js#_applyTilesToNodeMap' });
  vm.runInContext('window.__lastNodeTileLayer = _applyTilesToNodeMap(_map);', ctx);
  return map;
}

test('node helper: light + light pick=carto-voyager → voyager base layer (acceptance #1)', () => {
  const ctx = makeSandbox({ theme: 'light' });
  loadResolver(ctx);
  ctx.window.MC_setLightTileProvider('carto-voyager');
  const mock = makeL();
  const map = loadNodeHelper(ctx, mock.newMap, mock.L);
  const base = map._layers.filter(l => /voyager|light_all/.test(l.url));
  assert.ok(base.length === 1 && /voyager/.test(base[0].url),
    'exactly one base layer, voyager — got ' + JSON.stringify(map._layers.map(l => l.url)));
});

test('node helper: maxZoom from the provider, not hardcoded (usgs-topo → 16)', () => {
  const ctx = makeSandbox({ theme: 'light' });
  ctx.window.MC_MAP_CFG = { tiles: { providers: { usgs: { enabled: true } } } };
  loadResolver(ctx);
  ctx.window.MC_setLightTileProvider('usgs-topo');
  const mock = makeL();
  const map = loadNodeHelper(ctx, mock.newMap, mock.L);
  assert.ok(map._layers.length >= 1, 'layer added');
  assert.strictEqual(map._layers[0].opts.maxZoom, 16, 'provider maxZoom — got ' + map._layers[0].opts.maxZoom);
});

test('node helper: re-sync on mc-tile-provider-changed swaps layers, never stacks', () => {
  const ctx = makeSandbox({ theme: 'light' });
  loadResolver(ctx);
  const mock = makeL();
  const map = loadNodeHelper(ctx, mock.newMap, mock.L);
  assert.strictEqual(map._layers.length, 1);
  ctx.window.MC_setLightTileProvider('carto-voyager'); // dispatches the change event
  ctx.fire('mc-tile-provider-changed', { type: 'mc-tile-provider-changed', detail: { id: 'carto-voyager', type: 'light' } });
  const bases = map._layers; // removeLayer splices, so this IS the live set
  assert.strictEqual(bases.length, 1, 'exactly one live base layer after re-sync — got ' + bases.length);
  assert.ok(/voyager/.test(bases[0].url), 'swapped to the new provider — got ' + bases[0].url);
  // Removed layers are gone from the map but still visible in the creation log.
  const removedOld = mock.layers.filter(e => e.map === map && e.layer._removed && /light_all/.test(e.layer.url));
  assert.strictEqual(removedOld.length, 1, 'old positron layer was removed, not stacked');
});

test('node helper: late Carto key (fromConfig re-init) is picked up in place', () => {
  const ctx = makeSandbox({ theme: 'light' });
  loadResolver(ctx);
  const mock = makeL();
  const map = loadNodeHelper(ctx, mock.newMap, mock.L);
  assert.ok(!/\?key=/.test(map._layers[0].url), 'pre-config: keyless — got ' + map._layers[0].url);
  ctx.window.MC_MAP_CFG = { tiles: { providers: { carto: { enabled: true, key: 'KEY-LATER' } } } };
  ctx.window.MC_initTileRegistry(true); // fires fromConfig → maps re-sync
  ctx.fire('mc-tile-provider-changed', { type: 'mc-tile-provider-changed', detail: { fromConfig: true } });
  assert.ok(/\?key=KEY-LATER/.test(map._layers.filter(l => !l._removed)[0].url),
    'post-config: keyed — got ' + map._layers.filter(l => !l._removed)[0].url);
});

test('node helper: pick for the OTHER theme does not re-sync', () => {
  const ctx = makeSandbox({ theme: 'light' });
  loadResolver(ctx);
  const mock = makeL();
  const map = loadNodeHelper(ctx, mock.newMap, mock.L);
  const before = map._layers[0].url;
  ctx.window.MC_setDarkTileProvider('carto-voyager-dark'); // dispatches with detail.type='dark'
  ctx.fire('mc-tile-provider-changed', { type: 'mc-tile-provider-changed', detail: { id: 'carto-voyager-dark', type: 'dark' } });
  assert.strictEqual(map._layers.filter(l => !l._removed)[0].url, before,
    'light tile layer untouched by a dark pick');
});

test('node helper: theme flip re-syncs via the data-theme observer path (onChange(null))', () => {
  const ctx = makeSandbox({ theme: 'light' });
  loadResolver(ctx);
  const mock = makeL();
  const map = loadNodeHelper(ctx, mock.newMap, mock.L);
  ctx.setTheme('dark');
  // Trigger the prune/sync entry point the MutationObserver calls.
  const st = ctx.window.__mcNodeTileSync;
  st.onChange(null);
  const live = map._layers.filter(l => !l._removed);
  assert.strictEqual(live.length, 1, 'one live base layer after theme flip');
  assert.ok(/dark_all|voyager/.test(live[0].url), 'now a dark provider — got ' + live[0].url);
});

test('node helper: map whose container left the DOM is pruned (no leak, no crash)', () => {
  const ctx = makeSandbox({ theme: 'light' });
  loadResolver(ctx);
  const mock = makeL();
  const map = loadNodeHelper(ctx, mock.newMap, mock.L);
  assert.ok(Object.prototype.hasOwnProperty.call(map, 'getContainer'), 'prune test needs a getContainer-aware map');
  const st = ctx.window.__mcNodeTileSync;
  assert.strictEqual(st.maps.length, 1);
  map.getContainer = () => ({ isConnected: false }); // SPA navigated away
  ctx.fire('mc-tile-provider-changed', { type: 'mc-tile-provider-changed', detail: { fromConfig: true } });
  assert.strictEqual(st.maps.length, 0, 'disconnected map dropped from the sync registry');
});

test('node helper: two maps on one page both re-sync with one shared listener', () => {
  const ctx = makeSandbox({ theme: 'light' });
  loadResolver(ctx);
  const mock = makeL();
  const mapA = loadNodeHelper(ctx, mock.newMap, mock.L);
  ctx._map = null;
  const mapB = (() => { const m = mock.newMap(); ctx._map = m; return m; })();
  vm.runInContext('_applyTilesToNodeMap(_map);', ctx);
  ctx.window.MC_setLightTileProvider('carto-voyager');
  ctx.fire('mc-tile-provider-changed', { type: 'mc-tile-provider-changed', detail: { id: 'carto-voyager', type: 'light' } });
  for (const m of [mapA, mapB]) {
    const live = m._layers.filter(l => !l._removed);
    assert.strictEqual(live.length, 1, 'one live base layer per map');
    assert.ok(/voyager/.test(live[0].url), 'both maps swapped');
  }
});

// Reapplying the same helper on one map instance must not stack a second sync.
test('node helper: re-apply on the SAME map replaces the record, one base layer', () => {
  const ctx = makeSandbox({ theme: 'light' });
  loadResolver(ctx);
  const mock = makeL();
  const map = loadNodeHelper(ctx, mock.newMap, mock.L);
  vm.runInContext('_applyTilesToNodeMap(_map);', ctx); // rx-coverage-style re-init
  assert.strictEqual(Object.keys(map._layers).length, map._layers.length, 'sanity');
  const live = map._layers.filter(l => !l._removed);
  assert.strictEqual(live.length, 1, 'no stacking on same-map re-init — got ' + live.length +
    (live.length ? ' url=' + live[0].url : ''));
  assert.strictEqual(ctx.window.__mcNodeTileSync.maps.length, 1, 'no duplicate sync records');
});

console.log(`\ntile resolver: ${passed} passed, ${failed} failed`);
process.exit(failed === 0 ? 0 : 1);

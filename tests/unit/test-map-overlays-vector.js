/* test-map-overlays-vector.js — Sprint 3 of #2428: gray basemaps, overlays,
 * vector basemap registry + dispatch.
 *
 * Covers, against the real public/map-tile-providers.js in a vm sandbox:
 *   - entry `filter`: resolver, MC_applyTileFilter and explicit picks, both themes
 *   - gray built-ins, style:'gray' tags, picker order (plain before gray)
 *   - the 10 vector built-ins; optIn group gating
 *   - MC_buildTileLayer -> MC_buildVectorLayer with a raster fallback factory
 *   - MC_TILE_OVERLAYS (hillshade gated on MC_createHillshadeLayer, EOX labels,
 *     custom overlays), MC_buildOverlayLayer, the overlay pane
 *   - the 'mc-tile-overlays' preference (S7) and MC_attachAutoTiles restoring,
 *     mirroring and destroying overlays
 *
 * Runs via: node tests/unit/test-map-overlays-vector.js
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

const SRC = fs.readFileSync(path.join(REPO_ROOT, 'public', 'map-tile-providers.js'), 'utf8');

const GRAY_LIGHT = 'grayscale(1) contrast(0.92) brightness(1.04)';
const GRAY_DARK = 'grayscale(1) invert(1) contrast(0.9) brightness(0.95)';
const TERRARIUM = 'https://s3.amazonaws.com/elevation-tiles-prod/terrarium/{z}/{x}/{y}.png';
const OK_URL = 'https://tiles.example.org/{z}/{x}/{y}.png';

function makeStorage() {
  const store = {};
  return {
    store,
    getItem(k)    { return Object.prototype.hasOwnProperty.call(store, k) ? store[k] : null; },
    setItem(k, v) { store[k] = String(v); },
    removeItem(k) { delete store[k]; },
  };
}

function makePane() {
  const attrs = {};
  return {
    style: { filter: '' },
    setAttribute(k, v) { attrs[k] = String(v); },
    getAttribute(k) { return Object.prototype.hasOwnProperty.call(attrs, k) ? attrs[k] : null; },
    removeAttribute(k) { delete attrs[k]; },
  };
}

// Minimal DOM element; innerHTML writes are counted so S2 can be checked.
function makeEl(tag, stats) {
  let text = '';
  const el = {
    tagName: tag, children: [], title: '', className: '', parentNode: null, attrs: {},
    appendChild(c) {
      if (c.parentNode) c.parentNode.children = c.parentNode.children.filter(x => x !== c);
      el.children.push(c); c.parentNode = el; return c;
    },
    insertBefore(c, ref) {
      if (c.parentNode) c.parentNode.children = c.parentNode.children.filter(x => x !== c);
      const i = el.children.indexOf(ref);
      el.children.splice(i < 0 ? el.children.length : i, 0, c); c.parentNode = el; return c;
    },
    querySelectorAll(sel) { return sel === 'span' ? el.children.filter(c => c.tagName === 'span') : []; },
    setAttribute(k, v) { el.attrs[k] = String(v); },
    getAttribute(k) { return Object.prototype.hasOwnProperty.call(el.attrs, k) ? el.attrs[k] : null; },
  };
  Object.defineProperty(el, 'textContent', {
    get() { return text + el.children.map(c => c.textContent).join(''); },
    set(v) { text = String(v); el.children = []; },
  });
  Object.defineProperty(el, 'innerHTML', {
    get() { return text; },
    set(v) { stats.innerHTMLWrites++; text = String(v); el.children = []; },
  });
  return el;
}

// Fake Leaflet: tile layers, layer groups and a layers control that renders
// base radios and overlay checkboxes the way Leaflet names them (raw keys).
function makeLeaflet(stats) {
  function evented(o) {
    o._events = {};
    o.on = (e, cb) => { o._events[e] = cb; return o; };
    return o;
  }
  function renderList(names) {
    const list = makeEl('div', stats);
    const labels = names.map((id) => {
      const label = makeEl('label', stats);
      const holder = makeEl('span', stats);
      holder.appendChild(makeEl('input', stats));
      const name = makeEl('span', stats);
      name.textContent = ' ' + id;
      holder.appendChild(name);
      label.appendChild(holder);
      label.querySelectorAll = (sel) => (sel === 'span' ? [holder, name] : []);
      list.appendChild(label);
      return label;
    });
    return { list, labels };
  }
  const L = {
    created: [],
    tileLayer(url, opts) {
      const t = evented({ kind: 'tile', _url: url, options: Object.assign({}, opts), setUrlCalls: [] });
      t._ctorOpts = opts;
      t.setUrl = (u) => { t.setUrlCalls.push(u); t._url = u; return t; };
      L.created.push(t);
      return t;
    },
    layerGroup(layers) {
      const g = evented({ kind: 'group', _layers: (layers || []).slice() });
      g.addLayer = (l) => { if (!g._layers.includes(l)) g._layers.push(l); return g; };
      g.removeLayer = (l) => { g._layers = g._layers.filter(x => x !== l); return g; };
      g.hasLayer = (l) => g._layers.includes(l);
      g.getLayers = () => g._layers.slice();
      g.addTo = (m) => { m.addLayer(g); return g; };
      return g;
    },
    control: {
      layers(baseMaps, overlays, opts) {
        const base = renderList(Object.keys(baseMaps));
        const over = renderList(Object.keys(overlays || {}));
        const c = {
          baseMaps, overlays, opts, base, over, removed: false,
          addTo(m) { c.map = m; return c; },
          remove() { c.removed = true; return c; },
          getContainer() {
            return { querySelectorAll: (sel) => (sel.indexOf('overlays') >= 0 ? over.labels : base.labels) };
          },
        };
        stats.controls.push(c);
        return c;
      },
    },
  };
  return L;
}

function makeMap(opts) {
  opts = opts || {};
  const tilePane = makePane();
  const panes = { tilePane };
  const m = {
    _layers: [], _handlers: {}, panes, createPaneCalls: 0,
    tilePane,
    getPane: (n) => panes[n] || null,
    addLayer(l) { if (!m._layers.includes(l)) m._layers.push(l); return m; },
    removeLayer(l) { m._layers = m._layers.filter(x => x !== l); return m; },
    hasLayer(l) { return m._layers.includes(l); },
    on(e, fn) { m._handlers[e] = fn; },
    off(e, fn) { if (m._handlers[e] === fn) delete m._handlers[e]; },
    attributionControl: { addAttribution() {}, removeAttribution() {}, _update() {} },
  };
  if (!opts.noPanes) {
    m.createPane = (n) => { m.createPaneCalls++; panes[n] = { style: {} }; return panes[n]; };
  }
  return m;
}

// The #2428 groups are opt-in (off unless enabled:true); the suites that
// exercise them start from a config that turns them all on.
const ALL_ON = () => ({ eox: { enabled: true }, osmfr: { enabled: true }, openfreemap: { enabled: true },
  versatiles: { enabled: true }, hillshade: { enabled: true } });

function makeSandbox(opts) {
  opts = opts || {};
  const listeners = {};
  const warns = [];
  const stats = { innerHTMLWrites: 0, controls: [] };
  const docPane = makePane();
  const state = { theme: opts.theme || 'dark' };
  const observers = [];
  function MutationObserver(cb) { this.cb = cb; observers.push(this); }
  MutationObserver.prototype.observe = function () { this.observing = true; };
  MutationObserver.prototype.disconnect = function () { this.observing = false; };

  const ctx = {
    console: { log: console.log, error: console.error, warn: (...a) => warns.push(a.join(' ')) },
    JSON, Date, Math, Object, Array, String, Number, Boolean, isFinite,
    MutationObserver,
    document: {
      documentElement: { getAttribute: (k) => (k === 'data-theme' ? state.theme : null) },
      querySelector: (sel) => (sel === '.leaflet-tile-pane' ? docPane : null),
      createElement: (tag) => makeEl(tag, stats),
    },
    window: {
      addEventListener(type, fn) { (listeners[type] = listeners[type] || []).push(fn); },
      removeEventListener(type, fn) { listeners[type] = (listeners[type] || []).filter(f => f !== fn); },
      dispatchEvent(ev) { (listeners[ev.type] || []).slice().forEach(fn => fn(ev)); return true; },
      matchMedia: () => ({ matches: false }),
      localStorage: opts.storage || makeStorage(),
    },
    CustomEvent: function (type, init) { this.type = type; this.detail = (init && init.detail) || null; },
  };
  ctx.localStorage = ctx.window.localStorage;
  ctx.L = ctx.window.L = makeLeaflet(stats);
  vm.createContext(ctx);
  ctx.window.document = ctx.document;
  ctx.window.MC_MAP_CFG = opts.mapCfg !== undefined ? opts.mapCfg : { tiles: { providers: ALL_ON() } };
  if (opts.before) opts.before(ctx.window);
  vm.runInContext(SRC, ctx);
  for (const k of Object.keys(ctx.window)) if (!(k in ctx)) ctx[k] = ctx.window[k];
  return {
    ctx, w: ctx.window, listeners, warns, stats, docPane, state, L: ctx.L,
    setTheme(t) { state.theme = t; observers.filter(o => o.observing).forEach(o => o.cb([])); },
    count(type) { return (listeners[type] || []).length; },
    load(cfg) { ctx.window.MC_MAP_CFG = cfg; ctx.window.MC_initTileRegistry(); return ctx.window.MC_TILE_PROVIDERS; },
  };
}

// A stand-in for map-hillshade.js: records its calls, returns a layer object.
function hillshadeStub(w, calls) {
  w.MC_createHillshadeLayer = function (o) {
    calls.push(o);
    return { kind: 'hillshade', options: { url: o.url, opacity: o.opacity }, on() { return this; } };
  };
}

// A stand-in for map-vector.js.
function vectorStub(w, calls) {
  w.MC_buildVectorLayer = function (desc, o) {
    const g = w.L.layerGroup();
    g.vector = true; g.desc = desc; g.fallback = o.fallback;
    g.setUrl = () => { throw new Error('setUrl called on a vector layer'); };
    calls.push(g);
    return g;
  };
}

const VECTOR = {
  'openfreemap-liberty': ['openfreemap', 'OpenFreeMap Liberty', 'light', undefined, 'https://tiles.openfreemap.org/styles/liberty'],
  'openfreemap-bright': ['openfreemap', 'OpenFreeMap Bright', 'light', undefined, 'https://tiles.openfreemap.org/styles/bright'],
  'openfreemap-positron': ['openfreemap', 'OpenFreeMap Positron', 'light', 'gray', 'https://tiles.openfreemap.org/styles/positron'],
  'openfreemap-dark': ['openfreemap', 'OpenFreeMap Dark', 'dark', 'gray', 'https://tiles.openfreemap.org/styles/dark'],
  'openfreemap-fiord': ['openfreemap', 'OpenFreeMap Fiord', 'dark', undefined, 'https://tiles.openfreemap.org/styles/fiord'],
  'versatiles-colorful': ['versatiles', 'VersaTiles Colorful', 'light', undefined, 'https://tiles.versatiles.org/assets/styles/colorful/style.json'],
  'versatiles-muted': ['versatiles', 'VersaTiles Muted', 'light', undefined, 'https://tiles.versatiles.org/assets/styles/muted/style.json'],
  'versatiles-gray': ['versatiles', 'VersaTiles Gray', 'light', 'gray', 'https://tiles.versatiles.org/assets/styles/gray/style.json'],
  'versatiles-colorful-dark': ['versatiles', 'VersaTiles Colorful Dark', 'dark', undefined, 'https://tiles.versatiles.org/assets/styles/colorful-dark/style.json'],
  'versatiles-gray-dark': ['versatiles', 'VersaTiles Gray Dark', 'dark', 'gray', 'https://tiles.versatiles.org/assets/styles/gray-dark/style.json'],
};
const VECTOR_ATTR = { openfreemap: 'OpenFreeMap © OpenMapTiles Data from OpenStreetMap', versatiles: '© OpenStreetMap contributors, VersaTiles' };

function custom(entries, extra) {
  return { tiles: Object.assign({ providers: ALL_ON(), custom: entries }, extra || {}) };
}

console.log('── #2428 sprint 3: gray, overlays, vector ──');

// ─── filter ─────────────────────────────────────────────────────────────────

test('MC_resolveTile returns an entry filter as invertFilter in both themes (also for any)', () => {
  const s = makeSandbox();
  s.w.MC_setLightTileProvider('osmfr-gray');
  s.w.MC_setDarkTileProvider('osmfr-gray-dark');
  assert.strictEqual(s.w.MC_resolveTile(false).invertFilter, GRAY_LIGHT);
  assert.strictEqual(s.w.MC_resolveTile(true).invertFilter, GRAY_DARK);
  s.w.MC_TILE_PROVIDERS.anyg = { provider: 't', type: 'any', label: 'A', url: OK_URL, filter: 'grayscale(1)', invertFilter: 'invert(1)' };
  s.w.MC_setLightTileProvider('anyg');
  s.w.MC_setDarkTileProvider('anyg');
  assert.strictEqual(s.w.MC_resolveTile(false).invertFilter, 'grayscale(1)');
  assert.strictEqual(s.w.MC_resolveTile(true).invertFilter, 'grayscale(1)');
  // No filter: unchanged behaviour.
  s.w.MC_setDarkTileProvider('positron-dark');
  assert.ok(s.w.MC_resolveTile(true).invertFilter.indexOf('invert(') >= 0);
  s.w.MC_setLightTileProvider('carto-light');
  assert.strictEqual(s.w.MC_resolveTile(false).invertFilter, null);
});

test('MC_applyTileFilter applies a filter entry in both themes; others unchanged', () => {
  const s = makeSandbox({ theme: 'light' });
  s.w.MC_setLightTileProvider('osmfr-gray');
  assert.strictEqual(s.docPane.style.filter, GRAY_LIGHT, 'light theme, light gray entry');
  s.w.MC_setLightTileProvider('carto-light');
  assert.strictEqual(s.docPane.style.filter, '', 'no filter for a plain light entry');
  s.state.theme = 'dark';
  s.w.MC_setDarkTileProvider('osmfr-gray-dark');
  assert.strictEqual(s.docPane.style.filter, GRAY_DARK, 'dark theme, dark gray entry');
  s.w.MC_setDarkTileProvider('positron-dark');
  assert.ok(s.docPane.style.filter.indexOf('invert(') >= 0, 'invertFilter still applies in dark');
  s.w.MC_setDarkTileProvider('carto-dark');
  assert.strictEqual(s.docPane.style.filter, '');
});

test('an explicit pick applies its filter whatever the theme', () => {
  for (const theme of ['light', 'dark']) {
    const s = makeSandbox({ theme });
    const map = makeMap();
    s.w.MC_createLayerControl(map, s.L.layerGroup(), 'topleft');
    const c = s.stats.controls[0];
    c.baseMaps['osmfr-gray']._events.add();
    assert.strictEqual(map.tilePane.style.filter, GRAY_LIGHT, theme + ': osmfr-gray');
    c.baseMaps['osmfr-gray-dark']._events.add();
    assert.strictEqual(map.tilePane.style.filter, GRAY_DARK, theme + ': osmfr-gray-dark');
    c.baseMaps['carto-light']._events.add();
    assert.strictEqual(map.tilePane.style.filter, '', theme + ': plain light entry clears it');
    c.baseMaps['positron-dark']._events.add();
    assert.ok(map.tilePane.style.filter.indexOf('invert(') >= 0, theme + ': dark-inverted entry unchanged');
  }
});

test('attach: a gray pick filters its own pane in light mode too', () => {
  const s = makeSandbox({ theme: 'light' });
  s.w.localStorage.setItem('mc-light-tile-provider', 'osmfr-gray');
  const map = makeMap();
  const h = s.w.MC_attachAutoTiles(map);
  assert.ok(h.group._layers[0]._url.indexOf('/osmfr/') >= 0);
  assert.strictEqual(map.tilePane.style.filter, GRAY_LIGHT);
});

// ─── gray entries ───────────────────────────────────────────────────────────

test('gray raster built-ins have the exact fields', () => {
  const reg = makeSandbox().w.MC_TILE_PROVIDERS;
  const common = { provider: 'osmfr', category: 'street', style: 'gray', subdomains: 'abc', maxZoom: 19, optIn: true,
    url: 'https://{s}.tile.openstreetmap.fr/osmfr/{z}/{x}/{y}.png',
    attribution: '© OpenStreetMap contributors, tiles by OpenStreetMap France' };
  const want = {
    'osmfr-gray': Object.assign({ label: 'OSM Gray', type: 'light', filter: GRAY_LIGHT }, common),
    'osmfr-gray-dark': Object.assign({ label: 'OSM Gray Dark', type: 'dark', filter: GRAY_DARK }, common),
  };
  for (const id of Object.keys(want)) {
    assert.ok(reg[id], id);
    for (const k of Object.keys(want[id])) assert.strictEqual(reg[id][k], want[id][k], id + '.' + k);
    assert.ok(!reg[id].invertFilter, id + ' has no invertFilter');
    assert.strictEqual(reg[id].format || 'raster', 'raster');
  }
});

test("existing gray entries are tagged style:'gray' and nothing else about them changed", () => {
  const reg = makeSandbox().w.MC_TILE_PROVIDERS;
  for (const id of ['carto-light', 'carto-dark', 'positron-dark', 'esri-darkgray-labels']) {
    assert.strictEqual(reg[id].style, 'gray', id);
    assert.ok(!reg[id].filter, id + ' has no filter');
  }
  assert.ok(reg['positron-dark'].invertFilter.indexOf('invert(') >= 0, 'positron-dark still inverted');
  for (const id of ['carto-voyager', 'carto-voyager-dark', 'osmfr-hot', 'osmfr-cyclosm', 'eox-s2-2025']) {
    assert.ok(!reg[id].style, id + ' untagged');
  }
});

test('picker: inside each category and type, plain entries before gray ones', () => {
  const s = makeSandbox();
  s.w.MC_createLayerControl(makeMap(), s.L.layerGroup(), 'topleft');
  const ids = Object.keys(s.stats.controls[0].baseMaps).slice(1);
  const reg = s.w.MC_TILE_PROVIDERS;
  const CAT = ['street', 'topo', 'satellite'], TYPE = ['light', 'dark', 'any'];
  const rank = ids.map(id => (CAT.indexOf(reg[id].category || 'street') * 3 + TYPE.indexOf(reg[id].type)) * 2 + (reg[id].style === 'gray' ? 1 : 0));
  assert.deepStrictEqual(rank, rank.slice().sort((a, b) => a - b));
  const streetLight = ids.filter(id => (reg[id].category || 'street') === 'street' && reg[id].type === 'light');
  assert.deepStrictEqual(streetLight, ['carto-voyager', 'osmfr-hot', 'openfreemap-liberty', 'openfreemap-bright',
    'versatiles-colorful', 'versatiles-muted', 'carto-light', 'osmfr-gray', 'openfreemap-positron', 'versatiles-gray']);
});

// ─── vector entries ─────────────────────────────────────────────────────────

test('all 10 vector built-ins have the exact fields', () => {
  const reg = makeSandbox().w.MC_TILE_PROVIDERS;
  for (const id of Object.keys(VECTOR)) {
    const [group, label, type, style, url] = VECTOR[id];
    const p = reg[id];
    assert.ok(p, id + ' registered');
    assert.strictEqual(p.provider, group, id);
    assert.strictEqual(p.label, label, id);
    assert.strictEqual(p.type, type, id);
    assert.strictEqual(p.style, style, id + '.style');
    assert.strictEqual(p.url, url, id);
    assert.strictEqual(p.format, 'vector', id);
    assert.strictEqual(p.category, 'street', id);
    assert.strictEqual(p.maxZoom, 19, id);
    assert.strictEqual(p.optIn, true, id);
    assert.strictEqual(p.attribution, VECTOR_ATTR[group], id);
    assert.ok(!p.invertFilter && !p.filter, id + ' unfiltered');
  }
});

test('the optIn groups are off by default; each turns on only with enabled:true (openfreemap, versatiles, osmfr gray, eox labels, hillshade)', () => {
  const calls = [];
  const s = makeSandbox({ mapCfg: null, before: (w) => hillshadeStub(w, calls) });
  const ovl = () => Object.keys(s.w.MC_TILE_OVERLAYS || {});
  const vec = (reg, g) => Object.keys(VECTOR).every(id => (id.indexOf(g) === 0) === !!reg[id]);
  assert.deepStrictEqual(ovl(), [], 'no overlays by default');
  let reg = s.w.MC_TILE_PROVIDERS;
  assert.ok(Object.keys(VECTOR).every(id => !reg[id]) && !reg['osmfr-gray'] && !reg['osmfr-gray-dark'], 'no new base by default');
  for (const off of [{}, { enabled: false }, { enabled: 'true' }, { enabled: 1 }]) {
    reg = s.load({ tiles: { providers: { openfreemap: off, versatiles: off, osmfr: off, eox: off, hillshade: off } } });
    assert.ok(Object.keys(VECTOR).every(id => !reg[id]) && !reg['osmfr-gray'], 'still off for ' + JSON.stringify(off));
    assert.deepStrictEqual(ovl(), [], 'no overlays for ' + JSON.stringify(off));
  }
  reg = s.load({ tiles: { providers: { openfreemap: { enabled: true } } } });
  assert.ok(vec(reg, 'openfreemap'), 'only openfreemap on');
  reg = s.load({ tiles: { providers: { versatiles: { enabled: true } } } });
  assert.ok(vec(reg, 'versatiles'), 'only versatiles on');
  reg = s.load({ tiles: { providers: { osmfr: { enabled: true } } } });
  assert.ok(reg['osmfr-gray'] && reg['osmfr-gray-dark']);
  assert.deepStrictEqual(ovl(), []);
  s.load({ tiles: { providers: { eox: { enabled: true } } } });
  assert.deepStrictEqual(ovl(), ['eox-labels', 'eox-labels-dark']);
  s.load({ tiles: { providers: { hillshade: { enabled: true } } } });
  assert.deepStrictEqual(ovl(), ['hillshade']);
  s.load({ tiles: { providers: ALL_ON() } });
  assert.deepStrictEqual(ovl(), ['hillshade', 'eox-labels', 'eox-labels-dark']);
});

// ─── vector dispatch ────────────────────────────────────────────────────────

test('without MC_buildVectorLayer: one warn, raster fallback for the right theme', () => {
  const s = makeSandbox();
  const reg = s.w.MC_TILE_PROVIDERS;
  const a = s.w.MC_buildTileLayer(reg['openfreemap-dark']);
  assert.strictEqual(a.kind, 'tile');
  assert.ok(a._url.indexOf('dark_all') >= 0);
  const b = s.w.MC_buildTileLayer(reg['versatiles-colorful']);
  assert.ok(b._url.indexOf('light_all') >= 0);
  assert.strictEqual(s.warns.length, 1);
  assert.ok(/vector basemap support is not loaded/.test(s.warns[0]), s.warns[0]);
});

test('with MC_buildVectorLayer: called with the desc; fallback() builds the theme raster', () => {
  const calls = [];
  const s = makeSandbox({ before: (w) => vectorStub(w, calls) });
  const reg = s.w.MC_TILE_PROVIDERS;
  const l = s.w.MC_buildTileLayer(reg['openfreemap-fiord']);
  assert.ok(l.vector, 'the vector layer is returned');
  assert.strictEqual(l.desc, reg['openfreemap-fiord']);
  const fb = l.fallback();
  assert.strictEqual(fb.kind, 'tile');
  assert.ok(fb._url.indexOf('dark_all') >= 0, 'dark entry -> carto-dark');
  // A resolved light descriptor falls back to carto-light.
  s.w.MC_setLightTileProvider('versatiles-muted');
  const r = s.w.MC_resolveTile(false);
  assert.strictEqual(r.format, 'vector');
  assert.ok(s.w.MC_buildTileLayer(r).fallback()._url.indexOf('light_all') >= 0);
  // opts.fallbackId is honoured; a vector fallbackId is refused.
  assert.strictEqual(s.w.MC_buildTileLayer(r, { fallbackId: 'esri-darkgray-labels' }).fallback().kind, 'group');
  assert.ok(s.w.MC_buildTileLayer(r, { fallbackId: 'openfreemap-dark' }).fallback()._url.indexOf('light_all') >= 0);
  assert.strictEqual(s.warns.length, 0, 'no warning when vector support is loaded');
});

test('a throwing MC_buildVectorLayer still yields the raster fallback', () => {
  const s = makeSandbox({ before: (w) => { w.MC_buildVectorLayer = () => { throw new Error('boom'); }; } });
  const l = s.w.MC_buildTileLayer(s.w.MC_TILE_PROVIDERS['openfreemap-dark']);
  assert.strictEqual(l.kind, 'tile');
  assert.ok(l._url.indexOf('dark_all') >= 0);
});

test('attach: a vector url change rebuilds the layer; setUrl is never called on a vector layer', () => {
  const calls = [];
  const s = makeSandbox({ before: (w) => vectorStub(w, calls) });
  const map = makeMap();
  s.w.MC_setDarkTileProvider('openfreemap-dark');
  const h = s.w.MC_attachAutoTiles(map);
  const first = h.group._layers[0];
  assert.ok(first.vector);
  s.w.MC_setDarkTileProvider('versatiles-gray-dark');
  const second = h.group._layers[0];
  assert.notStrictEqual(second, first, 'rebuilt');
  assert.strictEqual(second.desc.url, VECTOR['versatiles-gray-dark'][4]);
  assert.strictEqual(h.group._layers.length, 1);
  h.sync(); // unchanged: no rebuild
  assert.strictEqual(h.group._layers[0], second);
  s.w.MC_setDarkTileProvider('carto-dark');
  assert.strictEqual(h.group._layers[0].kind, 'tile', 'vector -> raster rebuilds');
  s.w.MC_setDarkTileProvider('openfreemap-fiord');
  assert.ok(h.group._layers[0].vector, 'raster -> vector rebuilds');
});

test('picker: vector entries build (and load MapLibre) only when first picked', () => {
  const calls = [];
  const s = makeSandbox({ before: (w) => vectorStub(w, calls) });
  s.w.MC_attachAutoTiles(makeMap(), { controlPosition: 'topleft' });
  assert.strictEqual(calls.length, 0, 'building the picker built no vector layer');
  const c = s.stats.controls[0];
  const holder = c.baseMaps['versatiles-gray'];
  holder._events.add();
  assert.strictEqual(calls.length, 1);
  assert.ok(holder._layers[0].vector, 'the vector layer sits in the picker entry');
  assert.strictEqual(holder._layers[0].desc, s.w.MC_TILE_PROVIDERS['versatiles-gray']);
  holder._events.add();
  assert.strictEqual(calls.length, 1, 'built once');
});

// A transient fallback (MapLibre timed out, then arrived): map-vector.js
// marks the layer stale; the picker and the Auto group rebuild it once.
function staleStub(w, calls, staleSet) {
  vectorStub(w, calls);
  w.MC_vectorLayerStale = (l) => staleSet.has(l);
}

test('picker: re-picking a vector entry rebuilds it only when its fallback is stale', () => {
  const calls = [], staleSet = new Set();
  const s = makeSandbox({ before: (w) => staleStub(w, calls, staleSet) });
  s.w.MC_attachAutoTiles(makeMap(), { controlPosition: 'topleft' });
  const holder = s.stats.controls[0].baseMaps['openfreemap-dark'];
  holder._events.add();
  const first = holder._layers[0];
  holder._events.add();
  assert.strictEqual(calls.length, 1, 'not stale: kept');
  staleSet.add(first);
  holder._events.add();
  assert.strictEqual(calls.length, 2, 'stale: rebuilt on re-pick');
  assert.deepStrictEqual(holder._layers, [calls[1]], 'old layer swapped out');
  assert.strictEqual(calls[1].desc, s.w.MC_TILE_PROVIDERS['openfreemap-dark']);
  holder._events.add();
  assert.strictEqual(calls.length, 2, 'the new layer is not stale: kept');
});

test('attach: the next sync rebuilds a stale vector layer, once; a fresh one is kept', () => {
  const calls = [], staleSet = new Set();
  const s = makeSandbox({ before: (w) => staleStub(w, calls, staleSet) });
  s.w.MC_setDarkTileProvider('openfreemap-dark');
  const h = s.w.MC_attachAutoTiles(makeMap());
  const first = h.group._layers[0];
  h.sync();
  assert.strictEqual(h.group._layers[0], first, 'not stale: kept');
  staleSet.add(first);
  h.sync();
  const second = h.group._layers[0];
  assert.notStrictEqual(second, first, 'stale: rebuilt');
  assert.deepStrictEqual(h.group._layers, [second]);
  assert.strictEqual(second.desc.url, VECTOR['openfreemap-dark'][4]);
  h.sync();
  assert.strictEqual(h.group._layers[0], second, 'no further rebuild');
  assert.strictEqual(calls.length, 2);
});

test("a vector entry's raster fallback carries the entry's maxZoom (upsampling past the raster's own)", () => {
  const calls = [];
  const s = makeSandbox({ before: (w) => vectorStub(w, calls), mapCfg: custom([
    { id: 'lo', format: 'vector', url: 'https://v.example.org/style.json', maxZoom: 14 },
    { id: 'hi', format: 'vector', url: 'https://v.example.org/style.json', maxZoom: 22 },
  ]) });
  const reg = s.w.MC_TILE_PROVIDERS;
  const lo = s.w.MC_buildTileLayer(reg['custom-lo']).fallback();
  assert.strictEqual(lo.options.maxZoom, 14);
  assert.ok(!(lo.options.maxNativeZoom > 14), 'never asks for tiles past the map limit');
  const hi = s.w.MC_buildTileLayer(reg['custom-hi']).fallback();
  assert.strictEqual(hi.options.maxZoom, 22);
  assert.strictEqual(hi.options.maxNativeZoom, 19, 'Carto tiles stop at 19: upsampled above');
  const st = s.w.MC_buildTileLayer(reg['custom-hi'], { raster: true });
  assert.strictEqual(st.options.maxZoom, 22, 'static maps get the same raster');
  const builtin = s.w.MC_buildTileLayer(reg['openfreemap-dark']).fallback();
  assert.strictEqual(builtin.options.maxZoom, 19);
});

// ─── overlay registry ───────────────────────────────────────────────────────

test('no hillshade overlay while MC_createHillshadeLayer is absent; EOX labels fields', () => {
  const s = makeSandbox();
  const o = s.w.MC_TILE_OVERLAYS;
  assert.deepStrictEqual(Object.keys(o), ['eox-labels', 'eox-labels-dark']);
  const eox = (layer) => 'https://tiles.maps.eox.at/wmts/1.0.0/' + layer + '/default/g/{z}/{y}/{x}.png';
  const want = {
    'eox-labels': { label: 'Place labels (EOX)', url: eox('overlay_bright_3857') },
    'eox-labels-dark': { label: 'Place labels, dark text (EOX)', url: eox('overlay_3857') },
  };
  for (const id of Object.keys(want)) {
    assert.strictEqual(o[id].label, want[id].label);
    assert.strictEqual(o[id].url, want[id].url);
    assert.strictEqual(o[id].kind, 'raster');
    assert.strictEqual(o[id].provider, 'eox');
    assert.strictEqual(o[id].maxNativeZoom, 14);
    assert.strictEqual(o[id].attribution, 'Overlay: Data © OpenStreetMap contributors, Rendering © EOX');
  }
  assert.strictEqual(s.w.MC_buildOverlayLayer({ kind: 'hillshade', url: TERRARIUM }), null, 'null without the factory');
  assert.strictEqual(s.w.MC_setTileOverlay('hillshade', true), false);
});

test('hillshade is registered when the factory exists, even if it loads after this file', () => {
  const calls = [];
  const s = makeSandbox({ before: (w) => hillshadeStub(w, calls) });
  const h = s.w.MC_TILE_OVERLAYS.hillshade;
  assert.ok(h);
  assert.strictEqual(h.kind, 'hillshade');
  assert.strictEqual(h.label, 'Hillshade (terrain)');
  assert.strictEqual(h.url, TERRARIUM);
  assert.strictEqual(h.opacity, 0.35);
  assert.strictEqual(h.maxNativeZoom, 15);
  assert.strictEqual(h.attribution, 'Terrain: Mapzen/AWS Open Data (SRTM, USGS and others)');
  // Late load: the factory appears after the registry was built.
  const t = makeSandbox();
  assert.ok(!t.w.MC_TILE_OVERLAYS.hillshade);
  hillshadeStub(t.w, []);
  assert.strictEqual(t.w.MC_setTileOverlay('hillshade', true), true, 're-checked on use');
  assert.ok(t.w.MC_TILE_OVERLAYS.hillshade);
  assert.deepStrictEqual(Array.from(t.w.MC_getTileOverlays()), ['hillshade']);
  // ...and on a picker build.
  const u = makeSandbox();
  hillshadeStub(u.w, []);
  u.w.MC_attachAutoTiles(makeMap(), { controlPosition: 'topleft' });
  assert.ok(Object.keys(u.stats.controls[0].overlays).indexOf('hillshade') === 0, 'listed first');
});

test('providers.hillshade.url is used when valid; a hostile one falls back to the default', () => {
  const calls = [];
  const s = makeSandbox({ before: (w) => hillshadeStub(w, calls) });
  s.load({ tiles: { providers: { hillshade: { enabled: true, url: 'https://dem.example.org/{z}/{x}/{y}.png' } } } });
  assert.strictEqual(s.w.MC_TILE_OVERLAYS.hillshade.url, 'https://dem.example.org/{z}/{x}/{y}.png');
  s.load({ tiles: { providers: { hillshade: { enabled: true, url: '/dem/{z}/{x}/{y}.png' } } } });
  assert.strictEqual(s.w.MC_TILE_OVERLAYS.hillshade.url, '/dem/{z}/{x}/{y}.png');
  const same = s.w.MC_TILE_OVERLAYS.hillshade;
  s.w.MC_initTileRegistry(true);
  assert.strictEqual(s.w.MC_TILE_OVERLAYS.hillshade, same, 'same url -> same entry (layer kept)');
  for (const bad of ['javascript:alert(1)//{z}/{x}/{y}', '//evil.example/{z}/{x}/{y}.png', 'https://u:SECRET@h/{z}/{x}/{y}.png',
    'https://h/{z}/{x}/{y}.png?{SECRET}', 'https://h/static.png', 42]) {
    s.load({ tiles: { providers: { hillshade: { enabled: true, url: bad } } } });
    assert.strictEqual(s.w.MC_TILE_OVERLAYS.hillshade.url, TERRARIUM, String(bad));
  }
  assert.ok(s.warns.length >= 1);
  s.warns.forEach(w => assert.ok(w.indexOf('SECRET') < 0 && w.indexOf('evil') < 0, w));
});

// map-hillshade.js substitutes only {z} {x} {y}: any other placeholder (or a
// repeated one) would request a literal '{s}' and draw a silently blank layer.
test('providers.hillshade.url must use exactly {z} {x} {y} once each; else the default', () => {
  const s = makeSandbox({ before: (w) => hillshadeStub(w, []) });
  s.load({ tiles: { providers: { hillshade: { enabled: true, url: 'https://dem.example.org/{y}/{x}/{z}.png?k=SECRET' } } } });
  assert.strictEqual(s.w.MC_TILE_OVERLAYS.hillshade.url, 'https://dem.example.org/{y}/{x}/{z}.png?k=SECRET', 'any order is fine');
  for (const bad of ['https://{s}.dem.example.org/{z}/{x}/{y}.png?k=SECRET', 'https://dem.example.org/{z}/{x}/{y}{r}.png?k=SECRET',
    'https://dem.example.org/{z}/{x}/{-y}.png?k=SECRET', 'https://dem.example.org/{z}/{x}/{y}/{z}.png?k=SECRET',
    'https://dem.example.org/{z}/{x}.png?k=SECRET']) {
    s.load({ tiles: { providers: { hillshade: { enabled: true, url: bad } } } });
    assert.strictEqual(s.w.MC_TILE_OVERLAYS.hillshade.url, TERRARIUM, bad);
  }
  assert.ok(s.warns.some(w => /hillshade\.url rejected/.test(w)), 'warned: ' + JSON.stringify(s.warns));
  s.warns.forEach(w => assert.ok(w.indexOf('SECRET') < 0 && w.indexOf('dem.example') < 0, 'no url in the warning: ' + w));
});

test('custom overlay:true entries register as raster overlays (validated, attribution escaped)', () => {
  const s = makeSandbox({ mapCfg: custom([
    { id: 'rain', url: 'https://{s}.rain.example/{z}/{x}/{y}.png', overlay: true, label: 'Rain radar',
      attribution: '<b>Rain</b> & co', subdomains: 'ab', maxNativeZoom: 9.7, maxZoom: 30 },
    { id: 'base', url: OK_URL },
  ]) });
  const o = s.w.MC_TILE_OVERLAYS['custom-rain'];
  assert.ok(o, 'registered');
  assert.ok(!s.w.MC_TILE_PROVIDERS['custom-rain'], 'not a base provider');
  assert.ok(s.w.MC_TILE_PROVIDERS['custom-base'], 'base entries unaffected');
  assert.strictEqual(o.kind, 'raster');
  assert.strictEqual(o.label, 'Rain radar');
  assert.strictEqual(o.attribution, '&lt;b&gt;Rain&lt;/b&gt; &amp; co');
  assert.strictEqual(o.subdomains, 'ab');
  assert.strictEqual(o.maxNativeZoom, 9);
  assert.strictEqual(o.maxZoom, 24);
  assert.ok(Object.isFrozen(o));
  assert.deepStrictEqual(Object.keys(s.w.MC_TILE_OVERLAYS), ['eox-labels', 'eox-labels-dark', 'custom-rain']);
});

test('hostile custom overlays are rejected with a warn naming only the id', () => {
  const s = makeSandbox({ mapCfg: custom([
    { id: 'js', url: 'javascript:alert(1)', overlay: true },
    { id: 'creds', url: 'https://u:SECRET@h.example/{z}/{x}/{y}.png', overlay: true },
    { id: 'vec', url: 'https://h.example/style.json', format: 'vector', overlay: true },
    { id: 'nozxy', url: 'https://h.example/a.png', overlay: true },
    { id: 'sub', url: OK_URL, overlay: true, subdomains: '"><img>' },
    { id: 'dup', url: OK_URL }, { id: 'dup', url: OK_URL, overlay: true },
    { id: 'BAD ID', url: OK_URL, overlay: true },
  ]) });
  const ids = Object.keys(s.w.MC_TILE_OVERLAYS);
  assert.deepStrictEqual(ids, ['eox-labels', 'eox-labels-dark']);
  assert.strictEqual(s.warns.length, 7, s.warns.join('\n'));
  assert.ok(s.warns.some(w => /"vec".*overlay must be a raster/.test(w)));
  s.warns.forEach(w => assert.ok(w.indexOf('SECRET') < 0 && w.indexOf('h.example') < 0, w));
});

// ─── MC_buildOverlayLayer ───────────────────────────────────────────────────

test('MC_buildOverlayLayer: raster options, hillshade factory, pane, null when unavailable', () => {
  const calls = [];
  const s = makeSandbox({ before: (w) => hillshadeStub(w, calls) });
  const o = s.w.MC_TILE_OVERLAYS;
  const r = s.w.MC_buildOverlayLayer(o['eox-labels'], { pane: 'mcOverlayPane' });
  assert.strictEqual(r.kind, 'tile');
  assert.strictEqual(r._url, o['eox-labels'].url);
  assert.strictEqual(JSON.stringify(r._ctorOpts), JSON.stringify({ attribution: o['eox-labels'].attribution,
    maxZoom: 19, opacity: 1, zIndex: 250, maxNativeZoom: 14, pane: 'mcOverlayPane' }));
  const h = s.w.MC_buildOverlayLayer(o.hillshade, { pane: 'mcOverlayPane' });
  assert.strictEqual(h.kind, 'hillshade');
  assert.strictEqual(JSON.stringify(calls[0]), JSON.stringify({ attribution: o.hillshade.attribution, maxZoom: 19,
    opacity: 0.35, zIndex: 250, maxNativeZoom: 15, url: TERRARIUM }));
  assert.strictEqual(h.options.pane, 'mcOverlayPane', 'pane set on the instance');
  assert.strictEqual(s.w.MC_buildOverlayLayer(null), null);
  assert.strictEqual(s.w.MC_buildOverlayLayer({ kind: 'svg', url: OK_URL }), null);
  s.w.MC_createHillshadeLayer = () => { throw new Error('no canvas'); };
  assert.strictEqual(s.w.MC_buildOverlayLayer(o.hillshade), null);
});

// ─── preference (S7) ────────────────────────────────────────────────────────

test('MC_setTileOverlay / MC_getTileOverlays persist, dedupe, dispatch only on change', () => {
  const s = makeSandbox();
  const seen = [];
  s.w.addEventListener('mc-tile-overlays-changed', (e) => seen.push(e.detail.id + ':' + e.detail.on));
  assert.deepStrictEqual(Array.from(s.w.MC_getTileOverlays()), []);
  assert.strictEqual(s.w.MC_setTileOverlay('eox-labels', true), true);
  assert.strictEqual(s.w.MC_setTileOverlay('eox-labels', true), true, 'idempotent');
  assert.strictEqual(s.w.MC_setTileOverlay('eox-labels-dark', 1), true);
  assert.strictEqual(s.w.localStorage.getItem('mc-tile-overlays'), '["eox-labels","eox-labels-dark"]');
  assert.strictEqual(s.w.MC_setTileOverlay('eox-labels', false), true);
  assert.deepStrictEqual(Array.from(s.w.MC_getTileOverlays()), ['eox-labels-dark']);
  assert.strictEqual(s.w.MC_setTileOverlay('nope', true), false);
  assert.strictEqual(s.w.MC_setTileOverlay('__proto__', true), false);
  assert.strictEqual(s.w.MC_setTileOverlay('carto-dark', true), false, 'base ids are not overlays');
  assert.deepStrictEqual(seen, ['eox-labels:true', 'eox-labels-dark:true', 'eox-labels:false']);
});

test('S7: hostile stored values read as nothing (or only the registered ids)', () => {
  const s = makeSandbox();
  const get = (raw) => { s.w.localStorage.setItem('mc-tile-overlays', raw); return Array.from(s.w.MC_getTileOverlays()); };
  assert.deepStrictEqual(get('<script>'), []);
  assert.deepStrictEqual(get('{"0":"eox-labels","length":1}'), []);
  assert.deepStrictEqual(get('"eox-labels"'), []);
  assert.deepStrictEqual(get('null'), []);
  assert.deepStrictEqual(get('[1,{},null,"__proto__","constructor","eox-labels","x","eox-labels"]'), ['eox-labels']);
  const big = JSON.stringify(new Array(10000).fill('eox-labels'));
  const t0 = Date.now();
  assert.deepStrictEqual(get(big), [], 'oversized value ignored');
  const small = JSON.stringify(new Array(200).fill('eox-labels').concat(['eox-labels-dark']));
  assert.deepStrictEqual(get(small), ['eox-labels', 'eox-labels-dark'], 'duplicates dropped; never more ids than registered');
  assert.ok(Date.now() - t0 < 200);
  // Writing after a hostile value stores a clean list.
  s.w.localStorage.setItem('mc-tile-overlays', '[1,"x","eox-labels-dark"]');
  s.w.MC_setTileOverlay('eox-labels', true);
  assert.strictEqual(s.w.localStorage.getItem('mc-tile-overlays'), '["eox-labels-dark","eox-labels"]');
});

test('blocked localStorage: the preference still works within the page', () => {
  const throwing = { getItem() { throw new Error('denied'); }, setItem() { throw new Error('denied'); } };
  const s = makeSandbox({ storage: throwing });
  assert.strictEqual(s.w.MC_setTileOverlay('eox-labels', true), true);
  assert.deepStrictEqual(Array.from(s.w.MC_getTileOverlays()), ['eox-labels']);
});

// Quota: getItem works, setItem throws. The stored value is now older than
// the page's; reading it back would drop the tick the sync just made.
test('setItem throws but getItem works: the in-page value wins until a write succeeds', () => {
  const store = { 'mc-tile-overlays': '["eox-labels-dark"]' };
  let full = true;
  const quota = {
    getItem(k) { return Object.prototype.hasOwnProperty.call(store, k) ? store[k] : null; },
    setItem(k, v) { if (full) throw new Error('QuotaExceededError'); store[k] = String(v); },
  };
  const s = makeSandbox({ storage: quota });
  const map = makeMap();
  s.w.MC_attachAutoTiles(map, { controlPosition: 'topleft' });
  const c = s.stats.controls[0];
  assert.ok(map.hasLayer(c.overlays['eox-labels-dark']), 'restored from storage');
  // A user tick: Leaflet adds the layer, then fires overlayadd.
  map.addLayer(c.overlays['eox-labels']);
  map._handlers.overlayadd({ type: 'overlayadd', name: 'eox-labels' });
  assert.ok(map.hasLayer(c.overlays['eox-labels']), 'the ticked layer stays on the map');
  assert.ok(map.hasLayer(c.overlays['eox-labels-dark']));
  assert.deepStrictEqual(Array.from(s.w.MC_getTileOverlays()), ['eox-labels-dark', 'eox-labels']);
  // Storage has room again: the next write lands and storage is read again.
  full = false;
  s.w.MC_setTileOverlay('eox-labels-dark', false);
  assert.strictEqual(store['mc-tile-overlays'], '["eox-labels"]');
  store['mc-tile-overlays'] = '["eox-labels-dark"]'; // another tab writes
  assert.deepStrictEqual(Array.from(s.w.MC_getTileOverlays()), ['eox-labels-dark'], 'storage is the source again');
});

// ─── attach: restore, mirror, destroy ───────────────────────────────────────

function overlayLayersOn(map) { return map._layers.filter(l => l.options && l.options.pane === 'mcOverlayPane'); }

test('attach restores ticked overlays into its own pane; destroy removes them and keeps the preference', () => {
  const calls = [];
  const s = makeSandbox({ before: (w) => hillshadeStub(w, calls) });
  s.w.localStorage.setItem('mc-tile-overlays', '["hillshade","eox-labels"]');
  const map = makeMap();
  const evBefore = s.count('mc-tile-overlays-changed'), stBefore = s.count('storage');
  const h = s.w.MC_attachAutoTiles(map, { controlPosition: 'topleft' });
  const on = overlayLayersOn(map);
  assert.strictEqual(on.length, 2);
  assert.ok(on.some(l => l.kind === 'hillshade') && on.some(l => l._url === s.w.MC_TILE_OVERLAYS['eox-labels'].url));
  assert.ok(!h.group._layers.some(l => on.includes(l)), 'overlays are not in the base group');
  assert.strictEqual(map.panes.mcOverlayPane.style.zIndex, '250');
  assert.strictEqual(map.panes.mcOverlayPane.style.pointerEvents, 'none');
  assert.ok(s.count('mc-tile-overlays-changed') > evBefore && s.count('storage') > stBefore);
  // Leaflet fires overlayremove when the set removes its layers: not a user choice.
  const handler = map._handlers.overlayremove;
  map.removeLayer = ((orig) => function (l) { orig(l); if (handler && on.includes(l)) handler({ type: 'overlayremove', name: 'x' }); return map; })(map.removeLayer);
  h.destroy();
  assert.strictEqual(overlayLayersOn(map).length, 0, 'overlays removed');
  assert.strictEqual(s.count('mc-tile-overlays-changed'), evBefore);
  assert.strictEqual(s.count('storage'), stBefore);
  assert.ok(!map._handlers.overlayadd && !map._handlers.overlayremove, 'control handlers removed');
  assert.strictEqual(s.w.localStorage.getItem('mc-tile-overlays'), '["hillshade","eox-labels"]', 'preference kept');
  s.w.MC_setTileOverlay('eox-labels-dark', true);
  assert.strictEqual(overlayLayersOn(map).length, 0, 'no sync after destroy');
});

test('picker: Overlays fieldset, text labels, ids; ticking/unticking writes the shared preference', () => {
  const s = makeSandbox({ mapCfg: custom([{ id: 'x', url: OK_URL, overlay: true, label: '<img src=x onerror=alert(1)>' }]) });
  const map = makeMap();
  s.w.MC_attachAutoTiles(map, { controlPosition: 'topright' });
  const c = s.stats.controls[0];
  assert.deepStrictEqual(Object.keys(c.overlays), ['eox-labels', 'eox-labels-dark', 'custom-x']);
  const fs0 = c.over.list.children[0];
  assert.strictEqual(fs0.tagName, 'fieldset');
  assert.strictEqual(fs0.className, 'mc-tile-group mc-tile-overlays');
  assert.strictEqual(fs0.children[0].className, 'mc-tile-group-heading');
  assert.strictEqual(fs0.children[0].textContent, 'Overlays');
  const labels = fs0.children.slice(1);
  assert.deepStrictEqual(labels.map(l => l.getAttribute('data-tile-overlay-id')), ['eox-labels', 'eox-labels-dark', 'custom-x']);
  assert.deepStrictEqual(labels.map(l => l.querySelectorAll('span')[1].textContent),
    [' Place labels (EOX)', ' Place labels, dark text (EOX)', ' <img src=x onerror=alert(1)>']);
  assert.strictEqual(labels[2].querySelectorAll('span')[1].children.length, 0);
  assert.strictEqual(s.stats.innerHTMLWrites, 0);
  // A user tick: Leaflet adds the layer, then fires overlayadd with the raw key.
  map.addLayer(c.overlays['eox-labels']);
  map._handlers.overlayadd({ type: 'overlayadd', name: 'eox-labels', layer: c.overlays['eox-labels'] });
  assert.deepStrictEqual(Array.from(s.w.MC_getTileOverlays()), ['eox-labels']);
  map.removeLayer(c.overlays['eox-labels']);
  map._handlers.overlayremove({ type: 'overlayremove', name: 'eox-labels', layer: c.overlays['eox-labels'] });
  assert.deepStrictEqual(Array.from(s.w.MC_getTileOverlays()), []);
  map._handlers.overlayadd({ type: 'overlayadd', name: 'carto-dark' });
  assert.deepStrictEqual(Array.from(s.w.MC_getTileOverlays()), [], 'non-overlay names ignored');
});

test('two maps mirror ticks through mc-tile-overlays-changed and the storage event', () => {
  const s = makeSandbox();
  const m1 = makeMap(), m2 = makeMap();
  s.w.MC_attachAutoTiles(m1, { controlPosition: 'topleft' });
  s.w.MC_attachAutoTiles(m2, { controlPosition: 'topleft' });
  const [c1, c2] = s.stats.controls;
  m1.addLayer(c1.overlays['eox-labels']);
  m1._handlers.overlayadd({ type: 'overlayadd', name: 'eox-labels' });
  assert.ok(m2.hasLayer(c2.overlays['eox-labels']), 'map 2 mirrored the tick');
  assert.notStrictEqual(c1.overlays['eox-labels'], c2.overlays['eox-labels'], 'each map has its own layer');
  s.w.MC_setTileOverlay('eox-labels', false);
  assert.ok(!m1.hasLayer(c1.overlays['eox-labels']) && !m2.hasLayer(c2.overlays['eox-labels']));
  // Another tab wrote the preference.
  s.w.localStorage.setItem('mc-tile-overlays', '["eox-labels-dark"]');
  s.w.dispatchEvent({ type: 'storage', key: 'mc-tile-overlays', newValue: '["eox-labels-dark"]' });
  assert.ok(m1.hasLayer(c1.overlays['eox-labels-dark']) && m2.hasLayer(c2.overlays['eox-labels-dark']));
  s.w.dispatchEvent({ type: 'storage', key: 'other', newValue: 'x' }); // ignored
});

test('the overlay pane is created once per map; a map without panes still gets overlays', () => {
  const s = makeSandbox();
  s.w.MC_setTileOverlay('eox-labels', true);
  const map = makeMap();
  const h1 = s.w.MC_attachAutoTiles(map, { controlPosition: 'topleft' });
  h1.destroy();
  s.w.MC_attachAutoTiles(map, { controlPosition: 'topleft' });
  assert.strictEqual(map.createPaneCalls, 1);
  const bare = makeMap({ noPanes: true });
  s.w.MC_attachAutoTiles(bare, { controlPosition: 'topleft' });
  const l = s.stats.controls[2].overlays['eox-labels'];
  assert.ok(bare.hasLayer(l));
  assert.ok(!('pane' in l._ctorOpts));
});

test('surfaces without a picker show no overlays', () => {
  const s = makeSandbox();
  s.w.MC_setTileOverlay('eox-labels', true);
  const map = makeMap();
  s.w.MC_attachAutoTiles(map);
  assert.strictEqual(map._layers.length, 1, 'only the base group');
  assert.strictEqual(map.createPaneCalls, 0);
});

test('async config: a ticked custom overlay appears once its entry is registered', () => {
  const s = makeSandbox();
  s.w.localStorage.setItem('mc-tile-overlays', '["custom-rain"]');
  const map = makeMap();
  s.w.MC_attachAutoTiles(map, { controlPosition: 'topleft' });
  assert.strictEqual(overlayLayersOn(map).length, 0, 'not registered yet');
  assert.strictEqual(s.w.localStorage.getItem('mc-tile-overlays'), '["custom-rain"]', 'reading never rewrites');
  s.w.MC_MAP_CFG = custom([{ id: 'rain', url: OK_URL, overlay: true }]);
  s.w.MC_initTileRegistry(true);
  const on = overlayLayersOn(map);
  assert.strictEqual(on.length, 1);
  assert.strictEqual(on[0]._url, OK_URL);
  const c = s.stats.controls[s.stats.controls.length - 1];
  assert.strictEqual(c.overlays['custom-rain'], on[0], 'the rebuilt picker lists the same layer');
  // Built-in overlays keep their layer across a config re-registration.
  const eoxBefore = s.stats.controls[s.stats.controls.length - 1].overlays['eox-labels'];
  s.w.MC_initTileRegistry(true);
  assert.strictEqual(s.stats.controls[s.stats.controls.length - 1].overlays['eox-labels'], eoxBefore, 'eox layer reused');
  // Config changes the entry: the old layer is replaced, not duplicated.
  s.w.MC_MAP_CFG = custom([{ id: 'rain', url: 'https://rain2.example/{z}/{x}/{y}.png', overlay: true }]);
  s.w.MC_initTileRegistry(true);
  assert.deepStrictEqual(overlayLayersOn(map).map(l => l._url), ['https://rain2.example/{z}/{x}/{y}.png']);
  assert.strictEqual(s.w.localStorage.getItem('mc-tile-overlays'), '["custom-rain"]', 'preference survives the swap');
});

console.log('');
console.log('map overlays + vector: ' + passed + ' passed, ' + failed + ' failed');
if (failed > 0) process.exit(1);

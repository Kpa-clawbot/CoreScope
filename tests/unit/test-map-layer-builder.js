/* test-map-layer-builder.js — shared basemap layer builder (#2428).
 *
 * Covers, against the real public/map-tile-providers.js in a vm sandbox:
 *   - type 'any' entries: picked for both themes, never colour-inverted,
 *     listed after the dark entries in the layer control
 *   - MC_resolveTile's new descriptor fields and their defaults
 *   - MC_buildTileLayer: raster options, refUrl grouping, vector fallback
 *   - layer-control labels are plain text (S2)
 *   - MC_attachAutoTiles: build, in-place update, rebuild, theme flip, no-op,
 *     destroy, several maps on one page
 *
 * Runs via: node tests/unit/test-map-layer-builder.js
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

function makeStorage() {
  const store = {};
  return {
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

// Minimal DOM element: textContent/appendChild/title, and a trap that counts
// any innerHTML write so the S2 test can prove none happen.
function makeEl(tag, stats) {
  let text = '';
  const el = {
    tagName: tag, children: [], title: '', className: '', parentNode: null,
    appendChild(c) { el.children.push(c); c.parentNode = el; return c; },
    insertBefore(c) { el.children.push(c); c.parentNode = el; return c; },
    querySelectorAll(sel) { return sel === 'span' ? el.children.filter(c => c.tagName === 'span') : []; },
    setAttribute(k, v) { el['attr:' + k] = String(v); },
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

function makeSandbox(opts) {
  opts = opts || {};
  const listeners = {};
  const observers = [];
  const warns = [];
  const stats = { innerHTMLWrites: 0, controls: [] };
  const docPane = makePane();
  const state = { theme: opts.theme || 'dark' };

  function MutationObserver(cb) {
    this.cb = cb; this.observing = false; this.disconnected = false;
    observers.push(this);
  }
  MutationObserver.prototype.observe = function (target, o) { this.observing = true; this.target = target; this.opts = o; };
  MutationObserver.prototype.disconnect = function () { this.observing = false; this.disconnected = true; };

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
      localStorage: makeStorage(),
    },
    CustomEvent: function (type, init) { this.type = type; this.detail = (init && init.detail) || null; },
  };
  ctx.localStorage = ctx.window.localStorage;
  ctx.L = ctx.window.L = makeLeaflet(stats);
  vm.createContext(ctx);
  ctx.window.document = ctx.document;
  if (opts.mapCfg !== undefined) ctx.window.MC_MAP_CFG = opts.mapCfg;
  vm.runInContext(SRC, ctx);
  // The module publishes on `window`; surface it as globals too (browser parity).
  for (const k of Object.keys(ctx.window)) if (!(k in ctx)) ctx[k] = ctx.window[k];
  return {
    ctx, w: ctx.window, listeners, observers, warns, stats, docPane, state,
    setTheme(t) { state.theme = t; observers.filter(o => o.observing).forEach(o => o.cb([])); },
    count(type) { return (listeners[type] || []).length; },
  };
}

// Fake Leaflet: records construction options, setUrl calls and group membership.
function makeLeaflet(stats) {
  function evented(o) {
    o._events = {};
    o.on = (e, cb) => { o._events[e] = cb; return o; };
    return o;
  }
  const L = {
    created: [],
    tileLayer(url, opts) {
      const t = evented({ kind: 'tile', _url: url, options: Object.assign({}, opts), setUrlCalls: [] });
      t._ctorOpts = opts;
      t.setUrl = (u) => { t.setUrlCalls.push(u); t._url = u; return t; };
      t.addTo = (m) => { m.addLayer(t); return t; };
      L.created.push(t);
      return t;
    },
    layerGroup(layers) {
      const g = evented({ kind: 'group', _layers: (layers || []).slice() });
      g.addLayer = (l) => { if (!g._layers.includes(l)) g._layers.push(l); g.added = (g.added || 0) + 1; return g; };
      g.removeLayer = (l) => { g._layers = g._layers.filter(x => x !== l); g.removed = (g.removed || 0) + 1; return g; };
      g.hasLayer = (l) => g._layers.includes(l);
      g.getLayers = () => g._layers.slice();
      g.addTo = (m) => { m.addLayer(g); return g; };
      return g;
    },
    control: {
      layers(baseMaps, overlays, opts) {
        const labels = Object.keys(baseMaps).map((id) => {
          const label = makeEl('label', stats);
          const holder = makeEl('span', stats);
          holder.appendChild(makeEl('input', stats));
          const name = makeEl('span', stats);
          name.textContent = ' ' + id;
          holder.appendChild(name);
          label.appendChild(holder);
          label.querySelectorAll = (sel) => (sel === 'span' ? [holder, name] : []);
          return label;
        });
        const list = makeEl('div', stats);
        labels.forEach(l => list.appendChild(l));
        const c = {
          baseMaps, opts, labels, removed: false,
          addTo(m) { c.map = m; return c; },
          remove() { c.removed = true; return c; },
          getContainer() { return { querySelectorAll: () => labels }; },
        };
        stats.controls.push(c);
        return c;
      },
    },
  };
  return L;
}

function makeMap() {
  const pane = makePane();
  const attr = { counts: {}, updates: 0 };
  const m = {
    _layers: [], _handlers: {},
    tilePane: pane,
    getPane: (n) => (n === 'tilePane' ? pane : null),
    addLayer(l) { if (!m._layers.includes(l)) m._layers.push(l); return m; },
    removeLayer(l) { m._layers = m._layers.filter(x => x !== l); return m; },
    hasLayer(l) {
      return m._layers.some(x => x === l || (x.kind === 'group' && x._layers.some(y => y === l || (y.kind === 'group' && y._layers.includes(l)))));
    },
    on(e, fn) { m._handlers[e] = fn; },
    off(e, fn) { if (m._handlers[e] === fn) delete m._handlers[e]; },
    attributionControl: {
      addAttribution(t) { attr.counts[t] = (attr.counts[t] || 0) + 1; },
      removeAttribution(t) { if (attr.counts[t]) attr.counts[t]--; },
      _update() { attr.updates++; },
    },
    attr,
  };
  return m;
}

const SAT = { provider: 'test', label: 'Satellite', type: 'any', category: 'satellite',
  url: 'https://sat.example/{z}/{y}/{x}.jpg', attribution: 'Sat', maxZoom: 19, maxNativeZoom: 17,
  invertFilter: 'invert(1)' };

function tilesOf(layer) { return layer.kind === 'group' ? layer._layers : [layer]; }

// ─── 'any' entries ──────────────────────────────────────────────────────────

test("getters accept a stored 'any' id for either theme; wrong-theme ids still rejected", () => {
  const s = makeSandbox();
  s.w.MC_TILE_PROVIDERS.sat = SAT;
  s.w.localStorage.setItem('mc-dark-tile-provider', 'sat');
  s.w.localStorage.setItem('mc-light-tile-provider', 'sat');
  assert.strictEqual(s.w.MC_getDarkTileProvider(), 'sat');
  assert.strictEqual(s.w.MC_getLightTileProvider(), 'sat');
  s.w.localStorage.setItem('mc-dark-tile-provider', 'carto-light');
  s.w.localStorage.setItem('mc-light-tile-provider', 'carto-dark');
  assert.strictEqual(s.w.MC_getDarkTileProvider(), 'carto-dark');
  assert.strictEqual(s.w.MC_getLightTileProvider(), 'carto-light');
});

test("setActive accepts an 'any' entry for either theme, still rejects cross-theme ids", () => {
  const s = makeSandbox();
  s.w.MC_TILE_PROVIDERS.sat = SAT;
  assert.strictEqual(s.w.MC_setDarkTileProvider('sat'), true);
  assert.strictEqual(s.w.MC_setLightTileProvider('sat'), true);
  assert.strictEqual(s.w.localStorage.getItem('mc-dark-tile-provider'), 'sat');
  assert.strictEqual(s.w.localStorage.getItem('mc-light-tile-provider'), 'sat');
  assert.strictEqual(s.w.MC_setDarkTileProvider('carto-light'), false);
  assert.strictEqual(s.w.MC_setLightTileProvider('carto-dark'), false);
});

test("MC_resolveTile picks an 'any' entry in both themes and never carries its invertFilter", () => {
  const s = makeSandbox();
  s.w.MC_TILE_PROVIDERS.sat = SAT;
  s.w.MC_setDarkTileProvider('sat');
  s.w.MC_setLightTileProvider('sat');
  for (const dark of [true, false]) {
    const r = s.w.MC_resolveTile(dark);
    assert.strictEqual(r.id, 'sat');
    assert.strictEqual(r.url, SAT.url);
    assert.strictEqual(r.type, dark ? 'dark' : 'light', 'descriptor type is the theme asked for');
    assert.strictEqual(r.invertFilter, null);
    assert.strictEqual(r.category, 'satellite');
    assert.strictEqual(r.maxNativeZoom, 17);
  }
});

test("MC_resolveTile still rejects a light entry for the dark theme", () => {
  const s = makeSandbox();
  s.w.MC_getDarkTileProvider = () => 'carto-light';
  const r = s.w.MC_resolveTile(true);
  assert.strictEqual(r.id, null, 'falls through to the constants');
});

test("applyTileFilter never inverts an 'any' entry, still inverts positron-dark", () => {
  const s = makeSandbox();
  s.w.MC_TILE_PROVIDERS.sat = SAT;
  s.w.MC_setDarkTileProvider('positron-dark');
  assert.ok(s.docPane.style.filter.indexOf('invert(') >= 0);
  s.w.MC_setDarkTileProvider('sat');
  assert.strictEqual(s.docPane.style.filter, '');
});

test("layer control lists light, then dark, then 'any' entries; separator before the first dark", () => {
  const s = makeSandbox();
  s.w.MC_TILE_PROVIDERS.sat = SAT;
  const map = makeMap();
  s.w.MC_createLayerControl(map, s.ctx.L.layerGroup(), 'topleft');
  const c = s.stats.controls[0];
  const ids = Object.keys(c.baseMaps);
  const reg = s.w.MC_TILE_PROVIDERS;
  assert.strictEqual(ids[0], '__auto__');
  // Grouped by category (street, topo, satellite); inside a category light
  // comes first, then dark, then 'any'.
  const CAT = ['street', 'topo', 'satellite'], TYPE = ['light', 'dark', 'any'];
  const rank = ids.slice(1).map(id => CAT.indexOf(reg[id].category || 'street') * 3 + TYPE.indexOf(reg[id].type));
  assert.ok(rank.every(r => r >= 0), 'every entry has a known category and type');
  assert.deepStrictEqual(rank, rank.slice().sort((a, b) => a - b), 'ordered by category, then light/dark/any');
  const street = ids.slice(1).filter(id => (reg[id].category || 'street') === 'street').map(id => reg[id].type);
  assert.ok(street.indexOf('dark') > 0, 'street: light entries come before dark');
  assert.strictEqual(ids[ids.length - 1], 'sat', "the satellite 'any' entry comes last");
  const sepAt = c.labels.findIndex(l => l.parentNode.children.some(ch => ch.className === 'leaflet-control-layers-separator'));
  assert.ok(sepAt >= 0, 'separator inserted');
  // Picking the 'any' layer explicitly never inverts the pane.
  c.baseMaps.sat._events.add();
  assert.strictEqual(map.tilePane.style.filter, '');
  assert.strictEqual(map.tilePane.getAttribute('data-explicit-layer'), 'true');
});

// ─── Descriptor fields ──────────────────────────────────────────────────────

test('MC_resolveTile descriptor defaults: raster, street, null maxNativeZoom/subdomains', () => {
  const s = makeSandbox();
  for (const dark of [true, false]) {
    const r = s.w.MC_resolveTile(dark);
    assert.strictEqual(r.id, dark ? 'carto-dark' : 'carto-light');
    assert.strictEqual(r.format, 'raster');
    assert.strictEqual(r.category, 'street');
    assert.strictEqual(r.maxNativeZoom, null);
    assert.strictEqual(r.subdomains, null);
  }
  // Constants path (registry emptied) and legacy override carry the defaults too.
  s.w.MC_getDarkTileProvider = () => 'nope';
  for (const k of Object.keys(s.w.MC_TILE_PROVIDERS)) delete s.w.MC_TILE_PROVIDERS[k];
  let r = s.w.MC_resolveTile(true);
  assert.strictEqual(r.id, null);
  assert.strictEqual(r.format, 'raster');
  assert.strictEqual(r.maxNativeZoom, null);
  s.w.MC_LEGACY_TILE_OVERRIDE = { dark: true };
  r = s.w.MC_resolveTile(true);
  assert.strictEqual(r.override, true);
  assert.strictEqual(r.category, 'street');
  assert.strictEqual(r.subdomains, null);
});

test('MC_resolveTile passes format/category/maxNativeZoom/subdomains through', () => {
  const s = makeSandbox();
  s.w.MC_TILE_PROVIDERS.topo = { provider: 't', type: 'light', label: 'Topo', url: 'https://{s}.t/{z}/{x}/{y}.png',
    attribution: 'T', maxZoom: 20, maxNativeZoom: 0, subdomains: 'abcd', category: 'topo', format: 'raster' };
  s.w.MC_setLightTileProvider('topo');
  const r = s.w.MC_resolveTile(false);
  assert.strictEqual(r.category, 'topo');
  assert.strictEqual(r.maxNativeZoom, 0, 'a 0 maxNativeZoom is a value, not absent');
  assert.strictEqual(r.subdomains, 'abcd');
  assert.strictEqual(r.maxZoom, 20);
});

// ─── MC_buildTileLayer ──────────────────────────────────────────────────────

test('raster builder passes only attribution + maxZoom when the extras are unset', () => {
  const s = makeSandbox();
  const l = s.w.MC_buildTileLayer(s.w.MC_TILE_PROVIDERS['carto-dark']);
  assert.strictEqual(l.kind, 'tile');
  assert.strictEqual(typeof l._url, 'string', 'function-typed url is invoked');
  assert.ok(l._url.indexOf('dark_all') >= 0);
  assert.deepStrictEqual(Object.keys(l._ctorOpts).sort(), ['attribution', 'maxZoom']);
  assert.strictEqual(l._ctorOpts.maxZoom, 19);
});

test('raster builder passes maxNativeZoom and subdomains when set (entry or descriptor)', () => {
  const s = makeSandbox();
  const entry = { type: 'light', url: () => 'https://{s}.x/{z}/{x}/{y}.png', attribution: 'X', maxZoom: 20, maxNativeZoom: 16, subdomains: 'abcd' };
  const l = s.w.MC_buildTileLayer(entry);
  assert.strictEqual(l._url, 'https://{s}.x/{z}/{x}/{y}.png');
  // JSON compare: vm-realm objects never deepStrictEqual host literals.
  assert.strictEqual(JSON.stringify(l._ctorOpts), JSON.stringify({ attribution: 'X', maxZoom: 20, maxNativeZoom: 16, subdomains: 'abcd' }));
  // A resolved descriptor (nulls for the extras) builds without them.
  const r = s.w.MC_resolveTile(false);
  const l2 = s.w.MC_buildTileLayer(r);
  assert.strictEqual(l2._url, r.url);
  assert.ok(!('maxNativeZoom' in l2._ctorOpts) && !('subdomains' in l2._ctorOpts));
});

test('refUrl entries build a layer group of base + labels with the same options', () => {
  const s = makeSandbox();
  const g = s.w.MC_buildTileLayer(s.w.MC_TILE_PROVIDERS['esri-darkgray-labels']);
  assert.strictEqual(g.kind, 'group');
  assert.strictEqual(g._layers.length, 2);
  assert.ok(g._layers[0]._url.indexOf('World_Dark_Gray_Base') >= 0);
  assert.ok(g._layers[1]._url.indexOf('World_Dark_Gray_Reference') >= 0);
  assert.deepStrictEqual(g._layers[1]._ctorOpts, g._layers[0]._ctorOpts);
});

test('vector entries warn once and build the raster fallback', () => {
  const s = makeSandbox();
  const vDark  = { type: 'dark',  format: 'vector', url: 'https://v.example/style.json', attribution: 'V' };
  const vLight = { type: 'light', format: 'vector', url: 'https://v.example/style.json', attribution: 'V' };
  const a = s.w.MC_buildTileLayer(vDark);
  assert.ok(a._url.indexOf('dark_all') >= 0, 'dark -> carto-dark');
  const b = s.w.MC_buildTileLayer(vLight);
  assert.ok(b._url.indexOf('light_all') >= 0, 'light -> carto-light');
  const c = s.w.MC_buildTileLayer(vLight, { fallbackId: 'esri-darkgray-labels' });
  assert.strictEqual(c.kind, 'group', 'opts.fallbackId is honoured (incl. refUrl)');
  const d = s.w.MC_buildTileLayer(vDark, { fallbackId: 'no-such-id' });
  assert.ok(d._url.indexOf('dark_all') >= 0, 'unknown fallbackId -> shipped default');
  assert.strictEqual(s.warns.length, 1, 'warned exactly once: ' + JSON.stringify(s.warns));
});

// ─── S2: labels are text ────────────────────────────────────────────────────

test('layer-control labels are set as text; Auto keeps its tooltip; no innerHTML', () => {
  const s = makeSandbox();
  const evil = '<img src=x onerror=alert(1)>';
  s.w.MC_TILE_PROVIDERS.evil = { type: 'light', label: evil, url: 'https://e/{z}/{x}/{y}.png' };
  s.w.MC_createLayerControl(makeMap(), s.ctx.L.layerGroup(), 'topleft');
  const c = s.stats.controls[0];
  const byId = {};
  c.labels.forEach(l => { byId[l['attr:data-tile-id']] = l; });
  const evilSpan = byId.evil.querySelectorAll('span')[1];
  assert.strictEqual(evilSpan.textContent, ' ' + evil);
  assert.strictEqual(evilSpan.children.length, 0, 'no element parsed from the label');
  const autoSpan = byId.__auto__.querySelectorAll('span')[1];
  assert.strictEqual(autoSpan.textContent, ' Auto');
  assert.strictEqual(autoSpan.children[0].title, "Follows the app's current light/dark theme");
  assert.strictEqual(byId['carto-dark'].querySelectorAll('span')[1].textContent, ' Carto Dark');
  assert.strictEqual(s.stats.innerHTMLWrites, 0);
});

// ─── MC_attachAutoTiles ─────────────────────────────────────────────────────

test('attach: builds the themed layer in its own group and filters its own pane', () => {
  const s = makeSandbox();
  s.w.localStorage.setItem('mc-dark-tile-provider', 'positron-dark');
  const map = makeMap();
  const h = s.w.MC_attachAutoTiles(map);
  assert.ok(map._layers.includes(h.group), 'group added to the map');
  assert.strictEqual(h.group._layers.length, 1);
  assert.ok(h.group._layers[0]._url.indexOf('light_all') >= 0, 'positron-dark tiles');
  assert.ok(map.tilePane.style.filter.indexOf('invert(') >= 0, "this map's pane is inverted");
  assert.ok(map.attr.updates >= 1, 'attribution control refreshed');
  assert.strictEqual(typeof h.sync, 'function');
  assert.strictEqual(typeof h.destroy, 'function');
});

test('attach: same-format provider change updates in place (setUrl, no rebuild)', () => {
  const s = makeSandbox();
  const map = makeMap();
  const h = s.w.MC_attachAutoTiles(map);
  const layer = h.group._layers[0];
  const oldAttr = layer.options.attribution;
  map.attributionControl.addAttribution(oldAttr); // what Leaflet's layeradd did
  s.w.MC_TILE_PROVIDERS.alt = { provider: 'x', type: 'dark', label: 'Alt', url: 'https://alt/{z}/{x}/{y}.png', attribution: 'Alt tiles', maxZoom: 19 };
  s.w.MC_setDarkTileProvider('alt');
  assert.strictEqual(h.group._layers[0], layer, 'same layer object');
  assert.deepStrictEqual(layer.setUrlCalls, ['https://alt/{z}/{x}/{y}.png']);
  assert.strictEqual(layer.options.attribution, 'Alt tiles');
  assert.strictEqual(layer.options.maxZoom, 19);
  assert.strictEqual(map.attr.counts['Alt tiles'], 1, 'new attribution shown');
  assert.strictEqual(map.attr.counts[oldAttr], 0, 'old attribution dropped');
  assert.ok(!h.group.removed, 'nothing removed from the group');
});

// Leaflet reads a layer's maxZoom into map._layersMaxZoom only on layeradd/
// layerremove, so editing options.maxZoom in place leaves the map's zoom cap
// stale (blank z17-19 after carto 19 -> usgs-topo 16, or capped at 16 after
// swapping back). A maxZoom change must rebuild the layer.
test('attach: a maxZoom change rebuilds the layer; an equal maxZoom swaps in place', () => {
  const s = makeSandbox();
  s.w.MC_TILE_PROVIDERS.z16 = { provider: 'x', type: 'dark', label: 'Z16', url: 'https://z16/{z}/{x}/{y}.png', attribution: 'Z', maxZoom: 16 };
  s.w.MC_TILE_PROVIDERS.z16b = { provider: 'x', type: 'dark', label: 'Z16b', url: 'https://z16b/{z}/{x}/{y}.png', attribution: 'Z', maxZoom: 16 };
  const map = makeMap();
  const h = s.w.MC_attachAutoTiles(map);
  const carto = h.group._layers[0];
  assert.strictEqual(carto._ctorOpts.maxZoom, 19);
  s.w.MC_setDarkTileProvider('z16');
  const z16 = h.group._layers[0];
  assert.notStrictEqual(z16, carto, '19 -> 16 rebuilds');
  assert.strictEqual(carto.setUrlCalls.length, 0, 'old layer not edited in place');
  assert.strictEqual(z16._ctorOpts.maxZoom, 16);
  assert.strictEqual(h.group._layers.length, 1);
  s.w.MC_setDarkTileProvider('z16b');
  assert.strictEqual(h.group._layers[0], z16, '16 -> 16 swaps in place');
  assert.deepStrictEqual(z16.setUrlCalls, ['https://z16b/{z}/{x}/{y}.png']);
  s.w.MC_setDarkTileProvider('carto-dark');
  assert.notStrictEqual(h.group._layers[0], z16, '16 -> 19 rebuilds');
  assert.strictEqual(h.group._layers[0]._ctorOpts.maxZoom, 19);
});

test('attach: refUrl change rebuilds (base + labels group replaces the bare layer)', () => {
  const s = makeSandbox();
  const map = makeMap();
  const h = s.w.MC_attachAutoTiles(map);
  const old = h.group._layers[0];
  s.w.MC_setDarkTileProvider('esri-darkgray-labels');
  assert.strictEqual(h.group._layers.length, 1);
  assert.notStrictEqual(h.group._layers[0], old);
  assert.strictEqual(h.group._layers[0].kind, 'group');
  assert.strictEqual(old.setUrlCalls.length, 0, 'old layer not reused');
  s.w.MC_setDarkTileProvider('carto-dark');
  assert.strictEqual(h.group._layers[0].kind, 'tile', 'and back to a bare layer');
});

test('attach: format change rebuilds; a vector pick falls back to raster', () => {
  const s = makeSandbox();
  const map = makeMap();
  const h = s.w.MC_attachAutoTiles(map);
  s.w.MC_setDarkTileProvider('positron-dark');
  const old = h.group._layers[0];
  s.w.MC_TILE_PROVIDERS.vec = { provider: 'x', type: 'dark', label: 'Vec', format: 'vector', url: 'https://v/style.json', attribution: 'V' };
  s.w.MC_setDarkTileProvider('vec');
  const now = h.group._layers[0];
  assert.notStrictEqual(now, old, 'rebuilt on format change');
  assert.ok(now._url.indexOf('dark_all') >= 0, 'vector -> carto-dark raster');
  assert.strictEqual(h.group._layers.length, 1);
});

test('attach: theme flip re-syncs through the data-theme observer', () => {
  const s = makeSandbox({ theme: 'dark' });
  const map = makeMap();
  const h = s.w.MC_attachAutoTiles(map);
  const obs = s.observers.find(o => o.observing);
  assert.ok(obs, 'observer attached');
  assert.strictEqual(JSON.stringify(obs.opts.attributeFilter), '["data-theme"]');
  s.setTheme('light');
  const layer = h.group._layers[0];
  assert.ok(layer._url.indexOf('light_all') >= 0, 'now light tiles');
  assert.strictEqual(layer.setUrlCalls.length, 1, 'swapped in place');
});

test("attach: an 'any' pick survives a theme flip untouched and is never inverted", () => {
  const s = makeSandbox({ theme: 'dark' });
  s.w.MC_TILE_PROVIDERS.sat = SAT;
  s.w.MC_setDarkTileProvider('sat');
  s.w.MC_setLightTileProvider('sat');
  const map = makeMap();
  map.tilePane.style.filter = 'invert(1)';
  const h = s.w.MC_attachAutoTiles(map);
  const layer = h.group._layers[0];
  assert.strictEqual(layer._url, SAT.url);
  assert.strictEqual(layer._ctorOpts.maxNativeZoom, 17);
  assert.strictEqual(map.tilePane.style.filter, '');
  s.setTheme('light');
  assert.strictEqual(h.group._layers[0], layer);
  assert.strictEqual(layer.setUrlCalls.length, 0, 'no tile change');
  assert.strictEqual(map.tilePane.style.filter, '');
});

test('attach: unchanged resolution is a no-op; other-theme picks are ignored', () => {
  const s = makeSandbox();
  const map = makeMap();
  const h = s.w.MC_attachAutoTiles(map);
  const layer = h.group._layers[0];
  const adds = h.group.added || 0;
  h.sync();
  s.w.dispatchEvent(new s.ctx.CustomEvent('mc-tile-provider-changed', { detail: { auto: true } }));
  s.w.MC_setLightTileProvider('carto-voyager');
  assert.strictEqual(h.group._layers[0], layer);
  assert.strictEqual(layer.setUrlCalls.length, 0);
  assert.strictEqual(h.group.added || 0, adds);
  assert.ok(!h.group.removed);
});

test('attach: async config re-syncs (fromConfig) and picks up the Carto key', () => {
  const s = makeSandbox();
  const map = makeMap();
  const h = s.w.MC_attachAutoTiles(map);
  const layer = h.group._layers[0];
  s.w.MC_MAP_CFG = { tiles: { providers: { carto: { key: 'k1' } } } };
  s.w.MC_initTileRegistry(true);
  assert.strictEqual(h.group._layers[0], layer, 'same layer, url swapped');
  assert.ok(layer._url.indexOf('?key=k1') >= 0, layer._url);
});

test('attach: destroy removes listener, observer, group and the layer control', () => {
  const s = makeSandbox();
  const map = makeMap();
  const before = s.count('mc-tile-provider-changed');
  const h = s.w.MC_attachAutoTiles(map, { controlPosition: 'topright' });
  const c = s.stats.controls[0];
  assert.strictEqual(c.opts.position, 'topright', 'control at the requested corner');
  assert.strictEqual(c.baseMaps.__auto__, h.group, 'Auto entry is this group');
  assert.ok(s.count('mc-tile-provider-changed') > before);
  const obs = s.observers.find(o => o.observing);
  const layer = h.group._layers[0];
  h.destroy();
  assert.strictEqual(s.count('mc-tile-provider-changed'), before, 'window listeners removed');
  assert.ok(obs.disconnected, 'observer disconnected');
  assert.ok(!map._layers.includes(h.group), 'group removed from the map');
  assert.ok(c.removed, 'layer control removed');
  assert.ok(!map._handlers.baselayerchange, 'baselayerchange handler removed');
  s.w.MC_setDarkTileProvider('positron-dark');
  s.w.MC_initTileRegistry(true);
  assert.strictEqual(layer.setUrlCalls.length, 0, 'no sync after destroy');
  assert.strictEqual(s.stats.controls.length, 1, 'control not rebuilt after destroy');
  h.destroy(); // idempotent
});

// A pick is saved (mc-tile-pinned). On a map WITH a picker the pin is the
// explicit layer only: the hidden Auto group stays on the theme's map, so a
// later click on Auto never first loads the pinned map, and a pin set in
// another tab never shows under a radio that says Auto. A map WITHOUT a
// picker follows the pin (it fits the theme here).
test('picker: the hidden Auto group never loads the pinned map; a map without a picker shows it', () => {
  const s = makeSandbox({ theme: 'light' });
  s.w.MC_MAP_CFG = { tiles: { providers: { osmfr: { enabled: true } } } };
  s.w.MC_initTileRegistry(true);
  const map = makeMap();
  const h = s.w.MC_attachAutoTiles(map, { controlPosition: 'topleft' });
  const plain = makeMap();
  const hp = s.w.MC_attachAutoTiles(plain, {});
  const c = s.stats.controls[s.stats.controls.length - 1];
  map.addLayer(c.baseMaps['osmfr-hot']);
  c.baseMaps['osmfr-hot']._events.add();
  map._handlers.baselayerchange({ name: 'osmfr-hot', layer: c.baseMaps['osmfr-hot'] });
  assert.strictEqual(s.w.MC_getPinnedTileProvider(), 'osmfr-hot', 'pinned');
  const autoUrls = h.group._layers.map(l => tilesOf(l)[0]._url).join(' ');
  assert.ok(/light_all/.test(autoUrls) && !/openstreetmap\.fr/.test(autoUrls), 'Auto group still Carto light: ' + autoUrls);
  const plainUrls = hp.group._layers.map(l => tilesOf(l)[0]._url).join(' ');
  assert.ok(/openstreetmap\.fr/.test(plainUrls), 'map without a picker follows the pin: ' + plainUrls);
  // Another tab's pin (no storage event) and a theme flip: Auto stays the theme map.
  s.ctx.localStorage.setItem('mc-tile-pinned', 'osmfr-hot');
  h.sync();
  assert.ok(/light_all/.test(h.group._layers.map(l => tilesOf(l)[0]._url).join(' ')), 'Auto group ignores the pin on sync');
  h.destroy(); hp.destroy();
});

// The picker rebuilds when /api/config/client arrives (fromConfig). An
// explicit pick of a LIGHT entry while the app is dark must survive it: the
// radio and the tiles stay on the picked entry.
test('picker: an explicit pick survives the fromConfig rebuild (light entry in dark theme)', () => {
  const s = makeSandbox({ theme: 'dark' });
  const map = makeMap();
  const h = s.w.MC_attachAutoTiles(map, { controlPosition: 'topleft' });
  const c1 = s.stats.controls[0];
  // A user click: Leaflet adds the layer, then fires baselayerchange.
  map.addLayer(c1.baseMaps['carto-light']);
  c1.baseMaps['carto-light']._events.add();
  map._handlers.baselayerchange({ name: 'carto-light', layer: c1.baseMaps['carto-light'] });
  assert.ok(!map._layers.includes(h.group), 'Auto group hidden');
  map.removeLayer(c1.baseMaps['carto-light']); // Leaflet removes it with the old control
  s.w.MC_MAP_CFG = { tiles: { providers: { carto: { key: 'k1' } } } };
  s.w.MC_initTileRegistry(true);
  const c2 = s.stats.controls[s.stats.controls.length - 1];
  assert.notStrictEqual(c2, c1, 'picker rebuilt');
  assert.ok(map.hasLayer(c2.baseMaps['carto-light']), 'the explicit pick is re-selected');
  assert.ok(!map.hasLayer(c2.baseMaps['carto-dark']), 'not the dark default');
  assert.ok(!map._layers.includes(h.group), 'Auto stays off');
  // Unregistered on the next config: falls back to Auto.
  s.w.MC_MAP_CFG = { tiles: { providers: { carto: { enabled: false } } } };
  s.w.MC_initTileRegistry(true);
  const c3 = s.stats.controls[s.stats.controls.length - 1];
  assert.ok(!c3.baseMaps['carto-light'], 'carto disabled');
  assert.ok(map._layers.includes(h.group), 'back to Auto');
  // The saved pick (mc-tile-pinned) outlives the config that dropped it, so a
  // config that registers it again restores it: the pin only ever clears when
  // the user picks Auto.
  assert.strictEqual(s.w.MC_getPinnedTileProvider(), null, 'unregistered pin reads as nothing');
  assert.strictEqual(s.ctx.localStorage.getItem('mc-tile-pinned'), 'carto-light', 'but stays stored');
  s.w.MC_MAP_CFG = null;
  s.w.MC_initTileRegistry(true);
  const c4 = s.stats.controls[s.stats.controls.length - 1];
  assert.ok(map.hasLayer(c4.baseMaps['carto-light']), 'registered again: the saved pick is restored');
  assert.ok(!map._layers.includes(h.group), 'Auto off while a pick is restored');
  // Picking Auto clears the pin; a later rebuild then keeps Auto.
  map._handlers.baselayerchange({ name: '__auto__', layer: h.group });
  assert.strictEqual(s.ctx.localStorage.getItem('mc-tile-pinned'), null, 'Auto clears the pin');
  s.w.MC_initTileRegistry(true);
  const c5 = s.stats.controls[s.stats.controls.length - 1];
  assert.ok(map._layers.includes(h.group), 'Auto after the pin was cleared');
  assert.ok(!map.hasLayer(c5.baseMaps['carto-light']));
});

// Static/secondary maps pass { raster: true }: a vector desc builds its
// raster fallback directly and MapLibre is never touched.
test('MC_buildTileLayer({ raster: true }): vector -> raster fallback without MapLibre; raster unchanged', () => {
  const s = makeSandbox();
  const calls = { vector: 0, load: 0 };
  s.w.MC_buildVectorLayer = () => { calls.vector++; return s.ctx.L.layerGroup(); };
  s.w.MC_loadMapLibre = () => { calls.load++; return new Promise(() => {}); };
  const vDark  = { type: 'dark',  format: 'vector', url: 'https://v.example/style.json', attribution: 'V' };
  const vLight = { type: 'light', format: 'vector', url: 'https://v.example/style.json', attribution: 'V' };
  const a = s.w.MC_buildTileLayer(vDark, { raster: true });
  assert.strictEqual(a.kind, 'tile');
  assert.ok(a._url.indexOf('dark_all') >= 0, 'dark -> carto-dark');
  assert.ok(s.w.MC_buildTileLayer(vLight, { raster: true })._url.indexOf('light_all') >= 0, 'light -> carto-light');
  assert.strictEqual(s.w.MC_buildTileLayer(vLight, { raster: true, fallbackId: 'esri-darkgray-labels' }).kind, 'group', 'fallbackId honoured');
  assert.ok(s.w.MC_buildTileLayer(vDark, { raster: true, fallbackId: 'no-such-id' })._url.indexOf('dark_all') >= 0);
  assert.strictEqual(calls.vector, 0, 'MC_buildVectorLayer never called');
  assert.strictEqual(calls.load, 0, 'MC_loadMapLibre never called');
  assert.strictEqual(s.warns.length, 0, 'no "vector support not loaded" warning');
  // Raster descs build exactly as without the option.
  const entry = s.w.MC_TILE_PROVIDERS['esri-darkgray-labels'];
  const r1 = s.w.MC_buildTileLayer(entry, { raster: true }), r2 = s.w.MC_buildTileLayer(entry);
  assert.strictEqual(r1.kind, 'group');
  assert.deepStrictEqual(r1._layers.map(l => l._url), r2._layers.map(l => l._url));
  assert.strictEqual(JSON.stringify(r1._layers[0]._ctorOpts), JSON.stringify(r2._layers[0]._ctorOpts));
  // Without the option a vector desc still goes to MC_buildVectorLayer.
  s.w.MC_buildTileLayer(vDark);
  assert.strictEqual(calls.vector, 1);
});

test('attach: two maps on one page are independent', () => {
  const s = makeSandbox();
  const m1 = makeMap(), m2 = makeMap();
  const h1 = s.w.MC_attachAutoTiles(m1);
  const h2 = s.w.MC_attachAutoTiles(m2);
  assert.notStrictEqual(h1.group, h2.group);
  assert.notStrictEqual(h1.group._layers[0], h2.group._layers[0]);
  s.w.MC_setDarkTileProvider('positron-dark');
  assert.ok(m1.tilePane.style.filter.indexOf('invert(') >= 0, 'map 1 pane filtered');
  assert.ok(m2.tilePane.style.filter.indexOf('invert(') >= 0, 'map 2 pane filtered');
  h1.destroy();
  s.w.MC_setDarkTileProvider('esri-darkgray-labels');
  assert.strictEqual(h2.group._layers[0].kind, 'group', 'map 2 still syncs');
  assert.strictEqual(h1.group._layers.length, 1);
  assert.strictEqual(h1.group._layers[0].kind, 'tile', 'map 1 left alone');
  assert.ok(m2._layers.includes(h2.group));
});

// The expanded list fits the visible map (above bottomInset) and scrolls;
// Leaflet alone measured the whole map, under the Live replay bar.
test('picker: the expanded list fits above the bottom inset and scrolls', () => {
  const s = makeSandbox();
  const cls = (el) => el._cls || (el._cls = new Set());
  s.ctx.L.DomUtil = {
    hasClass: (el, c) => cls(el).has(c),
    addClass: (el, c) => { cls(el).add(c); },
    removeClass: (el, c) => { cls(el).delete(c); },
  };
  const layers = s.ctx.L.control.layers;
  s.ctx.L.control.layers = function () {
    const c = layers.apply(this, arguments);
    c._container = {};
    c._section = { style: { height: '' }, scrollHeight: 800, getBoundingClientRect: () => ({ top: 176 }) };
    c.expand = function () { cls(c._container).add('leaflet-control-layers-expanded'); return c; };
    return c;
  };
  let bottom = 900;
  s.w.innerHeight = 900;
  const map = makeMap();
  map.getContainer = () => ({ getBoundingClientRect: () => ({ bottom }) });
  const boxes = [];
  const h = s.w.MC_attachAutoTiles(map, { controlPosition: 'topright', bottomInset: (box) => { boxes.push(box); return 80; } });
  const c = s.stats.controls[0];
  const sec = c._section;
  map._handlers.resize();
  assert.strictEqual(sec.style.height, '', 'collapsed: untouched');
  c.expand();
  assert.strictEqual(sec.style.height, (900 - 80 - 176 - 12) + 'px');
  assert.ok(cls(sec).has('leaflet-control-layers-scrollbar'), 'scrolls');
  assert.strictEqual(boxes[0].top, 176, 'bottomInset gets the list box');
  s.w.innerHeight = 1300; bottom = 1300;
  map._handlers.resize();
  assert.strictEqual(sec.style.height, '', 'fits a taller window');
  assert.ok(!cls(sec).has('leaflet-control-layers-scrollbar'));
  bottom = 700; // the map ends above the viewport's bottom
  map._handlers.resize();
  assert.strictEqual(sec.style.height, (700 - 176 - 12) + 'px');
  h.destroy();
  assert.ok(!map._handlers.resize, 'resize handler removed');
});

// One picker fixture for the fit tests: an expandable control with a
// measurable list, an _update Leaflet calls on re-render, and a map whose
// resize handlers are counted.
function fitFixture(opts) {
  opts = opts || {};
  const s = makeSandbox();
  const cls = (el) => el._cls || (el._cls = new Set());
  s.ctx.L.DomUtil = {
    hasClass: (el, c) => cls(el).has(c),
    addClass: (el, c) => { cls(el).add(c); },
    removeClass: (el, c) => { cls(el).delete(c); },
  };
  const layers = s.ctx.L.control.layers;
  const list = { top: 100, left: 600, right: 800 };
  s.ctx.L.control.layers = function () {
    const c = layers.apply(this, arguments);
    c._container = {};
    // Like a browser, clearing the height (list no longer scrolls) resets scrollTop.
    let height = '';
    const sec = {
      scrollHeight: 800, scrollTop: 0,
      getBoundingClientRect: () => ({ top: list.top, left: list.left, right: list.right, height: 1, width: 200 }),
    };
    sec.style = {
      get height() { return height; },
      set height(v) { height = v; if (!v) sec.scrollTop = 0; },
    };
    c._section = sec;
    c.expand = function () { cls(c._container).add('leaflet-control-layers-expanded'); return c; };
    c._update = function () { c.updates = (c.updates || 0) + 1; };
    return c;
  };
  s.w.innerHeight = opts.vh || 900;
  const map = makeMap();
  const resize = [];
  map.on = (e, fn) => { if (e === 'resize') resize.push(fn); else map._handlers[e] = fn; };
  map.off = (e, fn) => { if (e === 'resize') { const i = resize.indexOf(fn); if (i >= 0) resize.splice(i, 1); } else if (map._handlers[e] === fn) delete map._handlers[e]; };
  let bottom = opts.mapBottom || s.w.innerHeight;
  map.getContainer = () => ({ getBoundingClientRect: () => ({ bottom }) });
  if (opts.corners) map._controlCorners = opts.corners;
  const h = s.w.MC_attachAutoTiles(map, { controlPosition: 'topright', bottomInset: opts.bottomInset });
  const ctl = () => s.stats.controls[s.stats.controls.length - 1];
  return { s, map, h, ctl, cls, resize, list, setBottom: (b) => { bottom = b; } };
}

test('MC_coveredBelow: only rects below the list top and in its columns count', () => {
  const s = makeSandbox();
  const list = { top: 100, left: 600, right: 800 };
  const r = (top, left, right, height) => ({ top, left, right, height: height == null ? 40 : height, width: right - left });
  assert.strictEqual(s.w.MC_coveredBelow(list, [], 900), 0, 'nothing below');
  assert.strictEqual(s.w.MC_coveredBelow(list, [r(820, 0, 1000)], 900), 80, 'full-width bar');
  assert.strictEqual(s.w.MC_coveredBelow(list, [r(820, 0, 1000), r(600, 700, 1000)], 900), 300, 'the highest one wins');
  assert.strictEqual(s.w.MC_coveredBelow(list, [r(600, 0, 600)], 900), 0, 'left of the list');
  assert.strictEqual(s.w.MC_coveredBelow(list, [r(600, 800, 1000)], 900), 0, 'right of the list');
  assert.strictEqual(s.w.MC_coveredBelow(list, [r(50, 600, 800)], 900), 0, 'starts above the list (beside it, not below)');
  assert.strictEqual(s.w.MC_coveredBelow(list, [r(600, 600, 800, 0)], 900), 0, 'zero-size box');
});

test('MC_layerListInset skips hidden, invisible and faded-out elements', () => {
  const s = makeSandbox();
  const list = { top: 100, left: 600, right: 800 };
  const el = (top, style) => ({ _style: style || {}, getBoundingClientRect: () => ({ top, left: 0, right: 1000, height: 40, width: 1000 }) });
  s.w.getComputedStyle = (e) => Object.assign({ display: 'block', visibility: 'visible', opacity: '1' }, e._style);
  assert.strictEqual(s.w.MC_layerListInset(list, [el(700)], 900), 200);
  assert.strictEqual(s.w.MC_layerListInset(list, [el(500, { visibility: 'hidden' }), el(700)], 900), 200, 'visibility:hidden');
  assert.strictEqual(s.w.MC_layerListInset(list, [el(500, { opacity: '0' }), el(700)], 900), 200, 'faded out (.live-legend.hidden)');
  assert.strictEqual(s.w.MC_layerListInset(list, [el(500, { display: 'none' })], 900), 0, 'display:none');
  s.w.innerHeight = 1000;
  assert.strictEqual(s.w.MC_layerListInset(list, [el(700)]), 300, 'defaults to window.innerHeight');
});

test('picker: the list keeps clear of the map\'s bottom-corner controls in its columns', () => {
  const box = (top, left, right) => ({ getBoundingClientRect: () => ({ top, left, right, height: 900 - top, width: right - left }) });
  const f = fitFixture({ corners: { bottomleft: { children: [box(500, 0, 300)] }, bottomright: { children: [box(840, 400, 1000)] } } });
  f.ctl().expand();
  assert.strictEqual(f.ctl()._section.style.height, (840 - 100 - 12) + 'px', 'stops above the attribution; the left control is not in its columns');
});

test('picker: the viewport caps the list when the map runs below it', () => {
  const f = fitFixture({ vh: 700, mapBottom: 1200 });
  f.ctl().expand();
  assert.strictEqual(f.ctl()._section.style.height, (700 - 100 - 12) + 'px');
});

test('picker: a re-render while open re-fits and keeps the scroll position', () => {
  let inset = 80;
  const f = fitFixture({ bottomInset: () => inset });
  const c = f.ctl();
  c.expand();
  const sec = c._section;
  assert.strictEqual(sec.style.height, (900 - 80 - 100 - 12) + 'px');
  sec.scrollTop = 250;
  inset = 300;
  c._update();
  assert.strictEqual(c.updates, 1, 'Leaflet\'s _update still runs');
  assert.strictEqual(sec.style.height, (900 - 300 - 100 - 12) + 'px', 're-fit on _update');
  assert.strictEqual(sec.scrollTop, 250, 'scroll position kept');
  f.resize.forEach(fn => fn());
  assert.strictEqual(sec.scrollTop, 250, 'kept across a resize too');
});

test('picker: a very short viewport still gets a small scrolling list', () => {
  const f = fitFixture({ vh: 180, bottomInset: () => 120 });
  const c = f.ctl();
  c.expand();
  assert.strictEqual(c._section.style.height, '60px');
  assert.ok(f.cls(c._section).has('leaflet-control-layers-scrollbar'));
});

test('picker: a fromConfig rebuild leaves one resize handler; destroy removes it', () => {
  const f = fitFixture();
  assert.strictEqual(f.resize.length, 1);
  f.s.w.MC_MAP_CFG = { tiles: { providers: { carto: { key: 'k1' } } } };
  f.s.w.MC_initTileRegistry(true);
  f.s.w.MC_initTileRegistry(true);
  assert.ok(f.s.stats.controls.length >= 3, 'picker rebuilt');
  assert.strictEqual(f.resize.length, 1, 'one handler after rebuilds');
  f.ctl().expand();
  f.resize[0]();
  assert.strictEqual(f.ctl()._section.style.height, (900 - 100 - 12) + 'px', 'the handler fits the new control');
  f.h.destroy();
  assert.strictEqual(f.resize.length, 0);
});

console.log('');
console.log('map layer builder: ' + passed + ' passed, ' + failed + ' failed');
if (failed > 0) process.exit(1);

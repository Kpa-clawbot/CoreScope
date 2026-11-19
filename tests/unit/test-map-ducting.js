/* test-map-ducting.js — tropospheric ducting map overlay (map-ducting.js).
 *
 * Covers, against the real public/map-ducting.js in a vm sandbox with a fake
 * Leaflet, fake CSApi and fake timers, using tests/fixtures/ducting-sample.json:
 *   - MC_ductBin / MC_ductTimeIndex boundaries
 *   - rectangles only for classes 2-4, colour read from --mc-duct-*, elevated
 *     lighter than surface-based, time change restyles without rebuilding
 *   - popup text per class and the 868/915 MHz answers
 *   - pending / error / failed-refresh legend states, legend content
 *   - remove during an in-flight fetch, timer cleanup, refresh cadence
 *   - overlay registry + MC_buildOverlayLayer (factory absent -> null)
 *
 * Runs via: node tests/unit/test-map-ducting.js
 */
'use strict';
const REPO_ROOT = require('path').resolve(__dirname, '..', '..');
const vm = require('vm');
const fs = require('fs');
const path = require('path');
const assert = require('assert');

const SRC = fs.readFileSync(path.join(REPO_ROOT, 'public', 'map-ducting.js'), 'utf8');
const PROV = fs.readFileSync(path.join(REPO_ROOT, 'public', 'map-tile-providers.js'), 'utf8');
const SAMPLE = JSON.parse(fs.readFileSync(path.join(REPO_ROOT, 'tests', 'fixtures', 'ducting-sample.json'), 'utf8'));
const T0 = SAMPLE.times[0], T1 = SAMPLE.times[1], T2 = SAMPLE.times[2];

const queue = [];
function test(name, fn) { queue.push([name, fn]); }

// ── fakes ───────────────────────────────────────────────────────────────────

function makeEl(tag) {
  const listeners = {};
  let text = '';
  const el = {
    tagName: tag, className: '', children: [], attrs: {}, parentNode: null, value: '', listeners,
    appendChild(c) { el.children.push(c); c.parentNode = el; return c; },
    setAttribute(k, v) { el.attrs[k] = String(v); },
    addEventListener(t, f) { (listeners[t] = listeners[t] || []).push(f); },
    removeEventListener(t, f) { listeners[t] = (listeners[t] || []).filter(x => x !== f); },
    fire(t) { (listeners[t] || []).slice().forEach(f => f({ type: t })); },
  };
  Object.defineProperty(el, 'textContent', {
    get() { return text + el.children.map(c => c.textContent).join('\n'); },
    set(v) { text = String(v); el.children = []; },
  });
  return el;
}

function makeLeaflet(stats) {
  const L = {};
  L.setOptions = (o, opts) => { o.options = Object.assign({}, o.options, opts); return o.options; };
  L.Layer = {
    extend(proto) {
      function C(opts) { this.initialize(opts); }
      Object.assign(C.prototype, proto);
      return C;
    },
  };
  L.Control = function (opts) { this.options = opts || {}; };
  L.Control.prototype.addTo = function (map) {
    this._map = map; this._container = this.onAdd(map); map._controls.push(this); return this;
  };
  L.Control.prototype.remove = function () {
    if (this._map._gone) throw new Error('map gone');
    this._map._controls = this._map._controls.filter(c => c !== this); this._removed = true; return this;
  };
  L.canvas = (o) => { const r = { kind: 'canvas', opts: o }; stats.renderers.push(r); return r; };
  L.layerGroup = () => {
    const g = { kind: 'group', layers: [] };
    g.addLayer = (l) => { g.layers.push(l); return g; };
    g.removeLayer = (l) => { g.layers = g.layers.filter(x => x !== l); return g; };
    g.clearLayers = () => { g.layers = []; return g; };
    return g;
  };
  L.rectangle = (bounds, opts) => {
    const r = { kind: 'rect', bounds, opts, style: {}, setStyleCalls: 0 };
    r.setStyle = (s) => { Object.assign(r.style, s); r.setStyleCalls++; return r; };
    r.bindPopup = (f) => { r.popupFn = f; return r; };
    stats.rects.push(r);
    return r;
  };
  return L;
}

function makeMap() {
  const m = { _controls: [], _layers: [], _gone: false };
  m.addLayer = (l) => { m._layers.push(l); return m; };
  m.removeLayer = (l) => { if (m._gone) throw new Error('map gone'); m._layers = m._layers.filter(x => x !== l); return m; };
  return m;
}

// Fake CSApi.fetch: each call is held until the test settles it.
function makeApi() {
  const api = { calls: [] };
  api.fetch = (url, opts) => new Promise((resolve, reject) => {
    api.calls.push({ url, opts, ok(body) { resolve({ ok: true, json: () => Promise.resolve(body) }); }, fail() { reject(new Error('net')); } });
  });
  return api;
}

function makeSandbox(opts) {
  opts = opts || {};
  const stats = { rects: [], renderers: [] };
  const timers = [];
  const cleared = [];
  let nextId = 1;
  const winL = {};
  const vars = { '--mc-duct-lora': ' #aa0001 ', '--mc-duct-uhf': '#bb0002', '--mc-duct-micro': '#cc0003', '--mc-duct-super': '#dd0004' };
  const ctx = {
    console, JSON, Date, Math, Object, Array, String, Number, isFinite, parseInt, Promise,
    document: { createElement: makeEl, documentElement: {} },
    addEventListener(t, f) { (winL[t] = winL[t] || []).push(f); },
    removeEventListener(t, f) { winL[t] = (winL[t] || []).filter(x => x !== f); },
    dispatch(t) { (winL[t] || []).slice().forEach(f => f({ type: t })); },
    getComputedStyle: () => ({ getPropertyValue: (n) => vars[n] || '' }),
    setInterval(fn, ms) { const id = nextId++; timers.push({ id, fn, ms }); return id; },
    clearInterval(id) { cleared.push(id); const i = timers.findIndex(t => t.id === id); if (i >= 0) timers.splice(i, 1); },
  };
  ctx.window = ctx;
  ctx.CSApi = makeApi();
  if (!opts.noLeaflet) ctx.L = makeLeaflet(stats);
  vm.createContext(ctx);
  vm.runInContext(SRC, ctx, { filename: 'map-ducting.js' });
  return { ctx, stats, timers, cleared, api: ctx.CSApi, vars, winL };
}

const tick = () => new Promise(r => setImmediate(r));
const clone = (o) => JSON.parse(JSON.stringify(o));
function fillOf(rect) { return rect.style.fillOpacity > 0 ? rect.style.fillColor : null; }
function find(node, pred, out) {
  out = out || [];
  if (pred(node)) out.push(node);
  (node.children || []).forEach(c => find(c, pred, out));
  return out;
}

// Adds a layer to a fresh map with time frozen at `now`, resolves the fetch.
async function mount(body, now) {
  const s = makeSandbox();
  const realNow = Date.now;
  Date.now = () => (now != null ? now : T1 + 60) * 1000;
  try {
    const map = makeMap();
    const layer = s.ctx.MC_createDuctingLayer({});
    layer.onAdd(map);
    s.api.calls[0].ok(body);
    await tick(); await tick();
    return Object.assign(s, { map, layer });
  } finally { Date.now = realNow; }
}
const legendText = (s) => s.map._controls.map(c => c._container.textContent).join('\n');

// ── pure helpers ────────────────────────────────────────────────────────────

test('MC_ductBin: class and frequency boundaries', () => {
  const { ctx } = makeSandbox();
  const b = ctx.MC_ductBin;
  assert.strictEqual(b(2, 0), 'super');
  assert.strictEqual(b(4, 380), 'lora');
  assert.strictEqual(b(3, 1000), 'lora');
  assert.strictEqual(b(3, 1001), 'uhf');
  assert.strictEqual(b(4, 3000), 'uhf');
  assert.strictEqual(b(4, 3001), 'micro');
  assert.strictEqual(b(1, 500), null);
  assert.strictEqual(b(0, 500), null);
  assert.strictEqual(b(3, 0), null, 'f=0 means no duct');
  assert.strictEqual(b(4, undefined), null);
});

test('MC_ductTimeIndex: before first, between, after last, empty', () => {
  const { ctx } = makeSandbox();
  const ti = ctx.MC_ductTimeIndex;
  assert.strictEqual(ti(SAMPLE.times, T0 - 5), 0);
  assert.strictEqual(ti(SAMPLE.times, T0), 0);
  assert.strictEqual(ti(SAMPLE.times, T1 + 10), 1);
  assert.strictEqual(ti(SAMPLE.times, T2), 2);
  assert.strictEqual(ti(SAMPLE.times, T2 + 99999), 2);
  assert.strictEqual(ti([], 5), -1);
  assert.strictEqual(ti(null, 5), -1);
});

// ── drawing ─────────────────────────────────────────────────────────────────

test('draws a rectangle only for cells that are ever class 2-4, bounds = centre +- step/2', async () => {
  const s = await mount(SAMPLE);
  assert.strictEqual(s.stats.rects.length, 2, 'third cell is only class 0/1');
  const r = s.stats.rects[0];
  assert.deepStrictEqual(JSON.parse(JSON.stringify(r.bounds)), [[51.0, -0.5], [51.5, 0.0]]);
  assert.strictEqual(r.opts.renderer, s.stats.renderers[0], 'shared canvas renderer');
  assert.strictEqual(s.stats.renderers.length, 1);
  assert.strictEqual(s.layer._group.layers.length, 2);
});

test('default time is the latest <= now, else the first; colour comes from the CSS variables', async () => {
  const s = await mount(SAMPLE, T1 + 60);
  assert.strictEqual(s.layer._idx, 1);
  assert.strictEqual(fillOf(s.stats.rects[0]), '#dd0004', 'cell 1 at t1 is class 2');
  const early = await mount(SAMPLE, T0 - 3600);
  assert.strictEqual(early.layer._idx, 0);
  early.vars['--mc-duct-lora'] = '#123456';
  early.layer._setIndex(2);
  assert.strictEqual(fillOf(early.stats.rects[0]), '#123456', 'read at draw time');
});

test('colours: none for class 1, then super, lora, uhf and micro by time', async () => {
  const s = await mount(SAMPLE, T0);
  const [a, b] = s.stats.rects;
  assert.strictEqual(fillOf(a), null, 'class 1 not drawn');
  assert.strictEqual(fillOf(b), '#aa0001', 'f=78 -> lora, value trimmed');
  s.layer._setIndex(1);
  assert.strictEqual(fillOf(a), '#dd0004', 'class 2 -> super');
  assert.strictEqual(fillOf(b), '#bb0002', 'f=1500 -> uhf');
  s.layer._setIndex(2);
  assert.strictEqual(fillOf(a), '#aa0001', 'class 4, f=380 -> lora');
  assert.strictEqual(fillOf(b), '#cc0003', 'f=4200 -> micro');
});

test('elevated duct is lighter than surface-based, same hue', async () => {
  const body = clone(SAMPLE);
  body.cells[0].c = [4, 4, 4]; body.cells[0].f = [380, 380, 380];
  body.cells[1].c = [3, 3, 3]; body.cells[1].f = [380, 380, 380];
  const s = await mount(body);
  const [surf, elev] = s.stats.rects;
  assert.strictEqual(surf.style.fillColor, elev.style.fillColor);
  assert.ok(elev.style.fillOpacity > 0 && elev.style.fillOpacity < surf.style.fillOpacity);
});

test('changing time restyles in place: no new rectangles, renderers or fetches', async () => {
  const s = await mount(SAMPLE, T0);
  const rects = s.stats.rects.length;
  const calls = s.api.calls.length;
  s.layer._slider.value = '2';
  s.layer._slider.fire('input');
  assert.strictEqual(s.stats.rects.length, rects);
  assert.strictEqual(s.stats.renderers.length, 1);
  assert.strictEqual(s.api.calls.length, calls);
  assert.strictEqual(s.layer._idx, 2);
  assert.strictEqual(fillOf(s.stats.rects[0]), '#aa0001');
  assert.ok(/Forecast time:/.test(s.layer._timeLabel.textContent));
  assert.strictEqual(s.layer._slider.max, '2');
});

test('slider is disabled until data arrives', () => {
  const s = makeSandbox();
  const layer = s.ctx.MC_createDuctingLayer({});
  layer.onAdd(makeMap());
  assert.strictEqual(layer._slider.disabled, true);
});

// ── popup ───────────────────────────────────────────────────────────────────

test('popup text for each class and the 868/915 answers', async () => {
  const s = await mount(SAMPLE, T0);
  const pop = (rect, i) => { s.layer._idx = i; return rect.popupFn().textContent; };
  const [a, b] = s.stats.rects;
  const normal = pop(a, 0);
  assert.ok(/Normal/.test(normal) && !/868/.test(normal));
  const sup = pop(a, 1);
  assert.ok(/Super-refraction/.test(sup) && /Can carry 868 MHz: no/.test(sup) && /Can carry 915 MHz: no/.test(sup), sup);
  const sfc = pop(a, 2);
  assert.ok(/Surface-based duct/.test(sfc));
  assert.ok(/Base: 0 m above ground/.test(sfc) && /Top: 100 m above ground/.test(sfc), sfc);
  assert.ok(/Strength: 10\.0 M-units/.test(sfc), sfc);
  assert.ok(/Lowest trapped frequency: 380 MHz/.test(sfc), sfc);
  assert.ok(/Can carry 868 MHz: yes/.test(sfc) && /Can carry 915 MHz: yes/.test(sfc), sfc);
  const elev = pop(b, 1);
  assert.ok(/Elevated duct/.test(elev));
  assert.ok(/Base: 600 m/.test(elev) && /Top: 820 m/.test(elev) && /Strength: 6\.0/.test(elev), elev);
  assert.ok(/Lowest trapped frequency: 1500 MHz/.test(elev));
  assert.ok(/Can carry 868 MHz: no/.test(elev) && /Can carry 915 MHz: no/.test(elev));
});

test('popup: 868 vs 915 split between 868 and 915 MHz; no innerHTML use', async () => {
  const body = clone(SAMPLE);
  body.cells[1].f = [900, 868, 915];
  const s = await mount(body, T0);
  const r = s.stats.rects[1];
  s.layer._idx = 0;
  const t0 = r.popupFn().textContent;
  assert.ok(/868 MHz: no/.test(t0) && /915 MHz: yes/.test(t0), t0);
  s.layer._idx = 1;
  assert.ok(/868 MHz: yes/.test(r.popupFn().textContent));
  assert.ok(!/innerHTML/.test(SRC.replace(/\/\*[\s\S]*?\*\/|\/\/.*$/gm, '')), 'no innerHTML in map-ducting.js');
});

// ── legend & states ─────────────────────────────────────────────────────────

test('legend: bins, model, attribution, updated time, indicator-only note', async () => {
  const s = await mount(SAMPLE);
  const t = legendText(s);
  assert.ok(/868\/915/.test(t) && /1-3 GHz/.test(t) && /above 3 GHz/.test(t) && /Super-refraction/.test(t), t);
  assert.ok(t.includes('NOAA GFS') && t.includes('Open-Meteo.com (CC BY 4.0)'), t);
  assert.ok(/Updated /.test(t));
  assert.ok(t.includes('Indicator only: model levels are ~200 m apart, so thinner ducts are not visible.'));
  const sw = find(s.map._controls[1]._container, n => /mc-duct-swatch-/.test(n.className));
  assert.deepStrictEqual(sw.map(n => n.className.split(' ')[1]), ['mc-duct-swatch-lora', 'mc-duct-swatch-uhf', 'mc-duct-swatch-micro', 'mc-duct-swatch-super']);
  assert.ok(s.map._controls.every(c => c.options.position === 'bottomright'));
});

test('pending status shows the preparing message and draws nothing', async () => {
  const s = await mount({ status: 'pending' });
  const t = legendText(s);
  assert.ok(t.includes('Ducting forecast is being prepared'), t);
  assert.ok(t.includes('Indicator only'));
  assert.strictEqual(s.stats.rects.length, 0);
});

test('error status and fetch failure show unavailable, without error text', async () => {
  const s1 = await mount({ status: 'error', error: 'boom at db.go:12' });
  assert.ok(legendText(s1).includes('Ducting forecast unavailable'));
  assert.ok(!/boom|db\.go/.test(legendText(s1)));
  const s2 = makeSandbox();
  const layer = s2.ctx.MC_createDuctingLayer({});
  const map = makeMap();
  layer.onAdd(map);
  s2.api.calls[0].fail();
  await tick(); await tick();
  assert.ok(map._controls.map(c => c._container.textContent).join('').includes('Ducting forecast unavailable'));
});

test('malformed body counts as unavailable', async () => {
  const s = await mount({ status: 'ok', cells: 'nope' });
  assert.ok(legendText(s).includes('Ducting forecast unavailable'));
});

test('failed or pending refresh keeps the last good data', async () => {
  const s = await mount(SAMPLE, T0);
  assert.strictEqual(s.timers.length, 1);
  assert.strictEqual(s.timers[0].ms, 30 * 60 * 1000);
  s.timers[0].fn();
  assert.strictEqual(s.api.calls.length, 2);
  s.api.calls[1].fail();
  await tick(); await tick();
  assert.strictEqual(s.stats.rects.length, 2);
  assert.strictEqual(fillOf(s.stats.rects[1]), '#aa0001');
  assert.ok(!legendText(s).includes('unavailable'));
  s.timers[0].fn();
  s.api.calls[2].ok({ status: 'pending' });
  await tick(); await tick();
  assert.ok(!legendText(s).includes('being prepared'));
  assert.ok(legendText(s).includes('Updated '));
  assert.strictEqual(s.layer._group.layers.length, 2);
});

test('a good refresh rebuilds cells and keeps a user-picked time', async () => {
  const s = await mount(SAMPLE, T1 + 60);
  s.layer._slider.value = '2';
  s.layer._slider.fire('input');
  s.timers[0].fn();
  s.api.calls[1].ok(clone(SAMPLE));
  await tick(); await tick();
  assert.strictEqual(s.layer._idx, 2);
  assert.strictEqual(s.layer._group.layers.length, 2, 'old rects cleared');
});

// ── teardown ────────────────────────────────────────────────────────────────

test('remove during an in-flight fetch: nothing drawn afterwards, timer cleared, controls removed', async () => {
  const s = makeSandbox();
  const map = makeMap();
  const layer = s.ctx.MC_createDuctingLayer({});
  layer.onAdd(map);
  const group = layer._group, ctls = map._controls.slice();
  assert.strictEqual(s.timers.length, 1);
  layer.onRemove(map);
  assert.strictEqual(s.timers.length, 0);
  assert.strictEqual(s.cleared.length, 1);
  assert.ok(ctls.every(c => c._removed) && map._controls.length === 0);
  assert.ok(!map._layers.includes(group));
  s.api.calls[0].ok(SAMPLE);
  await tick(); await tick();
  assert.strictEqual(s.stats.rects.length, 0);
  assert.strictEqual(map._layers.length, 0);
});

test('remove clears the slider listener; a late failure and a gone map never throw', async () => {
  const s = makeSandbox();
  const map = makeMap();
  const layer = s.ctx.MC_createDuctingLayer({});
  layer.onAdd(map);
  const slider = layer._slider;
  map._gone = true;
  assert.doesNotThrow(() => layer.onRemove(map));
  assert.strictEqual((slider.listeners.input || []).length, 0);
  assert.doesNotThrow(() => layer.onRemove(map), 'double remove');
  s.api.calls[0].fail();
  await tick(); await tick();
});

test('re-adding after remove fetches again and ignores the first fetch', async () => {
  const s = makeSandbox();
  const layer = s.ctx.MC_createDuctingLayer({});
  const map = makeMap();
  layer.onAdd(map);
  layer.onRemove(map);
  layer.onAdd(map);
  s.api.calls[0].ok(SAMPLE);
  await tick(); await tick();
  assert.strictEqual(s.stats.rects.length, 0, 'stale generation ignored');
  s.api.calls[1].ok(SAMPLE);
  await tick(); await tick();
  assert.strictEqual(s.stats.rects.length, 2);
  assert.strictEqual(s.api.calls[0].url, '/api/ducting');
});

// ── theme & missing variables ───────────────────────────────────────────────

test('missing --mc-duct-* variable: cell left transparent, nothing invented, no throw', async () => {
  const s = makeSandbox();
  delete s.vars['--mc-duct-lora'];
  const map = makeMap();
  const layer = s.ctx.MC_createDuctingLayer({});
  layer.onAdd(map);
  s.api.calls[0].ok(SAMPLE);
  await tick(); await tick();
  layer._setIndex(2);
  const [a, b] = s.stats.rects;
  assert.strictEqual(fillOf(a), null, 'lora var missing -> not drawn');
  assert.strictEqual(fillOf(b), '#cc0003', 'other bins unaffected');
  assert.ok(!/magenta|orange|gold|lightblue/.test(SRC.replace(/\/\*[\s\S]*?\*\/|\/\/.*$/gm, '')));
});

test('theme-changed restyles from the new CSS variables; after remove it is inert', async () => {
  const s = await mount(SAMPLE, T0);
  s.layer._setIndex(2);
  assert.strictEqual(fillOf(s.stats.rects[0]), '#aa0001');
  s.vars['--mc-duct-lora'] = '#ff00ff';
  s.ctx.dispatch('theme-changed');
  assert.strictEqual(fillOf(s.stats.rects[0]), '#ff00ff');
  assert.strictEqual((s.winL['theme-changed'] || []).length, 1);
  s.layer.onRemove(s.map);
  assert.strictEqual((s.winL['theme-changed'] || []).length, 0);
  s.vars['--mc-duct-lora'] = '#00ff00';
  assert.doesNotThrow(() => s.ctx.dispatch('theme-changed'));
  assert.strictEqual(fillOf(s.stats.rects[0]), '#ff00ff', 'no restyle after remove');
});

// ── overlay registry ────────────────────────────────────────────────────────

function loadProviders(before) {
  const ctx = {
    console: { log() {}, error() {}, warn() {} },
    JSON, Date, Math, Object, Array, String, Number, Boolean, isFinite,
    document: { documentElement: { getAttribute: () => null }, querySelector: () => null, createElement: makeEl },
  };
  ctx.window = { addEventListener() {}, removeEventListener() {}, dispatchEvent() {}, matchMedia: () => ({ matches: false }), localStorage: null };
  ctx.window.document = ctx.document;
  ctx.L = ctx.window.L = { tileLayer: () => ({ kind: 'tile' }) };
  vm.createContext(ctx);
  if (before) before(ctx.window);
  vm.runInContext(PROV, ctx);
  return ctx.window;
}

test('overlay is not registered without MC_DUCTING_CFG', () => {
  const w = loadProviders();
  assert.ok(!('ducting' in w.MC_TILE_OVERLAYS));
});

test('overlay is registered from MC_DUCTING_CFG and rebuilt when config arrives later', () => {
  const w = loadProviders();
  w.MC_DUCTING_CFG = { model: 'gfs', modelLabel: 'NOAA GFS', stepDeg: 0.5, attribution: 'Weather data by X' };
  w.MC_initTileRegistry(true);
  const o = w.MC_TILE_OVERLAYS.ducting;
  assert.ok(o);
  assert.strictEqual(o.kind, 'ducting');
  assert.strictEqual(o.label, 'Tropospheric ducting');
  assert.strictEqual(o.attribution, 'Weather data by X');
  assert.ok(o.zIndex > w.MC_TILE_OVERLAYS['eox-labels'].zIndex - 1);
  w.MC_initTileRegistry(true);
  assert.strictEqual(w.MC_TILE_OVERLAYS.ducting, o, 'same entry object across rebuilds');
  w.MC_DUCTING_CFG = null;
  w.MC_initTileRegistry(true);
  assert.ok(!('ducting' in w.MC_TILE_OVERLAYS));
});

test('MC_buildOverlayLayer: kind ducting calls the factory; absent factory -> null, no throw', () => {
  const w = loadProviders(x => { x.MC_DUCTING_CFG = { attribution: 'a' }; });
  w.MC_initTileRegistry();
  const entry = w.MC_TILE_OVERLAYS.ducting;
  assert.strictEqual(w.MC_createDuctingLayer, undefined);
  assert.strictEqual(w.MC_buildOverlayLayer(entry, { pane: 'p' }), null);
  const seen = [];
  w.MC_createDuctingLayer = (d) => { seen.push(d); return { kind: 'ducting-layer' }; };
  assert.strictEqual(w.MC_buildOverlayLayer(entry).kind, 'ducting-layer');
  assert.strictEqual(seen[0], entry);
  w.MC_createDuctingLayer = () => { throw new Error('x'); };
  assert.strictEqual(w.MC_buildOverlayLayer(entry), null);
});

test('factory without Leaflet returns null instead of throwing', () => {
  const s = makeSandbox({ noLeaflet: true });
  assert.strictEqual(s.ctx.MC_createDuctingLayer({}), null);
});

// ── run ─────────────────────────────────────────────────────────────────────

(async () => {
  let passed = 0, failed = 0;
  for (const [name, fn] of queue) {
    try { await fn(); passed++; console.log('  ✅ ' + name); }
    catch (e) { failed++; console.log('  ❌ ' + name + ': ' + e.message); }
  }
  console.log('\n' + passed + ' passed, ' + failed + ' failed');
  process.exit(failed ? 1 : 0);
})();

/* test-map-ducting.js — tropospheric ducting map overlay (map-ducting.js).
 *
 * Covers, against the real public/map-ducting.js in a vm sandbox with a fake
 * Leaflet, fake canvas, fake CSApi and fake timers, using
 * tests/fixtures/ducting-sample.json:
 *   - MC_ductTimeIndex boundaries
 *   - the colour bands (MC_ductFieldColor / MC_ductFieldStops): flat colour
 *     at and between stops, transparent for normal values, constant alpha,
 *     missing variables; the legend bar's hard stops
 *   - the field value is the cells' n (mean dN/dh over the lowest 1.5 km);
 *     n 0 or missing (an older server) is no data
 *   - the lattice, Mercator row -> latitude, Catmull-Rom bicubic sampling
 *     (exact at cell centres, continuous, bilinear next to missing data, the
 *     separable renderer equal to a naive 4x4 evaluation) and the fade to
 *     transparent at cells without data
 *   - the field image layer: pane, bounds, pixels, redrawn (not rebuilt) on
 *     time and theme change, resized/rebounded on refresh, removed on teardown
 *   - one invisible rectangle per cell with data; dashed LoRa outline only
 *     for ducts with 0 < fmin <= 1000 MHz
 *   - the forecast area's dotted outline (merged boundary runs, antimeridian
 *     seam, one layer per build, colour from --mc-duct-edge) and the wider
 *     fade at the area's outer edge
 *   - popup text per class, the 1.5 km gradient line and the 868/915 MHz answers
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
// One colour per stop (-44, -48, -54, -58, -62, -70, -80, -90 N/km).
const STOP_RGB = [[128, 0, 128], [0, 0, 255], [0, 200, 200], [0, 160, 0], [240, 240, 0], [250, 150, 0], [230, 0, 0], [250, 0, 200]];
const STOP_N = [-44, -48, -54, -58, -62, -70, -80, -90];
const STOP_VARS = {};
STOP_RGB.forEach((c, k) => { STOP_VARS['--mc-duct-s' + (k + 1)] = '#' + c.map(v => v.toString(16).padStart(2, '0')).join(''); });
STOP_VARS['--mc-duct-s3'] = ' rgb(0, 200, 200) ';   // rgb() form too
const STOPS = STOP_N.map((g, k) => ({ g, rgb: STOP_RGB[k] }));

const queue = [];
function test(name, fn) { queue.push([name, fn]); }

// ── fakes ───────────────────────────────────────────────────────────────────

function makeEl(tag) {
  const listeners = {};
  let text = '';
  const el = {
    tagName: tag, className: '', children: [], attrs: {}, parentNode: null, value: '', listeners, style: {},
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
  if (tag === 'canvas') {
    el.width = 300; el.height = 150; el.puts = 0; el.img = null;
    const ctx2d = {
      createImageData: (w, h) => ({ width: w, height: h, data: new Uint8ClampedArray(w * h * 4) }),
      putImageData: (img) => { el.img = img; el.puts++; },
    };
    el.getContext = (k) => (k === '2d' ? ctx2d : null);
  }
  return el;
}

// RGBA of the field canvas at pixel (x, y).
function px(canvas, x, y) {
  const o = (y * canvas.img.width + x) * 4, d = canvas.img.data;
  return [d[o], d[o + 1], d[o + 2], d[o + 3]];
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
    this._map = map; this._container = this.onAdd(map); this._container.parentNode = map.corner; map._controls.push(this); return this;
  };
  L.Control.prototype.remove = function () {
    if (this._map._gone) throw new Error('map gone');
    this._map._controls = this._map._controls.filter(c => c !== this); this._removed = true; return this;
  };
  L.extend = Object.assign;
  L.latLngBounds = (b) => ({ b });
  L.canvas = (o) => { const r = { kind: 'canvas', opts: o }; stats.renderers.push(r); return r; };
  L.ImageOverlay = {
    extend(proto) {
      function F(url, bounds, opts) { this._url = url; this._bounds = bounds; this.options = opts; this.setBoundsCalls = 0; stats.fields.push(this); }
      Object.assign(F.prototype, proto);
      F.prototype.setBounds = function (b) { this._bounds = b; this.setBoundsCalls++; return this; };
      return F;
    },
  };
  L.layerGroup = () => {
    const g = { kind: 'group', layers: [] };
    g.addLayer = (l) => { g.layers.push(l); return g; };
    g.removeLayer = (l) => { g.layers = g.layers.filter(x => x !== l); return g; };
    g.clearLayers = () => { g.layers = []; return g; };
    return g;
  };
  L.polyline = (latlngs, opts) => {
    const l = { kind: 'polyline', latlngs, opts, style: {}, setStyleCalls: 0 };
    l.setStyle = (st) => { Object.assign(l.style, st); l.setStyleCalls++; return l; };
    stats.lines.push(l);
    return l;
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
  const m = { _controls: [], _layers: [], _gone: false, _on: {}, _panes: {} };
  m.getPane = (n) => m._panes[n];
  m.createPane = (n) => { m._panes[n] = { style: {} }; return m._panes[n]; };
  m.on = (ev, f) => { ev.split(' ').forEach(e => { (m._on[e] = m._on[e] || []).push(f); }); return m; };
  m.off = (ev, f) => { ev.split(' ').forEach(e => { m._on[e] = (m._on[e] || []).filter(x => x !== f); }); return m; };
  m.fire = (e) => (m._on[e] || []).slice().forEach(f => f());
  m.corner = { style: { marginBottom: '' }, getBoundingClientRect: () => ({ left: 0, right: 270 }) };
  m.attr = { offsetHeight: 36, rect: { left: 0 }, getBoundingClientRect() { return this.rect; } };
  m.attributionControl = { getContainer: () => m.attr };
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
  const stats = { rects: [], renderers: [], fields: [], lines: [] };
  const timers = [];
  const cleared = [];
  let nextId = 1;
  const winL = {};
  const vars = Object.assign({ '--mc-duct-lora': ' #0a0b0c ', '--mc-duct-edge': ' rgba(1, 2, 3, 0.8) ', '--mc-duct-alpha': '0.5' }, STOP_VARS);
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
const loraOf = (rect) => (rect.style.stroke ? rect.style.color + '/' + rect.style.dashArray : null);
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

// ── colour bands ────────────────────────────────────────────────────────────

test('MC_ductFieldColor: each stop starts its band; flat colour down to the next stop, alpha 1', () => {
  const { ctx } = makeSandbox();
  const f = (g) => Array.from(ctx.MC_ductFieldColor(g, STOPS));
  STOPS.forEach((st, k) => {
    assert.deepStrictEqual(f(st.g), st.rgb.concat(1), 'at the stop g=' + st.g);
    const next = k + 1 < STOPS.length ? STOPS[k + 1].g : st.g - 1000;
    // Everywhere inside the band: the same colour, no blending toward the next.
    for (const t of [0.01, 0.25, 0.5, 0.75, 0.99]) assert.deepStrictEqual(f(st.g + (next - st.g) * t), st.rgb.concat(1), 'inside band ' + k + ' at ' + t);
    if (k + 1 < STOPS.length) {
      assert.deepStrictEqual(f(next + 1e-9), st.rgb.concat(1), 'just above the next stop');
      assert.deepStrictEqual(f(next), STOPS[k + 1].rgb.concat(1), 'the next stop belongs to the next band');
    }
  });
  assert.deepStrictEqual(f(-66.5), STOP_RGB[4].concat(1), 'between -62 and -70: the -62 colour, not a mix');
  assert.deepStrictEqual(f(-51), STOP_RGB[1].concat(1), 'between -48 and -54: the -48 colour');
  assert.deepStrictEqual(f(-7945), STOP_RGB[7].concat(1), 'beyond the last stop');
});

test('MC_ductFieldColor: transparent above -44 (normal), NaN or without stops; alpha never anything but 0 or 1', () => {
  const { ctx } = makeSandbox();
  const f = (g) => Array.from(ctx.MC_ductFieldColor(g, STOPS));
  for (const g of [100, 0, -30, -40, -43, -43.999]) assert.strictEqual(f(g)[3], 0, 'n=' + g);
  assert.deepStrictEqual(f(-44), STOP_RGB[0].concat(1), 'the colour starts at -44, at full alpha');
  for (let g = -44; g > -800; g -= 1.7) assert.strictEqual(f(g)[3], 1, 'constant alpha at ' + g);
  assert.strictEqual(f(NaN)[3], 0);
  assert.strictEqual(Array.from(ctx.MC_ductFieldColor(-300, []))[3], 0, 'no stops -> transparent');
  assert.strictEqual(Array.from(ctx.MC_ductFieldColor(-300, null))[3], 0);
});

test('MC_ductFieldStops: reads hex and rgb() variables, drops missing or unreadable ones', () => {
  const s = makeSandbox();
  const st = JSON.parse(JSON.stringify(s.ctx.MC_ductFieldStops()));
  assert.deepStrictEqual(st, STOPS);
  delete s.vars['--mc-duct-s2'];
  s.vars['--mc-duct-s5'] = 'tomato';
  s.vars['--mc-duct-s6'] = '#f90';
  const st2 = JSON.parse(JSON.stringify(s.ctx.MC_ductFieldStops()));
  assert.deepStrictEqual(st2.map(x => x.g), [-44, -54, -58, -70, -80, -90]);
  assert.deepStrictEqual(st2[3].rgb, [255, 153, 0], '#rgb expanded');
  // Without -48 the -44 band runs on to -54; without -62 the -58 one to -70.
  assert.deepStrictEqual(Array.from(s.ctx.MC_ductFieldColor(-50.5, st2)), STOP_RGB[0].concat(1));
  assert.deepStrictEqual(Array.from(s.ctx.MC_ductFieldColor(-65, st2)), STOP_RGB[3].concat(1));
  // Without -44 nothing is coloured above the first stop that is there.
  const st3 = st2.slice(1);
  assert.strictEqual(Array.from(s.ctx.MC_ductFieldColor(-50, st3))[3], 0);
  assert.deepStrictEqual(Array.from(s.ctx.MC_ductFieldColor(-54, st3)), [0, 200, 200, 1]);
});

// ── lattice, projection, sampling ───────────────────────────────────────────

test('MC_ductRowLat: rows follow Web Mercator, not linear latitude', () => {
  const { ctx } = makeSandbox();
  const lat = ctx.MC_ductRowLat;
  assert.ok(Math.abs(lat(0, 400, 60, 0) - 60) < 1e-9);
  assert.ok(Math.abs(lat(400, 400, 60, 0)) < 1e-9);
  const merc = (d) => Math.log(Math.tan(Math.PI / 4 + d * Math.PI / 360));
  const mid = lat(200, 400, 60, 0);
  assert.ok(Math.abs(merc(mid) - merc(60) / 2) < 1e-12, 'mid row is the Mercator midpoint');
  assert.ok(mid > 34 && mid < 36, 'and not 30: ' + mid);
  assert.ok(Math.abs(lat(100, 400, 35.5, 25) - lat(100, 400, 35.5, 25)) < 1e-12);
});

test('MC_ductGrid: lattice of the cells with data, outer edges as bounds, image size', () => {
  const { ctx } = makeSandbox();
  const g = ctx.MC_ductGrid(SAMPLE.cells, 0.5);
  assert.strictEqual(g.rows, 2);
  assert.strictEqual(g.cols, 2);
  assert.deepStrictEqual([g.south, g.west, g.north, g.east], [51, -0.5, 52, 0.5]);
  assert.deepStrictEqual(Array.from(g.slot), [0, 1, 2], 'row-major from the south-west');
  assert.strictEqual(g.w, 96, '48 px per cell');
  assert.ok(g.h > 96 && g.h < 180, 'Mercator stretches the height at 51N: ' + g.h);
  const none = clone(SAMPLE.cells);
  none.forEach(c => { c.c = [0, 0, 0]; });
  assert.strictEqual(ctx.MC_ductGrid(none, 0.5), null, 'no data at any hour');
  assert.strictEqual(ctx.MC_ductGrid(SAMPLE.cells, 0), null);
  const only = clone(SAMPLE.cells);
  only[2].c = [0, 0, 0];
  assert.strictEqual(ctx.MC_ductGrid(only, 0.5).slot[2], -1, 'a cell never with data is off the lattice');
});

test('MC_ductGrid: several far-apart regions share one image, capped at 2048 px', () => {
  const { ctx } = makeSandbox();
  const cell = (lat, lon) => ({ lat, lon, c: [1], f: [0], b: [0], t: [0], dm: [0], n: [-50] });
  const cells = [cell(-33.75, 151.25), cell(51.25, -0.25), cell(30.25, -95.25), cell(64.25, -147.75)];
  const g = ctx.MC_ductGrid(cells, 0.5);
  assert.ok(g.w <= 2048 && g.h <= 2048 && Math.max(g.w, g.h) === 2048, g.w + 'x' + g.h);
  assert.strictEqual(g.west, -148);
  assert.strictEqual(g.east, 151.5);
  // Lattice indices tolerate float noise in the centres.
  const n = ctx.MC_ductGrid([cell(30.25 + 1e-9, -95.25 - 1e-9), cell(30.75, -94.75)], 0.5);
  assert.strictEqual(n.rows, 2);
  assert.strictEqual(n.cols, 2);
});

test('MC_ductSample: value at centres, bilinear between, fades out at cells without data', () => {
  const { ctx } = makeSandbox();
  const cells = clone(SAMPLE.cells);
  const g = ctx.MC_ductGrid(cells, 0.5);
  const v = ctx.MC_ductGridValues(g, cells, 2);           // n: a=-66, b=-55, c=-40, (51.75,0.25) none
  assert.ok(Number.isNaN(v[3]), 'missing slot is NaN');
  const at = (lat, lon) => ctx.MC_ductSample(g, v, lat, lon);
  assert.strictEqual(at(51.25, -0.25).g, -66);
  assert.strictEqual(at(51.25, -0.25).a, 0.5, 'centre of an outer-edge cell: half faded');
  assert.strictEqual(at(51.25, 0).g, -60.5, 'halfway between a and b');
  assert.strictEqual(at(51.25, 0).a, 0.5);
  assert.strictEqual(at(51.25, 0.5).a, 0, 'b east edge: no neighbour, faded out');
  assert.strictEqual(at(51, -0.25).a, 0, 'south edge');
  const q = at(51.25, 0.375).a;
  assert.ok(q > 0 && q < 1, 'fading within half a cell: ' + q);
  assert.strictEqual(at(51.75, 0.25).a, 0, 'centre of the missing cell');
  assert.ok(Math.abs(at(51.5, 0).g - (-66 - 55 - 40) / 3) < 1e-4, 'mean over the cells that have data');
  const v0 = ctx.MC_ductGridValues(g, cells, 0);           // cell c is class 0 at t0
  assert.ok(Number.isNaN(v0[2]), 'class 0 at this hour is no data');
});

test('MC_ductGridValues: the field is n, not g; n 0, missing or not a number is no data', () => {
  const { ctx } = makeSandbox();
  const cells = clone(SAMPLE.cells);
  const g = ctx.MC_ductGrid(cells, 0.5);
  assert.deepStrictEqual(Array.from(ctx.MC_ductGridValues(g, cells, 1)).slice(0, 3), [-46, -57, -36], 'n at t1, not g (-96, -170, -38)');
  cells[0].n[1] = 0;                                       // 0 = no data at this hour
  cells[1].n = null;
  delete cells[2].n;
  const v = ctx.MC_ductGridValues(g, cells, 1);
  assert.ok([0, 1, 2, 3].every(k => Number.isNaN(v[k])), 'all no data: ' + Array.from(v));
  cells[1].n = [-50, 'x', -52];
  const w = ctx.MC_ductGridValues(g, cells, 1);
  assert.ok(Number.isNaN(w[1]), 'not a number');
  assert.strictEqual(ctx.MC_ductGridValues(g, cells, 2)[1], -52);
  assert.ok(Number.isNaN(ctx.MC_ductGridValues(g, cells, 7)[1]), 'past the end of n');
  assert.ok(Number.isNaN(ctx.MC_ductSample(g, v, 51.25, -0.25).g), 'no field anywhere');
});

// Catmull-Rom, written out the plain way: 16 lookups per point.
function crW(t) {
  const t2 = t * t, t3 = t2 * t;
  return [0.5 * (-t3 + 2 * t2 - t), 0.5 * (3 * t3 - 5 * t2 + 2), 0.5 * (-3 * t3 + 4 * t2 + t), 0.5 * (t3 - t2)];
}
function naiveCubic(vals, rows, cols, v, u) {
  const i = Math.floor(v), j = Math.floor(u), wv = crW(v - i), wu = crW(u - j);
  const cl = (x, n) => Math.max(0, Math.min(n - 1, x));
  let s = 0;
  for (let a = 0; a < 4; a++) for (let b = 0; b < 4; b++) s += wv[a] * wu[b] * vals[cl(i - 1 + a, rows) * cols + cl(j - 1 + b, cols)];
  return s;
}
// A filled rows x cols block with a smooth but not polynomial field g(lat, lon).
function wavy(rows, cols, step, lat0, lon0) {
  const cells = [];
  for (let r = 0; r < rows; r++) {
    for (let c = 0; c < cols; c++) {
      const g = -150 + 90 * Math.sin(r * 0.9) * Math.cos(c * 0.7) - 4 * r * c;
      cells.push({ lat: lat0 + r * step, lon: lon0 + c * step, c: [3], f: [500], b: [0], t: [0], dm: [0], n: [g] });
    }
  }
  return cells;
}

test('MC_ductSample: bicubic equals the data at every cell centre and matches a naive 4x4 Catmull-Rom between', () => {
  const { ctx } = makeSandbox();
  const cells = wavy(6, 7, 1, 20.5, -90.5);
  const g = ctx.MC_ductGrid(cells, 1);
  const v = ctx.MC_ductGridValues(g, cells, 0);
  cells.forEach((c) => assert.strictEqual(ctx.MC_ductSample(g, v, c.lat, c.lon).g, v[g.slot[cells.indexOf(c)]], 'centre ' + c.lat + ',' + c.lon));
  for (let lat = 20.01; lat < 26; lat += 0.173) {
    for (let lon = -90.99; lon < -84; lon += 0.191) {
      const want = naiveCubic(v, g.rows, g.cols, lat - 0.5 - g.i0, lon - 0.5 - g.j0);
      const got = ctx.MC_ductSample(g, v, lat, lon).g;
      assert.ok(Math.abs(got - want) < 1e-9, `${lat},${lon}: ${got} vs naive ${want}`);
    }
  }
  // Not bilinear: halfway between two centres the cubic bends with the neighbours.
  const bil = (v[2 * 7 + 2] + v[2 * 7 + 3]) / 2;
  assert.ok(Math.abs(ctx.MC_ductSample(g, v, 23, -87).g - bil) > 0.5, 'differs from bilinear');
});

test('MC_ductSample: bicubic is continuous across cell boundaries and centres', () => {
  const { ctx } = makeSandbox();
  const cells = wavy(6, 7, 1, 20.5, -90.5);
  const g = ctx.MC_ductGrid(cells, 1);
  const v = ctx.MC_ductGridValues(g, cells, 0);
  const at = (lat, lon) => ctx.MC_ductSample(g, v, lat, lon).g;
  const eps = 1e-7;
  for (let k = 0; k < 7; k++) {
    const lon = -90.5 + k;                                 // a lattice column: interval boundary
    for (const lat of [21.3, 23.5, 24.9]) {
      assert.ok(Math.abs(at(lat, lon - eps) - at(lat, lon + eps)) < 1e-4, `jump at lon ${lon}`);
    }
  }
  for (let r = 0; r < 6; r++) {
    const lat = 20.5 + r;
    assert.ok(Math.abs(at(lat - eps, -87.3) - at(lat + eps, -87.3)) < 1e-4, `jump at lat ${lat}`);
  }
  // A fine walk never jumps by more than the slope allows.
  let prev = at(23.2, -90.9);
  for (let lon = -90.9; lon < -84.1; lon += 0.001) {
    const cur = at(23.2, lon);
    assert.ok(Math.abs(cur - prev) < 1, `step at ${lon}: ${prev} -> ${cur}`);
    prev = cur;
  }
});

test('MC_ductSample: next to a cell without data the value falls back to bilinear', () => {
  const { ctx } = makeSandbox();
  const cells = wavy(6, 7, 1, 20.5, -90.5);
  const g = ctx.MC_ductGrid(cells, 1);
  const v = ctx.MC_ductGridValues(g, cells, 0);
  v[3 * 7 + 3] = NaN;                                      // no data at (23.5, -87.5) this hour
  const bilinear = (lat, lon) => {                        // over the slots that have data
    const vv = lat - 0.5 - g.i0, uu = lon - 0.5 - g.j0, i = Math.floor(vv), j = Math.floor(uu), fv = vv - i, fu = uu - j;
    let p = 0, s = 0;
    for (let a = 0; a < 2; a++) for (let b = 0; b < 2; b++) {
      const x = v[(i + a) * 7 + j + b];
      if (Number.isNaN(x)) continue;
      const w = (a ? fv : 1 - fv) * (b ? fu : 1 - fu);
      p += w; s += w * x;
    }
    return s / p;
  };
  // (24.2, -86.3): its 4x4 reaches the missing slot; (21.2, -85.2) does not.
  const near = ctx.MC_ductSample(g, v, 24.2, -86.3).g;
  assert.ok(Math.abs(near - bilinear(24.2, -86.3)) < 1e-9, near + ' vs bilinear ' + bilinear(24.2, -86.3));
  const far = ctx.MC_ductSample(g, v, 21.2, -85.2).g;
  assert.ok(Math.abs(far - naiveCubic(v, g.rows, g.cols, 21.2 - 0.5 - g.i0, -85.2 - 0.5 - g.j0)) < 1e-9, 'bicubic away from it');
  assert.ok(Math.abs(far - bilinear(21.2, -85.2)) > 1e-3, 'and that is not bilinear');
  assert.ok(Number.isFinite(near) && Number.isFinite(ctx.MC_ductSample(g, v, 23.5, -87.2).g), 'no NaN leaks while a neighbour has data');
});

test('MC_ductFieldValues: the separable renderer equals sample() and the naive 4x4 at every pixel', () => {
  const { ctx } = makeSandbox();
  const cells = wavy(5, 6, 1, 20.5, -90.5).filter(c => !(c.lat === 22.5 && c.lon === -88.5));   // with a hole
  const g = ctx.MC_ductGrid(cells, 1);
  const v = ctx.MC_ductGridValues(g, cells, 0);
  const fv = ctx.MC_ductFieldValues(g, v);
  assert.strictEqual(fv.g.length, g.w * g.h);
  let cubic = 0, fallback = 0, covered = 0;
  for (let y = 0; y < g.h; y += 3) {
    const lat = ctx.MC_ductRowLat(y + 0.5, g.h, g.north, g.south);
    for (let x = 0; x < g.w; x += 3) {
      const lon = g.west + (x + 0.5) / g.w * (g.east - g.west);
      const s = ctx.MC_ductSample(g, v, lat, lon), k = y * g.w + x;
      assert.strictEqual(fv.g[k], Math.fround(s.g), `g at ${x},${y}`);
      assert.strictEqual(fv.a[k], Math.fround(s.a), `a at ${x},${y}`);
      if (s.a > 0) covered++;
      const naive = naiveCubic(v, g.rows, g.cols, lat - 0.5 - g.i0, lon - 0.5 - g.j0);
      if (Number.isNaN(naive)) fallback++;
      else { cubic++; assert.ok(Math.abs(s.g - naive) < 1e-9, `${x},${y}: ${s.g} vs naive ${naive}`); }
    }
  }
  assert.ok(cubic > 100 && fallback > 100 && covered > 100, `both paths exercised: ${cubic} cubic, ${fallback} fallback`);
});

// A filled block of cells (rows x cols, lower-left centre lat0/lon0).
function block(rows, cols, lat0, lon0, step) {
  step = step || 0.5;
  const cells = [];
  for (let i = 0; i < rows; i++) for (let j = 0; j < cols; j++) {
    cells.push({ lat: lat0 + i * step, lon: lon0 + j * step, c: [1], f: [0], b: [0], t: [0], dm: [0], n: [-50] });
  }
  return cells;
}
const segs = (ctx, cells) => JSON.parse(JSON.stringify(ctx.MC_ductEdges(ctx.MC_ductGrid(cells, 0.5)))).map(sg => sg.map(p => p.join(',')).sort().join(' | ')).sort();

test('MC_ductEdges: a filled 3x4 block is 4 merged runs on its outer boundary', () => {
  const { ctx } = makeSandbox();
  // rows 3 (lat 10.25..11.25), cols 4 (lon 20.25..21.75): edges 10..11.5, 20..22
  assert.deepStrictEqual(segs(ctx, block(3, 4, 10.25, 20.25)), [
    '10,20 | 10,22', '11.5,20 | 11.5,22', '10,20 | 11.5,20', '10,22 | 11.5,22',
  ].map(x => x.split(' | ').sort().join(' | ')).sort());
});

test('MC_ductEdges: an L-shape has 6 runs; two separate blocks have two outlines', () => {
  const { ctx } = makeSandbox();
  // L: 2x2 block plus 2 extra cells to the east on the bottom row.
  const l = block(2, 2, 10.25, 20.25).concat(block(1, 2, 10.25, 21.25));
  assert.strictEqual(ctx.MC_ductEdges(ctx.MC_ductGrid(l, 0.5)).length, 6);
  const two = block(2, 2, 10.25, 20.25).concat(block(2, 2, 30.25, 40.25));
  const e = ctx.MC_ductEdges(ctx.MC_ductGrid(two, 0.5));
  assert.strictEqual(e.length, 8, '4 + 4');
  assert.ok(e.every(sg => Math.abs(sg[0][0] - sg[1][0]) <= 1 && Math.abs(sg[0][1] - sg[1][1]) <= 1), 'no run bridges the blocks');
  assert.strictEqual(ctx.MC_ductEdges(null).length, 0);
});

test('MC_ductEdges: a hole inside the area is outlined too', () => {
  const { ctx } = makeSandbox();
  const cells = block(3, 3, 10.25, 20.25).filter(c => !(c.lat === 10.75 && c.lon === 20.75));
  assert.strictEqual(ctx.MC_ductEdges(ctx.MC_ductGrid(cells, 0.5)).length, 8, '4 outer + 4 around the hole');
});

test('MC_ductEdges: a block across the antimeridian draws no line across the world and no seam line', () => {
  const { ctx } = makeSandbox();
  const cells = block(2, 1, 10.25, 179.75).concat(block(2, 1, 10.25, -179.75));
  const e = ctx.MC_ductEdges(ctx.MC_ductGrid(cells, 0.5));
  assert.ok(e.every(sg => Math.abs(sg[0][1] - sg[1][1]) <= 0.5 + 1e-9), 'no run spans more than a cell in lon');
  assert.ok(!e.some(sg => Math.abs(Math.abs(sg[0][1]) - 180) < 1e-9 && sg[0][1] === sg[1][1]), 'no line at lon +-180 where the cells meet');
  assert.strictEqual(e.length, 6, 'top and bottom of each half, west edge of the east half, east edge of the west half');
  // Without the other half the seam IS an edge.
  assert.strictEqual(ctx.MC_ductEdges(ctx.MC_ductGrid(block(2, 1, 10.25, 179.75), 0.5)).length, 4);
});

test('fade at the area\'s outer edge spans a cell; interior cells next to filled neighbours are unaffected', () => {
  const { ctx } = makeSandbox();
  const cells = block(5, 5, 10.25, 20.25);        // lon 20..22.5, lat 10..12.5
  const g = ctx.MC_ductGrid(cells, 0.5);
  const v = ctx.MC_ductGridValues(g, cells, 0);
  const a = (lat, lon) => ctx.MC_ductSample(g, v, lat, lon).a;
  assert.strictEqual(a(11.25, 20), 0, 'at the edge');
  assert.ok(Math.abs(a(11.25, 20.25) - 0.5) < 1e-9, 'half a cell in: ' + a(11.25, 20.25));
  assert.ok(a(11.25, 20.125) > 0 && a(11.25, 20.125) < a(11.25, 20.25), 'rising within the outer half cell');
  assert.ok(a(11.25, 20.5) > 0.5 && a(11.25, 20.5) < 1, 'still fading between the first and second cell');
  assert.strictEqual(a(11.25, 20.75), 1, 'second cell in: full');
  assert.strictEqual(a(11.25, 21.25), 1, 'interior');
  assert.strictEqual(a(11.25, 21.75), 1, 'interior, next to the outer ring');
  // A hole in an otherwise filled area: the cells next to it are still the interior's... a hole is an edge too.
  const holed = cells.filter(c => !(c.lat === 11.25 && c.lon === 21.25));
  const gh = ctx.MC_ductGrid(holed, 0.5);
  assert.strictEqual(gh.soft[(1) * 5 + 0], 1);
});

// ── drawing ─────────────────────────────────────────────────────────────────

// Pixel nearest (lat, lon) in the field canvas.
function pixelAt(s, lat, lon) {
  const g = s.layer._grid, cv = s.layer._canvas;
  const merc = (d) => Math.log(Math.tan(Math.PI / 4 + d * Math.PI / 360));
  const x = Math.floor((lon - g.west) / (g.east - g.west) * cv.width);
  const y = Math.floor((merc(g.north) - merc(lat)) / (merc(g.north) - merc(g.south)) * cv.height);
  return { x, y, rgba: px(cv, x, y) };
}

test('one invisible rectangle per cell with data (class >= 1); bounds = centre +- step/2', async () => {
  const s = await mount(SAMPLE);
  assert.strictEqual(s.stats.rects.length, 3, 'third cell is class 0/1/1: has data');
  const r = s.stats.rects[0];
  assert.deepStrictEqual(JSON.parse(JSON.stringify(r.bounds)), [[51.0, -0.5], [51.5, 0.0]]);
  assert.strictEqual(r.opts.renderer, s.stats.renderers[0], 'shared canvas renderer');
  assert.strictEqual(r.opts.fillOpacity, 0);
  assert.strictEqual(r.opts.stroke, false);
  assert.ok(s.stats.rects.every(x => x._mcCell && x.style.fillOpacity !== 1));
  assert.strictEqual(s.stats.renderers.length, 1);
  assert.strictEqual(s.layer._group.layers.length, 4, '3 rects + the edge outline');
});

test('edge outline: one non-interactive dotted polyline per build, in the group, on the shared renderer', async () => {
  const s = await mount(SAMPLE, T0);
  assert.strictEqual(s.stats.lines.length, 1);
  const l = s.stats.lines[0];
  assert.strictEqual(l._mcEdge, true);
  assert.ok(s.layer._group.layers.includes(l), 'removed with the group');
  assert.strictEqual(l.opts.renderer, s.stats.renderers[0]);
  assert.strictEqual(l.opts.interactive, false);
  assert.strictEqual(l.opts.weight, 1);
  assert.strictEqual(l.opts.dashArray, '1 5');
  assert.notStrictEqual(l.opts.dashArray, '4 3', 'not the LoRa dash');
  assert.strictEqual(l.opts.color, 'rgba(1, 2, 3, 0.8)', 'from --mc-duct-edge, trimmed');
  assert.ok(l.opts.opacity > 0 && l.opts.opacity < 1);
  assert.ok(l.latlngs.length >= 4, 'merged runs');
  // Not rebuilt on slider moves or theme changes; restyled only when the colour changes.
  s.layer._setIndex(2);
  s.ctx.dispatch('theme-changed');
  assert.strictEqual(s.stats.lines.length, 1);
  assert.strictEqual(l.setStyleCalls, 0, 'same colour: no restyle');
  s.vars['--mc-duct-edge'] = '#abcdef';
  s.ctx.dispatch('theme-changed');
  assert.strictEqual(s.stats.lines.length, 1);
  assert.strictEqual(l.style.color, '#abcdef');
  assert.strictEqual(l.setStyleCalls, 1);
  // A rebuild (refresh) makes a fresh one.
  s.layer._build();
  assert.strictEqual(s.stats.lines.length, 2);
  assert.strictEqual(s.layer._group.layers.filter(x => x._mcEdge).length, 1);
  assert.strictEqual(s.stats.lines[1].opts.color, '#abcdef');
  const group = s.layer._group;
  s.layer.onRemove(s.map);
  assert.strictEqual(s.layer._edge, null);
  assert.ok(!s.map._layers.includes(group), 'group (and the edge) gone from the map');
});

test('edge outline: no colour variable -> stroke off, never invented', async () => {
  const s = makeSandbox();
  s.vars['--mc-duct-edge'] = '';
  const map = makeMap();
  const layer = s.ctx.MC_createDuctingLayer({});
  layer.onAdd(map);
  s.api.calls[0].ok(clone(SAMPLE));
  await tick(); await tick();
  assert.strictEqual(s.stats.lines[0].opts.stroke, false);
});

test('legend: dotted-line row for the forecast area edge', async () => {
  const s = await mount(SAMPLE);
  assert.ok(legendText(s).includes('Dotted line: edge of the forecast area (no forecast beyond it)'));
  assert.strictEqual(find(s.map._controls[1]._container, n => /mc-duct-swatch-edge/.test(n.className || '')).length, 1);
});

test('field: one image layer in its own pane below markers, over the cells\' outer edges', async () => {
  const s = await mount(SAMPLE);
  assert.strictEqual(s.stats.fields.length, 1);
  const f = s.stats.fields[0];
  assert.ok(s.map._layers.includes(f), 'added to the map');
  assert.strictEqual(f._mcField, true);
  assert.strictEqual(f._url, s.layer._canvas, 'the image is the canvas itself');
  assert.strictEqual(f.options.pane, 'mcDuctFieldPane');
  assert.strictEqual(f.options.interactive, false);
  const pane = s.map._panes.mcDuctFieldPane;
  assert.strictEqual(pane.style.pointerEvents, 'none');
  assert.ok(+pane.style.zIndex > 200 && +pane.style.zIndex < 400, 'between tiles and vectors');
  assert.deepStrictEqual(JSON.parse(JSON.stringify(f._bounds)), [[51, -0.5], [52, 0.5]]);
  assert.strictEqual(s.layer._canvas.width, 96);
  assert.strictEqual(s.layer._canvas.height, s.layer._grid.h);
});

test('field pixels: cell coloured like MC_ductFieldColor of its n, normal cell transparent', async () => {
  const s = await mount(SAMPLE, T2);                       // a: surface duct, n=-66
  const p = pixelAt(s, 51.25, -0.25);
  // Exactly the band colour of the value sampled at that pixel's centre ...
  const g = s.layer._grid, cv = s.layer._canvas;
  const lat = s.ctx.MC_ductRowLat(p.y + 0.5, cv.height, g.north, g.south);
  const lon = g.west + (p.x + 0.5) / cv.width * (g.east - g.west);
  const smp = s.ctx.MC_ductSample(g, s.ctx.MC_ductGridValues(g, s.layer._data.cells, 2), lat, lon);
  const exact = s.ctx.MC_ductFieldColor(smp.g, STOPS);
  for (let k = 0; k < 3; k++) assert.ok(Math.abs(p.rgba[k] - exact[k]) <= 2, `channel ${k}: ${p.rgba} vs ${exact}`);
  assert.ok(Math.abs(p.rgba[3] - 0.5 * 255 * smp.a) <= 2, 'alpha = --mc-duct-alpha x coverage: ' + p.rgba[3]);
  // ... which, half a pixel from the centre, is close to the cell's own n.
  assert.ok(Math.abs(smp.g - -66) < 1, 'sampled ' + smp.g);
  const want = s.ctx.MC_ductFieldColor(-66, STOPS);
  assert.deepStrictEqual(p.rgba.slice(0, 3), Array.from(want).slice(0, 3), 'the -62..-70 band colour, flat');
  assert.deepStrictEqual(p.rgba.slice(0, 3), STOP_RGB[4]);
  const s0 = await mount(SAMPLE, T0);                      // a: n=-38 (above -44)
  assert.strictEqual(pixelAt(s0, 51.25, -0.25).rgba[3], 0, 'normal cell transparent');
  assert.ok(pixelAt(s0, 51.25, 0.25).rgba[3] > 40, 'n=-60 next to it is coloured (every cell here is on the outer edge: half faded)');
});

test('field pixels: soft edge where a neighbour has no data, nothing outside the cells', async () => {
  const s = await mount(SAMPLE, T2);
  const cv = s.layer._canvas;
  const W = cv.width, H = cv.height;
  // Cell b (51.25, 0.25) has no neighbour to the east: alpha falls toward the edge.
  const yb = pixelAt(s, 51.25, 0.25).y;
  const row = [];
  for (let x = W * 3 / 4; x < W; x++) row.push(px(cv, x, yb)[3]);
  for (let k = 1; k < row.length; k++) assert.ok(row[k] <= row[k - 1], 'monotonic fade: ' + row.join(','));
  assert.ok(row[0] > 40 && row[row.length - 1] < 10, row.join(','));
  // The missing cell's centre (51.75, 0.25) is transparent; so are the corners.
  assert.strictEqual(pixelAt(s, 51.75, 0.25).rgba[3], 0);
  assert.strictEqual(px(cv, W - 1, 0)[3], 0);
  assert.strictEqual(px(cv, 0, H - 1)[3], 0);
});

test('default time is the latest <= now, else the first', async () => {
  const s = await mount(SAMPLE, T1 + 60);
  assert.strictEqual(s.layer._idx, 1);
  const early = await mount(SAMPLE, T0 - 3600);
  assert.strictEqual(early.layer._idx, 0);
});

test('LoRa outline: dashed --mc-duct-lora only for ducts with 0 < fmin <= 1000 MHz', async () => {
  const s = await mount(SAMPLE, T0);
  const [a, b, c] = s.stats.rects;
  assert.strictEqual(loraOf(a), null, 'class 1');
  assert.strictEqual(loraOf(b), '#0a0b0c/4 3', 'elevated duct f=78, value trimmed');
  assert.strictEqual(b.style.fillOpacity, 0, 'outline only, no fill');
  assert.strictEqual(loraOf(c), null);
  s.layer._setIndex(1);
  assert.strictEqual(loraOf(a), null, 'super-refraction');
  assert.strictEqual(loraOf(b), null, 'f=1500');
  s.layer._setIndex(2);
  assert.strictEqual(loraOf(a), '#0a0b0c/4 3', 'surface duct f=380');
  assert.strictEqual(loraOf(b), null, 'f=4200');
  const body = clone(SAMPLE);
  body.cells[0].c = [3, 4, 3]; body.cells[0].f = [1000, 1001, 0];
  const e = await mount(body, T0);
  assert.ok(loraOf(e.stats.rects[0]), 'f=1000');
  e.layer._setIndex(1);
  assert.strictEqual(loraOf(e.stats.rects[0]), null, 'f=1001');
  e.layer._setIndex(2);
  assert.strictEqual(loraOf(e.stats.rects[0]), null, 'class 3 with f=0 is no duct');
});

test('changing time redraws the field in place: same rects, image layer, renderer; no fetch', async () => {
  const s = await mount(SAMPLE, T0);
  const rects = s.stats.rects.length;
  const calls = s.api.calls.length;
  const cv = s.layer._canvas, puts = cv.puts;
  const before = pixelAt(s, 51.25, -0.25).rgba;
  const styleCalls = s.stats.rects.map(r => r.setStyleCalls);
  s.layer._slider.value = '2';
  s.layer._slider.fire('input');
  assert.strictEqual(s.stats.rects.length, rects);
  assert.strictEqual(s.stats.renderers.length, 1);
  assert.strictEqual(s.stats.fields.length, 1);
  assert.strictEqual(s.stats.fields[0].setBoundsCalls, 0, 'bounds unchanged');
  assert.strictEqual(s.layer._canvas, cv);
  assert.strictEqual(cv.puts, puts + 1, 'one redraw');
  assert.strictEqual(s.api.calls.length, calls);
  assert.strictEqual(s.layer._idx, 2);
  assert.notDeepStrictEqual(pixelAt(s, 51.25, -0.25).rgba, before, 'n -38 -> -66');
  const img = cv.img;
  s.layer._setIndex(0);
  assert.strictEqual(cv.img, img, 'pixel buffer reused for the same size');
  assert.deepStrictEqual(pixelAt(s, 51.25, -0.25).rgba, before, 'and cleared: duct -> normal is transparent again');
  assert.strictEqual(s.stats.rects[2].setStyleCalls, styleCalls[2], 'unchanged outline not restyled');
  assert.ok(/Forecast time:/.test(s.layer._timeLabel.textContent));
  assert.strictEqual(s.layer._slider.max, '2');
});

test('field build is O(pixels) and fast: 860 cells (Gulf-sized lattice)', () => {
  const { ctx } = makeSandbox();
  const cells = [];
  for (let i = 0; i < 21; i++) {
    for (let j = 0; j < 41; j++) {
      if (cells.length >= 860) break;
      cells.push({ lat: 25.25 + i * 0.5, lon: -99.75 + j * 0.5, c: [((i + j) % 4) + 1], f: [500], b: [0], t: [0], dm: [0], n: [-36 - ((i * 7 + j * 13) % 60)] });
    }
  }
  const g = ctx.MC_ductGrid(cells, 0.5);
  const t0 = process.hrtime.bigint();
  const vals = ctx.MC_ductGridValues(g, cells, 0);
  let n = 0;
  for (let y = 0; y < g.h; y += 7) for (let x = 0; x < g.w; x += 7) if (ctx.MC_ductSample(g, vals, ctx.MC_ductRowLat(y + 0.5, g.h, g.north, g.south), g.west + (x + 0.5) / g.w * (g.east - g.west)).a > 0) n++;
  assert.ok(n > 0);
  // The real renderer through a layer:
  const s = makeSandbox();
  const layer = s.ctx.MC_createDuctingLayer({});
  layer.onAdd(makeMap());
  layer._data = { status: 'ok', stepDeg: 0.5, times: [T0], cells };
  layer._build();
  layer._idx = 0;
  const t1 = process.hrtime.bigint();
  layer._drawField();
  const ms = Number(process.hrtime.bigint() - t1) / 1e6;
  assert.strictEqual(layer._canvas.width, 1968, '48 px per cell, under the 2048 cap');
  assert.ok(ms < 400, `field build took ${ms.toFixed(1)} ms for ${layer._canvas.width}x${layer._canvas.height}`);
  void t0;
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
  assert.ok(normal.includes('Gradient, lowest 1.5 km: \u221238 N/km'), normal);
  const sup = pop(a, 1);
  assert.ok(/Super-refraction/.test(sup) && /Can carry 868 MHz: no/.test(sup) && /Can carry 915 MHz: no/.test(sup), sup);
  assert.ok(sup.includes('Gradient, lowest 1.5 km: \u221246 N/km'), sup);
  const sfc = pop(a, 2);
  assert.ok(/Surface-based duct/.test(sfc));
  assert.ok(/Base: 0 m above ground/.test(sfc) && /Top: 100 m above ground/.test(sfc), sfc);
  assert.ok(/Strength: 10\.0 M-units/.test(sfc), sfc);
  assert.ok(/Lowest trapped frequency: 380 MHz/.test(sfc), sfc);
  assert.ok(/Can carry 868 MHz: yes/.test(sfc) && /Can carry 915 MHz: yes/.test(sfc), sfc);
  assert.deepStrictEqual(sfc.split('\n').slice(0, 2), ['Surface-based duct', 'Gradient, lowest 1.5 km: \u221266 N/km'], 'the gradient right under the type');
  const elev = pop(b, 1);
  assert.ok(/Elevated duct/.test(elev));
  assert.ok(/Base: 600 m/.test(elev) && /Top: 820 m/.test(elev) && /Strength: 6\.0/.test(elev), elev);
  assert.ok(/Lowest trapped frequency: 1500 MHz/.test(elev));
  assert.ok(/Can carry 868 MHz: no/.test(elev) && /Can carry 915 MHz: no/.test(elev));
  assert.ok(elev.includes('Gradient, lowest 1.5 km: \u221257 N/km'), elev);
});

test('popup: gradient line rounded, only when n has data; class 0 shows no data', async () => {
  const body = clone(SAMPLE);
  body.cells[0].n = [-52.6, 0, 7];
  delete body.cells[1].n;
  const s = await mount(body, T0);
  const pop = (rect, i) => { s.layer._idx = i; return rect.popupFn().textContent; };
  const [a, b, c] = s.stats.rects;
  assert.ok(pop(a, 0).includes('Gradient, lowest 1.5 km: \u221253 N/km'), pop(a, 0));
  assert.ok(!/Gradient/.test(pop(a, 1)), 'n 0: no line: ' + pop(a, 1));
  assert.ok(pop(a, 2).includes('Gradient, lowest 1.5 km: 7 N/km'), pop(a, 2));
  const eb = pop(b, 0);
  assert.ok(!/Gradient/.test(eb) && /Elevated duct/.test(eb) && /Can carry 868 MHz: yes/.test(eb), 'no n: the rest as before: ' + eb);
  assert.strictEqual(pop(c, 0), 'No data');
});

test('older server without n: no field, but cells, LoRa outlines, popups and edge as before; no throw', async () => {
  const body = clone(SAMPLE);
  body.cells.forEach(c => { delete c.n; });
  const s = await mount(body, T2);
  assert.strictEqual(s.stats.rects.length, 3);
  assert.strictEqual(s.stats.fields.length, 1, 'the (empty) image layer is still there');
  assert.ok(s.layer._canvas.img.data.every(v => v === 0), 'nothing coloured');
  assert.strictEqual(loraOf(s.stats.rects[0]), '#0a0b0c/4 3', 'surface duct f=380 still outlined');
  assert.strictEqual(s.stats.lines.length, 1, 'forecast-area edge');
  s.layer._idx = 2;
  assert.ok(/Surface-based duct/.test(s.stats.rects[0].popupFn().textContent));
  for (const i of [0, 1, 2]) s.layer._setIndex(i);
  s.ctx.dispatch('theme-changed');
  assert.ok(s.layer._canvas.img.data.every(v => v === 0));
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

test('legend: banded bar from the field variables, scale labels, LoRa row, attribution, note', async () => {
  const s = await mount(SAMPLE);
  const t = legendText(s);
  assert.ok(t.includes('Dashed outline: a duct here can carry LoRa 868/915 MHz'), t);
  assert.ok(t.includes('Refractivity gradient over the lowest 1.5 km (N/km)'), t);
  assert.ok(!/-79|-157|Super-refraction|Ducting \(/.test(t), 'no g-based thresholds or labels: ' + t);
  assert.ok(t.includes('NOAA GFS') && t.includes('Open-Meteo.com (CC BY 4.0)'), t);
  assert.ok(/Updated /.test(t));
  assert.ok(t.includes('Indicator only: model levels are ~200 m apart, so thinner ducts are not visible.'));
  const box = s.map._controls[1]._container;
  const bar = find(box, n => n.className === 'mc-duct-scale-bar')[0];
  assert.ok(bar && /^linear-gradient\(to right, transparent 0%/.test(bar.style.background), bar && bar.style.background);
  for (let k = 1; k <= 8; k++) assert.ok(bar.style.background.includes(`var(--mc-duct-s${k})`), 's' + k);
  // Hard stops: each colour appears twice (start and end of its band), and
  // every band starts exactly where the previous one ends.
  const parts = bar.style.background.replace(/^linear-gradient\(to right, |\)$/g, '').split(/,\s*(?![^(]*\))/);
  const stops = parts.map((p) => { const m = /^(.*\S)\s+([\d.]+)%$/.exec(p.trim()); return { c: m[1], at: +m[2] }; });
  assert.strictEqual(stops.length, 18, 'transparent + 8 bands, two stops each: ' + parts.join(' | '));
  for (let k = 0; k < 18; k += 2) {
    assert.strictEqual(stops[k].c, stops[k + 1].c, 'band ' + k / 2 + ' has one colour');
    assert.ok(stops[k + 1].at > stops[k].at, 'band ' + k / 2 + ' has width');
    if (k) assert.strictEqual(stops[k].at, stops[k - 1].at, 'band ' + k / 2 + ' starts where the previous ends (no blend)');
  }
  assert.deepStrictEqual(stops.filter((_, i) => i % 2 === 0).map(x => x.c), ['transparent'].concat(STOP_N.map((_, k) => `var(--mc-duct-s${k + 1})`)));
  assert.strictEqual(stops[0].at, 0);
  assert.strictEqual(stops[17].at, 100);
  assert.ok(!/#[0-9a-f]{3,8}\b|rgb\(/i.test(bar.style.background), 'no literal colour in the bar');
  const labels = find(box, n => /mc-duct-scale-label\b/.test(n.className));
  assert.deepStrictEqual(labels.map(n => n.textContent), ['Normal', 'Enhanced', 'Strong', 'Very strong']);
  const left = labels.map(n => parseFloat(n.style.left));
  assert.ok(left.every((v, i) => i === 0 || v > left[i - 1]), 'labels in order: ' + left);
  // "Enhanced" marks where the colour starts (-44, band 1), "Strong" the -62 band (5).
  assert.strictEqual(left[0], 0);
  assert.strictEqual(left[1], stops[2].at, 'Enhanced at the start of the first colour');
  assert.strictEqual(left[2], stops[10].at, 'Strong at the start of the -62 band');
  assert.strictEqual(left[3], 100);
  assert.ok(/mc-duct-scale-label-end/.test(labels[3].className), 'Very strong right-aligned at the end');
  const sw = find(box, n => /mc-duct-swatch-/.test(n.className));
  assert.deepStrictEqual(sw.map(n => n.className), ['mc-duct-swatch mc-duct-swatch-lora', 'mc-duct-swatch mc-duct-swatch-edge']);
  assert.ok(s.map._controls.every(c => c.options.position === 'bottomleft'));
});

test('pending status shows the preparing message and draws nothing', async () => {
  const s = await mount({ status: 'pending' });
  const t = legendText(s);
  assert.ok(t.includes('Ducting forecast is being prepared'), t);
  assert.ok(t.includes('Indicator only'));
  assert.strictEqual(s.stats.rects.length, 0);
  assert.strictEqual(s.stats.fields.length, 0, 'no field image');
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
  assert.strictEqual(s.stats.rects.length, 3);
  assert.strictEqual(loraOf(s.stats.rects[1]), '#0a0b0c/4 3');
  assert.ok(s.map._layers.includes(s.stats.fields[0]), 'field kept');
  assert.ok(!legendText(s).includes('unavailable'));
  s.timers[0].fn();
  s.api.calls[2].ok({ status: 'pending' });
  await tick(); await tick();
  assert.ok(!legendText(s).includes('being prepared'));
  assert.ok(legendText(s).includes('Updated '));
  assert.strictEqual(s.layer._group.layers.length, 4, '3 rects + the edge outline');
});

test('no data yet: retries every minute until data arrives, then every 30 minutes', async () => {
  const s = await mount({ status: 'pending' });
  assert.strictEqual(s.timers.length, 1);
  assert.strictEqual(s.timers[0].ms, 60 * 1000, 'pending -> retry in a minute');
  s.timers[0].fn();
  s.api.calls[1].fail();
  await tick(); await tick();
  assert.strictEqual(s.timers.length, 1, 'still one timer after a failure');
  assert.strictEqual(s.timers[0].ms, 60 * 1000);
  s.timers[0].fn();
  s.api.calls[2].ok(clone(SAMPLE));
  await tick(); await tick();
  assert.strictEqual(s.stats.rects.length, 3);
  assert.strictEqual(s.timers.length, 1);
  assert.strictEqual(s.timers[0].ms, 30 * 60 * 1000, 'back to the normal refresh');
  s.layer.onRemove(s.map);
  assert.strictEqual(s.timers.length, 0);
});

test('a good refresh rebuilds cells and keeps a user-picked time', async () => {
  const s = await mount(SAMPLE, T1 + 60);
  s.layer._slider.value = '2';
  s.layer._slider.fire('input');
  s.timers[0].fn();
  s.api.calls[1].ok(clone(SAMPLE));
  await tick(); await tick();
  assert.strictEqual(s.layer._idx, 2);
  assert.strictEqual(s.layer._group.layers.length, 4, 'old rects and edge cleared, new ones added');
  assert.strictEqual(s.stats.fields.length, 1, 'same field layer');
  assert.strictEqual(s.stats.fields[0].setBoundsCalls, 0);
});

test('a refresh with a larger area moves the field bounds and resizes its canvas', async () => {
  const s = await mount(SAMPLE, T1 + 60);
  const body = clone(SAMPLE);
  body.cells.push({ lat: 52.75, lon: 1.25, c: [4, 4, 4], f: [300, 300, 300], b: [0, 0, 0], t: [90, 90, 90], dm: [80, 80, 80], g: [-400, -400, -400], n: [-95, -95, -95] });
  s.timers[0].fn();
  s.api.calls[1].ok(body);
  await tick(); await tick();
  const f = s.stats.fields[0];
  assert.strictEqual(s.stats.fields.length, 1);
  assert.strictEqual(f.setBoundsCalls, 1);
  assert.deepStrictEqual(JSON.parse(JSON.stringify(f._bounds)).b, [[51, -0.5], [53, 1.5]]);
  assert.strictEqual(s.layer._canvas.width, 192);
  assert.ok(pixelAt(s, 52.75, 1.25).rgba[3] > 40, 'new cell drawn (outer-edge cell: half faded)');
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
  assert.strictEqual(s.stats.fields.length, 0, 'no field image after remove');
  assert.strictEqual(map._layers.length, 0);
});

test('remove after data: the field image layer leaves the map with the cells', async () => {
  const s = await mount(SAMPLE);
  const f = s.stats.fields[0];
  assert.ok(s.map._layers.includes(f));
  s.layer.onRemove(s.map);
  assert.ok(!s.map._layers.includes(f), 'field removed');
  assert.strictEqual(s.map._layers.length, 0, 'nothing left on the map');
  assert.strictEqual(s.layer._field, null);
  assert.strictEqual(s.layer._canvas, null);
  s.layer.onAdd(s.map);
  s.api.calls[1].ok(SAMPLE);
  await tick(); await tick();
  assert.strictEqual(s.stats.fields.length, 2, 're-add builds a fresh field');
  assert.strictEqual(s.map._layers.filter(l => l._mcField).length, 1, 'one field per map');
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
  assert.strictEqual(s.stats.rects.length, 3);
  assert.strictEqual(s.api.calls[0].url, '/api/ducting');
});

// ── legend text, compact legend ─────────────────────────────────────────────

test('legend shows only the attribution, not the model twice', async () => {
  const s = await mount(SAMPLE);
  const t = legendText(s);
  assert.strictEqual(t.split('NOAA GFS').length - 1, 1, 'model name once (inside the attribution)');
  assert.ok(t.includes('Weather data by Open-Meteo.com (CC BY 4.0), NOAA GFS'));
  assert.ok(!/NOAA GFS\. /.test(t));
});

test('legend is collapsible and starts collapsed on narrow screens', async () => {
  const s = makeSandbox();
  s.ctx.matchMedia = (q) => ({ matches: /max-width: 600px/.test(q) });
  const map = makeMap();
  const layer = s.ctx.MC_createDuctingLayer({});
  layer.onAdd(map);
  s.api.calls[0].ok(SAMPLE);
  await tick(); await tick();
  const box = map._controls[1]._container;
  const head = () => find(box, n => n.tagName === 'button')[0];
  assert.strictEqual(head().attrs['aria-expanded'], 'false');
  assert.ok(!box.textContent.includes('Indicator only'), 'body hidden when compact');
  head().fire('click');
  assert.strictEqual(head().attrs['aria-expanded'], 'true');
  assert.ok(box.textContent.includes('Indicator only'));
});

test('bottom-left corner is lifted above the attribution bar, re-measured, and released', () => {
  const s = makeSandbox();
  const map = makeMap();
  const layer = s.ctx.MC_createDuctingLayer({});
  layer.onAdd(map);
  assert.strictEqual(map.corner.style.marginBottom, '36px', 'two-line bar');
  map.attr.offsetHeight = 54;
  map.fire('resize');
  assert.strictEqual(map.corner.style.marginBottom, '54px', 'follows a taller bar');
  map.attr.rect = { left: 400 };            // short bar at the right: no overlap
  map.fire('layeradd');
  assert.strictEqual(map.corner.style.marginBottom, '', 'no lift without overlap');
  map.attr.rect = { left: 0 };
  map.fire('resize');
  assert.strictEqual(map.corner.style.marginBottom, '54px');
  layer.onRemove(map);
  assert.strictEqual(map.corner.style.marginBottom, '', 'released on remove');
  assert.strictEqual((map._on.resize || []).length, 0);
  assert.strictEqual((map._on.layeradd || []).length, 0);
});

// ── theme & missing variables ───────────────────────────────────────────────

test('missing variables: no stops -> transparent field, no LoRa variable -> no outline; no throw', async () => {
  const s = makeSandbox();
  for (let k = 1; k <= 8; k++) delete s.vars['--mc-duct-s' + k];
  delete s.vars['--mc-duct-lora'];
  delete s.vars['--mc-duct-alpha'];
  const map = makeMap();
  const layer = s.ctx.MC_createDuctingLayer({});
  layer.onAdd(map);
  s.api.calls[0].ok(SAMPLE);
  await tick(); await tick();
  layer._setIndex(2);
  const d = layer._canvas.img.data;
  assert.ok(d.every(v => v === 0), 'nothing drawn without colours');
  // (-58 is the only stop below; with --mc-duct-alpha unset the opacity is the default.)
  assert.ok(s.stats.rects.every(r => !r.style.stroke), 'no outline without --mc-duct-lora');
  assert.ok(!/magenta|orange|gold|lightblue|#[0-9a-f]{6}\b/i.test(SRC.replace(/\/\*[\s\S]*?\*\/|\/\/.*$/gm, '')), 'no colour literals in map-ducting.js');
  s.vars['--mc-duct-s4'] = '#00a000';
  s.ctx.dispatch('theme-changed');
  assert.ok(layer._canvas.img.data.some(v => v !== 0), 'one stop is enough to draw; default opacity used');
});

test('theme-changed redraws the field and outlines from the new variables, without new rects; inert after remove', async () => {
  const s = await mount(SAMPLE, T2);
  const rects = s.stats.rects.slice();
  const before = pixelAt(s, 51.25, -0.25).rgba;
  const puts = s.layer._canvas.puts;
  s.vars['--mc-duct-s4'] = '#ff00ff';
  s.vars['--mc-duct-s5'] = '#ff00ff';
  s.vars['--mc-duct-lora'] = '#fefefe';
  s.ctx.dispatch('theme-changed');
  assert.strictEqual(s.layer._canvas.puts, puts + 1);
  const after = pixelAt(s, 51.25, -0.25).rgba;
  assert.notDeepStrictEqual(after, before);
  assert.ok(after[0] > 240 && after[1] < 15 && after[2] > 240, 'magenta now: ' + after);
  assert.strictEqual(loraOf(s.stats.rects[0]), '#fefefe/4 3');
  assert.strictEqual(s.stats.rects.length, rects.length, 'rects not rebuilt');
  assert.ok(s.stats.rects.every((r, i) => r === rects[i]));
  assert.strictEqual(s.stats.fields.length, 1);
  assert.strictEqual((s.winL['theme-changed'] || []).length, 1);
  s.layer.onRemove(s.map);
  assert.strictEqual((s.winL['theme-changed'] || []).length, 0);
  s.vars['--mc-duct-lora'] = '#00ff00';
  assert.doesNotThrow(() => s.ctx.dispatch('theme-changed'));
  assert.strictEqual(loraOf(rects[0]), '#fefefe/4 3', 'no restyle after remove');
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

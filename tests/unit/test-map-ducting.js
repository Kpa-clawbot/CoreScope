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
 *   - nothing interactive on the map: the cells with data are a list, the
 *     dashed LoRa outline (shown fmin <= the highest judged LoRa frequency:
 *     915 MHz by default, else the site's los.frequencyMhz when the operator
 *     set one: the shipped default 910.525 counts as not set) one
 *     non-interactive multi-polygon on a canvas in a pane without pointer
 *     events
 *   - map clicks: MC_ductCellAt lattice lookup (world copies), one map click
 *     handler that never stops the event and opens the cell popup only when
 *     the click did nothing else; the popup follows the shown hour and refreshes
 *   - the forecast area's dotted outline (merged boundary runs, antimeridian
 *     seam, one layer per build, colour from --mc-duct-edge) and the wider
 *     fade at the area's outer edge
 *   - popup text per class, the 1.5 km gradient line, the LoRa line (one
 *     threshold with the outline), the frequency to 2 significant figures
 *   - pending / error / failed-refresh states, legend content; the
 *     out-of-date notice on error, failure or a run-out forecast, not on
 *     pending; the one status line outside the legend, so a collapsed panel
 *     shows it; no rebuild when a refresh brings the same generatedAt
 *   - the hour tick: one timer aimed at the top of the hour moves the now
 *     line and the shown hour (not a user-picked one) without a request
 *   - remove during an in-flight fetch, timer cleanup, refresh cadence
 *   - overlay registry + MC_buildOverlayLayer (factory absent -> null)
 *   - the panel: one control (header, timeline, legend body), collapse and
 *     its persistence (storage missing, null or throwing), moving it
 *     (pointer drag, arrow keys, fractions restored and re-clamped, reset,
 *     off on phones, no corner lift while moved) and its teardown
 *   - the timeline: built once per set of times, aria state, keys, buttons,
 *     Now, wheel, mouse drag, click, rAF-coalesced user scrolls that settle
 *     on a time, code positioning that never counts as a user pick, a
 *     strip laid out late (hidden map) positioned once it has a size
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

// Elements whose scrollLeft changed: the browser fires 'scroll' on them
// later (flushScroll), never synchronously.
const scrolled = new Set();
function flushScroll() { const l = Array.from(scrolled); scrolled.clear(); l.forEach(e => e.fire('scroll')); }

function makeEl(tag) {
  const listeners = {};
  let text = '';
  let scrollLeft = 0;
  const el = {
    tagName: tag, className: '', children: [], attrs: {}, parentNode: null, value: '', listeners, style: {}, rect: null,
    // Laid out, with plenty to scroll. 0 = no layout box (a hidden map):
    // like the browser, scrollLeft then stays where it is.
    scrollWidth: 100000,
    appendChild(c) {
      if (c.parentNode && c.parentNode.removeChild) c.parentNode.removeChild(c);
      el.children.push(c); c.parentNode = el; return c;
    },
    insertBefore(c, ref) {
      if (c.parentNode && c.parentNode.removeChild) c.parentNode.removeChild(c);
      const i = ref ? el.children.indexOf(ref) : -1;
      if (i < 0) el.children.push(c); else el.children.splice(i, 0, c);
      c.parentNode = el; return c;
    },
    removeChild(c) { el.children = el.children.filter(x => x !== c); c.parentNode = null; return c; },
    get nextSibling() { const p = el.parentNode; return p ? p.children[p.children.indexOf(el) + 1] || null : null; },
    setAttribute(k, v) { el.attrs[k] = String(v); },
    removeAttribute(k) { delete el.attrs[k]; },
    getBoundingClientRect() { return el.rect || { left: 0, top: 0, right: 0, bottom: 0, width: 0, height: 0 }; },
    addEventListener(t, f) { (listeners[t] = listeners[t] || []).push(f); },
    removeEventListener(t, f) { listeners[t] = (listeners[t] || []).filter(x => x !== f); },
    // Fires t with an event carrying props; returns it (defaultPrevented, stopped).
    fire(t, props) {
      const ev = Object.assign({ type: t, defaultPrevented: false, stopped: false }, props);
      ev.preventDefault = () => { ev.defaultPrevented = true; };
      ev.stopPropagation = () => { ev.stopped = true; };
      (listeners[t] || []).slice().forEach(f => f(ev));
      return ev;
    },
    setPointerCapture(id) { el.captured = id; },
    releasePointerCapture() { el.captured = null; },
    focus() { el.focused = true; },
  };
  Object.defineProperty(el, 'scrollLeft', {
    get() { return scrollLeft; },
    set(v) { const n = Math.max(0, Number(v) || 0); if (el.scrollWidth && n !== scrollLeft) { scrollLeft = n; scrolled.add(el); } },
  });
  Object.defineProperty(el, 'textContent', {
    configurable: true,
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
    this._map = map; this._container = this.onAdd(map); this._container.className += ' leaflet-control';
    map.corner.appendChild(this._container); map._controls.push(this); return this;
  };
  L.Control.prototype.remove = function () {
    if (this._map._gone) throw new Error('map gone');
    const c = this._container;
    if (c.parentNode) c.parentNode.removeChild(c);
    this._map._controls = this._map._controls.filter(x => x !== this); this._removed = true; return this;
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
  L.polygon = (latlngs, opts) => {
    const l = { kind: 'polygon', latlngs, opts };
    stats.polys.push(l);
    return l;
  };
  // Nothing in the overlay may be an interactive vector any more.
  L.rectangle = () => { throw new Error('L.rectangle used'); };
  L.popup = (opts) => {
    const p = { opts, open: false, latlng: null, content: null, openCalls: 0 };
    p.setLatLng = (ll) => { p.latlng = ll; return p; };
    p.getLatLng = () => p.latlng;
    p.setContent = (c) => { p.content = c; return p; };
    p.openOn = (map) => { map._popup = p; p.open = true; p.openCalls++; return p; };
    p.isOpen = () => p.open;
    stats.popups.push(p);
    return p;
  };
  return L;
}

function makeMap() {
  const m = { _controls: [], _layers: [], _gone: false, _on: {}, _panes: {} };
  m.getPane = (n) => m._panes[n];
  m.createPane = (n) => { m._panes[n] = { style: {} }; return m._panes[n]; };
  m.on = (ev, f) => { ev.split(' ').forEach(e => { (m._on[e] = m._on[e] || []).push(f); }); return m; };
  m.off = (ev, f) => { ev.split(' ').forEach(e => { m._on[e] = (m._on[e] || []).filter(x => x !== f); }); return m; };
  m.fire = (e, arg) => (m._on[e] || []).slice().forEach(f => f(arg));
  m.closePopup = (p) => { p = p || m._popup; if (p) p.open = false; return m; };
  m.corner = makeEl('div');
  m.corner.style.marginBottom = '';
  m.corner.rect = { left: 0, right: 270 };
  // The map container: 1000 x 700 at the page's origin.
  m.el = makeEl('div');
  m.el.clientWidth = 1000; m.el.clientHeight = 700;
  m.el.rect = { left: 0, top: 0, right: 1000, bottom: 700, width: 1000, height: 700 };
  m.getContainer = () => m.el;
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
  const stats = { renderers: [], fields: [], lines: [], polys: [], popups: [] };
  const timers = [];
  const timeouts = [];   // { id, fn, ms } from setTimeout
  const frames = [];     // { id, fn } from requestAnimationFrame
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
    setTimeout(fn, ms) { const id = nextId++; timeouts.push({ id, fn, ms }); return id; },
    clearTimeout(id) { const i = timeouts.findIndex(t => t.id === id); if (i >= 0) timeouts.splice(i, 1); },
    requestAnimationFrame(fn) { const id = nextId++; frames.push({ id, fn }); return id; },
    cancelAnimationFrame(id) { const i = frames.findIndex(t => t.id === id); if (i >= 0) frames.splice(i, 1); },
  };
  if ('storage' in opts) ctx.localStorage = opts.storage;
  if (opts.narrow) ctx.matchMedia = (q) => ({ matches: /max-width: 600px/.test(q) });
  ctx.window = ctx;
  ctx.CSApi = makeApi();
  if (!opts.noLeaflet) ctx.L = makeLeaflet(stats);
  vm.createContext(ctx);
  vm.runInContext(SRC, ctx, { filename: 'map-ducting.js' });
  // Runs the queued frames / timeouts (and what they queue in turn). The
  // timeouts are the panel's short ones; the hour tick (up to an hour away)
  // only runs through runHourTick.
  const runFrames = () => { let n = 0; while (frames.length && n++ < 50) frames.shift().fn(); };
  const short = (t) => t.ms < 60000;
  const uiTimeouts = () => timeouts.filter(short);
  const runTimeouts = () => { let n = 0, i; while ((i = timeouts.findIndex(short)) >= 0 && n++ < 50) timeouts.splice(i, 1)[0].fn(); };
  const hourTicks = () => timeouts.filter(t => !short(t));
  const runHourTick = () => { const i = timeouts.findIndex(t => !short(t)); if (i >= 0) timeouts.splice(i, 1)[0].fn(); };
  // What the browser does after scrollLeft changes: scroll events, then a frame.
  const settleScroll = () => { flushScroll(); runFrames(); };
  return { ctx, stats, timers, cleared, api: ctx.CSApi, vars, winL, timeouts, frames, runFrames, runTimeouts, uiTimeouts, hourTicks, runHourTick, settleScroll };
}

// A Map-like localStorage; throwing: every call throws.
function makeStorage(init, throwing) {
  const data = Object.assign({}, init);
  const boom = () => { throw new Error('SecurityError'); };
  return {
    data,
    getItem: throwing ? boom : (k) => (k in data ? data[k] : null),
    setItem: throwing ? boom : (k, v) => { data[k] = String(v); },
  };
}

const tick = () => new Promise(r => setImmediate(r));
const clone = (o) => JSON.parse(JSON.stringify(o));
// The cells outlined as LoRa now ("lat,lon" sorted), and the outline layer.
const loraLayer = (s) => s.layer._lora;
const loraSet = (s) => (s.layer._lora ? Array.from(s.layer._lora._mcCells, c => c.lat + ',' + c.lon).sort() : []);
// A map click as Leaflet delivers it (preclick, then click), on target.
function click(s, lat, lng, target) {
  const ev = { latlng: { lat, lng }, originalEvent: { target: target || { classList: { contains: () => false } } } };
  s.map.fire('preclick', ev);
  s.map.fire('click', ev);
  return ev;
}
const popupText = (s) => { const p = s.stats.popups[0]; return p && p.open ? p.content.textContent : null; };
function find(node, pred, out) {
  out = out || [];
  if (pred(node)) out.push(node);
  (node.children || []).forEach(c => find(c, pred, out));
  return out;
}

// Adds a layer to a fresh map with time frozen at `now`, resolves the fetch.
// los: window.MC_LOS_CFG (the site's line-of-sight config), unset by default.
async function mount(body, now, los, opts) {
  const s = makeSandbox(opts);
  if (los) s.ctx.MC_LOS_CFG = los;
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
// The forecast-time label as the browser reads it (the fake joins children with newlines).
const labelText = (layer) => layer._timeLabel.textContent.replace(/\n/g, '');
const byClass = (node, cls) => find(node, n => String(n.className || '').split(' ').includes(cls));
const one = (node, cls) => byClass(node, cls)[0];
// Every listener left on node and its descendants.
const listenerCount = (node) => find(node, () => true).reduce((a, n) => a + Object.values(n.listeners || {}).reduce((b, l) => b + l.length, 0), 0);

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

// ── timeline and panel geometry ─────────────────────────────────────────────

// Local-clock seconds, so the expectations follow the test machine's zone
// exactly as the code does.
function localSec(y, mo, d, h) { return new Date(y, mo, d, h || 0).getTime() / 1000; }

test('MC_ductTimelineLayout: hourly steps, x follows time, a day is 240 px', () => {
  const { ctx } = makeSandbox();
  const start = localSec(2026, 10, 22, 13);
  const times = [];
  for (let i = 0; i < 48; i++) times.push(start + i * 3600);
  const l = ctx.MC_ductTimelineLayout(times);
  assert.strictEqual(l.pxPerSec, 240 / 86400);
  assert.strictEqual(l.xs.length, 48);
  assert.strictEqual(l.xs[0], 0);
  assert.ok(Math.abs(l.xs[1] - 10) < 1e-9, 'hourly step is 10 px');
  assert.ok(Math.abs(l.width - 47 * 10) < 1e-9);
  // First day starts mid-day: a label at x 0, then the two midnights inside.
  assert.deepStrictEqual(Array.from(l.days, d => d.sec), [start, localSec(2026, 10, 23), localSec(2026, 10, 24)]);
  assert.strictEqual(l.days[0].x, 0);
  // 18:00 on day one, 06/12/18 on day two, 06/12 on day three (ends 12:00).
  assert.deepStrictEqual(Array.from(l.hours, h => h.sec), [
    localSec(2026, 10, 22, 18),
    localSec(2026, 10, 23, 6), localSec(2026, 10, 23, 12), localSec(2026, 10, 23, 18),
    localSec(2026, 10, 24, 6), localSec(2026, 10, 24, 12),
  ]);
  for (const h of l.hours) assert.ok(Math.abs(h.x - (h.sec - start) * l.pxPerSec) < 1e-6);
});

test('MC_ductTimelineLayout: 3-hour steps widen to 8 px per step minimum only when needed; uneven steps keep their gaps', () => {
  const { ctx } = makeSandbox();
  const start = localSec(2026, 10, 20, 0);
  const three = [];
  for (let i = 0; i < 9; i++) three.push(start + i * 3 * 3600);
  const l3 = ctx.MC_ductTimelineLayout(three);
  assert.strictEqual(l3.pxPerSec, 240 / 86400, '3 h = 30 px, above the 8 px floor');
  assert.strictEqual(l3.days[0].sec, start, 'starts at midnight: no extra x-0 label');
  assert.strictEqual(l3.days.filter(d => d.x === 0).length, 1);
  // 10-minute steps: the 8 px floor sets the scale.
  const ten = [start, start + 600, start + 1200];
  assert.ok(Math.abs(ctx.MC_ductTimelineLayout(ten).pxPerSec - 8 / 600) < 1e-12);
  // History every 3 h, then a gap, then hourly: x is proportional to time.
  const mixed = [start, start + 10800, start + 21600, start + 25200, start + 28800];
  const lm = ctx.MC_ductTimelineLayout(mixed);
  for (let i = 0; i < mixed.length; i++) assert.ok(Math.abs(lm.xs[i] - (mixed[i] - start) * lm.pxPerSec) < 1e-9);
});

test('MC_ductTimelineLayout: labels stay on the local clock across DST changes', () => {
  const { ctx } = makeSandbox();
  // 24 Oct 00:00 to 3 Nov 00:00 local, hourly: covers the EU (25 Oct) and
  // US (1 Nov) 2026 changes; run under several TZ values to exercise them.
  const start = localSec(2026, 9, 24), end = localSec(2026, 10, 3);
  const times = [];
  for (let t = start; t <= end; t += 3600) times.push(t);
  const l = ctx.MC_ductTimelineLayout(times);
  assert.strictEqual(l.days.length, 11, '24 Oct .. 3 Nov midnights');
  for (const d of l.days) {
    const dt = new Date(d.sec * 1000);
    assert.strictEqual(dt.getHours() * 60 + dt.getMinutes(), 0, 'day label at local midnight: ' + dt);
  }
  assert.strictEqual(l.hours.length, 30, 'three hour labels per full day');
  for (const h of l.hours) assert.ok([6, 12, 18].includes(new Date(h.sec * 1000).getHours()), 'hour label off the local clock');
});

test('MC_ductTimelineLayout: empty, single, bad input', () => {
  const { ctx } = makeSandbox();
  for (const bad of [null, [], 'x', [NaN], [5, 1]]) {
    const l = ctx.MC_ductTimelineLayout(bad);
    assert.strictEqual(l.width, 0);
    assert.strictEqual(l.xs.length, 0);
    assert.strictEqual(l.days.length, 0);
  }
  const one = ctx.MC_ductTimelineLayout([localSec(2026, 10, 22, 9)]);
  assert.deepStrictEqual(Array.from(one.xs), [0]);
  assert.strictEqual(one.width, 0);
  assert.strictEqual(one.days.length, 1, 'the one time still gets its day label');
});

test('MC_ductTimelineIndexAt: nearest, ties to the earlier, clamped at the ends', () => {
  const { ctx } = makeSandbox();
  const at = ctx.MC_ductTimelineIndexAt;
  const xs = [0, 10, 20, 50];
  assert.strictEqual(at(xs, -99), 0);
  assert.strictEqual(at(xs, 0), 0);
  assert.strictEqual(at(xs, 4.9), 0);
  assert.strictEqual(at(xs, 5), 0, 'tie goes to the earlier time');
  assert.strictEqual(at(xs, 5.1), 1);
  assert.strictEqual(at(xs, 34), 2);
  assert.strictEqual(at(xs, 36), 3);
  assert.strictEqual(at(xs, 999), 3);
  assert.strictEqual(at([], 3), -1);
  assert.strictEqual(at(null, 3), -1);
  assert.strictEqual(at([7], 100), 0);
});

test('MC_ductClampPos / MC_ductPosToFrac / MC_ductPosFromFrac: the panel stays inside the map', () => {
  const { ctx } = makeSandbox();
  const c = (...a) => { const p = ctx.MC_ductClampPos(...a); return [p.left, p.top]; };
  assert.deepStrictEqual(c(50, 60, 100, 80, 800, 600, 8), [50, 60]);
  assert.deepStrictEqual(c(-20, -5, 100, 80, 800, 600, 8), [8, 8]);
  assert.deepStrictEqual(c(9999, 9999, 100, 80, 800, 600, 8), [692, 512]);
  assert.deepStrictEqual(c(300, 300, 900, 700, 800, 600, 8), [8, 8], 'bigger than the map: pinned to the margin');
  assert.deepStrictEqual(c(NaN, 40, 100, 80, 800, 600, 8), [8, 40]);
  assert.deepStrictEqual(c(-3, 700, 100, 80, 800, 600), [0, 520], 'margin defaults to 0');
  const f = ctx.MC_ductPosToFrac(350, 260, 100, 80, 800, 600);
  assert.ok(Math.abs(f.fx - 0.5) < 1e-9 && Math.abs(f.fy - 0.5) < 1e-9);
  assert.deepStrictEqual(JSON.parse(JSON.stringify(ctx.MC_ductPosToFrac(-50, 9999, 100, 80, 800, 600))), { fx: 0, fy: 1 });
  assert.deepStrictEqual(JSON.parse(JSON.stringify(ctx.MC_ductPosToFrac(10, 10, 900, 80, 800, 600))), { fx: 0, fy: 10 / 520 });
  // Same fractions on a smaller map: same relative spot, still inside.
  const p = ctx.MC_ductPosFromFrac({ fx: 0.5, fy: 1 }, 100, 80, 400, 300, 8);
  assert.deepStrictEqual([p.left, p.top], [150, 212]);
  const q = ctx.MC_ductPosFromFrac(null, 100, 80, 400, 300, 8);
  assert.deepStrictEqual([q.left, q.top], [8, 8]);
  const r = ctx.MC_ductPosFromFrac({ fx: 'x', fy: Infinity }, 100, 80, 400, 300, 8);
  assert.deepStrictEqual([r.left, r.top], [8, 8]);
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

test('nothing interactive: the cells with data are a list; outlines on one canvas in a pane without pointer events', async () => {
  const s = await mount(SAMPLE, T0);
  assert.strictEqual(s.layer._cells.length, 3, 'third cell is class 0/1/1: has data');
  assert.strictEqual(s.layer._mcDuct, true);
  assert.strictEqual(s.stats.renderers.length, 1);
  const r = s.stats.renderers[0];
  assert.strictEqual(r.opts.pane, 'mcDuctLinePane');
  const pane = s.map._panes.mcDuctLinePane;
  assert.strictEqual(pane.style.pointerEvents, 'none', 'the line canvas never takes the pointer');
  assert.ok(+pane.style.zIndex > +s.map._panes.mcDuctFieldPane.style.zIndex && +pane.style.zIndex < 390, 'above the field, below vectors and the planner node pane: ' + pane.style.zIndex);
  // The group holds the edge and the LoRa outline only, both non-interactive.
  const kinds = s.layer._group.layers.map(l => (l._mcEdge ? 'edge' : l._mcLora ? 'lora' : 'other'));
  assert.deepStrictEqual(kinds.sort(), ['edge', 'lora']);
  s.layer._group.layers.forEach((l) => { assert.strictEqual(l.opts.interactive, false); assert.strictEqual(l.opts.renderer, r); });
  assert.strictEqual(s.layer._popup, undefined, 'no popup until a click');
});

test('MC_ductCellAt: the cell whose square holds the point; world copies wrap; -1 outside', () => {
  const { ctx } = makeSandbox();
  const g = ctx.MC_ductGrid(SAMPLE.cells, 0.5);
  const at = (lat, lon) => ctx.MC_ductCellAt(g, lat, lon);
  assert.strictEqual(at(51.25, -0.25), 0, 'centre');
  assert.strictEqual(at(51.0, -0.5), 0, 'south-west corner belongs to the cell');
  assert.strictEqual(at(51.4999, -0.0001), 0);
  assert.strictEqual(at(51.3, 0.0), 1, 'east of the shared edge');
  assert.strictEqual(at(51.9, -0.4), 2);
  assert.strictEqual(at(51.75, 0.25), -1, 'slot on the lattice without a cell');
  assert.strictEqual(at(50.99, -0.25), -1, 'south of the lattice');
  assert.strictEqual(at(51.25, 0.5), -1, 'east edge is outside');
  assert.strictEqual(at(51.25, 359.75), 0, 'world copy east');
  assert.strictEqual(at(51.25, -360.25), 0, 'world copy west');
  assert.strictEqual(at(NaN, 0), -1);
  assert.strictEqual(ctx.MC_ductCellAt(null, 51.25, -0.25), -1);
  // A cell never with data is not on the lattice: nothing to find there.
  const only = clone(SAMPLE.cells); only[2].c = [0, 0, 0];
  assert.strictEqual(ctx.MC_ductCellAt(ctx.MC_ductGrid(only, 0.5), 51.75, -0.25), -1);
  // Across the antimeridian.
  const am = [{ lat: 10.25, lon: 179.75, c: [1] }, { lat: 10.25, lon: -179.75, c: [1] }];
  const ga = ctx.MC_ductGrid(am, 0.5);
  assert.strictEqual(ctx.MC_ductCellAt(ga, 10.3, 179.9), 0);
  assert.strictEqual(ctx.MC_ductCellAt(ga, 10.3, 180.1), 1, '180.1 is -179.9');
  assert.strictEqual(ctx.MC_ductCellAt(ga, 10.3, -179.9), 1);
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
  assert.strictEqual(find(s.layer._legendEl, n => /mc-duct-swatch-edge/.test(n.className || '')).length, 1);
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

test('LoRa outline: dashed --mc-duct-lora only for ducts whose shown fmin <= 915 MHz (default bands)', async () => {
  const s = await mount(SAMPLE, T0);
  assert.deepStrictEqual(loraSet(s), ['51.25,0.25'], 'elevated duct f=78; class 1 and 0 cells not');
  const l = loraLayer(s);
  assert.strictEqual(l.opts.color, '#0a0b0c', 'value trimmed');
  assert.strictEqual(l.opts.dashArray, '4 3');
  assert.strictEqual(l.opts.fill, false, 'outline only, no fill');
  assert.strictEqual(l.opts.interactive, false);
  assert.deepStrictEqual(JSON.parse(JSON.stringify(l.latlngs)), [[[[51, 0], [51, 0.5], [51.5, 0.5], [51.5, 0]]]], 'one ring per cell: centre +- step/2');
  s.layer._setIndex(1);
  assert.deepStrictEqual(loraSet(s), [], 'super-refraction, f=1500');
  assert.strictEqual(loraLayer(s), null, 'no empty layer left');
  s.layer._setIndex(2);
  assert.deepStrictEqual(loraSet(s), ['51.25,-0.25'], 'surface duct f=380; f=4200 not');
  const body = clone(SAMPLE);
  body.cells[0].c = [3, 4, 3]; body.cells[0].f = [868, 920, 0];
  body.cells[1].f = [914, 1000, 4200];
  const e = await mount(body, T0);
  assert.deepStrictEqual(loraSet(e), ['51.25,-0.25', '51.25,0.25'], 'f=868 (shown 870) and f=914 (shown 910) are <= 915');
  e.layer._setIndex(1);
  assert.deepStrictEqual(loraSet(e), [], 'f=920, f=1000 (shown 920, 1000) are above 915');
  e.layer._setIndex(2);
  assert.deepStrictEqual(loraSet(e), [], 'class 3 with f=0 is no duct');
  const g = clone(SAMPLE);
  g.cells[0].c = [3, 3, 3]; g.cells[0].f = [916, 925, 900];
  const ge = await mount(g, T0);
  assert.deepStrictEqual(loraSet(ge), ['51.25,0.25'], 'f=916 shows 920: above 915 even though f itself is close');
  ge.layer._setIndex(2);
  assert.ok(loraSet(ge).includes('51.25,-0.25'), 'f=900 outlined (trapped at 915)');
});

test('default bands: f=900 is outlined; popup says 868 not trapped, 915 possibly', async () => {
  const body = clone(SAMPLE);
  body.cells[0].c = [4, 4, 4]; body.cells[0].f = [900, 900, 900];
  const s = await mount(body, T0);
  assert.ok(loraSet(s).includes('51.25,-0.25'));
  const lines = s.ctx.MC_ductPopupContent(body.cells[0], 0).textContent.split('\n').filter(l => /^LoRa /.test(l));
  assert.deepStrictEqual(lines, ['LoRa 868 MHz: not trapped by this layer', 'LoRa 915 MHz: possibly trapped (model estimate)']);
});

test('site frequency 912.5 MHz: one verdict line, outline threshold 912.5 (905 yes, 950 no)', async () => {
  const body = clone(SAMPLE);
  body.cells[0].c = [4, 4, 4]; body.cells[0].f = [905, 950, 868];
  const s = await mount(body, T0, { frequencyMhz: 912.5 });
  const pop = (i) => s.ctx.MC_ductPopupContent(body.cells[0], i).textContent;
  const lora = (i) => pop(i).split('\n').filter(l => /^LoRa /.test(l));
  assert.ok(loraSet(s).includes('51.25,-0.25'), 'f=905 (shown 910) outlined');
  assert.deepStrictEqual(lora(0), ['LoRa 912.5 MHz: possibly trapped (model estimate)']);
  assert.ok(pop(0).includes('Lowest trapped frequency: \u2248 910 MHz (model estimate)'), pop(0));
  s.layer._setIndex(1);
  assert.ok(!loraSet(s).includes('51.25,-0.25'), 'f=950 not outlined');
  assert.deepStrictEqual(lora(1), ['LoRa 912.5 MHz: not trapped by this layer']);
  s.layer._setIndex(2);
  assert.ok(loraSet(s).includes('51.25,-0.25'), 'f=868 (shown 870) outlined at 912.5');
  assert.ok(legendText(s).includes('Dashed outline: the model shows a duct that may carry LoRa at 912.5 MHz'), legendText(s));
  // Out of range site frequencies fall back to the two bands.
  for (const bad of [50, 7000, 'x', null]) {
    const b = await mount(clone(SAMPLE), T0, { frequencyMhz: bad });
    assert.ok(legendText(b).includes('LoRa at 868 / 915 MHz'), 'frequencyMhz ' + bad);
  }
});

test('site frequency left at the shipped default 910.525 is not the operator\'s choice: both bands; 869.525 and 915 are judged alone', async () => {
  const body = clone(SAMPLE);
  body.cells[0].c = [4, 4, 4]; body.cells[0].f = [860, 905, 950];
  const lora = (s, i) => s.ctx.MC_ductPopupContent(body.cells[0], i).textContent.split('\n').filter(l => /^LoRa /.test(l));
  // The default, as the server sends it and as a string: an EU site that
  // never set los.frequencyMhz still gets its 868 MHz verdict.
  for (const dflt of [910.525, '910.525']) {
    const d = await mount(body, T0, { frequencyMhz: dflt });
    assert.ok(legendText(d).includes('may carry LoRa at 868 / 915 MHz'), legendText(d));
    assert.deepStrictEqual(lora(d, 0), ['LoRa 868 MHz: possibly trapped (model estimate)', 'LoRa 915 MHz: possibly trapped (model estimate)']);
    assert.deepStrictEqual(lora(d, 1), ['LoRa 868 MHz: not trapped by this layer', 'LoRa 915 MHz: possibly trapped (model estimate)']);
    assert.ok(loraSet(d).includes('51.25,-0.25'), 'outlined up to 915');
  }
  const eu = await mount(body, T0, { frequencyMhz: 869.525 });
  assert.ok(legendText(eu).includes('may carry LoRa at 869.525 MHz'), legendText(eu));
  assert.deepStrictEqual(lora(eu, 0), ['LoRa 869.525 MHz: possibly trapped (model estimate)']);
  assert.deepStrictEqual(lora(eu, 1), ['LoRa 869.525 MHz: not trapped by this layer']);
  eu.layer._setIndex(1);
  assert.ok(!loraSet(eu).includes('51.25,-0.25'), 'f=905 is above 869.525: no outline');
  const us = await mount(body, T0, { frequencyMhz: 915 });
  assert.ok(legendText(us).includes('may carry LoRa at 915 MHz'), legendText(us));
  assert.deepStrictEqual(lora(us, 1), ['LoRa 915 MHz: possibly trapped (model estimate)']);
  assert.deepStrictEqual(lora(us, 2), ['LoRa 915 MHz: not trapped by this layer']);
  // Next to the default is a choice.
  const near = await mount(body, T0, { frequencyMhz: 910.53 });
  assert.ok(legendText(near).includes('may carry LoRa at 910.53 MHz'), legendText(near));
});

test('LoRa outline and popup agree for every class and frequency (default bands and a site frequency)', async () => {
  const fs_ = [0, 78, 380, 700, 864, 865, 868, 905, 909, 914, 915, 916, 950, 1000, 1500];
  const body = clone(SAMPLE);
  body.times = fs_.map((_, i) => T0 + i * 3600);
  const mk = (c) => ({ lat: 51.25, lon: -0.25, c: fs_.map(() => c), f: fs_.slice(), b: fs_.map(() => 0), t: fs_.map(() => 100), dm: fs_.map(() => 50), n: fs_.map(() => -60) });
  const sig2 = (v) => Number(Number(v).toPrecision(2));
  for (const [los, freqs] of [[null, [868, 915]], [{ frequencyMhz: 910.525 }, [868, 915]], [{ frequencyMhz: 906.875 }, [906.875]]]) {
    for (const cls of [2, 3, 4]) {
      body.cells = [mk(cls)];
      const s = await mount(body, T0, los);
      fs_.forEach((f, i) => {
        s.layer._setIndex(i);
        const outlined = loraSet(s).length === 1;
        const txt = s.ctx.MC_ductPopupContent(body.cells[0], i).textContent;
        const lines = txt.split('\n').filter(l => /^LoRa /.test(l));
        const shown = cls >= 3 && f > 0 ? sig2(f) : 0;
        assert.strictEqual(lines.length, freqs.length, 'one line per judged frequency: ' + txt);
        freqs.forEach((fr, k) => {
          const want = shown > 0 && shown <= fr;
          assert.strictEqual(lines[k], `LoRa ${fr} MHz: ` + (want ? 'possibly trapped (model estimate)' : 'not trapped by this layer'), `class ${cls} f=${f} at ${fr}`);
        });
        const says = lines.some(l => /possibly trapped/.test(l));
        assert.strictEqual(outlined, says, `class ${cls} f=${f}: outline ${outlined}, popup ${txt}`);
        assert.strictEqual(outlined, shown > 0 && shown <= freqs[freqs.length - 1], `class ${cls} f=${f}`);
        // The frequency line shows the very value the verdicts used.
        assert.ok(txt.includes('Lowest trapped frequency: ' + (shown ? '\u2248 ' + shown + ' MHz (model estimate)' : 'none')), txt);
      });
    }
  }
});

test('changing time redraws the field in place: same cells, image layer, renderer; no fetch', async () => {
  const s = await mount(SAMPLE, T0);
  const cells = s.layer._cells.slice();
  const calls = s.api.calls.length;
  const cv = s.layer._canvas, puts = cv.puts;
  const before = pixelAt(s, 51.25, -0.25).rgba;
  s.layer._tl.fire('keydown', { key: 'End' });
  assert.ok(s.layer._cells.every((c, i) => c === cells[i]) && s.layer._cells.length === cells.length);
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
  const polys = s.stats.polys.length, l = loraLayer(s);
  s.layer._setIndex(0);
  s.ctx.dispatch('theme-changed');
  assert.strictEqual(s.stats.polys.length, polys, 'same outlined cells and colour: outline not rebuilt');
  assert.strictEqual(loraLayer(s), l);
  assert.ok(/^Forecast time: \S/.test(labelText(s.layer)), labelText(s.layer));
  assert.strictEqual(s.layer._tl.attrs['aria-valuemax'], '2');
});

// Correctness only: wall-clock bounds do not belong in the unit gate.
test('field build on a Gulf-sized lattice (860 cells): full width, pixels drawn', () => {
  const { ctx } = makeSandbox();
  const cells = [];
  for (let i = 0; i < 21; i++) {
    for (let j = 0; j < 41; j++) {
      if (cells.length >= 860) break;
      cells.push({ lat: 25.25 + i * 0.5, lon: -99.75 + j * 0.5, c: [((i + j) % 4) + 1], f: [500], b: [0], t: [0], dm: [0], n: [-36 - ((i * 7 + j * 13) % 60)] });
    }
  }
  const g = ctx.MC_ductGrid(cells, 0.5);
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
  layer._drawField();
  assert.strictEqual(layer._canvas.width, 1968, '48 px per cell, under the 2048 cap');
  const d = layer._canvas.img.data;
  let drawn = 0;
  for (let i = 3; i < d.length; i += 4 * 97) if (d[i]) drawn++;
  assert.ok(drawn > 0, 'pixels drawn');
});

test('timeline is disabled until data arrives', () => {
  const s = makeSandbox();
  const layer = s.ctx.MC_createDuctingLayer({});
  layer.onAdd(makeMap());
  assert.strictEqual(layer._tl.attrs['aria-disabled'], 'true');
  assert.strictEqual(layer._tl.attrs['aria-valuemax'], '0');
  assert.ok(!('aria-valuenow' in layer._tl.attrs));
  assert.ok(layer._prevEl.disabled && layer._nextEl.disabled && layer._nowEl.disabled);
  assert.strictEqual(labelText(layer), 'Forecast time');
  const ev = layer._tl.fire('keydown', { key: 'End' });
  assert.ok(!ev.defaultPrevented, 'keys ignored without times');
});

// ── popup ───────────────────────────────────────────────────────────────────

// The LoRa verdict lines at the default bands (868 and 915 MHz).
const loraLines = (both) => ['LoRa 868 MHz: ' + both[0], 'LoRa 915 MHz: ' + both[1]];
const YES = 'possibly trapped (model estimate)', NO = 'not trapped by this layer';
const POSSIBLY = loraLines([YES, YES]).join('\n');          // trapped at both
const NOT_TRAPPED = loraLines([NO, NO]).join('\n');         // at neither
const LAYER_LINE = 'Trapping layer in the model (its levels are ~200 m apart, so thinner layers are not resolved)';

test('popup text for each class: trapping layer, frequency estimate and the LoRa line', async () => {
  const s = await mount(SAMPLE, T0);
  const pop = (cell, i) => s.ctx.MC_ductPopupContent(cell, i).textContent;
  const [a, b] = SAMPLE.cells;
  const normal = pop(a, 0);
  assert.ok(/Normal/.test(normal) && !/868|LoRa/.test(normal));
  assert.ok(normal.includes('Gradient, lowest 1.5 km: \u221238 N/km'), normal);
  const sup = pop(a, 1);
  assert.ok(/Super-refraction/.test(sup) && sup.includes(NOT_TRAPPED) && /Lowest trapped frequency: none/.test(sup), sup);
  assert.ok(!sup.includes(LAYER_LINE), 'no trapping layer for super-refraction');
  assert.ok(sup.includes('Gradient, lowest 1.5 km: \u221246 N/km'), sup);
  assert.ok(!/Can carry|: yes|: no\b/.test(sup), 'no yes/no answers: ' + sup);
  const sfc = pop(a, 2);
  assert.deepStrictEqual(sfc.split('\n'), [
    'Surface-based duct',
    'Gradient, lowest 1.5 km: \u221266 N/km',
    LAYER_LINE,
    'Base: 0 m above ground',
    'Top: 100 m above ground',
    'Strength: 10.0 M-units',
    'Lowest trapped frequency: \u2248 380 MHz (model estimate)',
  ].concat(loraLines([YES, YES])));
  assert.ok(!/\u2265 ~200 m thick/.test(sfc), 'old thickness claim gone');
  const elev = pop(b, 1);
  assert.ok(/Elevated duct/.test(elev) && elev.includes(LAYER_LINE));
  assert.ok(/Base: 600 m/.test(elev) && /Top: 820 m/.test(elev) && /Strength: 6\.0/.test(elev), elev);
  assert.ok(elev.includes('Lowest trapped frequency: \u2248 1500 MHz (model estimate)'), elev);
  assert.ok(elev.includes(NOT_TRAPPED), elev);
  assert.ok(elev.includes('Gradient, lowest 1.5 km: \u221257 N/km'), elev);
  assert.ok(pop(b, 0).includes('\u2248 78 MHz') && pop(b, 0).includes(POSSIBLY), pop(b, 0));
});

test('popup: lowest trapped frequency rounded to 2 significant figures', async () => {
  const s = makeSandbox();
  const cell = { lat: 0, lon: 0, c: [3, 3, 3, 3, 3, 3, 3], f: [78.4, 868, 915, 1234, 4249, 99.6, 905], b: [0, 0, 0, 0, 0, 0, 0], t: [0, 0, 0, 0, 0, 0, 0], dm: [0, 0, 0, 0, 0, 0, 0] };
  const got = cell.f.map((_, i) => /Lowest trapped frequency: \u2248 (\d+) MHz \(model estimate\)/.exec(s.ctx.MC_ductPopupContent(cell, i).textContent)[1]);
  assert.deepStrictEqual(got, ['78', '870', '920', '1200', '4200', '100', '910']);
  // The verdicts follow the shown figure: f=868 shows 870, so not at 868 but at 915;
  // f=915 shows 920, at neither; f=905 shows 910, at 915.
  const t = (i) => s.ctx.MC_ductPopupContent(cell, i).textContent;
  assert.ok(t(1).includes(loraLines([NO, YES]).join('\n')), '868 itself: ' + t(1));
  assert.ok(t(2).includes(NOT_TRAPPED), '915 itself: ' + t(2));
  assert.ok(t(6).includes(loraLines([NO, YES]).join('\n')), '905: ' + t(6));
});

test('popup: gradient line rounded, only when n has data; class 0 shows no data', async () => {
  const body = clone(SAMPLE);
  body.cells[0].n = [-52.6, 0, 7];
  delete body.cells[1].n;
  const s = await mount(body, T0);
  const pop = (cell, i) => s.ctx.MC_ductPopupContent(cell, i).textContent;
  const [a, b, c] = body.cells;
  assert.ok(pop(a, 0).includes('Gradient, lowest 1.5 km: \u221253 N/km'), pop(a, 0));
  assert.ok(!/Gradient/.test(pop(a, 1)), 'n 0: no line: ' + pop(a, 1));
  assert.ok(pop(a, 2).includes('Gradient, lowest 1.5 km: 7 N/km'), pop(a, 2));
  const eb = pop(b, 0);
  assert.ok(!/Gradient/.test(eb) && /Elevated duct/.test(eb) && eb.includes(POSSIBLY), 'no n: the rest as before: ' + eb);
  assert.strictEqual(pop(c, 0), 'No data');
});

test('older server without n: no field, but cells, LoRa outlines, popups and edge as before; no throw', async () => {
  const body = clone(SAMPLE);
  body.cells.forEach(c => { delete c.n; });
  const s = await mount(body, T2);
  assert.strictEqual(s.layer._cells.length, 3);
  assert.strictEqual(s.stats.fields.length, 1, 'the (empty) image layer is still there');
  assert.ok(s.layer._canvas.img.data.every(v => v === 0), 'nothing coloured');
  assert.deepStrictEqual(loraSet(s), ['51.25,-0.25'], 'surface duct f=380 still outlined');
  assert.strictEqual(s.stats.lines.length, 1, 'forecast-area edge');
  click(s, 51.25, -0.25);
  await tick();
  assert.ok(/Surface-based duct/.test(popupText(s)), 'popup by click: ' + popupText(s));
  for (const i of [0, 1, 2]) s.layer._setIndex(i);
  s.ctx.dispatch('theme-changed');
  assert.ok(s.layer._canvas.img.data.every(v => v === 0));
});

test('popup: one LoRa line per default band, no yes/no; no innerHTML use', async () => {
  const body = clone(SAMPLE);
  body.cells[1].f = [900, 860, 915];
  const s = await mount(body, T0);
  const pop = (i) => s.ctx.MC_ductPopupContent(body.cells[1], i).textContent;
  assert.ok(pop(0).includes(loraLines([NO, YES]).join('\n')) && !/Can carry|: yes|: no\b/.test(pop(0)), pop(0));
  assert.ok(pop(1).includes(POSSIBLY), '860 shows 860: both bands: ' + pop(1));
  assert.ok(pop(2).includes(NOT_TRAPPED), '915 shows 920, above both: ' + pop(2));
  assert.ok(!/innerHTML/.test(SRC.replace(/\/\*[\s\S]*?\*\/|\/\/.*$/gm, '')), 'no innerHTML in map-ducting.js');
});

// ── map clicks ──────────────────────────────────────────────────────────────

test('click in a cell with data opens its popup after the click, without stopping it', async () => {
  const s = await mount(SAMPLE, T2);
  assert.strictEqual((s.map._on.click || []).length, 1, 'one map click handler');
  const ev = click(s, 51.3, -0.2);
  assert.strictEqual(popupText(s), null, 'not inside the click: the other handlers run first');
  await tick();
  assert.ok(/Surface-based duct/.test(popupText(s)) && popupText(s).includes(POSSIBLY), popupText(s));
  const p = s.stats.popups[0];
  assert.deepStrictEqual(p.latlng, { lat: 51.3, lng: -0.2 }, 'opens where clicked');
  assert.strictEqual(s.map._popup, p);
  assert.ok(!ev.originalEvent._stopped, 'event not stopped');
  // The other cell, a world copy of it, outside the area, a cell without data at this hour.
  click(s, 51.25, 0.25); await tick();
  assert.ok(/Elevated duct/.test(popupText(s)), popupText(s));
  assert.strictEqual(s.stats.popups.length, 1, 'one popup object, reused');
  s.map.closePopup();
  click(s, 51.25, 360.25); await tick();
  assert.ok(/Elevated duct/.test(popupText(s)), 'world copy: ' + popupText(s));
  s.map.closePopup();
  click(s, 52.5, 3); await tick();
  assert.strictEqual(popupText(s), null, 'outside the forecast area');
  const s0 = await mount(SAMPLE, T0);
  click(s0, 51.75, -0.25); await tick();
  assert.strictEqual(popupText(s0), null, 'class 0 at this hour: nothing to show');
  assert.strictEqual(s0.layer.openCellPopup({ lat: 51.25, lng: -0.25 }), true, 'openCellPopup');
  assert.ok(/Normal/.test(popupText(s0)));
});

test('click: no popup when it hit an interactive layer or another handler drew or opened something', async () => {
  const s = await mount(SAMPLE, T2);
  const interactive = { classList: { contains: (c) => c === 'leaflet-interactive' } };
  click(s, 51.25, -0.25, interactive); await tick();
  assert.strictEqual(popupText(s), null, 'on a path/marker (or a canvas hovering one)');
  // Another map click handler places something (Planner) ...
  const place = () => s.map.fire('layeradd', {});
  s.map.on('click', place);
  click(s, 51.25, -0.25); await tick();
  assert.strictEqual(popupText(s), null, 'the page used the click');
  s.map.off('click', place);
  // ... or a path opened its own popup before the map click (Live).
  const ev = { latlng: { lat: 51.25, lng: -0.25 }, originalEvent: { target: {} } };
  s.map.fire('preclick', ev);
  s.map.fire('popupopen', {});
  s.map.fire('click', ev);
  await tick();
  assert.strictEqual(popupText(s), null, 'a popup opened during the click');
  // Changes before the click do not count.
  s.map.fire('layeradd', {});
  click(s, 51.25, -0.25); await tick();
  assert.ok(/Surface-based duct/.test(popupText(s)), 'a plain click still opens it');
});

test('open popup follows the shown hour, closes where the hour has no data, and goes with the layer', async () => {
  const body = clone(SAMPLE);
  const s = await mount(body, T2);
  click(s, 51.8, -0.3); await tick();                      // cell c: class 1 at t2
  assert.ok(/^Normal/.test(popupText(s)), popupText(s));
  s.layer._setIndex(1);
  assert.ok(/Gradient, lowest 1.5 km: \u221236/.test(popupText(s)), 'text of hour 1: ' + popupText(s));
  s.layer._setIndex(0);
  assert.strictEqual(popupText(s), null, 'class 0 at hour 0: closed');
  s.layer._setIndex(2);
  click(s, 51.25, -0.25); await tick();
  assert.ok(/Surface-based/.test(popupText(s)));
  s.layer.onRemove(s.map);
  assert.strictEqual(popupText(s), null, 'closed with the layer');
  ['click', 'preclick', 'popupopen'].forEach(e => assert.strictEqual((s.map._on[e] || []).length, 0, e + ' listener left'));
  assert.ok(!(s.map._on.layeradd || []).length, 'layeradd listeners left');
  // A click queued just before the remove opens nothing afterwards.
  const s2 = await mount(SAMPLE, T2);
  click(s2, 51.25, -0.25);
  s2.layer.onRemove(s2.map);
  await tick();
  assert.strictEqual(s2.stats.popups.length, 0, 'no popup after remove');
});

// ── legend & states ─────────────────────────────────────────────────────────

test('legend: banded bar from the field variables, scale labels, LoRa row, attribution, note', async () => {
  const s = await mount(SAMPLE);
  const t = legendText(s);
  assert.ok(t.includes('Dashed outline: the model shows a duct that may carry LoRa at 868 / 915 MHz'), t);
  assert.ok(t.includes('Refractivity gradient over the lowest 1.5 km (N/km)'), t);
  assert.ok(!/-79|-157|Super-refraction|Ducting \(/.test(t), 'no g-based thresholds or labels: ' + t);
  assert.ok(t.includes('NOAA GFS') && t.includes('Open-Meteo.com (CC BY 4.0)'), t);
  assert.ok(/Updated /.test(t));
  assert.ok(t.includes('Indicator only: model levels are ~200 m apart, so thinner ducts are not visible.'));
  const box = s.layer._legendEl;
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
  assert.deepStrictEqual(labels.map(n => n.textContent), ['Normal', 'Above normal', 'High', 'Very high']);
  assert.ok(!/Enhanced|Strong/.test(t), 'old band words gone');
  const left = labels.map(n => parseFloat(n.style.left));
  assert.ok(left.every((v, i) => i === 0 || v > left[i - 1]), 'labels in order: ' + left);
  // "Above normal" marks where the colour starts (-44, band 1), "High" the -62 band (5).
  assert.strictEqual(left[0], 0);
  assert.strictEqual(left[1], stops[2].at, 'Above normal at the start of the first colour');
  assert.strictEqual(left[2], stops[10].at, 'High at the start of the -62 band');
  assert.strictEqual(left[3], 100);
  assert.ok(/mc-duct-scale-label-end/.test(labels[3].className), 'Very high right-aligned at the end');
  const sw = find(box, n => /mc-duct-swatch-/.test(n.className));
  assert.deepStrictEqual(sw.map(n => n.className), ['mc-duct-swatch mc-duct-swatch-lora', 'mc-duct-swatch mc-duct-swatch-edge']);
  assert.ok(s.map._controls.length === 1 && s.map._controls[0].options.position === 'bottomleft', 'one bottom-left control');
});

test('pending status shows the preparing message and draws nothing', async () => {
  const s = await mount({ status: 'pending' });
  const t = legendText(s);
  assert.ok(t.includes('Ducting forecast is being prepared'), t);
  assert.ok(t.includes('Indicator only'));
  assert.strictEqual(s.layer._cells.length, 0);
  assert.strictEqual(s.stats.polys.length, 0);
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

test('failed or pending refresh keeps the last good data drawn', async () => {
  const s = await mount(SAMPLE, T0);
  assert.strictEqual(s.timers.length, 1);
  assert.strictEqual(s.timers[0].ms, 30 * 60 * 1000);
  s.timers[0].fn();
  assert.strictEqual(s.api.calls.length, 2);
  s.api.calls[1].fail();
  await tick(); await tick();
  assert.strictEqual(s.layer._cells.length, 3);
  assert.deepStrictEqual(loraSet(s), ['51.25,0.25']);
  assert.ok(s.map._layers.includes(s.stats.fields[0]), 'field kept');
  assert.ok(!legendText(s).includes('unavailable'));
  s.timers[0].fn();
  s.api.calls[2].ok({ status: 'pending' });
  await tick(); await tick();
  assert.ok(!legendText(s).includes('being prepared'));
  assert.ok(legendText(s).includes('Updated '));
  assert.strictEqual(s.layer._group.layers.length, 2, 'the edge and LoRa outlines');
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
  assert.strictEqual(s.layer._cells.length, 3);
  assert.strictEqual(s.timers.length, 1);
  assert.strictEqual(s.timers[0].ms, 30 * 60 * 1000, 'back to the normal refresh');
  s.layer.onRemove(s.map);
  assert.strictEqual(s.timers.length, 0);
});

test('a refresh with a new forecast rebuilds and keeps a user-picked time', async () => {
  const s = await mount(SAMPLE, T1 + 60);
  s.layer._tl.fire('keydown', { key: 'ArrowRight' });
  assert.ok(s.layer._userPicked && s.layer._idx === 2);
  s.timers[0].fn();
  const body = clone(SAMPLE);
  body.generatedAt = SAMPLE.generatedAt + 3600;
  s.api.calls[1].ok(body);
  await tick(); await tick();
  assert.strictEqual(s.layer._idx, 2);
  assert.strictEqual(s.layer._data.generatedAt, body.generatedAt);
  assert.strictEqual(s.stats.lines.length, 2, 'edge rebuilt');
  assert.strictEqual(s.layer._group.layers.length, 2, 'old edge and outline cleared, new ones added');
  assert.strictEqual(s.stats.fields.length, 1, 'same field layer');
  assert.strictEqual(s.stats.fields[0].setBoundsCalls, 0);
});

test('a refresh with the same generatedAt rebuilds nothing, keeps an open popup and clears the out-of-date notice', async () => {
  const realNow = Date.now;
  const s = await mount(SAMPLE, T1 + 60);
  try {
    Date.now = () => (T1 + 60) * 1000;
    click(s, 51.25, 0.25); await tick();
    const popup = s.stats.popups[0];
    assert.ok(popup.open);
    s.timers[0].fn();
    s.api.calls[1].fail();                                   // out of date ...
    await tick(); await tick();
    assert.ok(legendText(s).includes('out of date'));
    const data = s.layer._data, lines = s.stats.lines.length, polys = s.stats.polys.length, puts = s.layer._canvas.puts;
    s.timers[0].fn();
    s.api.calls[2].ok(clone(SAMPLE));                        // ... then the same forecast again
    await tick(); await tick();
    assert.ok(!legendText(s).includes('out of date'), 'notice cleared');
    assert.strictEqual(s.layer._data, data, 'held data kept');
    assert.strictEqual(s.stats.lines.length, lines, 'edge not rebuilt');
    assert.strictEqual(s.stats.polys.length, polys, 'outline not rebuilt');
    assert.strictEqual(s.layer._canvas.puts, puts, 'field not redrawn');
    assert.ok(popup.open && s.map._popup === popup, 'popup still open');
    // Same forecast later on: the hour moves on (no user pick), still no rebuild.
    Date.now = () => (T2 + 60) * 1000;
    s.timers[0].fn();
    s.api.calls[3].ok(clone(SAMPLE));
    await tick(); await tick();
    assert.strictEqual(s.layer._idx, 2, 'hour moved on');
    assert.strictEqual(s.stats.lines.length, lines, 'still not rebuilt');
    assert.ok(/Elevated duct/.test(popupText(s)) && /Base: 1200 m/.test(popupText(s)), 'popup shows the new hour: ' + popupText(s));
  } finally { Date.now = realNow; }
});

test('a refresh with a larger area moves the field bounds and resizes its canvas', async () => {
  const s = await mount(SAMPLE, T1 + 60);
  const body = clone(SAMPLE);
  body.cells.push({ lat: 52.75, lon: 1.25, c: [4, 4, 4], f: [300, 300, 300], b: [0, 0, 0], t: [90, 90, 90], dm: [80, 80, 80], g: [-400, -400, -400], n: [-95, -95, -95] });
  body.generatedAt += 1800;
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
  assert.strictEqual(layer._cells.length, 0);
  assert.strictEqual(s.stats.lines.length + s.stats.polys.length, 0, 'no outlines after remove');
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

test('remove clears the panel listeners; a late failure and a gone map never throw', async () => {
  const s = makeSandbox();
  const map = makeMap();
  const layer = s.ctx.MC_createDuctingLayer({});
  layer.onAdd(map);
  const panel = layer._panelEl;
  assert.ok(listenerCount(panel) > 0);
  map._gone = true;
  assert.doesNotThrow(() => layer.onRemove(map));
  assert.strictEqual(listenerCount(panel), 0, 'listeners left on the panel');
  assert.strictEqual(panel.parentNode, null, 'panel left in the corner');
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
  assert.strictEqual(layer._cells.length, 0, 'stale generation ignored');
  assert.strictEqual(s.stats.lines.length, 0);
  s.api.calls[1].ok(SAMPLE);
  await tick(); await tick();
  assert.strictEqual(layer._cells.length, 3);
  assert.strictEqual(s.api.calls[0].url, '/api/ducting');
});

test('re-add then pending: cells and field are drawn from the kept data at once, and not called out of date', async () => {
  const realNow = Date.now;
  const s = await mount(SAMPLE, T0);
  try {
    Date.now = () => (T0 + 60) * 1000;
    s.layer.onRemove(s.map);
    s.stats.lines.length = 0; s.stats.fields.length = 0;
    s.layer.onAdd(s.map);
    assert.strictEqual(s.layer._cells.length, 3, 'cells listed before any answer');
    assert.strictEqual(s.stats.lines.length, 1, 'edge drawn before any answer');
    assert.ok(s.map._layers.some(l => l._mcField), 'field drawn before any answer');
    assert.ok(!legendText(s).includes('out of date'), 'nothing wrong yet');
    s.api.calls[s.api.calls.length - 1].ok({ status: 'pending' });
    await tick(); await tick();
    assert.strictEqual(s.layer._cells.length, 3);
    assert.ok(s.map._layers.some(l => l._mcField), 'field kept');
    assert.ok(!legendText(s).includes('out of date'), 'a restarting server answers pending: the held forecast is still valid');
  } finally { Date.now = realNow; }
});

test('re-add then fetch failure: the kept data stays drawn', async () => {
  const s = await mount(SAMPLE, T0);
  s.layer.onRemove(s.map);
  s.layer.onAdd(s.map);
  s.api.calls[s.api.calls.length - 1].fail();
  await tick(); await tick();
  assert.strictEqual(s.layer._cells.length, 3);
  assert.ok(s.map._layers.some(l => l._mcField));
  assert.ok(!legendText(s).includes('unavailable'));
  assert.ok(legendText(s).includes('out of date'), 'a failed request marks it out of date');
});

test('re-add then success: the kept data is replaced by the new data', async () => {
  const s = await mount(SAMPLE, T0);
  s.layer.onRemove(s.map);
  s.stats.lines.length = 0;
  s.layer.onAdd(s.map);
  assert.strictEqual(s.stats.lines.length, 1, 'kept data first');
  const body = clone(SAMPLE);
  body.generatedAt = T2;
  s.api.calls[s.api.calls.length - 1].ok(body);
  await tick(); await tick();
  assert.strictEqual(s.stats.lines.length, 2, 'rebuilt from the new answer');
  assert.strictEqual(s.layer._data.generatedAt, T2);
  assert.ok(s.layer._cells.every(c => body.cells.includes(c)), 'cells of the new answer');
});

test('re-add re-picks the hour; a user-picked hour is kept while it exists', async () => {
  const s = await mount(SAMPLE, T0);
  assert.strictEqual(s.layer._idx, 0);
  s.layer.onRemove(s.map);
  const realNow = Date.now;
  Date.now = () => (T2 + 5) * 1000;
  try {
    s.layer.onAdd(s.map);
    assert.strictEqual(s.layer._idx, 2, 'latest hour <= now, not the old one');
    s.layer._userPicked = true;
    s.layer._setIndex(1);
    s.layer.onRemove(s.map);
    s.layer.onAdd(s.map);
    assert.strictEqual(s.layer._idx, 1, 'picked hour kept');
  } finally { Date.now = realNow; }
});

test('out-of-date notice: on error and failure while old data is drawn, not on pending; gone on the next ok', async () => {
  const realNow = Date.now;
  const s = await mount(SAMPLE, T0);
  const MSG = 'Forecast out of date \u2014 showing the last one received';
  const refresh = async (body) => {
    s.timers[0].fn();
    const c = s.api.calls[s.api.calls.length - 1];
    if (body) c.ok(body); else c.fail();
    await tick(); await tick();
  };
  try {
    Date.now = () => (T0 + 60) * 1000;
    assert.ok(!legendText(s).includes('out of date'));
    await refresh({ status: 'pending' });
    assert.ok(!legendText(s).includes('out of date'), 'pending alone: still valid');
    for (const body of [{ status: 'error', error: 'forecast expired' }, { status: 'error', error: 'boom' }, null, { status: 'pending' }]) {
      await refresh(body);
      assert.ok(legendText(s).includes(MSG), JSON.stringify(body));
      assert.strictEqual(s.layer._cells.length, 3, 'old data still drawn');
      assert.strictEqual(statusOf(s).text, MSG, 'the one status line');
    }
    const body = clone(SAMPLE); body.generatedAt += 60;
    await refresh(body);
    assert.ok(!legendText(s).includes('out of date'), 'cleared by ok');
    assert.deepStrictEqual([statusOf(s).text, statusOf(s).el.hidden], ['', true], 'nothing to say: hidden and empty');
  } finally { Date.now = realNow; }
});

// The panel's one status line: { el, text shown ('' while hidden) }. Throws
// unless there is exactly one, with role=status, in the always-visible
// forecast-time block and not in the legend (which collapse hides).
function statusOf(s) {
  const panel = s.layer._panelEl;
  const all = find(panel, n => n.attrs && n.attrs.role === 'status');
  assert.strictEqual(all.length, 1, 'one role=status element in the panel');
  const el = all[0];
  assert.strictEqual(el.className, 'mc-duct-status');
  assert.strictEqual(el.parentNode, one(panel, 'mc-duct-time'), 'in the forecast-time block');
  assert.strictEqual(find(s.layer._legendEl, n => n === el).length, 0, 'not inside the legend');
  assert.ok(!one(panel, 'mc-duct-time').hidden, 'its block is never hidden');
  return { el, text: el.hidden ? '' : el.textContent };
}

test('status line: a collapsed panel still says out of date, being prepared or unavailable; nothing when all is well', async () => {
  const realNow = Date.now;
  const MSG = 'Forecast out of date \u2014 showing the last one received';
  // Phones start collapsed; a wide screen collapsed by a click.
  for (const narrow of [true, false]) {
    const collapsed = (s) => {
      if (!narrow) s.layer._collapseEl.fire('click');
      assert.strictEqual(s.layer._legendEl.hidden, true, 'legend collapsed');
      assert.ok(/mc-duct-panel-collapsed/.test(s.layer._panelEl.className));
      return s;
    };
    // Out of date: a failed refresh while a forecast is drawn.
    const s = collapsed(await mount(SAMPLE, T0, null, { narrow }));
    assert.deepStrictEqual([statusOf(s).text, statusOf(s).el.hidden], ['', true], 'fresh forecast: no status');
    try {
      Date.now = () => (T0 + 60) * 1000;
      s.timers[0].fn();
      s.api.calls[s.api.calls.length - 1].fail();
      await tick(); await tick();
    } finally { Date.now = realNow; }
    assert.strictEqual(s.layer._stale, true);
    assert.strictEqual(statusOf(s).text, MSG);
    assert.strictEqual(statusOf(s).el.hidden, false);
    assert.ok(!s.layer._legendEl.textContent.includes('out of date'), 'not repeated in the legend');
    // Expanding and collapsing changes nothing about it.
    s.layer._collapseEl.fire('click');
    assert.strictEqual(statusOf(s).text, MSG);
    s.layer._collapseEl.fire('click');
    assert.strictEqual(statusOf(s).text, MSG);
    // No forecast yet: loading, then being prepared / unavailable.
    const p = makeSandbox({ narrow });
    const map = makeMap();
    const layer = p.ctx.MC_createDuctingLayer({});
    layer.onAdd(map);
    Object.assign(p, { map, layer });
    collapsed(p);
    assert.strictEqual(statusOf(p).text, 'Loading ducting forecast');
    p.api.calls[0].ok({ status: 'pending' });
    await tick(); await tick();
    assert.strictEqual(statusOf(p).text, 'Ducting forecast is being prepared');
    p.timers[0].fn();
    p.api.calls[1].ok({ status: 'error', error: 'boom' });
    await tick(); await tick();
    assert.strictEqual(statusOf(p).text, 'Ducting forecast unavailable');
    p.timers[0].fn();
    p.api.calls[2].fail();
    await tick(); await tick();
    assert.strictEqual(statusOf(p).text, 'Ducting forecast unavailable');
    assert.strictEqual(p.layer._legendEl.hidden, true, 'still collapsed');
    // The forecast arrives: the status goes.
    try {
      Date.now = () => (T0 + 60) * 1000;
      p.timers[0].fn();
      p.api.calls[3].ok(clone(SAMPLE));
      await tick(); await tick();
    } finally { Date.now = realNow; }
    assert.deepStrictEqual([statusOf(p).text, statusOf(p).el.hidden], ['', true]);
  }
});

test('status line: the same element is kept (a live region), rewritten only when the text changes; gone with the panel', async () => {
  const s = await mount({ status: 'pending' });
  const el = statusOf(s).el;
  let writes = 0, text = el.textContent;
  Object.defineProperty(el, 'textContent', { get() { return text; }, set(v) { text = String(v); writes++; } });
  s.timers[0].fn();
  s.api.calls[1].ok({ status: 'pending' });
  await tick(); await tick();
  assert.strictEqual(writes, 0, 'same message: not written again (no repeat announcement)');
  s.timers[0].fn();
  s.api.calls[2].fail();
  await tick(); await tick();
  assert.strictEqual(writes, 1);
  assert.strictEqual(text, 'Ducting forecast unavailable');
  assert.strictEqual(statusOf(s).el, el);
  s.layer.onRemove(s.map);
  assert.strictEqual(s.layer._statusEl, null);
});

test('out-of-date notice: shown once the held forecast has run out, whatever the server says', async () => {
  const realNow = Date.now;
  const s = await mount(SAMPLE, T0);
  try {
    const last = SAMPLE.times[SAMPLE.times.length - 1];
    Date.now = () => (last + 3599) * 1000;                  // inside the last hour: fine
    s.timers[0].fn();
    s.api.calls[s.api.calls.length - 1].ok({ status: 'pending' });
    await tick(); await tick();
    assert.ok(!legendText(s).includes('out of date'), 'last hour still running');
    Date.now = () => (last + 3600) * 1000;                   // the last hour has ended
    for (const body of [{ status: 'pending' }, clone(SAMPLE)]) {
      s.timers[0].fn();
      s.api.calls[s.api.calls.length - 1].ok(body);
      await tick(); await tick();
      assert.ok(legendText(s).includes('out of date'), 'run out, after ' + body.status);
    }
    const fresh = await mount(SAMPLE, last + 7200);          // first answer already run out
    assert.ok(legendText(fresh).includes('out of date'));
  } finally { Date.now = realNow; }
});

test('an ok answer re-picks the hour, keeping a user-picked one that is still there', async () => {
  const s = await mount(SAMPLE, T0);
  const realNow = Date.now;
  Date.now = () => (T2 + 5) * 1000;
  try {
    s.timers[0].fn();
    s.api.calls[s.api.calls.length - 1].ok(clone(SAMPLE));
    await tick(); await tick();
    assert.strictEqual(s.layer._idx, 2);
  } finally { Date.now = realNow; }
});

// ── legend text, compact legend ─────────────────────────────────────────────

test('legend shows only the attribution, not the model twice', async () => {
  const s = await mount(SAMPLE);
  const t = legendText(s);
  assert.strictEqual(t.split('NOAA GFS').length - 1, 1, 'model name once (inside the attribution)');
  assert.ok(t.includes('Weather data by Open-Meteo.com (CC BY 4.0), NOAA GFS'));
  assert.ok(!/NOAA GFS\. /.test(t));
});

test('legend is collapsible and starts collapsed on narrow screens; header and timeline stay', async () => {
  const s = await mount(SAMPLE, T1 + 60, null, { narrow: true });
  const btn = s.layer._collapseEl, body = s.layer._legendEl, panel = s.layer._panelEl;
  assert.strictEqual(btn.attrs['aria-expanded'], 'false');
  assert.strictEqual(btn.attrs['aria-label'], 'Expand ducting legend');
  assert.strictEqual(body.hidden, true, 'body hidden when compact');
  assert.ok(/mc-duct-panel-collapsed/.test(panel.className) && /leaflet-control/.test(panel.className));
  assert.ok(body.textContent.includes('Indicator only'), 'content kept, only hidden');
  btn.fire('click');
  assert.strictEqual(btn.attrs['aria-expanded'], 'true');
  assert.strictEqual(btn.attrs['aria-label'], 'Collapse ducting legend');
  assert.strictEqual(body.hidden, false);
  assert.ok(!/mc-duct-panel-collapsed/.test(panel.className) && /leaflet-control/.test(panel.className), panel.className);
  btn.fire('click');
  assert.strictEqual(body.hidden, true);
  assert.ok(!one(panel, 'mc-duct-time').hidden && !one(panel, 'mc-duct-head').hidden, 'timeline and header stay');
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

// ── panel and timeline ──────────────────────────────────────────────────────

// SAMPLE stretched to n hourly times (the cells' per-time arrays repeat).
function longBody(n) {
  const b = clone(SAMPLE);
  b.times = Array.from({ length: n }, (_, i) => T0 + i * 3600);
  b.cells.forEach((c) => {
    Object.keys(c).forEach((k) => { if (Array.isArray(c[k])) { const a = c[k]; c[k] = Array.from({ length: n }, (_, i) => a[i % a.length]); } });
  });
  return b;
}
const xsOf = (s) => s.layer._tlLayout.xs;

test('MC_ductTimelineKeyIndex: arrows, page by 24 h (at least one step), home/end, clamped; other keys -1', () => {
  const { ctx } = makeSandbox();
  const ki = ctx.MC_ductTimelineKeyIndex;
  const lay = ctx.MC_ductTimelineLayout(Array.from({ length: 73 }, (_, i) => T0 + i * 3600));
  const xs = lay.xs, pps = lay.pxPerSec;
  assert.strictEqual(ki('ArrowRight', 5, xs, pps), 6);
  assert.strictEqual(ki('ArrowUp', 5, xs, pps), 6);
  assert.strictEqual(ki('ArrowLeft', 5, xs, pps), 4);
  assert.strictEqual(ki('ArrowDown', 5, xs, pps), 4);
  assert.strictEqual(ki('ArrowLeft', 0, xs, pps), 0, 'clamped at the first');
  assert.strictEqual(ki('ArrowRight', 72, xs, pps), 72, 'clamped at the last');
  assert.strictEqual(ki('PageUp', 5, xs, pps), 29);
  assert.strictEqual(ki('PageDown', 29, xs, pps), 5);
  assert.strictEqual(ki('PageUp', 60, xs, pps), 72);
  assert.strictEqual(ki('PageDown', 10, xs, pps), 0);
  assert.strictEqual(ki('Home', 40, xs, pps), 0);
  assert.strictEqual(ki('End', 4, xs, pps), 72);
  assert.strictEqual(ki('Enter', 4, xs, pps), -1);
  assert.strictEqual(ki('End', 0, [], pps), -1);
  assert.strictEqual(ki('ArrowRight', -1, xs, pps), 1, 'no index yet counts as the first');
  // A two-day gap: PageUp still moves one step, never stays put.
  const gap = ctx.MC_ductTimelineLayout([T0, T0 + 3600, T0 + 3 * 86400]);
  assert.strictEqual(ki('PageUp', 0, gap.xs, gap.pxPerSec), 1);
  assert.strictEqual(ki('PageDown', 2, gap.xs, gap.pxPerSec), 1);
});

test('MC_ductTimelineDaySpans: start to next start, the last to the end; a too-short first day is left out', () => {
  const { ctx } = makeSandbox();
  const sp = (lay, min) => JSON.parse(JSON.stringify(ctx.MC_ductTimelineDaySpans(lay, min)));
  const lay = { width: 600, days: [{ x: 0, sec: 1 }, { x: 40, sec: 2 }, { x: 280, sec: 3 }, { x: 520, sec: 4 }] };
  assert.deepStrictEqual(sp(lay, 96), [{ x: 40, w: 240, sec: 2 }, { x: 280, w: 240, sec: 3 }, { x: 520, w: 80, sec: 4 }],
    'the 40 px first day goes; the last keeps its short span (nothing follows it)');
  assert.deepStrictEqual(sp(lay, 30).map(d => d.w), [40, 240, 240, 80]);
  assert.deepStrictEqual(sp({ width: 0, days: [] }, 96), []);
  assert.deepStrictEqual(sp(null, 96), []);
  // Spans tile the strip: each ends where the next starts.
  const real = ctx.MC_ductTimelineLayout(Array.from({ length: 73 }, (_, i) => T0 + 5 * 3600 + i * 3600));
  const r = sp(real, 96);
  for (let k = 0; k + 1 < r.length; k++) assert.strictEqual(r[k].x + r[k].w, r[k + 1].x);
  assert.strictEqual(r[r.length - 1].x + r[r.length - 1].w, real.width);
});

test('MC_ductPanelPrefs: validated, clamped, unreadable is nothing stored', () => {
  const { ctx } = makeSandbox();
  const pp = (v) => JSON.parse(JSON.stringify(ctx.MC_ductPanelPrefs(v)));
  const none = { collapsed: null, pos: null };
  assert.deepStrictEqual(pp(JSON.stringify({ collapsed: true, pos: { fx: 0.25, fy: 1 } })), { collapsed: true, pos: { fx: 0.25, fy: 1 } });
  assert.deepStrictEqual(pp(JSON.stringify({ collapsed: false, pos: null })), { collapsed: false, pos: null });
  assert.deepStrictEqual(pp(JSON.stringify({ pos: { fx: -2, fy: 7 } })), { collapsed: null, pos: { fx: 0, fy: 1 } });
  assert.deepStrictEqual(pp(JSON.stringify({ collapsed: 'yes', pos: { fx: '0.5', fy: 0.5 } })), none);
  for (const v of [null, undefined, '', '{', 'null', '42', '[]', 7, {}]) assert.deepStrictEqual(pp(v), none, String(v));
});

// L.Layer's own methods (L.Evented and L.Layer, Leaflet 1.9.4): overriding
// one breaks Leaflet silently (a DOM helper named _on swallowed layer.on()
// calls, so the layers control never heard 'add' and the overlay pick was
// not saved).
test('the layer overrides none of L.Evented / L.Layer\'s methods', () => {
  const s = makeSandbox();
  const proto = Object.getPrototypeOf(s.ctx.MC_createDuctingLayer({}));
  const leaflet = ['on', 'off', 'fire', 'once', 'listens', 'addEventParent', 'removeEventParent', 'addEventListener', 'removeEventListener',
    'clearAllEventListeners', 'addOneTimeEventListener', 'fireEvent', 'hasEventListeners', '_on', '_off', '_listens', '_propagateEvent',
    'addTo', 'remove', 'removeFrom', 'getPane', 'addInteractiveTarget', 'removeInteractiveTarget', 'getAttribution', '_layerAdd',
    'bindPopup', 'unbindPopup', 'openPopup', 'closePopup', 'togglePopup', 'isPopupOpen', 'setPopupContent', 'getPopup', '_openPopup',
    'bindTooltip', 'unbindTooltip', 'openTooltip', 'closeTooltip', 'toggleTooltip', 'isTooltipOpen', 'setTooltipContent', 'getTooltip'];
  const clash = leaflet.filter(k => Object.prototype.hasOwnProperty.call(proto, k));
  assert.deepStrictEqual(clash, []);
});

test('panel: one control holding header, timeline and legend body, in order, wired for assistive tech', async () => {
  const s = await mount(SAMPLE, T1 + 60);
  assert.strictEqual(s.map._controls.length, 1);
  const panel = s.map._controls[0]._container;
  assert.strictEqual(panel, s.layer._panelEl);
  assert.ok(/(^| )mc-duct-panel( |$)/.test(panel.className));
  assert.deepStrictEqual(panel.children.map(c => c.className), ['mc-duct-head', 'mc-duct-time', 'mc-duct-legend']);
  assert.strictEqual(byClass(panel, 'mc-duct-legend').length, 1);
  assert.strictEqual(byClass(panel, 'mc-duct-time').length, 1);
  const head = panel.children[0];
  assert.deepStrictEqual(head.children.map(c => c.className), ['mc-duct-drag', 'mc-duct-title', 'mc-duct-reset', 'mc-duct-collapse']);
  assert.strictEqual(one(head, 'mc-duct-title').textContent, 'Tropospheric ducting');
  assert.strictEqual(one(head, 'mc-duct-drag').attrs['aria-label'], 'Move ducting panel');
  assert.strictEqual(one(head, 'mc-duct-reset').hidden, true, 'reset only once moved');
  assert.strictEqual(one(head, 'mc-duct-reset').attrs['aria-label'], 'Reset position');
  const coll = one(head, 'mc-duct-collapse');
  assert.strictEqual(coll.attrs['aria-controls'], s.layer._legendEl.id);
  assert.ok(/^mc-duct-legend-\d+$/.test(s.layer._legendEl.id));
  assert.strictEqual(coll.attrs['aria-expanded'], 'true');
  head.children.forEach(b => assert.strictEqual(b.tagName === 'button' ? b.type : 'button', 'button'));
  const vp = s.layer._tl;
  assert.ok(/(^| )mc-duct-timeline( |$)/.test(vp.className));
  assert.deepStrictEqual([vp.attrs.role, vp.attrs.tabindex, vp.attrs['aria-label'], vp.attrs['aria-valuemin'], vp.attrs['aria-valuemax'], vp.attrs['aria-valuenow']],
    ['slider', '0', 'Ducting forecast time', '0', '2', '1']);
  assert.ok(!('aria-disabled' in vp.attrs));
  assert.strictEqual(vp.attrs['aria-valuetext'], labelText(s.layer).replace('Forecast time: ', ''));
  // Forecast-time line: the label (prefix that may shorten, the time) and Now; then ◀ strip ▶.
  const time = one(panel, 'mc-duct-time');
  assert.deepStrictEqual(time.children.map(c => c.className), ['mc-duct-time-head', 'mc-duct-status', 'mc-duct-tl-row']);
  assert.deepStrictEqual([time.children[1].attrs.role, time.children[1].hidden], ['status', true], 'status line, hidden while there is nothing to say');
  const thead = time.children[0];
  assert.deepStrictEqual(thead.children.map(c => c.className), ['mc-duct-time-label', 'mc-duct-now']);
  assert.deepStrictEqual(thead.children[0].children.map(c => c.className), ['mc-duct-time-prefix', 'mc-duct-time-value']);
  assert.strictEqual(one(thead, 'mc-duct-time-value').textContent, vp.attrs['aria-valuetext'], 'the time is its own span');
  assert.strictEqual(s.layer._timeLabel.title, labelText(s.layer), 'full text on hover');
  assert.strictEqual(one(thead, 'mc-duct-now').textContent, 'Now');
  const row = one(panel, 'mc-duct-tl-row');
  assert.deepStrictEqual(row.children.map(c => c.className.split(' ').pop()), ['mc-duct-step-prev', 'mc-duct-timeline-wrap', 'mc-duct-step-next']);
  assert.strictEqual(one(row, 'mc-duct-step-prev').attrs['aria-label'], 'Earlier');
  assert.strictEqual(one(row, 'mc-duct-step-next').attrs['aria-label'], 'Later');
  assert.strictEqual(byClass(panel, 'mc-duct-timeline-cursor').length, 1);
  // The map never sees a press on the panel.
  assert.ok(panel.fire('pointerdown').stopped);
});

test('timeline: one tick per time placed by time, day and hour labels, now line; built only when the times change', async () => {
  const body = longBody(49);
  const s = await mount(body, T0 + 5 * 3600 + 1800);
  const inner = s.layer._tlInner, lay = s.layer._tlLayout;
  const ticks = byClass(inner, 'mc-duct-tick');
  assert.strictEqual(ticks.length, 49);
  assert.deepStrictEqual(ticks.map(t => parseFloat(t.style.left)), Array.from(lay.xs));
  assert.strictEqual(s.layer._tlTrack.style.width, lay.width + 'px');
  const days = byClass(inner, 'mc-duct-day');
  assert.ok(days.length >= 2, 'two days in 48 h');
  const spans = s.ctx.MC_ductTimelineDaySpans(lay, 96);
  assert.deepStrictEqual(days.map(d => [parseFloat(d.style.left), parseFloat(d.style.width)]), Array.from(spans, d => [d.x, d.w]), 'one span per day, start to next start');
  assert.ok(days.every(d => d.children.length === 1 && d.children[0].className === 'mc-duct-day-label'), 'label inside its span (sticky)');
  const d0 = new Date(lay.days[lay.days.length - 1].sec * 1000).toLocaleDateString([], { weekday: 'short', day: 'numeric', month: 'short' });
  assert.strictEqual(days[days.length - 1].textContent, d0);
  const dayTicks = ticks.filter(t => /mc-duct-tick-day/.test(t.className));
  assert.strictEqual(dayTicks.length, lay.days.filter(d => body.times.includes(d.sec)).length);
  const hours = byClass(inner, 'mc-duct-hour');
  assert.strictEqual(hours.length, lay.hours.length);
  assert.strictEqual(hours[0].textContent, new Date(lay.hours[0].sec * 1000).toLocaleTimeString([], { hour: '2-digit' }));
  const now = one(inner, 'mc-duct-now-line');
  assert.ok(!now.hidden);
  assert.strictEqual(parseFloat(now.style.left), (5.5 * 3600) * lay.pxPerSec);
  // A new forecast with the same times: same strip nodes.
  const first = ticks[0];
  s.timers[0].fn();
  const same = clone(body); same.generatedAt += 3600;
  s.api.calls[1].ok(same);
  await tick(); await tick();
  assert.strictEqual(byClass(s.layer._tlInner, 'mc-duct-tick')[0], first, 'strip not rebuilt');
  // Other times: rebuilt.
  s.timers[0].fn();
  const other = longBody(25); other.generatedAt += 7200;
  s.api.calls[2].ok(other);
  await tick(); await tick();
  assert.strictEqual(byClass(s.layer._tlInner, 'mc-duct-tick').length, 25);
  assert.notStrictEqual(byClass(s.layer._tlInner, 'mc-duct-tick')[0], first);
  // Now outside the times: no now line.
  const late = await mount(SAMPLE, T2 + 7200);
  assert.strictEqual(one(late.layer._tlInner, 'mc-duct-now-line').hidden, true);
});

test('timeline: keys and buttons pick a time: label, aria, field, strip position; user-picked; ends disable the steps', async () => {
  const s = await mount(longBody(49), T0 + 3600);
  const L0 = s.layer, vp = L0._tl, xs = xsOf(s);
  assert.strictEqual(L0._idx, 1);
  assert.strictEqual(vp.scrollLeft, xs[1], 'strip at the shown time');
  assert.ok(!L0._userPicked);
  const puts = L0._canvas.puts;
  const key = (k) => vp.fire('keydown', { key: k });
  let ev = key('ArrowRight');
  assert.ok(ev.defaultPrevented && ev.stopped);
  assert.strictEqual(L0._idx, 2);
  assert.ok(L0._userPicked);
  assert.strictEqual(L0._canvas.puts, puts + 1, 'one redraw');
  assert.strictEqual(vp.attrs['aria-valuenow'], '2');
  assert.strictEqual(vp.attrs['aria-valuetext'], labelText(L0).replace('Forecast time: ', ''));
  assert.strictEqual(vp.scrollLeft, xs[2]);
  key('PageUp'); assert.strictEqual(L0._idx, 26);
  key('PageDown'); assert.strictEqual(L0._idx, 2);
  key('End'); assert.strictEqual(L0._idx, 48);
  assert.ok(L0._nextEl.disabled && !L0._prevEl.disabled, 'Later off at the last time');
  key('ArrowRight'); assert.strictEqual(L0._idx, 48, 'clamped');
  key('Home'); assert.strictEqual(L0._idx, 0);
  assert.ok(L0._prevEl.disabled && !L0._nextEl.disabled, 'Earlier off at the first time');
  ev = key('a');
  assert.ok(!ev.defaultPrevented, 'other keys pass');
  L0._nextEl.fire('click'); assert.strictEqual(L0._idx, 1);
  L0._nextEl.fire('click'); assert.strictEqual(L0._idx, 2);
  L0._prevEl.fire('click'); assert.strictEqual(L0._idx, 1);
  assert.strictEqual(vp.scrollLeft, xs[1]);
  // The scroll events of that positioning are not user picks.
  L0._userPicked = false;
  s.settleScroll();
  assert.ok(!L0._userPicked && L0._idx === 1);
});

// Regression (CI flake on e10f03e1, instrumented build): rapid keys under a
// slow browser restart smooth scrolls that have not moved yet; stepping back
// to the strip's current position used to drop the target while the earlier
// smooth scroll was still in flight, so its scroll events picked an
// in-between time as if the user had scrolled.
test('timeline: rapid keys during a smooth scroll never let its late scroll events pick a time', async () => {
  const s = await mount(longBody(49), T0 + 60);
  const L0 = s.layer, vp = L0._tl, xs = xsOf(s);
  s.settleScroll();
  assert.strictEqual(vp.scrollLeft, xs[0]);
  // A browser too slow to move a smooth scroll before the next key.
  const smooth = [];
  vp.scrollTo = (o) => { smooth.push(o.left); };
  const key = (k) => vp.fire('keydown', { key: k });
  key('ArrowRight');
  assert.strictEqual(L0._idx, 1);
  assert.deepStrictEqual(smooth, [xs[1]], 'the first step scrolls smoothly');
  assert.strictEqual(vp.scrollLeft, xs[0], 'not moved yet');
  key('ArrowRight');
  key('ArrowLeft');
  key('ArrowLeft');
  assert.strictEqual(L0._idx, 0);
  assert.strictEqual(smooth.length, 1, 'no second smooth scroll while one is in flight');
  assert.strictEqual(L0._tlTarget, xs[0], 'the target is kept while a scroll may still move the strip');
  // The first smooth scroll now moves the strip part way: not a user pick.
  vp.scrollLeft = Math.round(xs[1] * 0.6) || 1;
  s.settleScroll();
  assert.strictEqual(L0._idx, 0, 'late scroll events of code\'s own scroll pick nothing');
  // It stops short: the idle settle finishes on the target at once.
  s.runTimeouts();
  assert.strictEqual(vp.scrollLeft, xs[0]);
  s.settleScroll();
  assert.strictEqual(L0._tlTarget, null, 'arrived');
  assert.strictEqual(L0._idx, 0);
  assert.strictEqual(vp.attrs['aria-valuenow'], '0');
});

test('timeline: Now returns to the current hour and follows it again', async () => {
  const s = await mount(longBody(49), T0 + 10 * 3600 + 60);
  const L0 = s.layer;
  L0._tl.fire('keydown', { key: 'End' });
  assert.ok(L0._userPicked && L0._idx === 48);
  const realNow = Date.now;
  Date.now = () => (T0 + 10 * 3600 + 60) * 1000;
  try { L0._nowEl.fire('click'); } finally { Date.now = realNow; }
  assert.strictEqual(L0._idx, 10);
  assert.strictEqual(L0._tl.scrollLeft, xsOf(s)[10]);
  assert.strictEqual(L0._userPicked, false, 'refreshes follow the clock again');
});

test('timeline: a user scroll picks the time under the centre once per frame, then settles on it', async () => {
  const s = await mount(longBody(49), T0 + 60);
  const L0 = s.layer, vp = L0._tl, xs = xsOf(s);
  s.settleScroll();
  const puts = L0._canvas.puts;
  vp.fire('touchstart');
  vp.scrollLeft = xs[7] - 3;
  vp.fire('scroll'); vp.fire('scroll'); vp.fire('scroll');
  assert.strictEqual(s.frames.length, 1, 'coalesced to one frame');
  assert.strictEqual(L0._idx, 0, 'nothing before the frame');
  s.runFrames();
  assert.strictEqual(L0._idx, 7);
  assert.ok(L0._userPicked);
  assert.strictEqual(L0._canvas.puts, puts + 1);
  assert.strictEqual(vp.scrollLeft, xs[7] - 3, 'the strip stays under the finger');
  vp.scrollLeft = xs[7] + 2;     // same time under the centre: no redraw
  s.settleScroll();
  assert.strictEqual(L0._canvas.puts, puts + 1);
  s.runTimeouts();
  assert.strictEqual(vp.scrollLeft, xs[7] + 2, 'no settling while the finger is down');
  vp.fire('touchend');
  s.runTimeouts();
  assert.strictEqual(vp.scrollLeft, xs[7], 'settled on the time');
  s.settleScroll();
  assert.strictEqual(L0._tlTarget, null, 'settling done');
  assert.strictEqual(L0._idx, 7);
  // scrollend settles at once.
  vp.fire('touchstart'); vp.scrollLeft = xs[3] + 4; s.settleScroll(); vp.fire('touchend');
  vp.fire('scrollend');
  assert.strictEqual(vp.scrollLeft, xs[3]);
  assert.strictEqual(L0._idx, 3);
});

test('timeline: positioning from code (refresh, _setIndex) never counts as a user pick', async () => {
  const s = await mount(longBody(49), T0 + 60);
  const L0 = s.layer, vp = L0._tl, xs = xsOf(s);
  L0._setIndex(20);
  assert.strictEqual(vp.scrollLeft, xs[20]);
  s.settleScroll();
  s.runTimeouts();
  assert.ok(!L0._userPicked);
  assert.strictEqual(L0._idx, 20);
  // Interrupted by the user mid-way (wheel): their scroll counts.
  L0._setIndex(30);
  vp.fire('wheel', { deltaY: 40, deltaX: 0, deltaMode: 0 });
  s.settleScroll();
  assert.ok(L0._userPicked);
  assert.strictEqual(L0._idx, ductIdx(xs, xs[30] + 40));
});
const ductIdx = (xs, x) => { let b = 0; xs.forEach((v, i) => { if (Math.abs(v - x) < Math.abs(xs[b] - x)) b = i; }); return b; };

test('timeline: wheel scrolls sideways (map never zooms), mouse drag scrolls, a click picks the nearest time', async () => {
  const s = await mount(longBody(49), T0 + 60);
  const L0 = s.layer, vp = L0._tl, xs = xsOf(s);
  s.settleScroll();
  let ev = vp.fire('wheel', { deltaY: 100, deltaX: 0, deltaMode: 0 });
  assert.ok(ev.defaultPrevented);
  assert.strictEqual(vp.scrollLeft, 100);
  ev = vp.fire('wheel', { deltaY: 1, deltaX: 0, deltaMode: 1 });
  assert.strictEqual(vp.scrollLeft, 116, 'line mode: 16 px a line');
  ev = vp.fire('wheel', { deltaY: 2, deltaX: 30, deltaMode: 0 });
  assert.ok(!ev.defaultPrevented, 'sideways wheel scrolls natively');
  s.settleScroll();
  assert.strictEqual(L0._idx, ductIdx(xs, 116));
  s.runTimeouts();
  const at0 = vp.scrollLeft;
  // Mouse drag: left by 50 px -> later by 50 px; no click pick after it.
  vp.fire('pointerdown', { pointerType: 'mouse', button: 0, clientX: 300, pointerId: 1 });
  assert.strictEqual(vp.captured, 1);
  assert.ok(/mc-duct-timeline-dragging/.test(vp.className));
  vp.fire('pointermove', { clientX: 298 });
  assert.strictEqual(vp.scrollLeft, at0, 'under 4 px is not a drag');
  vp.fire('pointermove', { clientX: 250 });
  assert.strictEqual(vp.scrollLeft, at0 + 50);
  s.settleScroll();
  s.runTimeouts();
  assert.strictEqual(vp.scrollLeft, at0 + 50, 'no settling while the button is down');
  vp.fire('pointerup', { pointerId: 1 });
  vp.fire('click', { clientX: 250 });
  assert.strictEqual(L0._idx, ductIdx(xs, at0 + 50));
  s.runTimeouts();
  assert.strictEqual(vp.scrollLeft, xs[L0._idx], 'settled after the drag');
  // Touch: no drag handling, native scroll.
  vp.fire('pointerdown', { pointerType: 'touch', button: 0, clientX: 300, pointerId: 2 });
  assert.strictEqual(L0._tlDrag, null);
  // Click: viewport 200 px wide at x 0, centre at 100; 30 px right = 3 h later.
  s.settleScroll();
  vp.clientWidth = 200; vp.rect = { left: 0, top: 0 };
  const before = L0._idx;
  vp.fire('pointerdown', { pointerType: 'mouse', button: 0, clientX: 130, pointerId: 3 });
  vp.fire('pointerup', { pointerId: 3 });
  vp.fire('click', { clientX: 130 });
  assert.strictEqual(L0._idx, before + 3);
  assert.ok(L0._userPicked);
});

test('timeline: fewer than two times is disabled; a refresh keeps a user pick that still exists', async () => {
  const single = clone(SAMPLE);
  single.times = [T0];
  single.cells.forEach(c => Object.keys(c).forEach(k => { if (Array.isArray(c[k])) c[k] = c[k].slice(0, 1); }));
  const s1 = await mount(single, T0 + 60);
  assert.strictEqual(s1.layer._tl.attrs['aria-disabled'], 'true');
  assert.strictEqual(s1.layer._tl.attrs['aria-valuenow'], '0');
  assert.ok(s1.layer._prevEl.disabled && s1.layer._nextEl.disabled && !s1.layer._nowEl.disabled);
  assert.ok(!s1.layer._tl.fire('keydown', { key: 'End' }).defaultPrevented);
  assert.ok(!s1.layer._tl.fire('wheel', { deltaY: 50, deltaX: 0 }).defaultPrevented);

  const s = await mount(longBody(49), T0 + 60);
  s.layer._tl.fire('keydown', { key: 'End' });
  s.timers[0].fn();
  const b = longBody(60); b.generatedAt += 3600;
  s.api.calls[1].ok(b);
  await tick(); await tick();
  assert.strictEqual(s.layer._idx, 48, 'picked time kept');
  assert.strictEqual(s.layer._tl.attrs['aria-valuemax'], '59');
  assert.strictEqual(s.layer._tl.scrollLeft, xsOf(s)[48]);
});

test('collapse persists; stored state is restored and overrides the narrow default; storage null or throwing never breaks', async () => {
  let st = makeStorage();
  const s = await mount(SAMPLE, T1 + 60, null, { storage: st });
  s.layer._collapseEl.fire('click');
  assert.deepStrictEqual(JSON.parse(st.data['mc-duct-panel']), { collapsed: true, pos: null });
  s.layer._collapseEl.fire('click');
  assert.deepStrictEqual(JSON.parse(st.data['mc-duct-panel']), { collapsed: false, pos: null });
  // Narrow screen, stored expanded: expanded.
  const n1 = await mount(SAMPLE, T1 + 60, null, { storage: makeStorage({ 'mc-duct-panel': '{"collapsed":false,"pos":null}' }), narrow: true });
  assert.strictEqual(n1.layer._legendEl.hidden, false);
  // Wide screen, stored collapsed: collapsed.
  const w1 = await mount(SAMPLE, T1 + 60, null, { storage: makeStorage({ 'mc-duct-panel': '{"collapsed":true}' }) });
  assert.strictEqual(w1.layer._legendEl.hidden, true);
  // Nothing chosen yet: a move stores collapsed null, so phones keep their default.
  const m = await mount(SAMPLE, T1 + 60, null, { storage: st = makeStorage() });
  m.layer._panelEl.offsetWidth = 260; m.layer._panelEl.offsetHeight = 300;
  m.layer._dragEl.fire('keydown', { key: 'ArrowUp' });
  assert.strictEqual(JSON.parse(st.data['mc-duct-panel']).collapsed, null);
  // Garbage, null storage, throwing storage, no storage at all.
  for (const storage of [makeStorage({ 'mc-duct-panel': '{oops' }), null, makeStorage({}, true), undefined]) {
    const x = await mount(SAMPLE, T1 + 60, null, { storage });
    assert.strictEqual(x.layer._legendEl.hidden, false, 'wide default: expanded');
    assert.doesNotThrow(() => x.layer._collapseEl.fire('click'));
    assert.strictEqual(x.layer._legendEl.hidden, true);
    assert.doesNotThrow(() => x.layer._dragEl.fire('keydown', { key: 'ArrowDown' }));
  }
});

// The panel docked in the corner, 260 x 300, at (10, 390) in a 1000 x 700 map.
// Mounts with the strip not laid out (the map is in a display:none
// wrapper): scrollWidth 0, and scrollLeft does not move.
async function mountHidden(body, now, withRo) {
  const s = makeSandbox();
  const ros = [];
  if (withRo) {
    s.ctx.ResizeObserver = function (cb) { this.cb = cb; this.observe = (e) => { this.el = e; }; this.disconnect = () => { this.el = null; }; ros.push(this); };
  }
  const realNow = Date.now;
  Date.now = () => now * 1000;
  try {
    const map = makeMap();
    const layer = s.ctx.MC_createDuctingLayer({});
    layer.onAdd(map);
    layer._tl.scrollWidth = 0;
    s.api.calls[0].ok(body);
    await tick(); await tick();
    return Object.assign(s, { map, layer, ros });
  } finally { Date.now = realNow; }
}

test('timeline: a strip laid out late (hidden map) records no target and is positioned once it has a size', async () => {
  // The forecast started 5 hours ago.
  const s = await mountHidden(longBody(49), T0 + 5 * 3600 + 60);
  const L0 = s.layer, vp = L0._tl, xs = xsOf(s);
  assert.strictEqual(L0._idx, 5);
  assert.strictEqual(vp.scrollLeft, 0, 'nothing to scroll yet');
  assert.strictEqual(L0._tlTarget, null, 'no target without a layout: no scroll event would ever clear it');
  s.settleScroll(); s.runTimeouts();
  // Still hidden: a resize callback and a step change nothing either.
  L0._onPanelResize();
  L0._setIndex(6);
  assert.deepStrictEqual([vp.scrollLeft, L0._tlTarget], [0, null]);
  L0._setIndex(5);
  // The wrapper is shown: the strip has a size, the panel's resize fires.
  vp.scrollWidth = xs[48] + 200; vp.clientWidth = 200;
  L0._onPanelResize();
  assert.strictEqual(vp.scrollLeft, xs[5], 'the shown hour is under the centre marker');
  s.settleScroll(); s.runTimeouts();
  assert.strictEqual(L0._tlTarget, null);
  assert.strictEqual(L0._idx, 5);
  assert.ok(!L0._userPicked, 'positioning from code is not a user pick');
  assert.strictEqual(vp.attrs['aria-valuenow'], '5');
});

test('timeline: the panel\'s ResizeObserver is what positions a late-laid-out strip', async () => {
  const s = await mountHidden(longBody(49), T0 + 5 * 3600 + 60, true);
  const L0 = s.layer, vp = L0._tl, xs = xsOf(s);
  const ro = s.ros.filter(r => r.el === L0._panelEl);
  assert.strictEqual(ro.length, 1, 'one observer on the panel');
  ro[0].cb([]);                      // the initial callback, still hidden
  assert.deepStrictEqual([vp.scrollLeft, L0._tlTarget], [0, null]);
  vp.scrollWidth = xs[48] + 200; vp.clientWidth = 200;
  ro[0].cb([]);                      // display:none lifted: 0x0 -> a size
  assert.strictEqual(vp.scrollLeft, xs[5]);
  s.settleScroll(); s.runTimeouts();
  assert.deepStrictEqual([L0._tlTarget, L0._idx, !!L0._userPicked], [null, 5, false]);
  L0.onRemove(s.map);
  assert.strictEqual(ro[0].el, null, 'disconnected');
});

test('timeline: a code scroll that moves nothing never leaves its target behind', async () => {
  const s = await mount(longBody(49), T0 + 60);
  const L0 = s.layer, vp = L0._tl, xs = xsOf(s);
  s.settleScroll(); s.runTimeouts();
  // A smooth scroll the browser never starts, superseded by a step back
  // to where the strip already is: no scroll event at all.
  vp.scrollTo = () => {};
  vp.fire('keydown', { key: 'ArrowRight' });
  vp.fire('keydown', { key: 'ArrowLeft' });
  assert.strictEqual(L0._tlTarget, xs[0], 'kept while a scroll may still move the strip');
  assert.strictEqual(scrolled.size, 0, 'no scroll event is coming');
  s.runTimeouts();
  assert.strictEqual(L0._tlTarget, null, 'dropped by the idle timer');
  // A target set while laid out, then the map is hidden before the strip
  // moved: the idle timer cannot move it and drops the target.
  vp.fire('keydown', { key: 'ArrowRight' });
  assert.strictEqual(L0._tlTarget, xs[1]);
  vp.scrollWidth = 0;
  s.runTimeouts();
  assert.deepStrictEqual([vp.scrollLeft, L0._tlTarget], [xs[0], null]);
  // Shown again: the late-layout restore puts the picked hour under the centre.
  vp.scrollWidth = 100000;
  L0._onPanelResize();
  assert.strictEqual(vp.scrollLeft, xs[1]);
  s.settleScroll(); s.runTimeouts();
  assert.deepStrictEqual([L0._tlTarget, L0._idx], [null, 1]);
});

// ── the hour tick ───────────────────────────────────────────────────────────

test('hour tick: one timer aimed at the top of the hour moves the now line and the shown hour, without a request', async () => {
  assert.strictEqual(T0 % 3600, 0, 'the fixture starts on the hour');
  const realNow = Date.now;
  const s = await mount(longBody(49), T0 + 60);
  const L0 = s.layer, vp = L0._tl, xs = xsOf(s);
  const nowLeft = () => parseFloat(one(L0._panelEl, 'mc-duct-now-line').style.left);
  try {
    assert.strictEqual(s.hourTicks().length, 1, 'one timer');
    assert.strictEqual(s.hourTicks()[0].ms, 3540 * 1000, 'aimed at the next top of the hour');
    assert.strictEqual(s.timers.length, 1, 'no interval besides the refresh');
    assert.strictEqual(L0._idx, 0);
    const calls = s.api.calls.length, left0 = nowLeft();
    // Fired a moment early: nothing changes, re-armed for the remainder.
    Date.now = () => (T0 + 3600) * 1000 - 2;
    s.runHourTick();
    assert.strictEqual(L0._idx, 0);
    assert.deepStrictEqual(s.timeouts.map(t => t.ms), [2]);
    // The hour turns.
    Date.now = () => (T0 + 3600) * 1000 + 500;
    s.timeouts.shift().fn();
    assert.strictEqual(L0._idx, 1, 'the shown hour moved on');
    assert.strictEqual(vp.scrollLeft, xs[1], 'and the strip with it');
    assert.strictEqual(vp.attrs['aria-valuenow'], '1');
    assert.ok(nowLeft() > left0, 'the now line moved');
    assert.ok(!L0._userPicked);
    s.settleScroll(); s.runTimeouts();
    assert.ok(!L0._userPicked, 'the tick\'s own scroll is not a user pick');
    assert.strictEqual(s.api.calls.length, calls, 'no request');
    assert.strictEqual(s.hourTicks().length, 1, 're-armed, still one');
    assert.strictEqual(s.hourTicks()[0].ms, 3600 * 1000 - 500);
    assert.strictEqual(s.timers.length, 1);
    // A user-picked hour stays; the now line still moves.
    vp.fire('keydown', { key: 'End' });
    s.settleScroll(); s.runTimeouts();
    assert.ok(L0._userPicked && L0._idx === 48);
    const left1 = nowLeft();
    Date.now = () => (T0 + 2 * 3600) * 1000 + 5;
    s.runHourTick();
    assert.strictEqual(L0._idx, 48, 'the picked hour is left alone');
    assert.strictEqual(vp.scrollLeft, xs[48]);
    assert.ok(nowLeft() > left1);
    // Now: following the clock again.
    L0._nowEl.fire('click');
    s.settleScroll(); s.runTimeouts();
    assert.strictEqual(L0._idx, 2);
    Date.now = () => (T0 + 3 * 3600) * 1000 + 5;
    s.runHourTick();
    assert.strictEqual(L0._idx, 3);
    // The forecast's last hour ends: said at once, not at the next refresh.
    assert.ok(!legendText(s).includes('out of date'));
    Date.now = () => (T0 + 49 * 3600) * 1000 + 5;
    s.runHourTick();
    assert.strictEqual(statusOf(s).text, 'Forecast out of date — showing the last one received');
    assert.strictEqual(s.api.calls.length, calls);
    // Removing the overlay clears the timer; a callback already taken does nothing.
    const stale = s.hourTicks()[0].fn;
    L0.onRemove(s.map);
    assert.strictEqual(s.timeouts.length, 0, 'timer cleared with the overlay');
    stale();
    assert.strictEqual(s.timeouts.length, 0, 'never re-armed after removal');
    // Re-added: one timer again.
    L0.onAdd(s.map);
    assert.strictEqual(s.hourTicks().length, 1);
    L0.onRemove(s.map);
    assert.strictEqual(s.timeouts.length, 0);
  } finally { Date.now = realNow; }
});

test('hour tick: runs without a forecast (nothing to move) and keeps the status', async () => {
  const realNow = Date.now;
  const s = await mount({ status: 'pending' }, T0 + 60);
  try {
    Date.now = () => (T0 + 3600) * 1000 + 5;
    s.runHourTick();
    assert.strictEqual(s.layer._idx, -1);
    assert.strictEqual(statusOf(s).text, 'Ducting forecast is being prepared');
    assert.strictEqual(s.hourTicks().length, 1);
    assert.strictEqual(s.api.calls.length, 1);
  } finally { Date.now = realNow; }
});

function sizePanel(s) {
  const p = s.layer._panelEl;
  p.offsetWidth = 260; p.offsetHeight = 300;
  p.rect = { left: 10, top: 390, right: 270, bottom: 690, width: 260, height: 300 };
  return p;
}

test('moving the panel: the drag floats it into the map container, clamped, saved as fractions; the map never sees it', async () => {
  const st = makeStorage();
  const s = await mount(SAMPLE, T1 + 60, null, { storage: st });
  const p = sizePanel(s), h = s.layer._dragEl;
  assert.strictEqual(p.parentNode, s.map.corner);
  s.layer._tl.scrollLeft = 0;   // what a browser does to a re-parented scroller (no scroll event)
  flushScroll();
  const down = h.fire('pointerdown', { pointerType: 'mouse', button: 0, clientX: 20, clientY: 400, pointerId: 5 });
  assert.ok(down.stopped && down.defaultPrevented, 'map never pans');
  assert.ok(h.fire('mousedown').stopped && h.fire('touchstart').stopped);
  assert.strictEqual(p.parentNode, s.map.el, 'in the map container');
  assert.strictEqual(h.captured, 5);
  assert.ok(/mc-duct-panel-floating/.test(p.className) && /mc-duct-panel-dragging/.test(p.className));
  assert.deepStrictEqual([p.style.left, p.style.top], ['10px', '390px'], 'where it was');
  assert.strictEqual(s.layer._tl.scrollLeft, xsOf(s)[s.layer._idx], 'strip back on the shown time after the move in the DOM');
  assert.strictEqual(s.layer._resetEl.hidden, false);
  h.fire('pointermove', { clientX: 520, clientY: 100 });
  assert.deepStrictEqual([p.style.left, p.style.top], ['510px', '90px']);
  h.fire('pointermove', { clientX: 5000, clientY: -500 });
  assert.deepStrictEqual([p.style.left, p.style.top], ['732px', '8px'], 'clamped 8 px inside');
  h.fire('pointerup', { pointerId: 5 });
  assert.strictEqual(h.captured, null);
  assert.ok(!/mc-duct-panel-dragging/.test(p.className));
  const saved = JSON.parse(st.data['mc-duct-panel']);
  assert.ok(Math.abs(saved.pos.fx - 732 / 740) < 1e-9 && Math.abs(saved.pos.fy - 8 / 400) < 1e-9, JSON.stringify(saved));
  h.fire('pointermove', { clientX: 0, clientY: 0 });
  assert.strictEqual(p.style.left, '732px', 'no move after the release');
  // No corner lift while moved.
  s.map.fire('resize');
  assert.strictEqual(s.map.corner.style.marginBottom, '');
  // Map resized: re-clamped from the fractions.
  s.map.el.clientWidth = 500; s.map.el.clientHeight = 400;
  s.map.fire('resize');
  // 500 x 400: free space 240 x 100; fx ~0.99 -> 237 > 232 (right margin), fy 0.02 -> 2 < 8.
  assert.deepStrictEqual([p.style.left, p.style.top], ['232px', '8px']);
  s.map.el.clientWidth = 2000; s.map.el.clientHeight = 1400;
  s.map.fire('resize');
  assert.deepStrictEqual([p.style.left, p.style.top], [732 / 740 * 1740 + 'px', 8 / 400 * 1100 + 'px'], 'same fractions on a larger map');
  s.map.el.clientWidth = 500; s.map.el.clientHeight = 400;
  s.map.fire('resize');
  // Panel grows (expand): stays inside.
  p.offsetHeight = 390;
  s.layer._collapseEl.fire('click');
  s.layer._collapseEl.fire('click');
  assert.strictEqual(p.style.top, '8px', 'taller than the room left: at the top margin');
  p.offsetHeight = 300;
  s.layer._collapseEl.fire('click');
  assert.strictEqual(p.style.top, '8px');
  // Reset: back in the corner, lifted again, nothing stored.
  s.map.el.clientWidth = 1000; s.map.el.clientHeight = 700;
  s.layer._tl.scrollLeft = 0; flushScroll();
  s.layer._resetEl.fire('click');
  assert.strictEqual(p.parentNode, s.map.corner);
  assert.strictEqual(s.layer._tl.scrollLeft, xsOf(s)[s.layer._idx], 'strip restored after docking');
  assert.deepStrictEqual([p.style.left, p.style.top], ['', '']);
  assert.ok(!/mc-duct-panel-floating/.test(p.className) && /leaflet-control/.test(p.className));
  assert.strictEqual(s.layer._resetEl.hidden, true);
  assert.strictEqual(s.map.corner.style.marginBottom, '36px', 'lifted above the attribution again');
  assert.strictEqual(JSON.parse(st.data['mc-duct-panel']).pos, null);
  assert.ok(s.layer._dragEl.focused, 'focus back on the handle');
});

test('moving the panel: arrow keys on the handle; restored on a wide screen only; off on phones', async () => {
  const st = makeStorage();
  const s = await mount(SAMPLE, T1 + 60, null, { storage: st });
  const p = sizePanel(s), h = s.layer._dragEl;
  let ev = h.fire('keydown', { key: 'ArrowUp' });
  assert.ok(ev.defaultPrevented);
  assert.deepStrictEqual([p.style.left, p.style.top], ['10px', '374px']);
  h.fire('keydown', { key: 'ArrowRight', shiftKey: true });
  assert.strictEqual(p.style.left, '74px');
  assert.ok(!h.fire('keydown', { key: 'Enter' }).defaultPrevented);
  const saved = st.data['mc-duct-panel'];
  // Re-add on a wide screen: floating at the stored place.
  const s2 = makeSandbox({ storage: makeStorage({ 'mc-duct-panel': saved }) });
  const map2 = makeMap(), layer2 = s2.ctx.MC_createDuctingLayer({});
  const realCreate = s2.ctx.document.createElement;
  s2.ctx.document.createElement = (t) => { const e = realCreate(t); e.offsetWidth = 260; e.offsetHeight = 300; return e; };
  layer2.onAdd(map2);
  assert.strictEqual(layer2._panelEl.parentNode, map2.el);
  assert.deepStrictEqual([layer2._panelEl.style.left, layer2._panelEl.style.top], ['74px', '374px']);
  assert.strictEqual(map2.corner.style.marginBottom, '', 'no lift');
  // Phone: docked, keys and drags do nothing; a phone-sized resize docks a moved panel.
  const s3 = await mount(SAMPLE, T1 + 60, null, { storage: makeStorage({ 'mc-duct-panel': saved }), narrow: true });
  const p3 = sizePanel(s3);
  assert.strictEqual(p3.parentNode, s3.map.corner);
  assert.ok(!s3.layer._dragEl.fire('keydown', { key: 'ArrowUp' }).defaultPrevented);
  s3.layer._dragEl.fire('pointerdown', { pointerType: 'touch', button: 0, clientX: 1, clientY: 1, pointerId: 1 });
  assert.strictEqual(p3.parentNode, s3.map.corner);
  s2.ctx.matchMedia = (q) => ({ matches: /max-width: 600px/.test(q) });
  map2.fire('resize');
  assert.strictEqual(layer2._panelEl.parentNode, map2.corner, 'docked on a narrow resize');
  s2.ctx.matchMedia = () => ({ matches: false });
  map2.fire('resize');
  assert.strictEqual(layer2._panelEl.parentNode, map2.el, 'moved back when wide again');
});

test('teardown: panel gone from the corner or the map container, every listener, frame and timer removed; re-add gives one panel', async () => {
  for (const moved of [false, true]) {
    const s = await mount(longBody(49), T0 + 60, null, { storage: makeStorage() });
    const p = sizePanel(s);
    if (moved) s.layer._dragEl.fire('keydown', { key: 'ArrowUp' });
    assert.strictEqual(p.parentNode, moved ? s.map.el : s.map.corner);
    // A pending frame and idle timer.
    s.layer._tl.fire('touchstart');
    s.layer._tl.scrollLeft = 55;
    flushScroll();
    s.layer._tl.fire('touchend');
    assert.ok(s.frames.length === 1 && s.uiTimeouts().length === 1 && s.hourTicks().length === 1);
    s.layer.onRemove(s.map);
    assert.strictEqual(p.parentNode, null);
    assert.strictEqual(s.map.el.children.length + s.map.corner.children.length, 0, 'nothing left');
    assert.strictEqual(listenerCount(p), 0);
    assert.strictEqual(s.frames.length + s.timeouts.length, 0, 'frame, idle timer and hour tick cancelled');
    assert.strictEqual((s.map._on.resize || []).length, 0);
    assert.strictEqual(s.map.corner.style.marginBottom, '');
    s.layer.onAdd(s.map);
    assert.strictEqual(s.map._controls.length, 1);
    assert.strictEqual(byClass(s.map.el, 'mc-duct-panel').length + byClass(s.map.corner, 'mc-duct-panel').length, 1);
    assert.strictEqual(byClass(s.map.corner, 'mc-duct-legend').length + byClass(s.map.el, 'mc-duct-legend').length, 1);
    s.layer.onRemove(s.map);
  }
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
  assert.strictEqual(s.stats.polys.length, 0, 'no outline without --mc-duct-lora');
  assert.strictEqual(layer._lora, null);
  assert.ok(!/magenta|orange|gold|lightblue|#[0-9a-f]{6}\b/i.test(SRC.replace(/\/\*[\s\S]*?\*\/|\/\/.*$/gm, '')), 'no colour literals in map-ducting.js');
  s.vars['--mc-duct-s4'] = '#00a000';
  s.ctx.dispatch('theme-changed');
  assert.ok(layer._canvas.img.data.some(v => v !== 0), 'one stop is enough to draw; default opacity used');
});

test('theme-changed redraws the field and outlines from the new variables, same cells; inert after remove', async () => {
  const s = await mount(SAMPLE, T2);
  const cells = s.layer._cells.slice();
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
  assert.strictEqual(loraLayer(s).opts.color, '#fefefe');
  assert.deepStrictEqual(loraSet(s), ['51.25,-0.25']);
  assert.strictEqual(s.layer._group.layers.filter(l => l._mcLora).length, 1, 'old outline replaced, not added to');
  assert.ok(s.layer._cells.length === cells.length && s.layer._cells.every((c, i) => c === cells[i]), 'cells not rebuilt');
  assert.strictEqual(s.stats.fields.length, 1);
  assert.strictEqual((s.winL['theme-changed'] || []).length, 1);
  s.layer.onRemove(s.map);
  assert.strictEqual((s.winL['theme-changed'] || []).length, 0);
  const polys = s.stats.polys.length;
  s.vars['--mc-duct-lora'] = '#00ff00';
  assert.doesNotThrow(() => s.ctx.dispatch('theme-changed'));
  assert.strictEqual(s.stats.polys.length, polys, 'no redraw after remove');
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
  // The EOX labels are an opt-in group: on here, to compare the stacking.
  w.MC_MAP_CFG = { tiles: { providers: { eox: { enabled: true } } } };
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

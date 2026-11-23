/* Unit tests for public/planner-coverage.js (Repeater Planner, Coverage mode)
 * in a vm sandbox with the real deployment.js + api-base.js and los.js. */
'use strict';
const path = require('path');
const fs = require('fs');
const vm = require('vm');
const assert = require('assert');
const { loadApiBase } = require('./api-base-sandbox');

const read = (f) => fs.readFileSync(path.resolve(__dirname, '..', '..', 'public', f), 'utf8');
const SRC = read('planner-coverage.js');
const LOS_SRC = read('los.js');
let passed = 0, failed = 0;
const pending = [];
function test(name, fn) {
  let out;
  try { out = fn(); } catch (e) { failed++; console.log('  FAIL ' + name + ': ' + e.message); return; }
  if (out && out.then) {
    pending.push(out.then(() => { passed++; console.log('  ok   ' + name); },
      (e) => { failed++; console.log('  FAIL ' + name + ': ' + e.message); }));
  } else { passed++; console.log('  ok   ' + name); }
}
const flush = () => new Promise((r) => setImmediate(r));

/* ---- stubs ---- */
function fakeEl() {
  const el = {
    _l: {}, style: {}, hidden: false, disabled: false, value: '', checked: false, textContent: '', innerHTML: '',
    classList: { add() {}, remove() {}, toggle() {}, contains() { return false; } },
    addEventListener(t, f) { (el._l[t] = el._l[t] || []).push(f); },
    removeEventListener(t, f) { el._l[t] = (el._l[t] || []).filter((x) => x !== f); },
    setAttribute() {}, getAttribute() { return null; }, closest() { return null; }
  };
  return el;
}
function fakePanel() {
  const els = {};
  const p = fakeEl();
  p.querySelector = (sel) => (els[sel] = els[sel] || fakeEl());
  p.querySelectorAll = () => [];
  p._els = els;
  return p;
}
function makeL(log) {
  const lg = () => {
    const o = { layers: [], addTo(g) { if (g.layers) g.layers.push(o); return o; }, addLayer(l) { o.layers.push(l); },
      removeLayer(l) { o.layers = o.layers.filter((x) => x !== l); }, clearLayers() { o.layers = []; } };
    return o;
  };
  const shape = (kind) => (a, opts) => {
    const o = { kind, a, opts, addTo(g) { g.layers.push(o); return o; }, bindTooltip() { return o; }, on() { return o; } };
    log.shapes.push(o);
    return o;
  };
  return {
    layerGroup: lg, polyline: shape('line'), circleMarker: shape('circle'),
    imageOverlay: (url, bounds, opts) => {
      const o = { kind: 'image', url, bounds, opts, addTo(g) { g.layers.push(o); return o; } };
      log.overlays.push(o);
      return o;
    }
  };
}
// A 2d context that knows just enough: fillStyle → the pixel getImageData reads back.
function fakeCanvas(log) {
  const c = {
    width: 0, height: 0,
    getContext() {
      const g = {
        fillStyle: '',
        clearRect() {}, fillRect() { g._px = g.fillStyle; },
        getImageData() { return { data: log.rgb[g._px] || [0, 0, 0, 0] }; },
        createImageData(w, h) { return { width: w, height: h, data: new Uint8ClampedArray(w * h * 4) }; },
        putImageData(img) { log.images.push(img); }
      };
      return g;
    },
    toDataURL() { return 'data:image/png;base64,AAAA'; }
  };
  return c;
}
const VARS = {
  '--pl-cov-user-2': 'orange', '--pl-cov-user-3': 'yellow', '--pl-cov-user-4': 'lime', '--pl-cov-user-5': 'green',
  '--pl-cov-rep-1': 'red', '--pl-cov-rep-2': 'orange', '--pl-cov-rep-3': 'lime',
  '--pl-cov-opacity': ' 0.5', '--pl-cov-opacity-outdated': ' 0.2'
};
const RGB = { red: [255, 0, 0, 255], orange: [255, 128, 0, 255], yellow: [255, 255, 0, 255], lime: [128, 255, 0, 255], green: [0, 128, 0, 255] };
function load() {
  const log = { shapes: [], overlays: [], images: [], rgb: RGB, styleReads: 0 };
  const sandbox = {
    L: makeL(log), console, AbortController, Promise, Uint8Array, Uint8ClampedArray,
    atob: (s) => Buffer.from(s, 'base64').toString('binary'),
    addEventListener() {}, removeEventListener() {},
    getComputedStyle: () => ({ getPropertyValue: (k) => { log.styleReads++; return VARS[k] || ''; } })
  };
  sandbox.document = { documentElement: fakeEl(), createElement: () => fakeCanvas(log) };
  sandbox.window = sandbox;
  vm.createContext(sandbox);
  loadApiBase(sandbox);
  vm.runInContext(LOS_SRC, sandbox, { filename: 'los.js' });
  vm.runInContext(SRC, sandbox, { filename: 'planner-coverage.js' });
  return { api: sandbox.MeshPlannerCoverage, log };
}
const CFG = { defaultRangeKm: 30, rangesKm: [10, 20, 30, 50], newAntennaM: 10, existingAntennaM: 5, userReach: { d50Km: 2.7, slope: 0.3 } };
const esc = (s) => String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');

const base = load();
const I = base.api._internals;

console.log('planner-coverage.js');

test('decodeCells round-trips a known base64', () => {
  const b64 = Buffer.from([0x00, 0x15, 0x4f]).toString('base64');
  assert.deepStrictEqual(Array.from(I.decodeCells(b64)), [0x00, 0x15, 0x4f]);
});

test('cellCode splits the nibbles per layer', () => {
  assert.strictEqual(I.cellCode(0x15, 'users'), 5);
  assert.strictEqual(I.cellCode(0x15, 'repeaters'), 1);
  assert.strictEqual(I.cellCode(0x00, 'users'), 0);
  assert.strictEqual(I.cellCode(0x00, 'repeaters'), 0);
  assert.strictEqual(I.cellCode(0x4f, 'repeaters'), 4);
});

test('colorVarFor names the CSS variable; 0 and out-of-range are transparent', () => {
  assert.strictEqual(I.colorVarFor('users', 3), '--pl-cov-user-3');
  assert.strictEqual(I.colorVarFor('users', 1), null, 'the <10% class is not painted');
  assert.strictEqual(I.colorVarFor('repeaters', 3), '--pl-cov-rep-3');
  assert.strictEqual(I.colorVarFor('repeaters', 4), null, 'three repeater classes since the 2026-11-16 calibration');
  assert.strictEqual(I.colorVarFor('repeaters', 1), null, 'the blocked class is not painted');
  assert.strictEqual(I.colorVarFor('users', 0), null);
  assert.strictEqual(I.colorVarFor('repeaters', 0), null);
  assert.strictEqual(I.colorVarFor('users', 6), null);
  assert.strictEqual(I.colorVarFor('repeaters', 5), null);
});

test('parseCoverageParams: bad values fall back to cfg defaults and users', () => {
  const p = I.parseCoverageParams({ site: 'x,y', h: 'abc', rx: '-3', range: '7', layer: 'x' }, CFG);
  assert.strictEqual(p.site, null);
  assert.strictEqual(p.h, 10);
  assert.strictEqual(p.rx, 5);
  assert.strictEqual(p.range, 30);
  assert.strictEqual(p.layer, 'users');
  assert.strictEqual(I.parseCoverageParams({ site: '95,5' }, CFG).site, null);
  assert.strictEqual(I.parseCoverageParams(null, CFG).site, null);
});

test('stateToHash(parseCoverageParams(p)) round-trips a valid set', () => {
  const p = { site: '51.0023,5.6708', h: '12', rx: '6', range: '20', layer: 'repeaters' };
  assert.deepStrictEqual(JSON.parse(JSON.stringify(I.stateToHash(I.parseCoverageParams(p, CFG)))), p);
  const q = { site: null, h: '10', rx: '5', range: '30', layer: 'users' };
  assert.deepStrictEqual(JSON.parse(JSON.stringify(I.stateToHash(I.parseCoverageParams({}, CFG)))), q);
});

test('buildViewshedUrl: fixed key order, 4 decimals', () => {
  const st = { site: { lat: 51.00231, lon: 5.67079 }, h: 10, rx: 5, range: 30, layer: 'users' };
  assert.strictEqual(I.buildViewshedUrl(st), '/api/planner/viewshed?site=51.0023,5.6708&h=10&rx=5&range=30');
  assert.strictEqual(I.buildViewshedUrl({ site: null, h: 10, rx: 5, range: 30 }), null);
});

/* ---- mount ---- */
function result(over) {
  return Object.assign({
    source: 'srtm', params: {}, bbox: [51, 5, 51.2, 5.4], rows: 2, cols: 2, cell_km: 0.2,
    cells: Buffer.from([0x15, 0x24, 0x31, 0x00]).toString('base64'),
    links: [{ pubkey: 'ab', name: 'Rep <b>', lat: 51.1, lon: 5.2, link: { distance_km: 4.2, clearance_pct: 80, verdict: 'clear60' } }],
    warnings: [], truncated: false
  }, over || {});
}
function deferred() {
  let resolve, reject;
  const promise = new Promise((a, b) => { resolve = a; reject = b; });
  return { promise, resolve, reject };
}
function mountWith(params, loaded) {
  const m = loaded || load();
  const calls = [];
  const hash = [];
  const panel = fakePanel();
  const map = { getPane: () => null, createPane: () => ({ style: {} }), fitBounds() { m.log.fits = (m.log.fits || 0) + 1; }, getContainer: () => fakeEl() };
  const ctx = {
    map, layer: { layers: [], clearLayers() { this.layers = []; } }, panel, cfg: CFG,
    params: params || {}, setParams: (o) => hash.push(o),
    fetchJob: (url, opts) => { const d = deferred(); calls.push({ url, opts, d }); return d.promise; },
    esc, nodes: [], fmtClearance: (x) => x + '%',
    jobErrorText: () => 'failed', progressHtml: () => '<div>progress</div>', errorHtml: (m) => '<p>' + m + '</p>', warningsHtml: () => ''
  };
  const inst = m.api.mount(ctx);
  return { m, ctx, calls, hash, panel, inst };
}
function fireLayer(panel, value) {
  (panel._l.change || []).forEach((f) => f({ target: { getAttribute: (k) => (k === 'data-f' ? 'layer' : null), value, checked: true } }));
}

test('mount without a site fetches nothing', () => {
  const t = mountWith({});
  assert.strictEqual(t.calls.length, 0);
  t.inst.unmount();
});

test('mount with a site in the hash fetches once with the state URL', () => {
  const t = mountWith({ site: '51.0023,5.6708', h: '12', rx: '6', range: '20' });
  assert.strictEqual(t.calls.length, 1);
  assert.strictEqual(t.calls[0].url, '/api/planner/viewshed?site=51.0023,5.6708&h=12&rx=6&range=20');
  t.inst.unmount();
});

test('stale result: a Compute click supersedes the in-flight run and its answer is ignored', async () => {
  const t = mountWith({ site: '51.0023,5.6708' });
  const first = t.calls[0];
  t.inst.setSite({ lat: 51.1, lon: 5.3, node: null });
  assert.strictEqual(t.calls.length, 1, 'setSite does not fetch');
  assert.strictEqual(first.opts.signal.aborted, false, 'setSite keeps the run');
  clickRun(t.panel);
  assert.strictEqual(first.opts.signal.aborted, true, 'Compute aborts the first request');
  assert.strictEqual(t.calls.length, 2);
  assert.strictEqual(t.calls[1].opts.signal.aborted, false);
  assert(/site=51\.1000,5\.3000/.test(t.calls[1].url), t.calls[1].url);
  first.d.resolve(result({ bbox: [1, 1, 2, 2] }));
  await flush();
  assert.strictEqual(t.m.log.overlays.length, 0, 'stale result painted nothing');
  t.calls[1].d.resolve(result());
  await flush();
  assert.strictEqual(t.m.log.overlays.length, 1);
  assert.deepStrictEqual(JSON.parse(JSON.stringify(t.m.log.overlays[0].bounds)), [[51, 5], [51.2, 5.4]]);
  assert.strictEqual(t.m.log.overlays[0].opts.opacity, 0.5);
  const last = t.hash[t.hash.length - 1];
  assert.strictEqual(last.site, '51.1000,5.3000');
  t.inst.unmount();
});

test('paint: users layer colours each cell from its nibble; code 0 stays transparent', async () => {
  const t = mountWith({ site: '51,5' });
  t.calls[0].d.resolve(result());
  await flush();
  const img = t.m.log.images[0];
  assert.strictEqual(img.width, 2);
  const px = (i) => Array.from(img.data.slice(i * 4, i * 4 + 4));
  assert.deepStrictEqual(px(0), RGB.green);   // 0x15 users 5
  assert.deepStrictEqual(px(1), RGB.lime);    // 0x24 users 4
  assert.deepStrictEqual(px(2), [0, 0, 0, 0]); // 0x31 users 1 (<10%) is not painted
  assert.deepStrictEqual(px(3), [0, 0, 0, 0]); // 0x00 transparent
  t.inst.unmount();
});

test('mercatorRowMap: each canvas row takes the grid row under its Mercator centre', () => {
  const merc = (lat) => Math.log(Math.tan(Math.PI / 4 + lat * Math.PI / 360));
  const S = 50.46, N = 51.54, rows = 300;   // about 60 km either side of 51° N
  const map = Array.from(I.mercatorRowMap(S, N, rows));
  assert.strictEqual(map.length, rows);
  assert.strictEqual(map[0], 0);
  assert.strictEqual(map[rows - 1], rows - 1);
  for (let j = 0; j < rows; j++) {
    const y = merc(N) + (j + 0.5) / rows * (merc(S) - merc(N));
    const lat = Math.atan(Math.sinh(y)) * 180 / Math.PI;
    assert.strictEqual(map[j], Math.floor((N - lat) / (N - S) * rows), 'row ' + j);
    if (j) assert(map[j] >= map[j - 1], 'rows stay in order');
  }
  assert(map.some((r, j) => r !== j), 'at 60 km the mapping is not the identity');
});

test('paint: canvas rows follow Mercator, not equal latitude bands', async () => {
  // 4 bands of 20° from 80° N to the equator. Spaced evenly in Mercator, the
  // canvas row centres fall at about 76°, 65°, 46° and 17° N.
  const t = mountWith({ site: '51,5' });
  t.calls[0].d.resolve(result({ bbox: [0, 0, 80, 1], rows: 4, cols: 1, cells: Buffer.from([0x05, 0x04, 0x03, 0x02]).toString('base64') }));
  await flush();
  const img = t.m.log.images[0];
  assert.strictEqual(img.height, 4);
  const px = (i) => Array.from(img.data.slice(i * 4, i * 4 + 4));
  assert.deepStrictEqual([px(0), px(1), px(2), px(3)], [RGB.green, RGB.green, RGB.lime, RGB.orange]);
  t.inst.unmount();
});

test('users legend has no <10% swatch', () => {
  const html = I.legendHtml('users', { rx: 5 }, CFG);
  assert(html.indexOf('< 10%') < 0, html);
  assert(html.indexOf('sw-user-1') < 0, html);
  assert(html.indexOf('10–25%') >= 0);
});

test('repeaters legend has no blocked swatch', () => {
  const html = I.legendHtml('repeaters', { rx: 5 }, CFG);
  assert(html.indexOf('sw-rep-1') < 0, html);
  assert(html.indexOf('sw-rep-4') < 0, html);
  assert(html.indexOf('sw-rep-3') >= 0 && /Line of sight/.test(html), html);
  assert(html.indexOf('sw-rep-2') >= 0 && /Slightly blocked/.test(html), html);
  assert(/20 km/.test(html), 'says line of sight alone stops predicting links beyond about 20 km');
});

test('linkClass follows the grid classes from clearance_pct', () => {
  const c = (k) => I.linkClass(k);
  assert.strictEqual(c({ clearance_pct: 54, verdict: 'marginal' }), 3);
  assert.strictEqual(c({ clearance_pct: 0, verdict: 'marginal' }), 3);
  assert.strictEqual(c({ clearance_pct: -0.1, verdict: 'blocked' }), 2);
  assert.strictEqual(c({ clearance_pct: -20, verdict: 'blocked' }), 2);
  assert.strictEqual(c({ clearance_pct: -20.1, verdict: 'blocked' }), 1);
  assert.strictEqual(c({ error: 'no terrain' }), 0);
  assert.strictEqual(c({}), 0);
});

test('links list names the class, not the textbook verdict', () => {
  const d = { links: [
    { pubkey: 'a', name: 'A', link: { distance_km: 4, clearance_pct: 20, verdict: 'marginal' } },
    { pubkey: 'b', name: 'B', link: { distance_km: 9, clearance_pct: -10, verdict: 'blocked' } },
    { pubkey: 'c', name: 'C', link: { distance_km: 20, clearance_pct: -90, verdict: 'blocked' } }
  ] };
  const html = I.linksHtml(d, esc);
  assert(/sw-rep-3[^]*A · 4\.0 km · Line of sight/.test(html), html);
  assert(/sw-rep-2[^]*B · 9\.0 km · Slightly blocked/.test(html), html);
  assert(/sw-rep-1[^]*C · 20\.0 km · Blocked by terrain/.test(html), html);
});

test('layer switch repaints without another fetch', async () => {
  const t = mountWith({ site: '51,5' });
  t.calls[0].d.resolve(result());
  await flush();
  assert.strictEqual(t.m.log.overlays.length, 1);
  const dots = () => t.m.log.shapes.filter((s) => s.kind === 'circle' && /mc-pl-cov-node/.test(s.opts.className));
  assert.strictEqual(dots().length, 0, 'users layer recolours no repeater dots');
  fireLayer(t.panel, 'repeaters');
  assert.strictEqual(t.calls.length, 1, 'no new fetch');
  assert.strictEqual(t.m.log.overlays.length, 2, 'repainted');
  const img = t.m.log.images[1];
  const px = (i) => Array.from(img.data.slice(i * 4, i * 4 + 4));
  assert.deepStrictEqual(px(0), [0, 0, 0, 0]); // 0x15 repeaters 1 (blocked) is not painted
  assert.deepStrictEqual(px(3), [0, 0, 0, 0]);
  const lines = t.m.log.shapes.filter((s) => s.kind === 'line');
  assert.strictEqual(lines.length, 1, 'repeaters layer draws the link line');
  assert(/mc-pl-cov-link-3/.test(lines[0].opts.className), lines[0].opts.className);
  assert.strictEqual(dots().length, 1, 'the linked repeater gets a dot in its class colour');
  assert(/mc-pl-cov-node-3/.test(dots()[0].opts.className), dots()[0].opts.className);
  assert.strictEqual(JSON.stringify(dots()[0].a), "[51.1,5.2]");
  assert.strictEqual(dots()[0].opts.interactive, false, 'planner node tooltip and click stay usable');
  assert.strictEqual(t.hash[t.hash.length - 1].layer, 'repeaters');
  t.inst.unmount();
});

test('blocked links get a red dot but no line', async () => {
  const t = mountWith({ site: '51,5', layer: 'repeaters' });
  t.calls[0].d.resolve(result({ links: [
    { pubkey: 'a', name: 'A', lat: 51.1, lon: 5.2, link: { distance_km: 4, clearance_pct: 20, verdict: 'marginal' } },
    { pubkey: 'b', name: 'B', lat: 51.0, lon: 5.3, link: { distance_km: 9, clearance_pct: -10, verdict: 'blocked' } },
    { pubkey: 'c', name: 'C', lat: 50.9, lon: 5.1, link: { distance_km: 12, clearance_pct: -90, verdict: 'blocked' } }
  ] }));
  await flush();
  const lines = t.m.log.shapes.filter((s) => s.kind === 'line');
  const dots = t.m.log.shapes.filter((s) => s.kind === 'circle' && /mc-pl-cov-node/.test(s.opts.className));
  assert.strictEqual(lines.length, 2, 'lines for line of sight and slightly blocked only');
  assert(!lines.some((l) => /mc-pl-cov-link-1/.test(l.opts.className)), 'no blocked line');
  assert.strictEqual(dots.length, 3, 'every linked repeater keeps a coloured dot');
  assert.strictEqual(dots.filter((d) => /mc-pl-cov-node-1/.test(d.opts.className)).length, 1);
  t.inst.unmount();
});

test('links list escapes repeater names', async () => {
  const t = mountWith({ site: '51,5', layer: 'repeaters' });
  t.calls[0].d.resolve(result());
  await flush();
  const html = Object.values(t.panel._els).map((e) => e.innerHTML).join('');
  assert(/Rep &lt;b&gt;/.test(html), 'name escaped');
  assert(!/Rep <b>/.test(html), 'no raw markup');
  assert(/4\.2 km/.test(html), 'distance shown');
  t.inst.unmount();
});

function clickRun(panel) {
  const btn = { getAttribute: (k) => (k === 'data-act' ? 'run' : null) };
  (panel._l.click || []).forEach((f) => f({ target: { closest: () => btn } }));
}
function fireInput(panel, name, value) {
  panel._els['[data-f="' + name + '"]'].value = value;
  (panel._l.change || []).forEach((f) => f({ target: { getAttribute: (k) => (k === 'data-f' ? name : null), value, checked: true } }));
}
const runBtn = (t) => t.panel._els['[data-act="run"]'];
const outdatedHtml = (t) => t.panel._els['[data-f="outdated"]'].innerHTML;

test('mount with a site in the hash computes once, without one never', () => {
  const a = mountWith({ site: '51,5' });
  assert.strictEqual(a.calls.length, 1);
  a.inst.unmount();
  const b = mountWith({});
  assert.strictEqual(b.calls.length, 0);
  assert.strictEqual(runBtn(b).disabled, true, 'no site: button disabled');
  b.inst.setSite({ lat: 51.1, lon: 5.1, node: null });
  assert.strictEqual(b.calls.length, 0, 'setSite does not fetch');
  assert.strictEqual(runBtn(b).disabled, false, 'site: button enabled');
  assert.strictEqual(outdatedHtml(b), '', 'no overlay yet: no outdated note');
  b.inst.unmount();
});

test('param changes never fetch, hash updates, overlay is dimmed and flagged outdated', async () => {
  const t = mountWith({ site: '51,5' });
  t.calls[0].d.resolve(result());
  await flush();
  assert.strictEqual(t.m.log.overlays[0].opts.opacity, 0.5);
  assert.strictEqual(outdatedHtml(t), '');
  const hashLen = t.hash.length;
  t.inst.setSite({ lat: 51.1, lon: 5.1, node: null });
  fireInput(t.panel, 'h', '12');
  fireInput(t.panel, 'rx', '7');
  fireInput(t.panel, 'range', '20');
  assert.strictEqual(t.calls.length, 1, 'no refetch');
  assert.strictEqual(t.hash.length, hashLen + 4, 'hash follows each change');
  assert(/Outdated/.test(outdatedHtml(t)), outdatedHtml(t));
  const last = t.m.log.overlays[t.m.log.overlays.length - 1];
  assert.strictEqual(last.opts.opacity, 0.2, 'overlay dimmed');
  t.inst.unmount();
});

test('a parameter change during the first compute does not abort it; the result arrives outdated', async () => {
  const t = mountWith({ site: '51,5' });
  fireInput(t.panel, 'h', '12');
  assert.strictEqual(t.calls.length, 1, 'no second fetch');
  assert.strictEqual(t.calls[0].opts.signal.aborted, false, 'in-flight run kept');
  t.calls[0].d.resolve(result());
  await flush();
  assert.strictEqual(t.calls.length, 1);
  assert.strictEqual(t.m.log.overlays.length, 1, 'overlay rendered');
  assert.strictEqual(t.m.log.overlays[0].opts.opacity, 0.2, 'dimmed');
  assert(/Outdated/.test(outdatedHtml(t)), 'note shown');
  t.inst.unmount();
});

test('button click starts exactly one job with the latest values; a newer click supersedes', async () => {
  const t = mountWith({ site: '51,5' });
  t.calls[0].d.resolve(result());
  await flush();
  t.inst.setSite({ lat: 51.1, lon: 5.1, node: null });
  fireInput(t.panel, 'h', '12');
  fireInput(t.panel, 'range', '20');
  clickRun(t.panel);
  assert.strictEqual(t.calls.length, 2);
  assert.strictEqual(t.calls[1].url, '/api/planner/viewshed?site=51.1000,5.1000&h=12&rx=5&range=20');
  clickRun(t.panel);
  assert.strictEqual(t.calls.length, 3);
  assert.strictEqual(t.calls[1].opts.signal.aborted, true, 'older click aborted');
  t.calls[1].d.resolve(result({ bbox: [1, 1, 2, 2] }));
  await flush();
  const n = t.m.log.overlays.length;
  t.calls[2].d.resolve(result());
  await flush();
  assert.strictEqual(t.m.log.overlays.length, n + 1, 'only the newest answer painted');
  assert.strictEqual(outdatedHtml(t), '');
  assert.strictEqual(t.m.log.overlays[n].opts.opacity, 0.5, 'fresh overlay at full opacity');
  t.inst.unmount();
});

const plain = (v) => JSON.parse(JSON.stringify(v));   // sandbox arrays → this realm
test('site marker and link lines start at the site the server computed for', async () => {
  const t = mountWith({ site: '51.00037,5.00041', layer: 'repeaters' });
  t.calls[0].d.resolve(result({ params: { site: { lat: 51, lon: 5 } } }));
  await flush();
  const site = t.m.log.shapes.filter((s) => s.kind === 'circle' && /mc-pl-cov-site/.test(s.opts.className)).pop();
  assert.deepStrictEqual(plain(site.a), [51, 5], 'marker on the rounded site');
  const line = t.m.log.shapes.filter((s) => s.kind === 'line').pop();
  assert.deepStrictEqual(plain(line.a[0]), [51, 5], 'line from the rounded site');
  t.inst.setSite({ lat: 51.3, lon: 5.3, node: null });
  const moved = t.m.log.shapes.filter((s) => s.kind === 'circle' && /mc-pl-cov-site/.test(s.opts.className)).pop();
  assert.deepStrictEqual(plain(moved.a), [51.3, 5.3], 'a new site shows where it was placed');
  t.inst.unmount();
});

test('an outdated result dims the links list as well', async () => {
  const t = mountWith({ site: '51,5', layer: 'repeaters' });
  t.calls[0].d.resolve(result());
  await flush();
  const list = t.panel._els['[data-f="results"]'];
  assert(!/is-outdated/.test(list.className || ''), 'fresh result: list not dimmed');
  fireInput(t.panel, 'h', '12');
  assert(/is-outdated/.test(list.className), 'outdated: list dimmed');
  clickRun(t.panel);
  t.calls[1].d.resolve(result());
  await flush();
  assert(!/is-outdated/.test(list.className), 'recomputed: list back to normal');
  t.inst.unmount();
});

test('the map fits a result once; coming back to the tab keeps the view', async () => {
  const m = load();
  const a = mountWith({ site: '51,5' }, m);
  a.calls[0].d.resolve(result());
  await flush();
  assert.strictEqual(m.log.fits, 1, 'first open fits the result');
  a.inst.unmount();
  const b = mountWith({ site: '51,5' }, m);
  b.calls[0].d.resolve(result());
  await flush();
  assert.strictEqual(m.log.fits, 1, 'same site again: pan and zoom kept');
  b.inst.unmount();
  const c = mountWith({ site: '50,4' }, m);
  c.calls[0].d.resolve(result());
  await flush();
  assert.strictEqual(m.log.fits, 2, 'a deep link to another site fits again');
  c.inst.unmount();
});

test('unmount aborts the running request and clears the layer', () => {
  const t = mountWith({ site: '51,5' });
  t.inst.unmount();
  assert.strictEqual(t.calls[0].opts.signal.aborted, true);
  assert.strictEqual(t.panel.innerHTML, '');
});

test('panel carries the height note verbatim', () => {
  const t = mountWith({});
  assert(t.panel.innerHTML.indexOf('Height mainly changes reach to other repeaters; user reach is measured and barely depends on height.') >= 0);
  t.inst.unmount();
});

Promise.all(pending).then(() => {
  console.log(`\n${passed} passed, ${failed} failed`);
  process.exit(failed ? 1 : 0);
});

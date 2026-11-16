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
  '--pl-cov-user-1': 'red', '--pl-cov-user-2': 'orange', '--pl-cov-user-3': 'yellow', '--pl-cov-user-4': 'lime', '--pl-cov-user-5': 'green',
  '--pl-cov-rep-1': 'red', '--pl-cov-rep-2': 'orange', '--pl-cov-rep-3': 'lime', '--pl-cov-rep-4': 'green',
  '--pl-cov-opacity': ' 0.5'
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
  assert.strictEqual(I.colorVarFor('repeaters', 4), '--pl-cov-rep-4');
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
    cells: Buffer.from([0x15, 0x24, 0x33, 0x00]).toString('base64'),
    links: [{ pubkey: 'ab', name: 'Rep <b>', lat: 51.1, lon: 5.2, link: { distance_km: 4.2, clearance_pct: 80, verdict: 'clear60' } }],
    warnings: [], truncated: false
  }, over || {});
}
function deferred() {
  let resolve, reject;
  const promise = new Promise((a, b) => { resolve = a; reject = b; });
  return { promise, resolve, reject };
}
function mountWith(params) {
  const m = load();
  const calls = [];
  const hash = [];
  const panel = fakePanel();
  const map = { getPane: () => null, createPane: () => ({ style: {} }), fitBounds() {}, getContainer: () => fakeEl() };
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

test('stale result: first answer after setSite is ignored and its signal aborted', async () => {
  const t = mountWith({ site: '51.0023,5.6708' });
  const first = t.calls[0];
  t.inst.setSite({ lat: 51.1, lon: 5.3, node: null });
  assert.strictEqual(t.calls.length, 2);
  assert.strictEqual(first.opts.signal.aborted, true, 'first request aborted');
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
  assert.deepStrictEqual(px(3), [0, 0, 0, 0]); // 0x00 transparent
  t.inst.unmount();
});

test('layer switch repaints without another fetch', async () => {
  const t = mountWith({ site: '51,5' });
  t.calls[0].d.resolve(result());
  await flush();
  assert.strictEqual(t.m.log.overlays.length, 1);
  fireLayer(t.panel, 'repeaters');
  assert.strictEqual(t.calls.length, 1, 'no new fetch');
  assert.strictEqual(t.m.log.overlays.length, 2, 'repainted');
  const img = t.m.log.images[1];
  const px = (i) => Array.from(img.data.slice(i * 4, i * 4 + 4));
  assert.deepStrictEqual(px(0), RGB.red);     // 0x15 repeaters 1 (blocked)
  assert.deepStrictEqual(px(3), [0, 0, 0, 0]);
  const lines = t.m.log.shapes.filter((s) => s.kind === 'line');
  assert.strictEqual(lines.length, 1, 'repeaters layer draws the link line');
  assert(/mc-pl-cov-link-3/.test(lines[0].opts.className), lines[0].opts.className);
  assert.strictEqual(t.hash[t.hash.length - 1].layer, 'repeaters');
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

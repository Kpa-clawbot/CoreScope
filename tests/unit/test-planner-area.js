/* Unit tests for public/planner-area.js (Repeater Planner, Area mode) via vm sandbox. */
'use strict';
const path = require('path');
const fs = require('fs');
const vm = require('vm');
const assert = require('assert');

const SRC = fs.readFileSync(path.resolve(__dirname, '..', '..', 'public', 'planner-area.js'), 'utf8');
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

/* ---- stubs ---- */
function fakeEl() {
  const el = {
    _l: {}, style: {}, hidden: false, disabled: false, value: '', checked: false, textContent: '', innerHTML: '',
    classList: { add() {}, remove() {}, contains() { return false; } },
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
    const o = { layers: [], addTo(g) { g.layers.push(o); return o; }, addLayer(l) { o.layers.push(l); },
      clearLayers() { o.layers = []; log.cleared++; } };
    return o;
  };
  const shape = (kind) => (a, opts) => {
    const o = { kind, a, opts, addTo(g) { g.layers.push(o); return o; },
      bindTooltip(h) { log.tips.push(h); return o; }, on() { return o; }, setBounds() {} };
    return o;
  };
  return {
    layerGroup: lg, rectangle: shape('rect'), polyline: shape('line'), circleMarker: shape('circle'),
    marker: shape('marker'), divIcon: (o) => o,
    latLngBounds: (a, b) => ({ getSouth: () => Math.min(a.lat, b.lat), getNorth: () => Math.max(a.lat, b.lat),
      getWest: () => Math.min(a.lng, b.lng), getEast: () => Math.max(a.lng, b.lng) })
  };
}
function load(log) {
  const sandbox = { window: {}, L: makeL(log), document: fakeEl(), console, AbortController, Promise };
  sandbox.window = sandbox;
  vm.createContext(sandbox);
  vm.runInContext(SRC, sandbox, { filename: 'planner-area.js' });
  return sandbox.MeshPlannerArea;
}
const log0 = { cleared: 0, tips: [] };
const API = load(log0);
const I = API._internals;
const CFG = { defaultRangeKm: 20, rangesKm: [5, 10, 20, 30], newAntennaM: 10, existingAntennaM: 5, maxAreaKm: 100, maxSites: 3 };
const esc = (s) => String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;').replace(/'/g, '&#39;');

console.log('planner-area.js');

test('bboxFromBounds rounds to 3 decimals', () => {
  const b = I.bboxFromBounds({ getSouth: () => 30.123456, getWest: () => -98.9999, getNorth: () => 30.5, getEast: () => -98.1 });
  assert.deepStrictEqual(JSON.parse(JSON.stringify(b)), { s: 30.123, w: -99, n: 30.5, e: -98.1 });
});

test('bboxSizeKm is cos-lat aware', () => {
  const eq = I.bboxSizeKm({ s: 0, w: 0, n: 1, e: 1 });
  const KM = 6371 * Math.PI / 180; // the server's sphere (planner_api.go)
  assert(Math.abs(eq.w - KM) < 0.01 && Math.abs(eq.h - KM) < 0.01);
  const t = I.bboxSizeKm({ s: 60, w: 0, n: 61, e: 1 });
  assert(Math.abs(t.w - KM * Math.cos(60.5 * Math.PI / 180)) < 0.01);
  assert(t.w < 60);
});

test('areaTooLarge uses max side vs cfg.maxAreaKm', () => {
  assert.strictEqual(I.areaTooLarge({ s: 30, w: -98, n: 30.5, e: -97.5 }, CFG), false);
  assert.strictEqual(I.areaTooLarge({ s: 30, w: -98, n: 31.2, e: -97.9 }, CFG), true); // 133 km tall
  // wide at high latitude is smaller than at equator
  assert.strictEqual(I.areaTooLarge({ s: 70, w: 0, n: 70.1, e: 2.0 }, CFG), false);
});

test('areaTooLarge measures the box after the server-side 0.05° outward snap', () => {
  const s = I.snapBox({ s: 30.01, w: -97.99, n: 30.04, e: -97.96 });
  assert.strictEqual(JSON.stringify(s), JSON.stringify({ s: 30, w: -98, n: 30.05, e: -97.95 }));
  assert.strictEqual(JSON.stringify(I.snapBox({ s: 30, w: -98, n: 30.05, e: -97.95 })), JSON.stringify({ s: 30, w: -98, n: 30.05, e: -97.95 }), 'grid lines stay put');
  // 0.893° tall raw = 99.3 km; snapped outward to 0.95° = 105.6 km: the server refuses it, so must the UI
  assert.strictEqual(I.areaTooLarge({ s: 30.004, w: -97.9, n: 30.897, e: -97.6 }, CFG), true);
  assert.strictEqual(I.areaTooLarge({ s: 30.0, w: -97.9, n: 30.85, e: -97.6 }, CFG), false);
});

test('buildCoverageUrl: explicit values', () => {
  const u = I.buildCoverageUrl({ bbox: { s: 30, w: -98, n: 30.5, e: -97.5 }, sites: 2, range: 30, hr: 15, he: 8, baseline: 'observed', stale: true }, CFG);
  assert.strictEqual(u, '/api/planner/coverage?bbox=30,-98,30.5,-97.5&sites=2&range=30&hr=15&he=8&baseline=observed&stale=1');
});
test('buildCoverageUrl: defaults, clamps sites to maxSites', () => {
  const u = I.buildCoverageUrl({ bbox: '30,-98,30.5,-97.5', sites: 99 }, CFG);
  assert.strictEqual(u, '/api/planner/coverage?bbox=30,-98,30.5,-97.5&sites=3&range=20&hr=10&he=5&baseline=predicted&stale=0');
  assert.strictEqual(I.buildCoverageUrl({ bbox: '30,-98,30.5,-97.5', sites: 0 }, CFG).includes('sites=1&'), true);
  assert.strictEqual(I.buildCoverageUrl({}, CFG), null);
});

test('parseAreaParams: good input', () => {
  const p = I.parseAreaParams({ bbox: '30,-98,30.5,-97.5', sites: '2', range: '10', hr: '12', he: '6', baseline: 'observed', stale: '1' }, CFG);
  assert.deepStrictEqual(JSON.parse(JSON.stringify(p)), { bbox: { s: 30, w: -98, n: 30.5, e: -97.5 }, sites: 2, range: 10, hr: 12, he: 6, baseline: 'observed', stale: true, marginal: false });
});
test('parseAreaParams: bad input falls back to defaults', () => {
  const p = I.parseAreaParams({ bbox: 'nope', sites: 'x', range: '7', hr: '-3', he: 'abc', baseline: 'zzz', stale: '0' }, CFG);
  assert.strictEqual(p.bbox, null);
  assert.strictEqual(p.sites, 3); assert.strictEqual(p.range, 20); assert.strictEqual(p.hr, 10);
  assert.strictEqual(p.he, 5); assert.strictEqual(p.baseline, 'predicted'); assert.strictEqual(p.stale, false);
  assert.strictEqual(I.parseAreaParams({ bbox: '31,-98,30,-97' }, CFG).bbox, null);      // s >= n
  assert.strictEqual(I.parseAreaParams({ bbox: '30,-98,95,-97' }, CFG).bbox, null);      // lat range
  assert.strictEqual(I.parseAreaParams({ bbox: '1,2,3' }, CFG).bbox, null);              // arity
  assert.strictEqual(I.parseAreaParams({ sites: '50' }, CFG).sites, 3);
  assert.strictEqual(I.parseAreaParams(null, CFG).bbox, null);
});

const NODES = [
  { name: 'A', component: 0 }, { name: 'B', component: 1 }, { name: 'C', component: 1 },
  { name: 'D', component: 2 }, { name: 'E', component: 0 }
];
test('reasonText: joins (plural, singular, counts added repeaters)', () => {
  assert.strictEqual(I.reasonText({ reason: 'joins', gain: { components_joined: 2 }, joins_components: [1, 2] }, NODES),
    'Joins 2 clusters (adds 3 repeaters to the main backbone)');
  assert.strictEqual(I.reasonText({ reason: 'joins', gain: { components_joined: 1 }, joins_components: [2] }, NODES),
    'Joins 1 cluster (adds 1 repeater to the main backbone)');
  assert.strictEqual(I.reasonText({ reason: 'joins', gain: { components_joined: 1 }, joins_components: [] }, NODES), 'Joins 1 cluster');
});
test('reasonText: redundancy and reach', () => {
  assert.strictEqual(I.reasonText({ reason: 'redundancy', gain: { cut_vertices_removed: 2 } }, NODES), 'Removes 2 single points of failure');
  assert.strictEqual(I.reasonText({ reason: 'redundancy', gain: { cut_vertices_removed: 1 } }, NODES), 'Removes 1 single point of failure');
  assert.strictEqual(I.reasonText({ reason: 'reach', gain: { repeaters_reached: 4 } }, NODES), 'Reaches 4 more repeaters');
  assert.strictEqual(I.reasonText({ reason: 'reach', gain: { repeaters_reached: 1 } }, NODES), 'Reaches 1 more repeater');
});
test('reasonText: falls back when the stated reason has no gain', () => {
  assert.strictEqual(I.reasonText({ reason: 'joins', gain: { repeaters_reached: 2 } }, NODES), 'Reaches 2 more repeaters');
  assert.strictEqual(I.reasonText({ reason: 'reach', gain: {} }, NODES), 'Adds link coverage in this area');
});

test('summaryRows: four rows, stale only when > 0', () => {
  const b = { components: 4, largest_component: 10, isolated: 2, cut_vertices: 3, stale_excluded: 0 };
  const f = { components: 1, largest_component: 16, isolated: 0, cut_vertices: 1, stale_excluded: 0 };
  const rows = I.summaryRows(b, f);
  assert.strictEqual(rows.map((r) => r.label).join('|'), 'Clusters|Largest cluster|Isolated repeaters|Single points of failure');
  assert.strictEqual(rows[0].before, 4); assert.strictEqual(rows[0].after, 1);
  const r2 = I.summaryRows(Object.assign({}, b, { stale_excluded: 5 }), Object.assign({}, f, { stale_excluded: 5 }));
  assert.strictEqual(r2.length, 5); assert.strictEqual(r2[4].label, 'Stale excluded');
});

test('errorText: plain-language messages', () => {
  assert.strictEqual(I.errorText({ status: 400, message: 'no_nodes_in_area' }), 'No known repeaters in this area');
  assert.strictEqual(I.errorText({ status: 429, retryAfter: 12 }), 'Busy, try again in 12 s');
  assert.strictEqual(I.errorText({ status: 503 }), 'Busy, try again in 10 s');
});

const XSS = '"><img src=x onerror=alert(1)>';
function cannedData(name) {
  return {
    status: 'improved', truncated: true, warnings: [name],
    baseline: { repeaters: 3, components: 2, largest_component: 2, isolated: 1, cut_vertices: 0, stale_excluded: 0 },
    final: { repeaters: 3, components: 1, largest_component: 3, isolated: 0, cut_vertices: 0, stale_excluded: 0 },
    nodes: [
      { pubkey: 'aa', name: name, role: 'repeater', lat: 30, lon: -98, stale: false, in_area: true, component: 0 },
      { pubkey: 'bb', name: 'B', role: 'repeater', lat: 30.1, lon: -98.1, stale: true, in_area: false, component: 1 }
    ],
    edges: [{ a: 0, b: 1, class: 'marginal', distance_km: 5, clearance_pct: 50 }],
    sites: [{ step: 1, lat: 30.05, lon: -98.05, elev_m: 312, antenna_m: 10, reason: 'joins',
      gain: { components_joined: 1 }, joins_components: [1],
      links: [{ node: 0, distance_km: 18.24, clearance_pct: 74, class: name }, { node: 1, distance_km: 3, clearance_pct: 60, class: 'marginal' }] }]
  };
}

test('XSS: planner-area.js card/links/warnings render node names inert', () => {
  const html = I.resultsHtml(cannedData(XSS), { he: 5 }, esc);
  assert(!html.includes('<img'), 'raw <img> leaked into results HTML');
  assert(html.includes('&lt;img src=x onerror=alert(1)&gt;'));
  assert(html.includes('#1 · 312 m elevation · 10 m mast'));
  assert(html.includes('18.2 km · 74% clearance'));
});

function makeCtx(over) {
  const log = { cleared: 0, tips: [] };
  const api = load(log);
  const layer = makeL(log).layerGroup();
  const panel = fakePanel();
  const jobs = [];
  const ctx = Object.assign({
    map: { getBounds: () => ({ getSouth: () => 30, getWest: () => -98, getNorth: () => 30.5, getEast: () => -97.5 }),
      getContainer: () => fakeEl(), dragging: { disable() {}, enable() {} }, on() {}, off() {}, fitBounds() {}, getZoom: () => 9 },
    layer, panel, cfg: CFG, params: {}, esc, nodes: [],
    setParams() {}, showProfiles() {},
    fetchJob(url, o) { const j = { url, signal: o.signal, onProgress: o.onProgress }; jobs.push(j);
      return new Promise((res, rej) => { j.res = res; j.rej = rej; }); }
  }, over || {});
  return { api, ctx, jobs, log, layer, panel };
}

test('mount with bbox in params auto-runs; unmount aborts the job and clears layer + panel', () => {
  const m = makeCtx({ params: { bbox: '30,-98,30.5,-97.5' } });
  const inst = m.api.mount(m.ctx);
  assert.strictEqual(m.jobs.length, 1);
  assert(m.jobs[0].url.startsWith('/api/planner/coverage?bbox=30,-98,30.5,-97.5'));
  assert.strictEqual(m.jobs[0].signal.aborted, false);
  m.log.cleared = 0;
  inst.unmount();
  assert.strictEqual(m.jobs[0].signal.aborted, true);
  assert(m.log.cleared >= 1, 'layer.clearLayers not called');
  assert.strictEqual(m.panel.innerHTML, '');
  assert.strictEqual((m.panel._l.click || []).length, 0);
});

test('mount without bbox does not run; onParams with a new bbox runs and aborts the previous job', () => {
  const m = makeCtx();
  const inst = m.api.mount(m.ctx);
  assert.strictEqual(m.jobs.length, 0);
  inst.onParams({ bbox: '30,-98,30.5,-97.5' });
  assert.strictEqual(m.jobs.length, 1);
  inst.onParams({ bbox: '31,-98,31.5,-97.5' });
  assert.strictEqual(m.jobs.length, 2);
  assert.strictEqual(m.jobs[0].signal.aborted, true);
  assert.strictEqual(m.jobs[1].signal.aborted, false);
  inst.onParams({ bbox: '31,-98,31.5,-97.5' });            // same state: no re-run
  assert.strictEqual(m.jobs.length, 2);
  inst.unmount();
});

test('result renders into panel and layer; XSS name stays inert; progress label maps', () => {
  const m = makeCtx({ params: { bbox: '30,-98,30.5,-97.5' } });
  const inst = m.api.mount(m.ctx);
  m.jobs[0].onProgress('loading_terrain', 0.4);
  assert.strictEqual(m.panel._els['[data-f="progressLabel"]'].textContent, 'Loading terrain');
  m.jobs[0].res(cannedData(XSS));
  return new Promise((r) => setTimeout(r, 0)).then(() => {
    const html = m.panel._els['[data-f="results"]'].innerHTML;
    assert(html.includes('mc-pa-card') && !html.includes('<img'));
    assert(m.log.tips.every((t) => !t.includes('<img')), 'tooltip not escaped');
    assert(m.log.tips.some((t) => t.includes('&lt;img')));
    inst.unmount();
  });
});

test('error from fetchJob shows plain message', () => {
  const m = makeCtx({ params: { bbox: '30,-98,30.5,-97.5' } });
  m.api.mount(m.ctx);
  const e = new Error('no_nodes_in_area'); e.status = 400;
  m.jobs[0].rej(e);
  return new Promise((r) => setTimeout(r, 0)).then(() => {
    assert.strictEqual(m.panel._els['[data-f="error"]'].textContent, 'No known repeaters in this area');
  });
});

/* ---- marginal links, legend, framing, clearance wording ---- */
function marginalData() {
  const d = cannedData('Alpha');
  d.nodes.push({ pubkey: 'cc', name: 'C', role: 'repeater', lat: 30.2, lon: -98.2, stale: false, in_area: true, component: 0 });
  d.edges = [
    { a: 0, b: 1, class: 'observed', distance_km: 5, clearance_pct: 80 },
    { a: 0, b: 2, class: 'solid', distance_km: 6, clearance_pct: 90 },
    { a: 1, b: 2, class: 'marginal', distance_km: 7, clearance_pct: 5 },
    { a: 0, b: 2, class: 'marginal', distance_km: 7, clearance_pct: 6 }
  ];
  d.sites[0].links = [{ node: 0, distance_km: 41.3, clearance_pct: -999, class: 'marginal' }, { node: 1, distance_km: 3, clearance_pct: 999, class: 'solid' }];
  return d;
}
const edgeCount = (m) => m.layer.layers[0].layers[1].layers.length;   // root -> edgeLayer
function mountedWith(params, extra) {
  const m = makeCtx(Object.assign({ params: Object.assign({ bbox: '30,-98,30.5,-97.5' }, params) }, extra || {}));
  const inst = m.api.mount(m.ctx);
  m.jobs[0].res(marginalData());
  return new Promise((r) => setTimeout(r, 0)).then(() => ({ m, inst }));
}

test('marginal param: parse and hash round-trip; never part of the coverage request', () => {
  assert.strictEqual(I.parseAreaParams({ marginal: '1' }, CFG).marginal, true);
  assert.strictEqual(I.parseAreaParams({}, CFG).marginal, false);
  assert.strictEqual(I.parseAreaParams({ marginal: '0' }, CFG).marginal, false);
  const a = I.buildCoverageUrl({ bbox: '30,-98,30.5,-97.5', marginal: true }, CFG);
  const b = I.buildCoverageUrl({ bbox: '30,-98,30.5,-97.5', marginal: false }, CFG);
  assert.strictEqual(a, b);
  assert(!/marginal/.test(a));
});

test('edge filtering: marginal hidden by default, drawn with marginal=1', () => mountedWith({}).then(({ m, inst }) => {
  assert.strictEqual(edgeCount(m), 2);
  inst.unmount();
  return mountedWith({ marginal: '1' });
}).then(({ m, inst }) => {
  assert.strictEqual(edgeCount(m), 4);
  inst.unmount();
}));

test('toggle re-renders edges only: no refetch, no refit, hash gets marginal', () => {
  const sets = [], fits = [];
  return mountedWith({}, { setParams: (p) => sets.push(p),
    map: { getBounds() {}, getContainer: () => fakeEl(), dragging: { disable() {}, enable() {} }, on() {}, off() {}, fitBounds: (...a) => fits.push(a), getZoom: () => 9 } })
    .then(({ m, inst }) => {
      assert.strictEqual(fits.length, 2, 'bbox fit on start, result fit on arrival');
      const box = { getAttribute: (k) => (k === 'data-f' ? 'marginal' : null), checked: true };
      m.panel._l.change[0]({ target: box });
      assert.strictEqual(edgeCount(m), 4);
      assert.strictEqual(m.jobs.length, 1, 'no refetch');
      assert.strictEqual(fits.length, 2, 'no refit');
      assert.strictEqual(sets[sets.length - 1].marginal, '1');
      box.checked = false;
      m.panel._l.change[0]({ target: box });
      assert.strictEqual(edgeCount(m), 2);
      assert.strictEqual(sets[sets.length - 1].marginal, '0');
      inst.onParams({ bbox: '30,-98,30.5,-97.5', marginal: '1' });   // hash edit by hand
      assert.strictEqual(edgeCount(m), 4);
      assert.strictEqual(m.jobs.length, 1);
      inst.unmount();
    });
});

test('fit covers the area bbox and the suggested sites', () => {
  const fits = [];
  return mountedWith({}, { map: { getBounds() {}, getContainer: () => fakeEl(), dragging: { disable() {}, enable() {} }, on() {}, off() {}, fitBounds: (...a) => fits.push(a), getZoom: () => 9 } })
    .then(({ inst }) => {
      const last = fits[fits.length - 1], pts = last[0], opt = last[1];
      assert(pts.some((p) => p[0] === 30 && p[1] === -98) && pts.some((p) => p[0] === 30.5 && p[1] === -97.5));
      assert(pts.some((p) => p[0] === 30.05 && p[1] === -98.05), 'site included');
      assert.strictEqual(JSON.stringify(opt.paddingTopLeft), '[24,24]');
      assert.strictEqual(JSON.stringify(opt.paddingBottomRight), '[24,24]');
      inst.unmount();
    });
});

test('legend uses the map classes; marginal row hidden until shown', () => {
  const off = I.resultsHtml(marginalData(), { he: 5, marginal: false }, esc);
  ['mc-pa-edge mc-pa-edge-observed', 'mc-pa-edge mc-pa-edge-solid', 'mc-pa-edge mc-pa-edge-marginal', 'mc-pa-link', 'mc-pa-stale'].forEach((c) => assert(off.includes(c), c));
  assert(/data-legend="marginal" hidden/.test(off));
  assert(/data-f="marginal"(?! checked)/.test(off) && off.includes('(2)'));
  const on = I.resultsHtml(marginalData(), { he: 5, marginal: true }, esc);
  assert(!/data-legend="marginal" hidden/.test(on) && /data-f="marginal" checked/.test(on));
});

test('site links use clearance wording, with or without ctx.fmtClearance', () => {
  const html = I.resultsHtml(marginalData(), { he: 5 }, esc);
  assert(html.includes('41.3 km · terrain blocks the path (marginal)'), html);
  assert(html.includes('full clearance (solid)'));
  assert(!html.includes('-999') && !html.includes('999%'));
  const custom = I.resultsHtml(marginalData(), { he: 5 }, esc, () => 'X');
  assert(custom.includes('41.3 km · X (marginal)'));
  assert.strictEqual(I.fmtClearance(999), 'full clearance');
  assert.strictEqual(I.fmtClearance(-100), 'terrain blocks the path');
});

/* ---- draw area: pointer events ---- */
function drawMap(opts) {
  const c = fakeEl();
  const cls = new Set();
  c.classList = { add: (x) => cls.add(x), remove: (x) => cls.delete(x), contains: (x) => cls.has(x) };
  c._cls = cls; c._captured = [];
  c.setPointerCapture = (id) => c._captured.push(id);
  c.releasePointerCapture = () => {};
  const st = { dragOn: !opts || opts.dragging !== false, log: [] };
  const map = { getBounds: () => ({ getSouth: () => 30, getWest: () => -98, getNorth: () => 30.5, getEast: () => -97.5 }),
    getContainer: () => c, fitBounds() {}, getZoom: () => 9, on() {}, off() {},
    mouseEventToLatLng: (e) => ({ lat: e.lat, lng: e.lng }),
    dragging: { enabled: () => st.dragOn, disable() { st.dragOn = false; st.log.push('off'); }, enable() { st.dragOn = true; st.log.push('on'); } } };
  return { map, c, st };
}
function drawSetup(opts) {
  const dm = drawMap(opts);
  const calls = [];
  const m = makeCtx({ map: dm.map, setParams(p) { calls.push(p); } });
  const inst = m.api.mount(m.ctx);
  m.panel._els['[data-act="draw"]'] = m.panel._els['[data-act="draw"]'] || fakeEl();
  const btn = m.panel._els['[data-act="draw"]'];
  const start = () => m.panel._l.click[0]({ target: { closest: () => ({ getAttribute: () => 'draw', disabled: false }) } });
  const fire = (type, o) => (dm.c._l[type] || []).slice().forEach((fn) => fn(Object.assign(
    { pointerId: 1, isPrimary: true, pointerType: 'mouse', button: 0, clientX: 0, clientY: 0, lat: 0, lng: 0 }, o)));
  return Object.assign({ inst, calls, btn, start, fire, m }, dm);
}
const lastBbox = (d) => d.calls.length ? d.calls[d.calls.length - 1] : null;
const nListeners = (d) => ['pointerdown', 'pointermove', 'pointerup', 'pointercancel'].reduce((n, t) => n + (d.c._l[t] || []).length, 0);

test('draw: mouse pointer drag produces the bbox', () => {
  const d = drawSetup();
  d.start();
  assert.strictEqual(nListeners(d), 4);
  d.fire('pointerdown', { clientX: 10, clientY: 10, lat: 30, lng: -98 });
  d.fire('pointermove', { clientX: 60, clientY: 60, lat: 30.2, lng: -97.8 });
  d.fire('pointerup', { clientX: 100, clientY: 100, lat: 30.4, lng: -97.6 });
  assert.deepStrictEqual(d.c._captured, [1]);
  assert(lastBbox(d) && JSON.stringify(lastBbox(d)).includes('30.4'), JSON.stringify(d.calls));
  assert.strictEqual(nListeners(d), 0, 'draw mode ends after a rectangle');
  d.inst.unmount();
});

test('draw: touch pointer drag produces the bbox', () => {
  const d = drawSetup();
  d.start();
  const t = { pointerType: 'touch', pointerId: 7 };
  d.fire('pointerdown', Object.assign({ clientX: 10, clientY: 10, lat: 30, lng: -98 }, t));
  d.fire('pointermove', Object.assign({ clientX: 80, clientY: 80, lat: 30.3, lng: -97.7 }, t));
  d.fire('pointerup', Object.assign({ clientX: 90, clientY: 90, lat: 30.45, lng: -97.55 }, t));
  assert(lastBbox(d) && JSON.stringify(lastBbox(d)).includes('30.45'), JSON.stringify(d.calls));
  d.inst.unmount();
});

test('draw: two taps pick the corners', () => {
  const d = drawSetup();
  d.start();
  const t = { pointerType: 'touch', pointerId: 3 };
  d.fire('pointerdown', Object.assign({ clientX: 10, clientY: 10, lat: 30, lng: -98 }, t));
  d.fire('pointerup', Object.assign({ clientX: 12, clientY: 11, lat: 30, lng: -98 }, t));
  assert.strictEqual(d.calls.length, 0, 'first tap only sets a corner');
  assert.strictEqual(nListeners(d), 4);
  d.fire('pointermove', { pointerId: 3, pointerType: 'touch', lat: 30.2, lng: -97.8 });   // rubber band follows
  d.fire('pointerdown', Object.assign({ clientX: 200, clientY: 200, lat: 30.45, lng: -97.55 }, t));
  d.fire('pointerup', Object.assign({ clientX: 200, clientY: 200, lat: 30.45, lng: -97.55 }, t));
  assert(lastBbox(d) && JSON.stringify(lastBbox(d)).includes('30.45'), JSON.stringify(d.calls));
  d.inst.unmount();
});

test('draw: a second pointer cancels the gesture; draw mode stays on', () => {
  const d = drawSetup();
  d.start();
  d.fire('pointerdown', { pointerType: 'touch', pointerId: 1, clientX: 10, clientY: 10, lat: 30, lng: -98 });
  d.fire('pointerdown', { pointerType: 'touch', pointerId: 2, isPrimary: false, clientX: 90, clientY: 90, lat: 30.4, lng: -97.6 });
  d.fire('pointerup', { pointerType: 'touch', pointerId: 1, clientX: 100, clientY: 100, lat: 30.4, lng: -97.6 });
  d.fire('pointerup', { pointerType: 'touch', pointerId: 2, isPrimary: false, clientX: 90, clientY: 90, lat: 30.4, lng: -97.6 });
  assert.strictEqual(d.calls.length, 0, 'no bbox from a cancelled gesture');
  assert.strictEqual(nListeners(d), 4);
  d.fire('pointerdown', { clientX: 0, clientY: 0, lat: 30, lng: -98 });                    // can still draw afterwards
  d.fire('pointerup', { clientX: 90, clientY: 90, lat: 30.4, lng: -97.6 });
  assert.strictEqual(d.calls.length, 1);
  d.inst.unmount();
});

test('draw: pointercancel aborts without a bbox', () => {
  const d = drawSetup();
  d.start();
  d.fire('pointerdown', { clientX: 0, clientY: 0, lat: 30, lng: -98 });
  d.fire('pointercancel', {});
  d.fire('pointerup', { clientX: 90, clientY: 90, lat: 30.4, lng: -97.6 });
  assert.strictEqual(d.calls.length, 0);
  d.inst.unmount();
});

test('draw: map dragging is disabled while drawing and restored after', () => {
  const d = drawSetup();
  d.start();
  assert.strictEqual(d.st.dragOn, false);
  assert(d.c._cls.has('mc-pa-drawing-map'));
  d.start();                                           // toggle off
  assert.strictEqual(d.st.dragOn, true);
  assert(!d.c._cls.has('mc-pa-drawing-map'));
  d.inst.unmount();
  const d2 = drawSetup({ dragging: false });          // was already off: stays off
  d2.start();
  d2.start();
  assert.strictEqual(d2.st.dragOn, false);
  assert.deepStrictEqual(d2.st.log, []);
  d2.inst.unmount();
});

test('draw: unmount mid-draw removes every listener', () => {
  const d = drawSetup();
  d.start();
  d.fire('pointerdown', { clientX: 0, clientY: 0, lat: 30, lng: -98 });
  d.inst.unmount();
  assert.strictEqual(nListeners(d), 0);
  assert.strictEqual(d.st.dragOn, true);
  assert(!d.c._cls.has('mc-pa-drawing-map'));
});

Promise.all(pending).then(() => {
  console.log(`\n${passed} passed, ${failed} failed`);
  process.exit(failed ? 1 : 0);
});

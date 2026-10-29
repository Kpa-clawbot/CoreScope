/* Unit tests for MediaMesh (radio2) nodes: mediamesh.js, and how the Map,
 * Live and Nodes pages use it. The server sends
 * `mediamesh: {mode, radio, source, last_seen}` on a node that runs it.
 *
 * Map tests use the same vm sandbox as test-issue-1862-map-region-filter.js,
 * so they exercise map.js itself.
 */
'use strict';
const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log(`  ✅ ${name}`); }
  catch (e) { failed++; console.log(`  ❌ ${name}: ${e.message}`); }
}

const MM = require('../../public/mediamesh.js');

const RXTX = { public_key: 'aa', name: 'Peak', role: 'repeater', lat: 47.6, lon: -122.3,
  mediamesh: { mode: 'rxtx', radio: '911.15,250,7,5', source: 'status', last_seen: '2026-10-29T00:00:00Z' } };
const RX = { public_key: 'bb', name: 'Valley', role: 'repeater', lat: 47.5, lon: -122.2,
  mediamesh: { mode: 'rx', source: 'neighbor', last_seen: '2026-10-29T00:00:00Z' } };
const PLAIN = { public_key: 'cc', name: 'Plain', role: 'repeater', lat: 47.4, lon: -122.1 };
const COMPANION_MM = { public_key: 'dd', name: 'Phone', role: 'companion', lat: 47.3, lon: -122.0,
  mediamesh: { mode: 'rxtx', source: 'heard', last_seen: '2026-10-29T00:00:00Z' } };

console.log('\n=== mediamesh.js ===');

test('info: only rx and rxtx marks count', () => {
  assert.strictEqual(MM.info(RXTX).mode, 'rxtx');
  assert.strictEqual(MM.info(RX).mode, 'rx');
  assert.strictEqual(MM.info(PLAIN), null);
  assert.strictEqual(MM.info({ mediamesh: { mode: 'off' } }), null);
  assert.strictEqual(MM.info({ mediamesh: true }), null);
  assert.strictEqual(MM.info(null), null);
});

test('markerClass: ring plus a filled (rxtx) or hollow (rx) badge', () => {
  assert.strictEqual(MM.markerClass(RXTX), ' mm-node mm-node--rxtx');
  assert.strictEqual(MM.markerClass(RX), ' mm-node mm-node--rx');
  assert.strictEqual(MM.markerClass(PLAIN), '');
});

test('describeRadio: readable setting, or nothing for junk', () => {
  assert.strictEqual(MM.describeRadio('911.15,250,7,5'), '911.15 MHz · 250 kHz SF7');
  assert.strictEqual(MM.describeRadio('869.618,62.5,8,8'), '869.618 MHz · 62.5 kHz SF8');
  assert.strictEqual(MM.describeRadio('SX1262'), '');
  assert.strictEqual(MM.describeRadio(undefined), '');
  assert.strictEqual(MM.describeRadio('0,0,0,0'), '');
});

test('describe: mode, setting and how it is known', () => {
  assert.strictEqual(MM.describe(RXTX), 'listens and sends · 911.15 MHz · 250 kHz SF7 — reported by the node');
  assert.strictEqual(MM.describe(RX), 'listens — reported by a neighbour');
  assert.strictEqual(MM.describe(COMPANION_MM), 'listens and sends — heard sending on radio2');
  assert.strictEqual(MM.describe(PLAIN), '');
});

test('filterAccepts: everything when off, MediaMesh nodes when on', () => {
  assert.ok(MM.filterAccepts(PLAIN, false));
  assert.ok(MM.filterAccepts(RXTX, true));
  assert.ok(!MM.filterAccepts(PLAIN, true));
});

function makeSandbox(stored) {
  const L = {};
  L.point = (x, y) => ({ x, y });
  L.latLng = (a, b) => ({ lat: a, lng: b });
  L.divIcon = (opts) => ({ _isDivIcon: true, options: opts, html: opts.html, className: opts.className });
  L.layerGroup = () => ({ addLayer(){ return this; }, removeLayer(){ return this; }, clearLayers(){ return this; }, eachLayer(){}, addTo(){ return this; }, hasLayer(){ return false; } });
  L.marker = (latlng, opts) => ({ _isMarker: true, _latlng: latlng, options: opts || {}, getLatLng(){ return this._latlng; }, bindPopup(){ return this; }, bindTooltip(){ return this; } });
  function MarkerClusterGroup(opts) { this.options = opts || {}; }
  MarkerClusterGroup.prototype.addLayer = function () { return this; };
  MarkerClusterGroup.prototype.addLayers = function () { return this; };
  MarkerClusterGroup.prototype.clearLayers = function () { return this; };
  MarkerClusterGroup.prototype.eachLayer = function () {};
  MarkerClusterGroup.prototype.addTo = function () { return this; };
  L.MarkerClusterGroup = MarkerClusterGroup;
  L.markerClusterGroup = (opts) => new MarkerClusterGroup(opts);

  const store = Object.assign({}, stored || {});
  const ctx = {
    window: {},
    document: { addEventListener(){}, getElementById(){ return null; }, querySelector(){ return null; }, querySelectorAll(){ return []; }, createElement(){ return { id:'', textContent:'', innerHTML:'', appendChild(){}, addEventListener(){}, setAttribute(){}, classList:{add(){},remove(){},toggle(){}} }; }, head: { appendChild(){} }, body: { appendChild(){} } },
    console, Date, Math, Array, Object, String, Number, JSON, RegExp, Error,
    parseInt, parseFloat, isFinite, isNaN, Map, Set, Promise,
    setTimeout: ()=>{}, clearTimeout: ()=>{}, setInterval: ()=>{}, clearInterval: ()=>{},
    registerPage: () => {}, esc: (s) => s, onWS: () => {}, offWS: () => {},
    localStorage: { getItem: k => (k in store ? store[k] : null), setItem: (k, v) => { store[k] = String(v); }, removeItem: k => { delete store[k]; } },
    fetch: () => Promise.resolve({ json: () => Promise.resolve({}) }),
    addEventListener(){}, dispatchEvent(){},
    L: L,
  };
  ctx.window.L = ctx.L;
  vm.createContext(ctx);
  vm.runInContext(fs.readFileSync('public/roles.js', 'utf8'), ctx);
  vm.runInContext(fs.readFileSync('public/mediamesh.js', 'utf8'), ctx);
  for (const k of Object.keys(ctx.window)) ctx[k] = ctx.window[k];
  vm.runInContext(fs.readFileSync('public/map.js', 'utf8'), ctx);
  for (const k of Object.keys(ctx.window)) ctx[k] = ctx.window[k];
  return ctx;
}

console.log('\n=== map.js ===');
{
  const m = makeSandbox().window.__meshcoreMapInternals;
  const base = Object.assign({}, m.filters, { repeater: true, companion: true, statusFilter: 'all', byteSize: 'all', scopeState: 'all', regionScope: '' });
  const ctx = { selectedReferenceNode: null, neighborPubkeys: null };

  test('MediaMesh only is off by default and every node passes', () => {
    assert.strictEqual(m.filters.mediaMeshOnly, false);
    for (const n of [RXTX, RX, PLAIN]) assert.ok(m.nodePassesMapFilters(n, base, ctx), n.name);
  });

  test('MediaMesh only keeps nodes that run it, any role', () => {
    const f = Object.assign({}, base, { mediaMeshOnly: true });
    assert.ok(m.nodePassesMapFilters(RXTX, f, ctx));
    assert.ok(m.nodePassesMapFilters(RX, f, ctx));
    assert.ok(m.nodePassesMapFilters(COMPANION_MM, f, ctx));
    assert.ok(!m.nodePassesMapFilters(PLAIN, f, ctx));
  });

  test('MediaMesh only narrows the map, so plain observer pins stand down', () => {
    assert.ok(m.nodeFiltersNarrowed(Object.assign({}, base, { mediaMeshOnly: true })));
    assert.ok(!m.observerLayerShown(Object.assign({}, base, { observer: true, mediaMeshOnly: true })));
    assert.ok(m.observerLayerShown(Object.assign({}, base, { observer: true, mediaMeshOnly: false })));
  });

  test('the toggle is remembered', () => {
    const s = makeSandbox({ 'meshcore-map-mediamesh-only': 'true' }).window.__meshcoreMapInternals;
    assert.strictEqual(s.filters.mediaMeshOnly, true);
  });

  test('dot markers carry the ring and badge classes', () => {
    assert.ok(m.makeMarkerIcon('repeater', false, false, null, MM.markerClass(RXTX)).className.includes('mm-node mm-node--rxtx'));
    assert.ok(m.makeMarkerIcon('repeater', false, false, null, MM.markerClass(RX)).className.includes('mm-node--rx'));
    assert.ok(!m.makeMarkerIcon('repeater', false, false, null, '').className.includes('mm-node'));
    assert.ok(!m.makeMarkerIcon('repeater', false, false).className.includes('mm-node'), 'callers without the argument are unchanged');
  });

  test('hash-label markers carry them too', () => {
    assert.ok(m.makeRepeaterLabelIcon(RXTX, false, false, null, null).className.includes('mm-node mm-node--rxtx'));
    assert.ok(!m.makeRepeaterLabelIcon(PLAIN, false, false, null, null).className.includes('mm-node'));
  });
}

console.log('\n=== Wiring and theme ===');

test('mediamesh.js loads before the pages that use it', () => {
  const html = fs.readFileSync('public/index.html', 'utf8');
  const at = html.indexOf('mediamesh.js');
  assert.ok(at > 0, 'not in index.html');
  for (const f of ['map.js', 'nodes.js', 'live.js']) assert.ok(html.indexOf('src="' + f) > at, f + ' loads first');
});

test('Map popup, Live and Nodes detail show the MediaMesh line, escaped', () => {
  const map = fs.readFileSync('public/map.js', 'utf8');
  assert.ok(map.includes('safeEsc(window.MediaMesh.describe(node))'));
  for (const f of ['public/live.js', 'public/nodes.js']) {
    assert.ok(fs.readFileSync(f, 'utf8').includes('escapeHtml(window.MediaMesh.describe(n))'), f);
  }
  assert.ok(fs.readFileSync('public/live.js', 'utf8').includes("window.MediaMesh.markerClass(n)"), 'Live markers');
});

test('ring and badge use --mediamesh-node-color', () => {
  const css = fs.readFileSync('public/style.css', 'utf8');
  const block = css.slice(css.indexOf('.mm-node::before'));
  assert.ok(block.includes('var(--mediamesh-node-color)'));
  assert.ok(/\.mm-node--rxtx::after\s*\{\s*background:\s*var\(--mediamesh-node-color\)/.test(css), 'rxtx badge is filled');
});

console.log(`\n${passed} passed, ${failed} failed`);
if (failed) process.exit(1);

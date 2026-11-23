/* test-map-vector-leaflet-zoom.js — vector basemap zoom limits (#2428).
 *
 * Runs the real public/map-vector.js against the real vendored Leaflet and
 * markercluster in jsdom: the vector group sets the map's min/max zoom on
 * add, before MapLibre arrives, while GL is shown and after a raster
 * fallback, and leaves no limit behind when removed.
 *
 * Needs jsdom, so it is a domIntegration suite (scripts/non-unit-tests.json)
 * run from the E2E job, not test-all.sh. No server or browser needed.
 * The rest of map-vector.js is covered by tests/unit/test-map-vector.js.
 *
 * Runs via: node tests/e2e/test-map-vector-leaflet-zoom.js
 */
'use strict';
const fs = require('fs');
const path = require('path');
const assert = require('assert');

const SRC = fs.readFileSync(path.join(__dirname, '..', '..', 'public', 'map-vector.js'), 'utf8');
const ATTR = '&copy; OpenFreeMap &copy; OpenMapTiles';
const STYLE = 'https://tiles.openfreemap.org/styles/liberty';

let passed = 0, failed = 0;
const queue = [];
function test(name, fn) { queue.push([name, fn]); }
const flush = () => new Promise((r) => setImmediate(r));

function makeEvented(obj) {
  const handlers = {};
  obj.on = function (type, fn) { (handlers[type] = handlers[type] || []).push(fn); return obj; };
  obj.once = function (type, fn) {
    const w = function (e) { obj.off(type, w); fn(e); };
    return obj.on(type, w);
  };
  obj.off = function (type, fn) { handlers[type] = (handlers[type] || []).filter((h) => h !== fn); return obj; };
  obj.fire = function (type, e) { (handlers[type] || []).slice().forEach((h) => h(e || {})); return obj; };
  return obj;
}

// ── zoom limits, against the real vendored Leaflet (jsdom) ──────────────
// Leaflet registers a layer's min/maxZoom with the map only for GridLayers;
// markercluster's onAdd throws "Map has no maxZoom specified" without one.
const { JSDOM } = require('jsdom');
const VENDOR_DIR = path.join(__dirname, '..', '..', 'public', 'vendor');
function makeLeafletSandbox() {
  const dom = new JSDOM('<!doctype html><html><head></head><body><div id="m" style="width:400px;height:300px"></div></body></html>',
    { runScripts: 'outside-only', url: 'https://example.org/sub/' });
  const w = dom.window;
  w.eval(fs.readFileSync(path.join(VENDOR_DIR, 'leaflet-1.9.4', 'leaflet.js'), 'utf8'));
  w.eval(fs.readFileSync(path.join(VENDOR_DIR, 'leaflet.markercluster.js'), 'utf8'));
  w.HTMLCanvasElement.prototype.getContext = () => ({ getExtension: () => null });
  w.console.warn = () => {};
  // Fake timers for map-vector.js only; Leaflet keeps the real ones.
  let now = 0, timers = [];
  w.__st = (fn, ms) => { const t = { fn, at: now + ms, id: timers.length + 1 }; timers.push(t); return t.id; };
  w.__ct = (id) => { timers = timers.filter((t) => t.id !== id); };
  function advance(ms) { now += ms; const due = timers.filter((t) => t.at <= now); timers = timers.filter((t) => t.at > now); due.forEach((t) => t.fn()); }
  w.eval('(function (setTimeout, clearTimeout) {\n' + SRC + '\n})(window.__st, window.__ct);');
  const L = w.L;
  // Mirrors leaflet-maplibre-gl's shape: a plain L.Layer, not a GridLayer.
  const FakeGL = L.Layer.extend({
    onAdd() { this._glMap = makeEvented({ remove() {} }); },
    onRemove() { this._glMap = null; },
    getMaplibreMap() { return this._glMap; },
  });
  function loadLibrary() {
    const tags = Array.from(w.document.head.querySelectorAll('script[data-mc-maplibre]'));
    w.maplibregl = { setWorkerUrl() {} };
    tags[0].onload();
    L.maplibreGL = (o) => new FakeGL(o);
    tags[1].onload();
  }
  function build(desc) {
    return w.MC_buildVectorLayer(Object.assign({ url: STYLE, attribution: ATTR, maxZoom: 19 }, desc),
      { fallback: () => L.tileLayer('https://t.example.org/{z}/{x}/{y}.png', { maxZoom: (desc && desc.maxZoom) || 19 }) });
  }
  const newMap = () => { const el = w.document.createElement('div'); w.document.body.appendChild(el); return L.map(el); };
  return { w, L, advance, loadLibrary, build, newMap };
}

test('Leaflet: a vector group sets the map zoom limit before MapLibre arrives; markers can be added at once', async () => {
  const s = makeLeafletSandbox();
  const map = s.newMap().setView([0, 0], 5);
  assert.strictEqual(map.getMaxZoom(), Infinity, 'no limit without a basemap');
  const g = s.build({ maxZoom: 17, minZoom: 2 });
  map.addLayer(g);
  assert.strictEqual(map.getMaxZoom(), 17, 'limit set synchronously on add');
  assert.strictEqual(map.getMinZoom(), 2);
  assert.doesNotThrow(() => s.L.markerClusterGroup().addTo(map), 'markercluster finds a maxZoom');
  map.removeLayer(g);
  assert.strictEqual(map.getMaxZoom(), Infinity, 'no lingering limit');
  assert.strictEqual(map.getMinZoom(), 0);
});

test('Leaflet: the limit holds while GL is shown and after a raster fallback; removed symmetrically', async () => {
  const s = makeLeafletSandbox();
  const map = s.newMap().setView([0, 0], 5);
  const g = s.build({ maxZoom: 18 });
  map.addLayer(g);
  s.loadLibrary(); await flush();
  assert.strictEqual(g.getLayers().length, 1, 'GL mounted');
  assert.ok(g.getLayers()[0].getMaplibreMap(), 'GL mounted');
  assert.strictEqual(map.getMaxZoom(), 18, 'GL shown');
  map.removeLayer(g);
  assert.strictEqual(map.getMaxZoom(), Infinity);
  map.addLayer(g);
  assert.strictEqual(map.getMaxZoom(), 18, 're-added');
  map.removeLayer(g);

  const g2 = s.build({ maxZoom: 18 });
  map.addLayer(g2);
  s.advance(15000);
  assert.strictEqual(g2.mcFallbackReason, 'load-timeout');
  assert.strictEqual(map.getMaxZoom(), 18, 'after the raster fallback');
  map.removeLayer(g2);
  assert.strictEqual(map.getMaxZoom(), Infinity, 'fallback and group both unregistered');
});

test('Leaflet: switching raster <-> vector moves the limit; a group added and removed before load leaves none', () => {
  const s = makeLeafletSandbox();
  const map = s.newMap();
  const early = s.build({ maxZoom: 12 });
  map.addLayer(early);
  map.removeLayer(early); // before setView: Leaflet skips onRemove
  map.setView([0, 0], 5);
  assert.strictEqual(map.getMaxZoom(), Infinity, 'pre-load add/remove leaves no limit');
  const raster = s.L.tileLayer('https://t.example.org/{z}/{x}/{y}.png', { maxZoom: 16 }).addTo(map);
  assert.strictEqual(map.getMaxZoom(), 16);
  map.removeLayer(raster);
  const g = s.build({ maxZoom: 19 });
  map.addLayer(g);
  assert.strictEqual(map.getMaxZoom(), 19, 'raster -> vector');
  map.removeLayer(g);
  raster.addTo(map);
  assert.strictEqual(map.getMaxZoom(), 16, 'vector -> raster');

  const map2 = s.newMap();
  const g3 = s.build({ maxZoom: 14 });
  map2.addLayer(g3); // before load: limit already set, kept through load
  assert.strictEqual(map2.getMaxZoom(), 14);
  map2.setView([0, 0], 5);
  assert.strictEqual(map2.getMaxZoom(), 14);
  assert.doesNotThrow(() => s.L.markerClusterGroup().addTo(map2));
});

(async () => {
  for (const [name, fn] of queue) {
    try { await fn(); passed++; console.log('  ✅ ' + name); }
    catch (e) { failed++; console.log('  ❌ ' + name + ': ' + e.message); }
  }
  console.log(`\nmap-vector-leaflet-zoom: ${passed} passed, ${failed} failed`);
  process.exit(failed > 0 ? 1 : 0);
})();

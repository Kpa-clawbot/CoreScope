/**
 * Unit tests for the client-side geo_filter port in public/app.js
 * (nodePassesGeoFilter / geoPointInPolygon / geoDistToSegmentKm).
 *
 * These MUST mirror internal/geofilter (Go) exactly — same ray-casting
 * point-in-polygon, same flat-earth segment distance, same precedence
 * (polygon+buffer, then bbox fallback), same "(0,0) or missing GPS always
 * passes" rule — otherwise the Nodes page's Domestic/Foreign filter would
 * silently disagree with what /api/nodes itself considers foreign.
 */
'use strict';

const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

let passed = 0, failed = 0;
function test(name, fn) {
  try {
    fn();
    passed++;
    console.log(`  ✅ ${name}`);
  } catch (e) {
    failed++;
    console.log(`  ❌ ${name}: ${e.message}`);
  }
}

function makeSandbox() {
  const ctx = {
    window: { addEventListener: () => {}, dispatchEvent: () => {} },
    document: {
      readyState: 'complete',
      createElement: () => ({ id: '', textContent: '', innerHTML: '' }),
      head: { appendChild: () => {} },
      getElementById: () => null,
      addEventListener: () => {},
      querySelectorAll: () => [],
      querySelector: () => null,
    },
    console, Date, Infinity, Math, Array, Object, String, Number, JSON, RegExp,
    Error, TypeError, parseInt, parseFloat, isNaN, isFinite,
    encodeURIComponent, decodeURIComponent,
    setTimeout: () => {}, clearTimeout: () => {}, setInterval: () => {}, clearInterval: () => {},
    fetch: () => Promise.resolve({ json: () => Promise.resolve({}) }),
    performance: { now: () => Date.now() },
    localStorage: (() => { const s = {}; return { getItem: k => s[k] || null, setItem: (k, v) => { s[k] = String(v); }, removeItem: k => { delete s[k]; } }; })(),
    location: { hash: '' },
    getHashParams: function() { return new URLSearchParams((ctx.location.hash.split('?')[1] || '')); },
    CustomEvent: class CustomEvent {},
    Map, Promise, URLSearchParams,
    addEventListener: () => {},
    dispatchEvent: () => {},
    requestAnimationFrame: (cb) => setTimeout(cb, 0),
    getComputedStyle: () => ({ getPropertyValue: () => '' }),
    registerPage: () => {},
  };
  vm.createContext(ctx);
  vm.runInContext(fs.readFileSync('public/app.js', 'utf8'), ctx);
  return ctx;
}

const ctx = makeSandbox();
const nodePassesGeoFilter = ctx.nodePassesGeoFilter;
const geoPointInPolygon = ctx.geoPointInPolygon;
const geoDistToSegmentKm = ctx.geoDistToSegmentKm;

console.log('\n=== app.js: nodePassesGeoFilter (mirrors internal/geofilter.PassesFilter) ===');

test('is exported as a global function', () => {
  assert.strictEqual(typeof nodePassesGeoFilter, 'function');
});

test('no geo_filter configured (null) → always passes', () => {
  assert.strictEqual(nodePassesGeoFilter(0, 0, null), true);
  assert.strictEqual(nodePassesGeoFilter(90, 180, null), true);
  assert.strictEqual(nodePassesGeoFilter(-90, -180, undefined), true);
});

test('(0,0) always passes regardless of configured box — matches the "no real GPS fix" sentinel', () => {
  const gf = { latMin: 53, latMax: 59, lonMin: 6, lonMax: 15 };
  assert.strictEqual(nodePassesGeoFilter(0, 0, gf), true);
});

test('missing/non-numeric lat or lon always passes (treated as unknown, not foreign)', () => {
  const gf = { latMin: 53, latMax: 59, lonMin: 6, lonMax: 15 };
  assert.strictEqual(nodePassesGeoFilter(null, null, gf), true);
  assert.strictEqual(nodePassesGeoFilter(undefined, undefined, gf), true);
  assert.strictEqual(nodePassesGeoFilter('55', '10', gf), true, 'string coordinates are not numbers — treated as unknown');
  assert.strictEqual(nodePassesGeoFilter(NaN, 10, gf), true);
});

test('bbox: point inside the box passes', () => {
  const gf = { latMin: 53, latMax: 59, lonMin: 6, lonMax: 15 };
  assert.strictEqual(nodePassesGeoFilter(55.0, 10.0, gf), true); // Copenhagen-ish
});

test('bbox: point outside the box fails', () => {
  const gf = { latMin: 53, latMax: 59, lonMin: 6, lonMax: 15 };
  assert.strictEqual(nodePassesGeoFilter(40.7, -74.0, gf), false); // New York
});

test('bbox: exact boundary values are inclusive', () => {
  const gf = { latMin: 53, latMax: 59, lonMin: 6, lonMax: 15 };
  assert.strictEqual(nodePassesGeoFilter(53, 6, gf), true, 'latMin/lonMin corner');
  assert.strictEqual(nodePassesGeoFilter(59, 15, gf), true, 'latMax/lonMax corner');
});

test('bbox: a partially-configured box (missing one bound) falls through to always-pass', () => {
  assert.strictEqual(nodePassesGeoFilter(40.7, -74.0, { latMin: 53, latMax: 59, lonMin: 6 }), true);
});

console.log('\n=== app.js: geoPointInPolygon (mirrors internal/geofilter.PointInPolygon) ===');

test('point inside a simple square polygon', () => {
  // Square roughly covering Denmark, lat/lon pairs (matches Go's [][2]float64 = [lat, lon])
  const square = [[53, 6], [53, 15], [59, 15], [59, 6]];
  assert.strictEqual(geoPointInPolygon(55, 10, square), true);
});

test('point outside a simple square polygon', () => {
  const square = [[53, 6], [53, 15], [59, 15], [59, 6]];
  assert.strictEqual(geoPointInPolygon(40.7, -74.0, square), false);
});

console.log('\n=== app.js: nodePassesGeoFilter with polygon + buffer ===');

test('polygon: point inside passes without needing the buffer', () => {
  const gf = { polygon: [[53, 6], [53, 15], [59, 15], [59, 6]], bufferKm: 0 };
  assert.strictEqual(nodePassesGeoFilter(55, 10, gf), true);
});

test('polygon: point just outside the edge fails when bufferKm is 0', () => {
  const gf = { polygon: [[53, 6], [53, 15], [59, 15], [59, 6]], bufferKm: 0 };
  // ~0.09 degrees lat ≈ 10km south of the 53°N edge
  assert.strictEqual(nodePassesGeoFilter(52.91, 10, gf), false);
});

test('polygon: point just outside the edge passes when within bufferKm', () => {
  const gf = { polygon: [[53, 6], [53, 15], [59, 15], [59, 6]], bufferKm: 15 };
  // ~10km south of the 53°N edge, buffer is 15km — should pass
  assert.strictEqual(nodePassesGeoFilter(52.91, 10, gf), true);
});

test('polygon: point far outside the edge still fails even with a buffer', () => {
  const gf = { polygon: [[53, 6], [53, 15], [59, 15], [59, 6]], bufferKm: 15 };
  assert.strictEqual(nodePassesGeoFilter(40.7, -74.0, gf), false);
});

test('polygon with <3 points falls through to bbox (matches Go: len(gf.Polygon) >= 3 gate)', () => {
  const gf = { polygon: [[53, 6], [59, 15]], latMin: 53, latMax: 59, lonMin: 6, lonMax: 15 };
  assert.strictEqual(nodePassesGeoFilter(55, 10, gf), true, 'bbox fallback should still apply');
  assert.strictEqual(nodePassesGeoFilter(40.7, -74.0, gf), false, 'bbox fallback should still reject outside points');
});

console.log('\n=== app.js: geoDistToSegmentKm (mirrors internal/geofilter.DistToSegmentKm) ===');

test('distance from a point to a zero-length segment (a === b) is the direct distance', () => {
  const d = geoDistToSegmentKm(53.0, 6.0, [53, 6], [53, 6]);
  assert.strictEqual(d, 0);
});

test('distance is roughly symmetric and non-negative for a real segment', () => {
  const d = geoDistToSegmentKm(52.9, 10, [53, 6], [53, 15]);
  assert.ok(d > 0, 'point south of the segment should have positive distance');
  assert.ok(d < 20, 'point ~0.1 degrees away should be well under 20km: got ' + d);
});

console.log('\n════════════════════════════════════════');
console.log(`  Geo Filter: ${passed} passed, ${failed} failed`);
console.log('════════════════════════════════════════');
process.exit(failed === 0 ? 0 : 1);

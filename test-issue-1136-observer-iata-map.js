/* Unit test (#1136): live.js must parse /api/observers correctly.
 *
 * Regression: PR #1080 wrote `if (Array.isArray(list))` and treated the
 * response as a top-level array. The actual /api/observers shape is
 * `{ observers: [...], server_time: "..." }` (cmd/server/types.go
 * ObserverListResponse). Result: observerIataMap stays empty and ANY
 * region selection drops every packet.
 *
 * This test loads live.js into a vm sandbox and asserts that the exposed
 * builder helper produces a populated map from the realistic API shape.
 */
'use strict';
const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log('  \u2705 ' + name); }
  catch (e) { failed++; console.log('  \u274C ' + name + ': ' + e.message); }
}

function makeSandbox() {
  const ctx = {
    window: { addEventListener: () => {}, dispatchEvent: () => {}, devicePixelRatio: 1 },
    document: {
      readyState: 'complete',
      createElement: () => ({ style: {}, classList: { add(){}, remove(){}, contains(){return false;} }, setAttribute(){}, addEventListener(){}, getContext: () => ({clearRect(){},fillRect(){},beginPath(){},arc(){},fill(){},scale(){},fillText(){}}) }),
      head: { appendChild: () => {} },
      getElementById: () => null,
      addEventListener: () => {},
      querySelectorAll: () => [], querySelector: () => null,
      createElementNS: () => ({ setAttribute(){} }),
      documentElement: { getAttribute: () => null, setAttribute: () => {}, dataset: {} },
      body: { appendChild: () => {}, removeChild: () => {}, contains: () => false },
      hidden: false,
    },
    console, Date, Infinity, Math, Array, Object, String, Number, JSON, RegExp,
    Error, TypeError, Map, Set, Promise, URLSearchParams,
    parseInt, parseFloat, isNaN, isFinite,
    encodeURIComponent, decodeURIComponent,
    setTimeout: () => 0, clearTimeout: () => {},
    setInterval: () => 0, clearInterval: () => {},
    fetch: () => Promise.resolve({ json: () => Promise.resolve({}) }),
    performance: { now: () => Date.now() },
    requestAnimationFrame: () => 0,
    cancelAnimationFrame: () => {},
    localStorage: (() => { const s = {}; return { getItem: k => s[k] !== undefined ? s[k] : null, setItem: (k,v) => { s[k] = String(v); }, removeItem: k => { delete s[k]; } }; })(),
    location: { hash: '', protocol: 'https:', host: 'localhost' },
    CustomEvent: class CustomEvent {},
    addEventListener: () => {}, dispatchEvent: () => {},
    getComputedStyle: () => ({ getPropertyValue: () => '' }),
    matchMedia: () => ({ matches: false, addEventListener: () => {} }),
    navigator: {}, visualViewport: null,
    MutationObserver: function() { this.observe=()=>{}; this.disconnect=()=>{}; },
    WebSocket: function() { this.close=()=>{}; },
    IATA_COORDS_GEO: {},
    L: {
      circleMarker: () => ({addTo(){return this;},bindTooltip(){return this;},on(){return this;},setRadius(){},setStyle(){},setLatLng(){},getLatLng(){return{lat:0,lng:0};},remove(){}}),
      polyline: () => ({addTo(){return this;},setStyle(){},remove(){}}),
      polygon: () => ({addTo(){return this;},remove(){}}),
      map: () => ({setView(){return this;},addLayer(){return this;},on(){return this;},getZoom(){return 11;},getCenter(){return{lat:0,lng:0};},getBounds(){return{contains:()=>true};},fitBounds(){return this;},invalidateSize(){},remove(){},hasLayer(){return false;},removeLayer(){}}),
      layerGroup: () => ({addTo(){return this;},addLayer(){},removeLayer(){},clearLayers(){},hasLayer(){return true;},eachLayer(){}}),
      tileLayer: () => ({addTo(){return this;}}),
      control: { attribution: () => ({addTo(){}}) },
      DomUtil: { addClass(){}, removeClass(){} },
    },
    registerPage: () => {}, onWS: () => {}, offWS: () => {}, connectWS: () => {},
    api: () => Promise.resolve([]), invalidateApiCache: () => {},
    favStar: () => '', bindFavStars: () => {},
    getFavorites: () => [], isFavorite: () => false,
    HopResolver: { init(){}, resolve: () => ({}), ready: () => false },
    MeshAudio: null,
    RegionFilter: { init(){}, getSelected: () => null, onChange: () => {}, offChange: () => {}, regionQueryString: () => '', getRegionParam: () => '' },
  };
  vm.createContext(ctx);
  return ctx;
}

function load(ctx, file) {
  vm.runInContext(fs.readFileSync(file, 'utf8'), ctx);
  for (const k of Object.keys(ctx.window)) ctx[k] = ctx.window[k];
}

console.log('\n=== live.js: /api/observers parse (#1136) ===');
const ctx = makeSandbox();
load(ctx, 'public/roles.js');
load(ctx, 'public/live.js');

const build = ctx.window._liveBuildObserverIataMap;
assert.ok(build, '_liveBuildObserverIataMap must be exposed (regression: missing parser helper)');

const realShape = {
  observers: [
    { id: 'OBS1', iata: 'SJC', name: 'A' },
    { id: 'OBS2', iata: 'OAK', name: 'B' },
    { id: 'OBS3', iata: 'SFO', name: 'C' },
    { id: 'OBS4', iata: null, name: 'no-iata' },
  ],
  server_time: '2026-05-07T00:00:00Z',
};

test('parses {observers:[...], server_time} response and populates map', () => {
  const m = build(realShape);
  assert.strictEqual(m.OBS1, 'SJC');
  assert.strictEqual(m.OBS2, 'OAK');
  assert.strictEqual(m.OBS3, 'SFO');
});

test('skips observers without iata', () => {
  const m = build(realShape);
  assert.ok(!('OBS4' in m), 'observers with null iata should not be in map');
});

test('returns empty map for null/undefined input', () => {
  assert.strictEqual(Object.keys(build(null)).length, 0);
  assert.strictEqual(Object.keys(build(undefined)).length, 0);
});

test('returns empty map when observers field is missing', () => {
  assert.strictEqual(Object.keys(build({ server_time: 'x' })).length, 0);
});

test('back-compat: also accepts a top-level array (defensive)', () => {
  // If the API shape ever changes back, don\'t silently break.
  const m = build([{ id: 'X1', iata: 'LAX' }]);
  assert.strictEqual(m.X1, 'LAX');
});

console.log('\n' + '='.repeat(40));
console.log('  observer iata map tests: ' + passed + ' passed, ' + failed + ' failed');
console.log('='.repeat(40) + '\n');
if (failed > 0) process.exit(1);

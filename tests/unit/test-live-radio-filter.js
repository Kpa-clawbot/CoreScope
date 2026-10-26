/* Unit tests for public/live-radio-filter.js (config liveRadioFilters).
 * groupOnlyOnFreq keeps a packet group only when every observation was
 * heard on the chosen frequency, e.g. a second mesh such as MediaMesh.
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

function makeSandbox() {
  const ctx = {
    window: {
      addEventListener: () => {}, dispatchEvent: () => {}, devicePixelRatio: 1,
      // live.js:14 captures these at load time from packet-helpers.js, which
      // this sandbox does not load. They must exist before live.js is evaluated.
      getParsedDecoded: (p) => { try { return p && p.decoded_json ? JSON.parse(p.decoded_json) : {}; } catch (e) { return {}; } },
      getParsedPath: (p) => { try { return p && p.path_json ? JSON.parse(p.path_json) : []; } catch (e) { return []; } },
    },
    document: {
      readyState: 'complete',
      createElement: () => ({ style:{}, classList:{add(){},remove(){},contains(){return false;}}, setAttribute(){}, addEventListener(){}, getContext: () => ({clearRect(){},fillRect(){},beginPath(){},arc(){},fill(){},scale(){},fillText(){}}) }),
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
    fetch: () => Promise.resolve({ json: () => Promise.resolve(globalThis.__clientCfg || {}) }),
    performance: { now: () => Date.now() },
    requestAnimationFrame: (cb) => 0,
    cancelAnimationFrame: () => {},
    localStorage: (() => {
      const s = {};
      return {
        getItem: k => s[k] !== undefined ? s[k] : null,
        setItem: (k, v) => { s[k] = String(v); },
        removeItem: k => { delete s[k]; },
      };
    })(),
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


console.log('\n=== live-radio-filter.js (liveRadioFilters) ===');
globalThis.__clientCfg = { liveRadioFilters: [{ label: 'MediaMesh', freqMHz: 911.15 }] };
const ctx = makeSandbox();
load(ctx, 'public/payload-labels.js');
load(ctx, 'public/roles.js');
load(ctx, 'public/live-radio-filter.js');
load(ctx, 'public/live.js'); // must still load with the module present

const mod = ctx.window.LiveRadioFilter;
assert.ok(mod, 'window.LiveRadioFilter must be exposed');
const fn = mod.groupOnlyOnFreq;
assert.ok(typeof fn === 'function', 'LiveRadioFilter.groupOnlyOnFreq must be exposed');

const MM = '911.15,250,7,5';
const MM_RANGE = '911.15,125,7,5';
const MAIN = '910.525,62.5,7,5';

test('no frequency selected → filter inactive', () => {
  assert.strictEqual(fn([{ radio: MAIN }], null), true);
  assert.strictEqual(fn([{ radio: MAIN }], undefined), true);
  assert.strictEqual(fn([{ radio: MAIN }], 0), true);
});

test('matches() passes everything until a radio is chosen', () => {
  assert.strictEqual(mod.matches([{ radio: MAIN }]), true);
  assert.strictEqual(mod.matches([{ radio: MM }, { radio: MAIN }]), true);
});

test('init() without the picker in the DOM is a no-op', () => {
  assert.doesNotThrow(() => mod.init(() => { throw new Error('onChange must not fire'); }));
});

test('single observation on the chosen frequency matches', () => {
  assert.strictEqual(fn([{ radio: MM }], 911.15), true);
});

test('any bandwidth/SF on the chosen frequency matches (mediamesh-range)', () => {
  assert.strictEqual(fn([{ radio: MM }, { radio: MM_RANGE }], 911.15), true);
});

test('main-channel observation drops the group (packet crossed to the main mesh)', () => {
  assert.strictEqual(fn([{ radio: MM }, { radio: MAIN }], 911.15), false);
});

test('observation without a known radio drops the group', () => {
  assert.strictEqual(fn([{ radio: MM }, { radio: null }], 911.15), false);
  assert.strictEqual(fn([{ radio: MM }, {}], 911.15), false);
});

test('empty group never matches an active filter', () => {
  assert.strictEqual(fn([], 911.15), false);
  assert.strictEqual(fn(null, 911.15), false);
});

test('frequency compare tolerates canonical rounding', () => {
  assert.strictEqual(fn([{ radio: '911.150,250,7,5' }], 911.15), true);
  assert.strictEqual(fn([{ radio: '911.151,250,7,5' }], 911.15), false);
});

ctx.window.MeshConfigReady.then(() => {
  test('roles.js exposes liveRadioFilters from /api/config/client', () => {
    const f = ctx.window.MC_LIVE_RADIO_FILTERS;
    assert.ok(Array.isArray(f), 'MC_LIVE_RADIO_FILTERS must be an array');
    assert.strictEqual(f.length, 1);
    assert.strictEqual(f[0].label, 'MediaMesh');
    assert.strictEqual(f[0].freqMHz, 911.15);
  });
}, () => {}).then(() => {
  console.log(`\n${passed} passed, ${failed} failed`);
  if (failed) process.exit(1);
});

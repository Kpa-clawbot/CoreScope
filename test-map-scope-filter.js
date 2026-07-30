/* Unit tests for map.js's own-scope map filter (Scope Adoption by Area
 * follow-up: visualize which nodes support a scope on the live map).
 *
 * Verifies nodePassesScopeFilter, the pure predicate behind the "Scope"
 * dropdown's node filtering in _renderMarkersInner.
 *
 * Tests run in a jsdom-free vm sandbox with a tiny Leaflet shim, same
 * pattern as test-map-clustering.js.
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

function makeLeafletShim() {
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
  L.MarkerClusterGroup = MarkerClusterGroup;
  L.markerClusterGroup = (opts) => new MarkerClusterGroup(opts);
  return L;
}

function makeSandbox() {
  const ctx = {
    window: {},
    document: { addEventListener(){}, getElementById(){ return null; }, querySelector(){ return null; }, querySelectorAll(){ return []; }, createElement(){ return { id:'', textContent:'', innerHTML:'', appendChild(){}, addEventListener(){}, setAttribute(){}, classList:{add(){},remove(){},toggle(){}} }; }, head: { appendChild(){} }, body: { appendChild(){} } },
    console, Date, Math, Array, Object, String, Number, JSON, RegExp, Error,
    parseInt, parseFloat, isFinite, isNaN, Map, Set, Promise,
    setTimeout: ()=>{}, clearTimeout: ()=>{}, setInterval: ()=>{}, clearInterval: ()=>{},
    registerPage: () => {}, esc: (s) => s, onWS: () => {}, offWS: () => {},
    localStorage: (() => { const s={}; return { getItem:k=>s[k]||null, setItem:(k,v)=>{s[k]=String(v);}, removeItem:k=>{delete s[k];} }; })(),
    fetch: () => Promise.resolve({ json: () => Promise.resolve({}) }),
    addEventListener(){}, dispatchEvent(){},
    L: makeLeafletShim(),
  };
  ctx.window.L = ctx.L;
  vm.createContext(ctx);
  vm.runInContext(fs.readFileSync('public/roles.js','utf8'), ctx);
  for (const k of Object.keys(ctx.window)) ctx[k] = ctx.window[k];
  vm.runInContext(fs.readFileSync('public/map.js','utf8'), ctx);
  for (const k of Object.keys(ctx.window)) ctx[k] = ctx.window[k];
  return ctx;
}

console.log('\n=== map.js: scope filter ===');
{
  const ctx = makeSandbox();
  const internals = ctx.window.__meshcoreMapInternals;

  test('exposes nodePassesScopeFilter test hook', () => {
    assert.ok(internals, 'window.__meshcoreMapInternals not exposed by map.js');
    assert.ok(typeof internals.nodePassesScopeFilter === 'function', 'nodePassesScopeFilter not exported');
  });

  test('"all" passes every node regardless of scope', () => {
    assert.strictEqual(internals.nodePassesScopeFilter({ default_scope: 'dk-mj' }, 'all'), true);
    assert.strictEqual(internals.nodePassesScopeFilter({ default_scope: null }, 'all'), true);
    assert.strictEqual(internals.nodePassesScopeFilter({}, 'all'), true);
  });

  test('exact scope match passes, mismatch fails', () => {
    assert.strictEqual(internals.nodePassesScopeFilter({ default_scope: 'dk-mj' }, 'dk-mj'), true);
    assert.strictEqual(internals.nodePassesScopeFilter({ default_scope: 'dk-oj' }, 'dk-mj'), false);
  });

  test('"__none__" matches only nodes with no default_scope', () => {
    assert.strictEqual(internals.nodePassesScopeFilter({ default_scope: null }, '__none__'), true);
    assert.strictEqual(internals.nodePassesScopeFilter({ default_scope: '' }, '__none__'), true);
    assert.strictEqual(internals.nodePassesScopeFilter({}, '__none__'), true);
    assert.strictEqual(internals.nodePassesScopeFilter({ default_scope: 'dk-mj' }, '__none__'), false);
  });

  test('missing default_scope field never matches a real scope value', () => {
    assert.strictEqual(internals.nodePassesScopeFilter({}, 'dk-mj'), false);
  });
}

console.log('\n=== map.js: relayed scope filter ===');
{
  const ctx = makeSandbox();
  const internals = ctx.window.__meshcoreMapInternals;

  test('exposes nodePassesRelayedScopeFilter test hook', () => {
    assert.ok(typeof internals.nodePassesRelayedScopeFilter === 'function', 'nodePassesRelayedScopeFilter not exported');
  });

  test('"all" passes every node regardless of transported_scopes_recent', () => {
    assert.strictEqual(internals.nodePassesRelayedScopeFilter({ transported_scopes_recent: ['dk-oj'] }, 'all'), true);
    assert.strictEqual(internals.nodePassesRelayedScopeFilter({}, 'all'), true);
  });

  test('array-membership match passes, non-member fails', () => {
    assert.strictEqual(internals.nodePassesRelayedScopeFilter({ transported_scopes_recent: ['dk-oj', 'dk-nj'] }, 'dk-oj'), true);
    assert.strictEqual(internals.nodePassesRelayedScopeFilter({ transported_scopes_recent: ['dk-nj'] }, 'dk-oj'), false);
  });

  test('"__none__" matches only nodes with no recently-relayed scope', () => {
    assert.strictEqual(internals.nodePassesRelayedScopeFilter({ transported_scopes_recent: [] }, '__none__'), true);
    assert.strictEqual(internals.nodePassesRelayedScopeFilter({}, '__none__'), true);
    assert.strictEqual(internals.nodePassesRelayedScopeFilter({ transported_scopes_recent: null }, '__none__'), true);
    assert.strictEqual(internals.nodePassesRelayedScopeFilter({ transported_scopes_recent: ['dk-oj'] }, '__none__'), false);
  });

  test('non-array transported_scopes_recent is treated as empty, not a crash', () => {
    assert.strictEqual(internals.nodePassesRelayedScopeFilter({ transported_scopes_recent: 'dk-oj' }, 'dk-oj'), false);
  });

  test('a node can match Default Scope and Relayed Scope independently (different values)', () => {
    const node = { default_scope: 'dk-nj', transported_scopes_recent: ['dk-oj'] };
    assert.strictEqual(internals.nodePassesScopeFilter(node, 'dk-nj'), true);
    assert.strictEqual(internals.nodePassesRelayedScopeFilter(node, 'dk-oj'), true);
    assert.strictEqual(internals.nodePassesScopeFilter(node, 'dk-oj'), false, 'own default_scope must not be satisfied by a relayed scope');
  });
}

if (failed > 0) {
  console.log(`\n${failed} test(s) failed, ${passed} passed`);
  process.exit(1);
}
console.log(`\nAll ${passed} test(s) passed`);

/* Unit tests for the map's scope-configuration state layer (issue #2001)
 *
 * Verifies the three pure pieces the layer is built from:
 *   - scopeFilterAccepts: which nodes survive a given filter selection
 *   - scopeTint: which CSS variable a node's marker is filled with
 *   - scopeFilterHtml: the button group, including which button reads active
 *
 * Runs in the same jsdom-free vm sandbox as test-map-clustering.js, with a
 * tiny Leaflet shim, so it exercises map.js and not the library.
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
  const L = {};
  L.point = (x, y) => ({ x, y });
  L.latLng = (a, b) => ({ lat: a, lng: b });
  L.divIcon = (opts) => ({ _isDivIcon: true, options: opts, html: opts.html, className: opts.className });
  L.layerGroup = () => ({ addLayer(){ return this; }, removeLayer(){ return this; }, clearLayers(){ return this; }, eachLayer(){}, addTo(){ return this; }, hasLayer(){ return false; } });
  L.marker = (latlng, opts) => ({ _isMarker: true, _latlng: latlng, options: opts || {}, getLatLng(){ return this._latlng; }, bindPopup(){ return this; }, bindTooltip(){ return this; } });
  function MarkerClusterGroup(opts) { this.options = opts || {}; this._layers = []; }
  MarkerClusterGroup.prototype.addLayer = function () { return this; };
  MarkerClusterGroup.prototype.addLayers = function () { return this; };
  MarkerClusterGroup.prototype.clearLayers = function () { return this; };
  MarkerClusterGroup.prototype.eachLayer = function () {};
  MarkerClusterGroup.prototype.addTo = function () { return this; };
  L.MarkerClusterGroup = MarkerClusterGroup;
  L.markerClusterGroup = (opts) => new MarkerClusterGroup(opts);

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
    L: L,
  };
  ctx.window.L = ctx.L;
  vm.createContext(ctx);
  vm.runInContext(fs.readFileSync('public/roles.js', 'utf8'), ctx);
  for (const k of Object.keys(ctx.window)) ctx[k] = ctx.window[k];
  vm.runInContext(fs.readFileSync('public/map.js', 'utf8'), ctx);
  for (const k of Object.keys(ctx.window)) ctx[k] = ctx.window[k];
  return ctx;
}

console.log('\n=== map.js: scope-configuration state (#2001) ===');
{
  const internals = makeSandbox().window.__meshcoreMapInternals;

  test('exposes the scope-state hooks', () => {
    assert.ok(internals, '__meshcoreMapInternals not exposed');
    assert.strictEqual(typeof internals.scopeFilterAccepts, 'function', 'scopeFilterAccepts not exported');
    assert.strictEqual(typeof internals.scopeTint, 'function', 'scopeTint not exported');
    assert.strictEqual(typeof internals.scopeFilterHtml, 'function', 'scopeFilterHtml not exported');
  });

  test('"all" accepts every node, classified or not', () => {
    assert.strictEqual(internals.scopeFilterAccepts({ scope_config_state: 'full' }, 'all'), true);
    assert.strictEqual(internals.scopeFilterAccepts({ role: 'companion' }, 'all'), true);
  });

  test('a selected state accepts only nodes in that state', () => {
    const node = { scope_config_state: 'no-scopes' };
    assert.strictEqual(internals.scopeFilterAccepts(node, 'no-scopes'), true);
    assert.strictEqual(internals.scopeFilterAccepts(node, 'full'), false);
    assert.strictEqual(internals.scopeFilterAccepts(node, 'none'), false);
  });

  // A node with no scope_config_state is unclassified, not "none". The server
  // omits the field for roles that do not forward, and for every node when the
  // declared-regions lookup failed. Treating that as "no scope data" would put
  // companions and phones in the category operators use to hunt for repeaters
  // with no region config.
  test('an unclassified node is not swept into "none"', () => {
    assert.strictEqual(internals.scopeFilterAccepts({ role: 'companion' }, 'none'), false);
    assert.strictEqual(internals.scopeFilterAccepts({ role: 'repeater' }, 'full'), false);
  });

  test('each state tints through its own CSS variable', () => {
    const want = {
      'full': 'var(--mc-scope-full)',
      'observed': 'var(--mc-scope-observed)',
      'no-unscoped': 'var(--mc-scope-no-unscoped)',
      'no-scopes': 'var(--mc-scope-no-scopes)',
      'no-flood': 'var(--mc-scope-no-flood)',
      'none': 'var(--mc-scope-none)',
    };
    for (const state of Object.keys(want)) {
      assert.strictEqual(internals.scopeTint({ scope_config_state: state }), want[state], `tint for ${state}`);
    }
  });

  test('an unclassified node has no tint, so its role colour stands', () => {
    assert.strictEqual(internals.scopeTint({ role: 'companion' }), null);
    assert.strictEqual(internals.scopeTint({}), null);
  });

  test('the filter group offers All plus every state', () => {
    const html = internals.scopeFilterHtml('all');
    for (const value of ['all', 'full', 'observed', 'no-unscoped', 'no-scopes', 'no-flood', 'none']) {
      assert.ok(html.indexOf('data-scope="' + value + '"') >= 0, `missing button for ${value}: ${html}`);
    }
  });

  // The map has no separate colour legend, so the filter group is the legend:
  // every state button carries the swatch its markers are painted with. A
  // colour on the map with nothing to read it against is a puzzle, not a
  // signal.
  test('every state button carries its own colour swatch', () => {
    const html = internals.scopeFilterHtml('all');
    for (const state of ['full', 'observed', 'no-unscoped', 'no-scopes', 'no-flood', 'none']) {
      assert.ok(html.indexOf('var(--mc-scope-' + state + ')') >= 0, `no swatch colour for ${state}: ${html}`);
    }
  });

  test('the current selection is the active button, and the only one', () => {
    const html = internals.scopeFilterHtml('none');
    const active = html.match(/class="btn active" data-scope="([^"]+)"/g) || [];
    assert.strictEqual(active.length, 1, `expected exactly one active button, got ${active.length}: ${html}`);
    assert.ok(active[0].indexOf('data-scope="none"') >= 0, `active button is not "none": ${active[0]}`);
  });
}

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed === 0 ? 0 : 1);

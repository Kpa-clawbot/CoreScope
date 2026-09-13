/* Unit tests for the map's region-scope filter (issue #1862)
 *
 * "Show me the repeaters that forward #be": the filter reads two per-node
 * fields /api/nodes already carries, declared_regions (the repeater's own
 * answer, same list as the Scope Audit) and transported_scopes (region scopes
 * seen in traffic through it), and never makes a request of its own.
 *
 * Same vm sandbox as test-issue-2001-map-scope-state.js, so it exercises
 * map.js itself.
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
  for (const k of Object.keys(ctx.window)) ctx[k] = ctx.window[k];
  vm.runInContext(fs.readFileSync('public/map.js', 'utf8'), ctx);
  for (const k of Object.keys(ctx.window)) ctx[k] = ctx.window[k];
  return ctx;
}

// Spellings as they arrive on live: transported_scopes keeps the '#'
// (transmissions.scope_name), declared_regions has it stripped by the server.
const DECLARED_ONLY = { public_key: 'aa', role: 'repeater', declared_regions: ['be', 'be-bru'] };
const OBSERVED_ONLY = { public_key: 'bb', role: 'repeater', transported_scopes: ['#be', '#de'] };
const BOTH = { public_key: 'cc', role: 'repeater', declared_regions: ['be'], transported_scopes: ['#be'] };
const ANSWERED_NONE = { public_key: 'dd', role: 'repeater', declared_regions: [] };
const NEVER_ASKED = { public_key: 'ee', role: 'repeater' };
const COMPANION = { public_key: 'ff', role: 'companion' };

console.log('\n=== map.js: region-scope filter (#1862) ===');
{
  const m = makeSandbox().window.__meshcoreMapInternals;

  test('exposes the region filter hooks', () => {
    assert.ok(m, '__meshcoreMapInternals not exposed');
    for (const fn of ['regionFilterAccepts', 'nodeRegionEvidence', 'collectRegionCounts', 'regionFilterOptionsHtml', 'regionFilterHintHtml', 'regionsPopupRowsHtml', 'nodeFiltersNarrowed']) {
      assert.strictEqual(typeof m[fn], 'function', fn + ' not exported');
    }
  });

  test('no region picked accepts every node', () => {
    for (const n of [DECLARED_ONLY, OBSERVED_ONLY, NEVER_ASKED, COMPANION]) {
      assert.strictEqual(m.regionFilterAccepts(n, ''), true);
    }
  });

  test('a node that declares the region is accepted, and says why', () => {
    assert.strictEqual(m.regionFilterAccepts(DECLARED_ONLY, 'be'), true);
    const ev = m.nodeRegionEvidence(DECLARED_ONLY, 'be');
    assert.strictEqual(ev.declared, true);
    assert.strictEqual(ev.observed, false);
  });

  // transported_scopes carries the '#', declared_regions does not. Comparing
  // raw would split one region into two, the trap normScope exists for.
  test('a node seen carrying the region is accepted despite the # spelling', () => {
    assert.strictEqual(m.regionFilterAccepts(OBSERVED_ONLY, 'be'), true);
    const ev = m.nodeRegionEvidence(OBSERVED_ONLY, 'be');
    assert.strictEqual(ev.declared, false);
    assert.strictEqual(ev.observed, true);
    assert.strictEqual(m.regionFilterAccepts(OBSERVED_ONLY, '#be'), true, 'a #-prefixed selection must match too');
  });

  test('declared and observed are reported together when both hold', () => {
    const ev = m.nodeRegionEvidence(BOTH, 'be');
    assert.strictEqual(ev.declared, true);
    assert.strictEqual(ev.observed, true);
  });

  // A picker, not a search box: #be must not pull in #be-bru repeaters.
  test('a region matches exactly, not as a prefix', () => {
    assert.strictEqual(m.regionFilterAccepts({ declared_regions: ['be-bru'] }, 'be'), false);
    assert.strictEqual(m.regionFilterAccepts({ transported_scopes: ['#be-bru'] }, 'be'), false);
  });

  test('nodes with no evidence for the region drop out while it is picked', () => {
    assert.strictEqual(m.regionFilterAccepts(OBSERVED_ONLY, 'nl'), false);
    assert.strictEqual(m.regionFilterAccepts(ANSWERED_NONE, 'be'), false);
    assert.strictEqual(m.regionFilterAccepts(NEVER_ASKED, 'be'), false);
    assert.strictEqual(m.regionFilterAccepts(COMPANION, 'be'), false);
  });

  test('region counts: one entry per region, sorted, a node counted once', () => {
    const counts = m.collectRegionCounts([DECLARED_ONLY, OBSERVED_ONLY, BOTH, ANSWERED_NONE, NEVER_ASKED, COMPANION]);
    const plain = JSON.parse(JSON.stringify(counts));
    assert.deepStrictEqual(plain, [
      { region: 'be', total: 3, declared: 2, observed: 2 },
      { region: 'be-bru', total: 1, declared: 1, observed: 0 },
      { region: 'de', total: 1, declared: 0, observed: 1 },
    ]);
  });

  test('the picker offers All plus every region, with the selection marked', () => {
    const counts = m.collectRegionCounts([DECLARED_ONLY, OBSERVED_ONLY, BOTH]);
    const html = m.regionFilterOptionsHtml(counts, 'de');
    assert.ok(/<option value="">All regions<\/option>/.test(html), 'no All option: ' + html);
    assert.ok(html.indexOf('<option value="be">#be (3)</option>') >= 0, 'no #be option: ' + html);
    const selected = html.match(/<option [^>]*selected[^>]*>/g) || [];
    assert.strictEqual(selected.length, 1, 'expected exactly one selected option: ' + html);
    assert.ok(selected[0].indexOf('value="de"') >= 0, 'wrong option selected: ' + selected[0]);
  });

  // A remembered region that the current data does not carry (different
  // lastHeard window, a restart emptied transported_scopes) must still show as
  // the selection, or the map is filtered by a control that reads "All".
  test('a remembered region missing from the data stays visible as the selection', () => {
    const html = m.regionFilterOptionsHtml([], 'nl-li');
    assert.ok(/<option value="nl-li" selected>#nl-li \(0\)<\/option>/.test(html), 'stored region not offered: ' + html);
  });

  test('region names are escaped in the picker and the popup', () => {
    const evil = { declared_regions: ['"><img src=x>'], transported_scopes: ['#<b>'] };
    const opts = m.regionFilterOptionsHtml(m.collectRegionCounts([evil]), '');
    assert.ok(opts.indexOf('<img') < 0 && opts.indexOf('<b>') < 0, 'unescaped region in picker: ' + opts);
    const rows = m.regionsPopupRowsHtml(evil);
    assert.ok(rows.indexOf('<img') < 0 && rows.indexOf('<b>') < 0, 'unescaped region in popup: ' + rows);
  });

  test('the hint is empty with no region picked', () => {
    assert.strictEqual(m.regionFilterHintHtml([], ''), '');
  });

  // Firmware drops scoped floods for regions it holds no key for, and most
  // repeaters have never been asked for their list. So the hint states what
  // was found and must not turn absence into a claim about the repeaters
  // that are not shown.
  test('the hint gives the evidence split and never reads absence as lacking the region', () => {
    const counts = m.collectRegionCounts([DECLARED_ONLY, OBSERVED_ONLY, BOTH]);
    const hint = m.regionFilterHintHtml(counts, 'be');
    assert.ok(/3/.test(hint) && /2 declare/.test(hint) && /2 seen carrying/.test(hint), 'counts missing from hint: ' + hint);
    assert.ok(/not proof/i.test(hint), 'absence caveat missing: ' + hint);
    assert.ok(!/(does not|doesn't|do not|don't) (support|have|forward|handle)|unsupported|lacks? /i.test(hint), 'hint reads absence as a finding: ' + hint);
  });

  test('the popup lists declared and observed regions separately', () => {
    const rows = m.regionsPopupRowsHtml({ declared_regions: ['be'], transported_scopes: ['#be', '#de'] });
    assert.ok(/Declared<\/dt>[\s\S]*#be/.test(rows), 'declared row missing: ' + rows);
    assert.ok(/Observed<\/dt>[\s\S]*#be[\s\S]*#de/.test(rows), 'observed row missing: ' + rows);
  });

  test('the popup says an answered-but-empty list names no region, and adds nothing for a never-asked node', () => {
    assert.ok(/no named region/.test(m.regionsPopupRowsHtml(ANSWERED_NONE)), 'empty answer not stated');
    assert.strictEqual(m.regionsPopupRowsHtml(NEVER_ASKED), '');
    assert.strictEqual(m.regionsPopupRowsHtml(COMPANION), '');
  });

  // The observer layer stands down while either node filter narrows the map,
  // for the reason #2006 gave: an observer pin would bring back a repeater
  // the filter just removed.
  test('observer pins stand down for the scope filter, the region filter, or both', () => {
    assert.strictEqual(m.nodeFiltersNarrowed({ scopeState: 'all', regionScope: '' }), false);
    assert.strictEqual(m.nodeFiltersNarrowed({ scopeState: 'full', regionScope: '' }), true);
    assert.strictEqual(m.nodeFiltersNarrowed({ scopeState: 'all', regionScope: 'be' }), true);
    assert.strictEqual(m.nodeFiltersNarrowed({ scopeState: 'observed', regionScope: 'be' }), true);
  });

  // Both filters apply together: "observed repeaters that carry #be".
  test('the region filter combines with the scope-state filter', () => {
    const pass = (n) => m.scopeFilterAccepts(n, 'observed') && m.regionFilterAccepts(n, 'be');
    assert.strictEqual(pass({ scope_config_state: 'observed', transported_scopes: ['#be'] }), true);
    assert.strictEqual(pass({ scope_config_state: 'observed', transported_scopes: ['#de'] }), false);
    assert.strictEqual(pass({ scope_config_state: 'full', declared_regions: ['be'] }), false);
  });
}

// Persistence: same localStorage idiom as the #2006 scope filter, read when
// map.js loads.
{
  test('the picked region is restored from localStorage next to the scope state', () => {
    const m = makeSandbox({ 'meshcore-map-region-filter': 'be', 'meshcore-map-scope-filter': 'observed' }).window.__meshcoreMapInternals;
    assert.strictEqual(m.filters.regionScope, 'be');
    assert.strictEqual(m.filters.scopeState, 'observed');
  });

  test('with nothing stored the region filter starts on All', () => {
    const m = makeSandbox().window.__meshcoreMapInternals;
    assert.strictEqual(m.filters.regionScope, '');
  });
}

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed === 0 ? 0 : 1);

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
const DECLARED_ONLY = { public_key: 'aa', role: 'repeater', lat: 51.0, lon: 4.0, declared_regions: ['be', 'be-bru'] };
const OBSERVED_ONLY = { public_key: 'bb', role: 'repeater', lat: 51.1, lon: 4.1, transported_scopes: ['#be', '#de'] };
const BOTH = { public_key: 'cc', role: 'repeater', lat: 51.2, lon: 4.2, declared_regions: ['be'], transported_scopes: ['#be'] };
const DECLARED_BE = { public_key: 'ab', role: 'room', lat: 51.3, lon: 4.3, declared_regions: ['be'] };
// Declares #be but has no position: it can never become a marker.
const UNPLACED = { public_key: 'ac', role: 'repeater', declared_regions: ['be'], transported_scopes: ['#be'] };
const ANSWERED_NONE = { public_key: 'dd', role: 'repeater', declared_regions: [] };
const NEVER_ASKED = { public_key: 'ee', role: 'repeater' };
const COMPANION = { public_key: 'ff', role: 'companion' };

console.log('\n=== map.js: region-scope filter (#1862) ===');
{
  const m = makeSandbox().window.__meshcoreMapInternals;

  test('exposes the region filter hooks', () => {
    assert.ok(m, '__meshcoreMapInternals not exposed');
    for (const fn of ['regionFilterAccepts', 'nodeRegionEvidence', 'collectRegionCounts', 'regionFilterOptionsHtml', 'regionFilterHintHtml', 'regionsPopupRowsHtml', 'nodeFiltersNarrowed', 'nodePassesMapFilters', 'observerLayerShown']) {
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
    // Declared and observed counts differ (3 and 2), so a hint that swapped
    // them would fail here.
    const counts = m.collectRegionCounts([DECLARED_ONLY, OBSERVED_ONLY, BOTH, DECLARED_BE]);
    const hint = m.regionFilterHintHtml(counts, 'be');
    assert.ok(/^4 nodes/.test(hint), 'total missing from hint: ' + hint);
    assert.ok(/\b3 declare it\b/.test(hint), 'declared count wrong in hint: ' + hint);
    assert.ok(/\b2 seen carrying\b/.test(hint), 'observed count wrong in hint: ' + hint);
    assert.ok(/not proof/i.test(hint), 'absence caveat missing: ' + hint);
    assert.ok(!/(does not|doesn't|do not|don't) (support|have|forward|handle)|unsupported|lacks? /i.test(hint), 'hint reads absence as a finding: ' + hint);
  });

  // The map only draws nodes with a position, so a count that included the
  // others would promise markers the filter can never show (#2022 review).
  test('picker and hint count only nodes that have a map position', () => {
    const counts = JSON.parse(JSON.stringify(m.collectRegionCounts([DECLARED_ONLY, UNPLACED])));
    assert.deepStrictEqual(counts, [
      { region: 'be', total: 1, declared: 1, observed: 0 },
      { region: 'be-bru', total: 1, declared: 1, observed: 0 },
    ]);
    const hint = m.regionFilterHintHtml(m.collectRegionCounts([UNPLACED]), 'be');
    assert.ok(/No loaded node with a map position has evidence for #be/.test(hint), 'unplaced node counted in hint: ' + hint);
    const withPos = m.regionFilterHintHtml(m.collectRegionCounts([DECLARED_ONLY]), 'be');
    assert.ok(/^1 node with a map position has evidence for #be/.test(withPos), 'hint does not say what it counts: ' + withPos);
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

  // A truncated answer is a partial list: the Scope Audit flags it, and the
  // popup must not present it (or its emptiness) as the whole answer.
  test('a truncated declared answer carries the caveat, a complete one does not', () => {
    const partial = m.regionsPopupRowsHtml({ declared_regions: ['be'], declared_regions_truncated: true });
    assert.ok(/#be/.test(partial) && /class="ns-truncated"[^>]*>truncated</.test(partial), 'truncated list shown as complete: ' + partial);
    const emptyPartial = m.regionsPopupRowsHtml({ declared_regions: [], declared_regions_truncated: true });
    assert.ok(/no named region/.test(emptyPartial) && /ns-truncated/.test(emptyPartial), 'truncated empty list shown as complete: ' + emptyPartial);
    assert.ok(!/truncated/.test(m.regionsPopupRowsHtml({ declared_regions: ['be'] })), 'caveat on a complete list');
    assert.ok(!/truncated/.test(m.regionsPopupRowsHtml({ declared_regions: [] })), 'caveat on a complete empty list');
  });

  // The observer layer stands down while either node filter narrows the map,
  // for the reason #2006 gave: an observer pin would bring back a repeater
  // the filter just removed.
  test('observer pins stand down for the scope filter, the region filter, or both', () => {
    assert.strictEqual(m.nodeFiltersNarrowed({ scopeState: 'all', regionScope: '' }), false);
    assert.strictEqual(m.nodeFiltersNarrowed({ scopeState: 'full', regionScope: '' }), true);
    assert.strictEqual(m.nodeFiltersNarrowed({ scopeState: 'all', regionScope: 'be' }), true);
    assert.strictEqual(m.nodeFiltersNarrowed({ scopeState: 'observed', regionScope: 'be' }), true);
    assert.strictEqual(m.observerLayerShown({ observer: true, scopeState: 'all', regionScope: '' }), true);
    assert.strictEqual(m.observerLayerShown({ observer: false, scopeState: 'all', regionScope: '' }), false);
    assert.strictEqual(m.observerLayerShown({ observer: true, scopeState: 'all', regionScope: 'be' }), false);
    assert.strictEqual(m.observerLayerShown({ observer: true, scopeState: 'full', regionScope: '' }), false);
  });

  // nodePassesMapFilters is the marker filter _renderMarkersInner runs.
  test('the marker filter applies the region filter next to the other filters', () => {
    const f = (over) => Object.assign({ repeater: true, companion: true, room: true, sensor: true, byteSize: 'all', scopeState: 'all', regionScope: '', statusFilter: 'all', neighbors: false }, over);
    const ctx = { selectedReferenceNode: null, neighborPubkeys: null };
    assert.strictEqual(m.nodePassesMapFilters(DECLARED_ONLY, f(), ctx), true);
    assert.strictEqual(m.nodePassesMapFilters(DECLARED_ONLY, f({ regionScope: 'be' }), ctx), true);
    assert.strictEqual(m.nodePassesMapFilters(DECLARED_ONLY, f({ regionScope: 'de' }), ctx), false, 'region filter not applied');
    assert.strictEqual(m.nodePassesMapFilters(UNPLACED, f(), ctx), false, 'a node without a position passed');
    assert.strictEqual(m.nodePassesMapFilters(DECLARED_ONLY, f({ repeater: false }), ctx), false, 'role filter not applied');
    assert.strictEqual(m.nodePassesMapFilters({ role: 'repeater', lat: 1, lon: 1 }, f({ scopeState: 'full' }), ctx), false, 'scope filter not applied');
    assert.strictEqual(m.nodePassesMapFilters(DECLARED_ONLY, f({ neighbors: true }), { selectedReferenceNode: 'zz', neighborPubkeys: new Set(['yy']) }), false, 'neighbor filter not applied');
    assert.strictEqual(m.nodePassesMapFilters(DECLARED_ONLY, f({ neighbors: true }), { selectedReferenceNode: 'zz', neighborPubkeys: new Set(['aa']) }), true);
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

// ── Render level: the page itself, driven through init() and loadNodes() ──
//
// The pure pieces above can all be right while the page never calls them
// (#2022 review). These tests run the registered map page in the same vm
// sandbox with a small fake DOM, let loadNodes() finish, and read back what
// the page drew: the markers it created, their popups, the picker it built,
// and what a change of the picker stored.

function makeFakeEl(id) {
  const classes = new Set();
  const el = {
    id, innerHTML: '', textContent: '', value: '', checked: false, hidden: false,
    style: {}, dataset: {}, attrs: {}, listeners: {},
    classList: {
      add(c) { classes.add(c); }, remove(c) { classes.delete(c); }, contains(c) { return classes.has(c); },
      toggle(c, on) { if (on === undefined) on = !classes.has(c); if (on) classes.add(c); else classes.delete(c); return on; },
    },
    setAttribute(k, v) { this.attrs[k] = String(v); }, getAttribute(k) { return k in this.attrs ? this.attrs[k] : null; },
    removeAttribute(k) { delete this.attrs[k]; },
    addEventListener(type, fn) { (this.listeners[type] = this.listeners[type] || []).push(fn); },
    removeEventListener() {},
    fire(type, extra) {
      for (const fn of this.listeners[type] || []) fn(Object.assign({ type, target: this, preventDefault() {} }, extra || {}));
    },
    querySelector() { return null; }, querySelectorAll() { return []; },
    appendChild() {}, remove() {}, closest() { return null; },
  };
  return el;
}

async function makePage(opts) {
  const stored = Object.assign({}, opts.stored || {});
  const els = new Map();
  const getEl = (id) => { if (!els.has(id)) els.set(id, makeFakeEl(id)); return els.get(id); };
  const markers = [];
  const errors = [];
  let page = null;

  const L = {};
  const group = () => ({ addLayer() { return this; }, addLayers() { return this; }, removeLayer() { return this; }, clearLayers() { return this; }, eachLayer() {}, addTo() { return this; }, hasLayer() { return false; } });
  L.point = (x, y) => ({ x, y });
  L.latLng = (a, b) => ({ lat: a, lng: b });
  L.divIcon = (o) => ({ _isDivIcon: true, options: o, html: o.html, className: o.className });
  L.layerGroup = group;
  L.markerClusterGroup = group;
  L.tileLayer = () => ({ addTo() { return this; }, setUrl() {}, options: {} });
  L.map = () => ({
    setView() { return this; }, on() {}, invalidateSize() {}, getCenter() { return { lat: 0, lng: 0 }; }, getZoom() { return 9; },
    hasLayer() { return false; }, addLayer() {}, removeLayer() {}, fitBounds() {}, remove() {},
  });
  L.marker = (latlng, o) => {
    const mk = { _latlng: latlng, options: o || {}, popup: null, bindPopup(html) { this.popup = html; return this; }, bindTooltip() { return this; } };
    markers.push(mk);
    return mk;
  };

  const ctx = {
    window: { addEventListener() {}, removeEventListener() {}, matchMedia: () => ({ matches: false }) },
    document: {
      addEventListener() {}, getElementById: getEl, querySelector() { return null; }, querySelectorAll() { return []; },
      createElement() { const e = makeFakeEl(''); e.querySelector = () => makeFakeEl(''); return e; }, head: { appendChild() {} }, body: { appendChild() {} },
      documentElement: { getAttribute() { return null; }, style: {} },
    },
    console: { log() {}, warn() {}, info() {}, error: (...a) => errors.push(a.map(x => (x && x.stack) || String(x)).join(' ')) },
    Date, Math, Array, Object, String, Number, JSON, RegExp, Error, Map, Set, Promise, URLSearchParams,
    parseInt, parseFloat, isFinite, isNaN,
    setTimeout: () => 0, clearTimeout: () => {}, setInterval: () => 0, clearInterval: () => {},
    location: { hash: '#/map' },
    MutationObserver: function () { this.observe = () => {}; this.disconnect = () => {}; },
    getComputedStyle: () => ({ getPropertyValue: () => '' }),
    registerPage: (name, handlers) => { if (name === 'map') page = handlers; },
    escapeHtml: (s) => (s == null ? '' : String(s)).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;').replace(/'/g, '&#39;'),
    timeAgo: () => 'now', truncate: (s) => s,
    onWS: () => {}, offWS: () => {}, debouncedOnWS: (fn) => fn,
    localStorage: { getItem: k => (k in stored ? stored[k] : null), setItem: (k, v) => { stored[k] = String(v); }, removeItem: k => { delete stored[k]; } },
    sessionStorage: { getItem: () => null, setItem() {}, removeItem() {} },
    fetch: () => Promise.resolve({ ok: true, json: () => Promise.resolve({}) }),
    api: (path) => Promise.resolve(path === '/observers' ? { observers: opts.observers || [] } : {}),
    fetchAllNodes: () => Promise.resolve({ nodes: opts.nodes, counts: {} }),
    CLIENT_TTL: { nodeList: 0, observers: 0 },
    AreaFilter: { init() {}, onChange() {}, areaQueryString: () => '' },
    addEventListener() {}, dispatchEvent() {},
    L,
  };
  ctx.window.L = L;
  vm.createContext(ctx);
  vm.runInContext(fs.readFileSync('public/roles.js', 'utf8'), ctx);
  for (const k of Object.keys(ctx.window)) ctx[k] = ctx.window[k];
  vm.runInContext(fs.readFileSync('public/map.js', 'utf8'), ctx);
  for (const k of Object.keys(ctx.window)) ctx[k] = ctx.window[k];

  await page.init({ innerHTML: '' });
  // loadNodes() is started, not awaited, by init: let its promise chain settle.
  for (let i = 0; i < 20; i++) await new Promise(r => setImmediate(r));
  if (getEl('leaflet-map').getAttribute('data-loaded') !== 'true') throw new Error('loadNodes did not finish');
  if (errors.length) throw new Error('page logged errors: ' + errors.join(' | '));

  return {
    stored, el: getEl, internals: ctx.window.__meshcoreMapInternals,
    // Markers created since load, or since the last markRender().
    markers: () => markers.slice(),
    markRender: () => { markers.length = 0; },
  };
}

const nodeMarkers = (ms) => ms.filter(mk => !/\(observer\)$/.test(mk.options.alt || ''));
const observerMarkers = (ms) => ms.filter(mk => /\(observer\)$/.test(mk.options.alt || ''));
const markerNames = (ms) => nodeMarkers(ms).map(mk => mk.options.alt.split(' (')[0]).sort();

const RENDER_NODES = [
  { public_key: 'r1', name: 'be-declared', role: 'repeater', lat: 51.0, lon: 4.0, declared_regions: ['be'] },
  { public_key: 'r2', name: 'de-observed', role: 'repeater', lat: 51.1, lon: 4.1, transported_scopes: ['#de'] },
  { public_key: 'r3', name: 'be-observed', role: 'repeater', lat: 51.2, lon: 4.2, transported_scopes: ['#be'] },
];
const RENDER_OBSERVERS = [{ id: 'obs1', name: 'lone-observer', lat: 50.9, lon: 3.9 }];

async function atest(name, fn) {
  try { await fn(); passed++; console.log(`  ✅ ${name}`); }
  catch (e) { failed++; console.log(`  ❌ ${name}: ${e.message}`); }
}

(async () => {
  console.log('\n=== map.js page render: region-scope filter wiring (#1862) ===');

  await atest('loadNodes builds the picker and hint from the loaded nodes', async () => {
    const p = await makePage({ nodes: RENDER_NODES, observers: RENDER_OBSERVERS, stored: { 'meshcore-map-region-filter': 'be' } });
    const opts = p.el('mcRegionFilter').innerHTML;
    assert.ok(/<option value="be" selected>#be \(2\)<\/option>/.test(opts), 'picker not built from loaded nodes: ' + opts);
    assert.ok(opts.indexOf('<option value="de">#de (1)</option>') >= 0, 'picker misses #de: ' + opts);
    assert.ok(/^2 nodes/.test(p.el('mcRegionHint').innerHTML), 'hint not built: ' + p.el('mcRegionHint').innerHTML);
  });

  await atest('a stored region filters the rendered markers', async () => {
    const p = await makePage({ nodes: RENDER_NODES, observers: RENDER_OBSERVERS, stored: { 'meshcore-map-region-filter': 'be' } });
    assert.deepStrictEqual(markerNames(p.markers()), ['be-declared', 'be-observed']);
  });

  await atest('observer pins stand down on the page while a region is picked, and return without one', async () => {
    const withRegion = await makePage({ nodes: RENDER_NODES, observers: RENDER_OBSERVERS, stored: { 'meshcore-map-region-filter': 'be' } });
    assert.strictEqual(observerMarkers(withRegion.markers()).length, 0, 'observer pin drawn while a region filter is active');
    const without = await makePage({ nodes: RENDER_NODES, observers: RENDER_OBSERVERS });
    assert.strictEqual(observerMarkers(without.markers()).length, 1, 'observer pin missing with no filter');
    assert.deepStrictEqual(markerNames(without.markers()), ['be-declared', 'be-observed', 'de-observed']);
  });

  await atest('a rendered popup carries the declared and observed rows', async () => {
    const p = await makePage({ nodes: RENDER_NODES, observers: [] });
    const byName = (n) => nodeMarkers(p.markers()).find(mk => mk.options.alt.startsWith(n + ' ('));
    assert.ok(/Declared<\/dt>[\s\S]*#be/.test(byName('be-declared').popup), 'declared row missing from popup');
    assert.ok(/Observed<\/dt>[\s\S]*#de/.test(byName('de-observed').popup), 'observed row missing from popup');
  });

  await atest('changing the picker stores the choice and re-renders filtered', async () => {
    const p = await makePage({ nodes: RENDER_NODES, observers: RENDER_OBSERVERS });
    p.markRender();
    const sel = p.el('mcRegionFilter');
    sel.value = 'de';
    sel.fire('change');
    assert.strictEqual(p.stored['meshcore-map-region-filter'], 'de', 'choice not persisted');
    assert.strictEqual(p.internals.filters.regionScope, 'de');
    assert.deepStrictEqual(markerNames(p.markers()), ['de-observed']);
    assert.ok(/#de/.test(p.el('mcRegionHint').innerHTML), 'hint not updated on change');
  });

  // On a phone the controls panel starts collapsed and the Region Scope
  // section with it, so a stored region would leave a thinned-out map with no
  // visible cause. The chip on the map names the filter and clears it.
  await atest('an active region filter shows a chip on the map, hidden without one', async () => {
    const p = await makePage({ nodes: RENDER_NODES, observers: [], stored: { 'meshcore-map-region-filter': 'be' } });
    assert.strictEqual(p.el('mcRegionChip').hidden, false, 'chip hidden while a region filter is active');
    assert.strictEqual(p.el('mcRegionChipText').textContent, 'Region: #be');
    const none = await makePage({ nodes: RENDER_NODES, observers: [] });
    assert.strictEqual(none.el('mcRegionChip').hidden, true, 'chip shown with no region filter');
  });

  await atest('the chip reset clears the filter, the stored choice, the picker and the chip', async () => {
    const p = await makePage({ nodes: RENDER_NODES, observers: RENDER_OBSERVERS, stored: { 'meshcore-map-region-filter': 'be' } });
    p.el('mcRegionFilter').value = 'be';
    p.markRender();
    p.el('mcRegionChipReset').fire('click');
    assert.strictEqual(p.internals.filters.regionScope, '');
    assert.ok(!p.stored['meshcore-map-region-filter'], 'stored region survived the reset: ' + p.stored['meshcore-map-region-filter']);
    assert.strictEqual(p.el('mcRegionFilter').value, '', 'picker still shows the old region');
    assert.strictEqual(p.el('mcRegionChip').hidden, true, 'chip still shown after reset');
    assert.strictEqual(p.el('mcRegionHint').innerHTML, '', 'hint still shown after reset');
    assert.deepStrictEqual(markerNames(p.markers()), ['be-declared', 'be-observed', 'de-observed']);
    assert.strictEqual(observerMarkers(p.markers()).length, 1, 'observer layer did not return after reset');
  });

  await atest('the chip names the region as text, never as markup', async () => {
    const p = await makePage({ nodes: RENDER_NODES, observers: [], stored: { 'meshcore-map-region-filter': '<img src=x>' } });
    assert.strictEqual(p.el('mcRegionChipText').textContent, 'Region: #<img src=x>');
    assert.strictEqual(p.el('mcRegionChipText').innerHTML, '', 'chip text written through innerHTML');
  });

  console.log(`\n${passed} passed, ${failed} failed`);
  process.exit(failed === 0 ? 0 : 1);
})();

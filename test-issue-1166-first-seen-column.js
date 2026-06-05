/* Regression test for issue #1166: "First Seen" column on the Nodes table.
 *
 * Asserts both surface (column header + sort key in the table markup)
 * AND sort behavior (sortNodes handles 'first_seen' column with empty-last
 * semantics).
 */
'use strict';
const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log('  ✅ ' + name); }
  catch (e) { failed++; console.log('  ❌ ' + name + ': ' + e.message); }
}

console.log('\n=== issue #1166: First Seen column ===');

// --- 1. Source-level assertions on public/nodes.js ---
const src = fs.readFileSync(__dirname + '/public/nodes.js', 'utf8');

test('nodes table header includes a "First Seen" column', () => {
  // Match a <th> with text "First Seen" (case-insensitive)
  assert.ok(/<th[^>]*>\s*First Seen\s*<\/th>/i.test(src),
    'expected <th>First Seen</th> in public/nodes.js (nodes table header)');
});

test('First Seen header carries data-sort-key="first_seen"', () => {
  assert.ok(/data-sort-key="first_seen"/.test(src),
    'expected data-sort-key="first_seen" on the First Seen <th>');
});

test('renderRows emits a first_seen cell', () => {
  // The render function must reference n.first_seen somewhere so the
  // cell renders the value (presence is what we gate on; richer
  // formatting is enforced by sortNodes / timestamp helper tests).
  assert.ok(/n\.first_seen/.test(src),
    'expected renderRows to read n.first_seen for the cell value');
});

// --- 2. sortNodes behavior via VM sandbox (mirrors test-frontend-helpers harness) ---
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
    setTimeout: () => {}, clearTimeout: () => {},
    setInterval: () => {}, clearInterval: () => {},
    fetch: () => Promise.resolve({ json: () => Promise.resolve({}) }),
    performance: { now: () => Date.now() },
    localStorage: (() => {
      const store = {};
      return {
        getItem: k => store[k] || null,
        setItem: (k, v) => { store[k] = String(v); },
        removeItem: k => { delete store[k]; },
      };
    })(),
    location: { hash: '' },
    CustomEvent: class CustomEvent {},
    Map, Set, Promise, URLSearchParams,
    addEventListener: () => {}, dispatchEvent: () => {},
    requestAnimationFrame: (cb) => setTimeout(cb, 0),
  };
  ctx.getHashParams = function() { return new URLSearchParams(''); };
  ctx.registerPage = () => {};
  ctx.RegionFilter = { init: () => {}, getSelected: () => null, onRegionChange: () => {} };
  ctx.onWS = () => {};
  ctx.offWS = () => {};
  ctx.invalidateApiCache = () => {};
  ctx.favStar = () => '';
  ctx.bindFavStars = () => {};
  ctx.getFavorites = () => [];
  ctx.isFavorite = () => false;
  ctx.connectWS = () => {};
  ctx.HopResolver = { init: () => {}, resolve: () => ({}), ready: () => false };
  ctx.api = () => Promise.resolve({ nodes: [], counts: {} });
  ctx.CLIENT_TTL = { nodeList: 90000, nodeDetail: 240000, nodeHealth: 240000 };
  ctx.initTabBar = () => {};
  ctx.makeColumnsResizable = () => {};
  ctx.debounce = (fn) => fn;
  vm.createContext(ctx);
  return ctx;
}
function loadInCtx(ctx, file) {
  vm.runInContext(fs.readFileSync(file, 'utf8'), ctx);
  for (const k of Object.keys(ctx.window)) ctx[k] = ctx.window[k];
}

const ctx = makeSandbox();
loadInCtx(ctx, __dirname + '/public/roles.js');
loadInCtx(ctx, __dirname + '/public/app.js');
loadInCtx(ctx, __dirname + '/public/nodes.js');

const sortNodes = ctx.window._nodesSortNodes;
const setState = ctx.window._nodesSetSortState;

test('sortNodes exposes first_seen as a sort column (desc, newest first)', () => {
  setState({ column: 'first_seen', direction: 'desc' });
  const now = Date.now();
  const arr = [
    { name: 'Old',    first_seen: new Date(now - 100000).toISOString() },
    { name: 'Newest', first_seen: new Date(now).toISOString() },
    { name: 'Mid',    first_seen: new Date(now - 50000).toISOString() },
  ];
  const r = sortNodes([...arr]);
  assert.strictEqual(r[0].name, 'Newest');
  assert.strictEqual(r[2].name, 'Old');
});

test('sortNodes by first_seen asc (oldest first)', () => {
  setState({ column: 'first_seen', direction: 'asc' });
  const now = Date.now();
  const arr = [
    { name: 'Newest', first_seen: new Date(now).toISOString() },
    { name: 'Old',    first_seen: new Date(now - 100000).toISOString() },
  ];
  const r = sortNodes([...arr]);
  assert.strictEqual(r[0].name, 'Old');
  assert.strictEqual(r[1].name, 'Newest');
});

test('sortNodes by first_seen puts empty cells LAST regardless of direction', () => {
  const now = Date.now();
  const arr = [
    { name: 'NoFS' },
    { name: 'WithFS', first_seen: new Date(now).toISOString() },
    { name: 'NullFS', first_seen: null },
  ];
  setState({ column: 'first_seen', direction: 'desc' });
  let r = sortNodes([...arr]);
  assert.strictEqual(r[0].name, 'WithFS', 'desc: dated node first');
  assert.notStrictEqual(r[2].name, 'WithFS', 'desc: empty cells should not sort above dated');

  setState({ column: 'first_seen', direction: 'asc' });
  r = sortNodes([...arr]);
  assert.strictEqual(r[0].name, 'WithFS', 'asc: dated node still first (empty cells last)');
});

console.log('\n' + (failed ? '❌' : '✅') + ' ' + passed + ' passed, ' + failed + ' failed');
if (failed) process.exit(1);

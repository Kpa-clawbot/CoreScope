/* test-issue-1420-tile-providers.js — Dark-tile provider registry tests.
 *
 * Asserts the MC_TILE_PROVIDERS registry shape, persistence helpers,
 * and CSS-filter swap behavior for inverted variants. Tests via VM
 * sandbox (no jsdom dep). Follows the pattern from cb-presets tests.
 */
'use strict';
const vm = require('vm');
const fs = require('fs');
const path = require('path');
const assert = require('assert');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log('  ✅ ' + name); }
  catch (e) { failed++; console.log('  ❌ ' + name + ': ' + e.message); }
}

function makeStorage() {
  const store = {};
  return {
    getItem(k) { return Object.prototype.hasOwnProperty.call(store, k) ? store[k] : null; },
    setItem(k, v) { store[k] = String(v); },
    removeItem(k) { delete store[k]; },
    clear() { for (const k of Object.keys(store)) delete store[k]; },
    _raw: store
  };
}

function makeSandbox(opts) {
  opts = opts || {};
  const events = [];
  const listeners = {};
  const tilePane = { style: { filter: '' } };
  const ctx = {
    console,
    setTimeout, clearTimeout,
    JSON, Date, Math, Object, Array, String, Number, Boolean,
    localStorage: makeStorage(),
    document: {
      documentElement: { getAttribute: () => opts.theme || 'dark' },
      querySelector: (sel) => sel === '.leaflet-tile-pane' ? tilePane : null,
      querySelectorAll: () => [],
      addEventListener: () => {},
    },
    window: {
      addEventListener: (type, fn) => { (listeners[type] = listeners[type] || []).push(fn); },
      dispatchEvent: (ev) => { events.push(ev); return true; },
      matchMedia: () => ({ matches: false, addEventListener: () => {} }),
    },
    CustomEvent: function (type, init) { this.type = type; this.detail = (init && init.detail) || null; }
  };
  ctx.window.localStorage = ctx.localStorage;
  ctx.globalThis = ctx;
  vm.createContext(ctx);
  // Make window mirror globals (the module uses window.X assignment)
  ctx.window.document = ctx.document;
  ctx.events = events;
  ctx.listeners = listeners;
  ctx.tilePane = tilePane;
  return ctx;
}

function loadProviders(ctx) {
  const src = fs.readFileSync(path.join(__dirname, 'public', 'map-tile-providers.js'), 'utf8');
  vm.runInContext(src, ctx);
}

console.log('── #1420 Dark-tile provider registry ──');

test('MC_TILE_PROVIDERS has all 4 IDs with url + attribution', () => {
  const ctx = makeSandbox();
  loadProviders(ctx);
  const reg = ctx.window.MC_TILE_PROVIDERS;
  assert.ok(reg, 'registry must exist on window');
  const ids = ['carto-dark', 'esri-darkgray-labels', 'voyager-inverted', 'positron-inverted'];
  for (const id of ids) {
    assert.ok(reg[id], 'missing provider: ' + id);
    assert.ok(typeof reg[id].attribution === 'string' && reg[id].attribution.length > 0, id + ' attribution');
    // Esri uses baseUrl + refUrl; others use url
    if (id === 'esri-darkgray-labels') {
      assert.ok(typeof reg[id].baseUrl === 'string' && reg[id].baseUrl.indexOf('{z}') >= 0, 'esri baseUrl');
      assert.ok(typeof reg[id].refUrl === 'string' && reg[id].refUrl.indexOf('{z}') >= 0, 'esri refUrl');
    } else {
      assert.ok(typeof reg[id].url === 'string' && reg[id].url.indexOf('{z}') >= 0, id + ' url has {z}');
    }
  }
});

test('Inverted providers have non-null invertFilter; non-inverted have null', () => {
  const ctx = makeSandbox();
  loadProviders(ctx);
  const reg = ctx.window.MC_TILE_PROVIDERS;
  assert.strictEqual(reg['carto-dark'].invertFilter, null);
  assert.strictEqual(reg['esri-darkgray-labels'].invertFilter, null);
  assert.ok(typeof reg['voyager-inverted'].invertFilter === 'string' && reg['voyager-inverted'].invertFilter.indexOf('invert(') >= 0);
  assert.ok(typeof reg['positron-inverted'].invertFilter === 'string' && reg['positron-inverted'].invertFilter.indexOf('invert(') >= 0);
});

test('MC_setDarkTileProvider persists to localStorage and dispatches mc-tile-provider-changed', () => {
  const ctx = makeSandbox();
  loadProviders(ctx);
  ctx.window.MC_setDarkTileProvider('voyager-inverted');
  assert.strictEqual(ctx.localStorage.getItem('mc-dark-tile-provider'), 'voyager-inverted');
  assert.ok(ctx.events.length >= 1, 'event dispatched');
  const ev = ctx.events[ctx.events.length - 1];
  assert.strictEqual(ev.type, 'mc-tile-provider-changed');
  assert.ok(ev.detail && ev.detail.id === 'voyager-inverted');
});

test('MC_setDarkTileProvider rejects unknown IDs (no persistence, no dispatch)', () => {
  const ctx = makeSandbox();
  loadProviders(ctx);
  const ok = ctx.window.MC_setDarkTileProvider('not-a-real-provider');
  assert.strictEqual(ok, false);
  assert.strictEqual(ctx.localStorage.getItem('mc-dark-tile-provider'), null);
  assert.strictEqual(ctx.events.length, 0);
});

test('MC_getDarkTileProvider falls back to server default, then carto-dark', () => {
  const ctx = makeSandbox();
  loadProviders(ctx);
  // No localStorage, no server hint → default carto-dark
  assert.strictEqual(ctx.window.MC_getDarkTileProvider(), 'carto-dark');
  // Server-provided default surfaces through
  ctx.window.MC_setServerDefaultTileProvider('esri-darkgray-labels');
  assert.strictEqual(ctx.window.MC_getDarkTileProvider(), 'esri-darkgray-labels');
  // localStorage wins over server
  ctx.window.MC_setDarkTileProvider('voyager-inverted');
  assert.strictEqual(ctx.window.MC_getDarkTileProvider(), 'voyager-inverted');
});

test('Apply filter for inverted provider in dark mode; clear when switching to non-inverted', () => {
  const ctx = makeSandbox({ theme: 'dark' });
  loadProviders(ctx);
  ctx.window.MC_setDarkTileProvider('voyager-inverted');
  ctx.window.MC_applyTileFilter();  // dark theme + inverted → filter set
  assert.ok(ctx.tilePane.style.filter.indexOf('invert(') >= 0, 'filter applied: ' + ctx.tilePane.style.filter);
  ctx.window.MC_setDarkTileProvider('carto-dark');
  ctx.window.MC_applyTileFilter();
  assert.strictEqual(ctx.tilePane.style.filter, '', 'filter cleared after switching to carto-dark');
});

test('Light mode always clears the CSS filter even if inverted provider is selected', () => {
  const ctx = makeSandbox({ theme: 'light' });
  loadProviders(ctx);
  ctx.tilePane.style.filter = 'invert(1)';  // pre-set from a prior dark session
  ctx.window.MC_setDarkTileProvider('voyager-inverted');
  ctx.window.MC_applyTileFilter();  // light theme → must clear regardless of provider
  assert.strictEqual(ctx.tilePane.style.filter, '');
});

test('Cross-tab storage event re-dispatches mc-tile-provider-changed and re-applies filter', () => {
  const ctx = makeSandbox({ theme: 'dark' });
  loadProviders(ctx);
  // Sanity: module registered a storage listener.
  assert.ok(ctx.listeners.storage && ctx.listeners.storage.length >= 1, 'storage listener registered');
  // Simulate localStorage change from another tab (do NOT call setActive — that's same-tab).
  ctx.localStorage.setItem('mc-dark-tile-provider', 'voyager-inverted');
  const before = ctx.events.length;
  ctx.listeners.storage[0]({ key: 'mc-dark-tile-provider', newValue: 'voyager-inverted', oldValue: null });
  assert.ok(ctx.events.length > before, 'storage event re-dispatched mc-tile-provider-changed');
  const ev = ctx.events[ctx.events.length - 1];
  assert.strictEqual(ev.type, 'mc-tile-provider-changed');
  assert.strictEqual(ev.detail.id, 'voyager-inverted');
  assert.strictEqual(ev.detail.crossTab, true);
  assert.ok(ctx.tilePane.style.filter.indexOf('invert(') >= 0, 'filter re-applied after cross-tab change');
  // Unknown values from other tabs must be ignored.
  const beforeIgnored = ctx.events.length;
  ctx.listeners.storage[0]({ key: 'mc-dark-tile-provider', newValue: 'bogus', oldValue: 'voyager-inverted' });
  assert.strictEqual(ctx.events.length, beforeIgnored, 'unknown ids do not re-dispatch');
  // Unrelated keys must be ignored.
  ctx.listeners.storage[0]({ key: 'other-key', newValue: 'carto-dark', oldValue: null });
  assert.strictEqual(ctx.events.length, beforeIgnored, 'unrelated key ignored');
});

process.on('beforeExit', () => {
  console.log('');
  console.log('  ' + passed + ' passed, ' + failed + ' failed');
  if (failed) process.exit(1);
});

/* test-carto-api-token.js — CARTO API token support in the tile registry.
 *
 * CARTO's raster basemaps require an API key since August 2026: anonymous
 * requests get an "API KEY REQUIRED" watermark on every tile. Config gains
 * tiles.providers.carto.token, mirroring the stamen/osm token knobs that
 * already exist.
 *
 * Contract under test:
 *   - With providers.carto.token set, every carto-provider url carries
 *     ?key=<token>, URL-encoded.
 *   - Without a token, urls are byte-identical to the historical anonymous
 *     form (no dangling '?').
 *   - The token never leaks onto a non-CARTO provider's url.
 *
 * Runs via: node test-carto-api-token.js
 * No jsdom or Playwright dependency — pure vm sandbox.
 */
'use strict';
const vm   = require('vm');
const fs   = require('fs');
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
    getItem(k)    { return Object.prototype.hasOwnProperty.call(store, k) ? store[k] : null; },
    setItem(k, v) { store[k] = String(v); },
    removeItem(k) { delete store[k]; },
    clear()       { for (const k of Object.keys(store)) delete store[k]; },
  };
}

function makeSandbox() {
  const tilePane = { style: { filter: '' }, setAttribute() {}, getAttribute() { return null; }, removeAttribute() {} };
  const ctx = {
    console, setTimeout, clearTimeout,
    JSON, Date, Math, Object, Array, String, Number, Boolean,
    localStorage: makeStorage(),
    document: {
      documentElement: { getAttribute: () => 'dark' },
      querySelector: (sel) => sel === '.leaflet-tile-pane' ? tilePane : null,
      querySelectorAll: () => [],
      addEventListener: () => {},
    },
    window: {
      addEventListener: () => {},
      dispatchEvent: () => true,
      matchMedia: () => ({ matches: false, addEventListener: () => {} }),
    },
    CustomEvent: function (type, init) { this.type = type; this.detail = (init && init.detail) || null; }
  };
  ctx.window.localStorage = ctx.localStorage;
  ctx.globalThis = ctx;
  vm.createContext(ctx);
  ctx.window.document = ctx.document;
  return ctx;
}

function loadProviders(ctx, mapCfg) {
  if (mapCfg !== undefined) ctx.window.MC_MAP_CFG = mapCfg;
  const src = fs.readFileSync(path.join(__dirname, 'public', 'map-tile-providers.js'), 'utf8');
  vm.runInContext(src, ctx);
}

const CARTO_IDS = ['carto-dark', 'carto-light', 'carto-voyager', 'carto-voyager-dark', 'positron-dark'];

console.log('── CARTO API token ──');

test('carto token rides every carto url as ?key=', () => {
  const ctx = makeSandbox();
  loadProviders(ctx, { tiles: { providers: { carto: { enabled: true, token: 'tok123' } } } });
  ctx.window.MC_initTileRegistry(false);
  const reg = ctx.window.MC_TILE_PROVIDERS;
  for (const id of CARTO_IDS) {
    const url = reg[id].url();
    assert.ok(url.indexOf('?key=tok123') >= 0, id + ' must carry the key, got ' + url);
    assert.ok(url.indexOf('{z}') >= 0, id + ' keeps its template');
  }
});

test('no token: urls stay byte-identical to the anonymous form', () => {
  const ctx = makeSandbox();
  loadProviders(ctx, { tiles: { providers: { carto: { enabled: true } } } });
  ctx.window.MC_initTileRegistry(false);
  const url = ctx.window.MC_TILE_PROVIDERS['carto-dark'].url();
  assert.strictEqual(url, 'https://{s}.basemaps.cartocdn.com/dark_all/{z}/{x}/{y}{r}.png');
});

test('token is URL-encoded', () => {
  const ctx = makeSandbox();
  loadProviders(ctx, { tiles: { providers: { carto: { enabled: true, token: 'a b&c' } } } });
  ctx.window.MC_initTileRegistry(false);
  assert.ok(ctx.window.MC_TILE_PROVIDERS['carto-dark'].url().indexOf('?key=a%20b%26c') >= 0);
});

test('token never leaks onto a non-CARTO provider', () => {
  const ctx = makeSandbox();
  loadProviders(ctx, { tiles: { providers: { carto: { enabled: true, token: 'tok123' }, osm: { enabled: true }, stamen: { enabled: true, token: 'stok' } } } });
  ctx.window.MC_initTileRegistry(false);
  const reg = ctx.window.MC_TILE_PROVIDERS;
  for (const id of Object.keys(reg)) {
    if (CARTO_IDS.indexOf(id) >= 0) continue;
    const url = typeof reg[id].url === 'function' ? reg[id].url() : reg[id].url;
    assert.ok(url.indexOf('tok123') < 0, id + ' must not carry the carto token: ' + url);
  }
});

test('carto domain override and token compose', () => {
  const ctx = makeSandbox();
  loadProviders(ctx, { tiles: { providers: { carto: { enabled: true, domain: 'example', token: 'tok' } } } });
  ctx.window.MC_initTileRegistry(false);
  const url = ctx.window.MC_TILE_PROVIDERS['carto-dark'].url();
  assert.ok(url.indexOf('https://{s}.example.cartocdn.com/') === 0, url);
  assert.ok(url.indexOf('?key=tok') >= 0, url);
});

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed ? 1 : 0);

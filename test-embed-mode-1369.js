/* Unit tests for issue #1369 embed-mode helper.
 *
 * Red commit: shouldEmbedRoute() does not exist yet; this test fails on import.
 * Green commit: define shouldEmbedRoute(basePage, hashSearch) in public/app.js
 * and expose for tests.
 *
 * The contract:
 *   - returns true ONLY when basePage is 'map' or 'channels' AND the hash
 *     query string contains embed=1 (e.g. '#/map?embed=1' → search='embed=1').
 *   - false for any other route, false when embed param is missing or != '1'.
 *   - the route-allowlist is deliberate: other pages have chrome assumptions
 *     that we are not committing to support in embed mode (Tufte: scope tight,
 *     ship the two surfaces operators asked for, no more).
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

// Load app.js into a sandbox. We only need the helper, so we wrap with a
// minimal browser shim that no-ops everything app.js touches at import time.
const appSrc = fs.readFileSync(path.join(__dirname, 'public', 'app.js'), 'utf8');
const ctx = {
  window: { addEventListener: () => {}, dispatchEvent: () => {}, matchMedia: () => ({ matches: false }) },
  document: {
    readyState: 'complete',
    documentElement: { setAttribute: () => {}, getAttribute: () => null, classList: { add: () => {}, remove: () => {}, toggle: () => {} } },
    body: { classList: { add: () => {}, remove: () => {}, toggle: () => {} } },
    createElement: () => ({ id: '', textContent: '', innerHTML: '', classList: { add: () => {}, remove: () => {} }, setAttribute: () => {}, appendChild: () => {} }),
    head: { appendChild: () => {} },
    getElementById: () => null,
    addEventListener: () => {},
    querySelectorAll: () => [],
    querySelector: () => null,
  },
  console, Date, Math, JSON, RegExp, Error, TypeError, Map, Set,
  Array, Object, String, Number, Boolean, parseInt, parseFloat, isNaN, isFinite,
  encodeURIComponent, decodeURIComponent, URLSearchParams,
  setTimeout: () => 0, clearTimeout: () => {}, setInterval: () => 0, clearInterval: () => {},
  fetch: () => Promise.resolve({ json: () => Promise.resolve({}) }),
  performance: { now: () => Date.now() },
  localStorage: { getItem: () => null, setItem: () => {}, removeItem: () => {} },
  location: { hash: '', search: '' },
  CustomEvent: class {},
  navigator: { language: 'en-US' },
  requestAnimationFrame: () => 0,
};
ctx.globalThis = ctx;
ctx.self = ctx;
try {
  vm.createContext(ctx);
  vm.runInContext(appSrc, ctx, { filename: 'app.js' });
} catch (e) {
  // app.js does a lot at import; if any helper init blows up, it's fine as
  // long as shouldEmbedRoute is bound by the time we test it.
  console.log('  (app.js init threw: ' + e.message + ' — proceeding)');
}

console.log('issue #1369 — shouldEmbedRoute');
test('exists', () => {
  assert.strictEqual(typeof ctx.shouldEmbedRoute, 'function', 'shouldEmbedRoute must be defined on window/global');
});
test('map + embed=1 → true', () => {
  assert.strictEqual(ctx.shouldEmbedRoute('map', 'embed=1'), true);
});
test('channels + embed=1 → true', () => {
  assert.strictEqual(ctx.shouldEmbedRoute('channels', 'embed=1'), true);
});
test('map + embed=1 mixed with other params → true', () => {
  assert.strictEqual(ctx.shouldEmbedRoute('map', 'region=SFO&embed=1&zoom=8'), true);
});
test('packets + embed=1 → false (route not in allowlist)', () => {
  assert.strictEqual(ctx.shouldEmbedRoute('packets', 'embed=1'), false);
});
test('nodes + embed=1 → false', () => {
  assert.strictEqual(ctx.shouldEmbedRoute('nodes', 'embed=1'), false);
});
test('map + no embed param → false', () => {
  assert.strictEqual(ctx.shouldEmbedRoute('map', 'region=SFO'), false);
});
test('map + embed=0 → false', () => {
  assert.strictEqual(ctx.shouldEmbedRoute('map', 'embed=0'), false);
});
test('map + empty search → false', () => {
  assert.strictEqual(ctx.shouldEmbedRoute('map', ''), false);
});

console.log('\n' + '═'.repeat(40));
console.log('  embed-mode helper: ' + passed + ' passed, ' + failed + ' failed');
console.log('═'.repeat(40) + '\n');
if (failed > 0) process.exit(1);

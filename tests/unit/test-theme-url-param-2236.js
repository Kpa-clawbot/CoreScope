/* Unit tests for issue #2236: ?theme=dark|light in the hash query.
 *
 * Runs the real pre-paint script from public/index.html in a vm sandbox and
 * checks the theme it sets before first paint, and the override it hands to
 * app.js (window.__themeOverride).
 *
 * Usage: node tests/unit/test-theme-url-param-2236.js
 */
'use strict';
const fs = require('fs');
const path = require('path');
const vm = require('vm');
const assert = require('assert');

const html = fs.readFileSync(path.join(__dirname, '..', '..', 'public', 'index.html'), 'utf8');
const m = /<script>\s*(\(function \(\) \{[\s\S]*?meshcore-theme[\s\S]*?\}\)\(\);)\s*<\/script>/.exec(html);
if (!m) { console.log('  ❌ pre-paint theme script not found in index.html'); process.exit(1); }
const SRC = m[1];

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log('  ✅ ' + name); }
  catch (e) { failed++; console.log('  ❌ ' + name + ': ' + e.message); }
}

function run(hash, opts) {
  opts = opts || {};
  const attrs = {};
  const store = Object.assign({}, opts.stored);
  const ctx = {
    URLSearchParams,
    location: { hash: hash },
    document: { documentElement: { setAttribute: (k, v) => { attrs[k] = v; } } },
    localStorage: opts.blocked
      ? { getItem: () => { throw new Error('SecurityError'); } }
      : { getItem: (k) => (k in store ? store[k] : null), setItem: (k, v) => { store[k] = String(v); } },
    matchMedia: () => ({ matches: !!opts.osDark }),
  };
  ctx.window = ctx;
  vm.createContext(ctx);
  vm.runInContext(SRC, ctx);
  return { theme: attrs['data-theme'], override: ctx.__themeOverride, store };
}

console.log('issue #2236 — pre-paint theme from the URL');
test('theme=dark wins over a saved light choice', () => {
  const r = run('#/map?embed=1&theme=dark', { stored: { 'meshcore-theme': 'light' } });
  assert.strictEqual(r.theme, 'dark');
  assert.strictEqual(r.override, 'dark');
});
test('theme=light wins over a saved dark choice and a dark OS', () => {
  const r = run('#/channels?theme=light', { stored: { 'meshcore-theme': 'dark' }, osDark: true });
  assert.strictEqual(r.theme, 'light');
  assert.strictEqual(r.override, 'light');
});
test('the override is not written to localStorage', () => {
  const r = run('#/map?theme=dark', { stored: { 'meshcore-theme': 'light' } });
  assert.strictEqual(r.store['meshcore-theme'], 'light');
});
test('works on any page, with the param anywhere in the query', () => {
  assert.strictEqual(run('#/packets?region=BE&theme=dark&x=1').theme, 'dark');
  assert.strictEqual(run('#/?theme=dark').theme, 'dark');
});
test('an unknown value is ignored', () => {
  const r = run('#/map?theme=blue', { stored: { 'meshcore-theme': 'light' } });
  assert.strictEqual(r.theme, 'light');
  assert.strictEqual(r.override, null);
});
test('without the param the saved choice applies', () => {
  const r = run('#/map?embed=1', { stored: { 'meshcore-theme': 'dark' } });
  assert.strictEqual(r.theme, 'dark');
  assert.strictEqual(r.override, null);
});
test('without the param or a saved choice the OS preference applies', () => {
  assert.strictEqual(run('#/map', { osDark: true }).theme, 'dark');
  assert.strictEqual(run('', {}).theme, 'light');
});
test('the override still applies when localStorage throws (sandboxed iframe)', () => {
  const r = run('#/map?embed=1&theme=dark', { blocked: true });
  assert.strictEqual(r.theme, 'dark');
});

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed > 0 ? 1 : 0);

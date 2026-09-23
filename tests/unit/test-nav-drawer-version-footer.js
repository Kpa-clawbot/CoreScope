/* test-nav-drawer-version-footer.js
 *
 * The nav drawer's footer reports the running build from GET /api/health
 * (#1985). Two behaviours are easy to lose in a later edit and are pinned here:
 *
 *   1. A failed or version-less /api/health leaves the neutral "CoreScope"
 *      label. Without this the footer can end up blank or reading
 *      "CoreScope undefined" on any instance whose health endpoint is
 *      unavailable, which is exactly when someone is looking at it.
 *   2. The version is written with textContent, never as markup. The string
 *      comes from the server, and a footer is a poor place to learn that.
 *
 * The functions are sliced out of the shipped public/nav-drawer.js and
 * evaluated, rather than copied here, so this tests what actually ships. Same
 * approach as tests/unit/test-direct-rf-heard-by.js. The rest of the module is
 * an IIFE that builds real DOM and binds pointer handlers, which is E2E
 * territory; these two are pure enough to run without a browser.
 */
'use strict';

const REPO_ROOT = require('path').resolve(__dirname, '..', '..');
const fs = require('fs');
const path = require('path');
const vm = require('vm');
const assert = require('assert');

let passed = 0, failed = 0;
async function test(name, fn) {
  try { await fn(); passed++; console.log('  ✓ ' + name); }
  catch (e) { failed++; console.error('  ✗ ' + name + ': ' + e.message); }
}

const SRC = fs.readFileSync(path.join(REPO_ROOT, 'public/nav-drawer.js'), 'utf8');

// Slice from the cached-promise declaration to the constants that follow
// fillVersion. If either marker moves the slice fails loudly rather than
// silently testing nothing.
function sliceVersionFns() {
  const start = SRC.indexOf('var versionPromise = null;');
  const end = SRC.indexOf('var EDGE_PX');
  assert.ok(start !== -1, 'could not find "var versionPromise = null;" in public/nav-drawer.js');
  assert.ok(end !== -1 && end > start, 'could not find the "var EDGE_PX" boundary after fillVersion');
  return SRC.slice(start, end);
}

// Fresh evaluation per case: versionPromise caches for the page lifetime, so a
// shared sandbox would hand the second case the first case's answer.
function load(fetchImpl) {
  const sandbox = { fetch: fetchImpl, console };
  vm.createContext(sandbox);
  vm.runInContext(sliceVersionFns() + '\nthis.fillVersion = fillVersion;', sandbox);
  return sandbox.fillVersion;
}

function fakeLink() {
  return { textContent: 'CoreScope', title: '', innerHTML: null };
}

// fillVersion does not return its promise, so settle the microtask queue.
const settle = () => new Promise((r) => setTimeout(r, 0));

(async () => {
  console.log('\n=== #1985: the nav drawer version footer ===');

  await test('a failing /api/health leaves the neutral label', async () => {
    const el = fakeLink();
    load(() => Promise.reject(new Error('offline')))(el);
    await settle();
    assert.strictEqual(el.textContent, 'CoreScope');
    assert.strictEqual(el.innerHTML, null, 'nothing may be written as markup');
  });

  await test('a non-ok response leaves the neutral label', async () => {
    const el = fakeLink();
    load(() => Promise.resolve({ ok: false, json: () => Promise.resolve({}) }))(el);
    await settle();
    assert.strictEqual(el.textContent, 'CoreScope');
  });

  await test('a response without a version leaves the neutral label', async () => {
    const el = fakeLink();
    load(() => Promise.resolve({ ok: true, json: () => Promise.resolve({ commit: 'abc1234' }) }))(el);
    await settle();
    assert.strictEqual(el.textContent, 'CoreScope',
      'a health response missing "version" must not produce "CoreScope undefined"');
  });

  await test('a version is rendered, with commit and build time in the tooltip', async () => {
    const el = fakeLink();
    load(() => Promise.resolve({
      ok: true,
      json: () => Promise.resolve({ version: 'v3.11.0', commit: 'a34244f4', buildTime: '2026-09-22T09:00:00Z' }),
    }))(el);
    await settle();
    assert.strictEqual(el.textContent, 'CoreScope v3.11.0');
    assert.ok(/a34244f4/.test(el.title), 'commit belongs in the tooltip: ' + el.title);
    assert.ok(/2026-09-22/.test(el.title), 'build time belongs in the tooltip: ' + el.title);
  });

  await test('a version carrying markup is written as text, not HTML', async () => {
    const el = fakeLink();
    const hostile = '<img src=x onerror=alert(1)>';
    load(() => Promise.resolve({ ok: true, json: () => Promise.resolve({ version: hostile }) }))(el);
    await settle();
    assert.strictEqual(el.textContent, 'CoreScope ' + hostile,
      'the string must land verbatim in textContent');
    assert.strictEqual(el.innerHTML, null, 'innerHTML must never be touched');
  });

  await test('the health endpoint is requested once, however often the footer is filled', async () => {
    let calls = 0;
    const fill = load(() => { calls++; return Promise.resolve({ ok: true, json: () => Promise.resolve({ version: 'v1' }) }); });
    fill(fakeLink()); fill(fakeLink()); fill(fakeLink());
    await settle();
    assert.strictEqual(calls, 1, 'the promise is cached for the page lifetime, got ' + calls + ' requests');
  });

  console.log(`\nTotal: ${passed} passed, ${failed} failed`);
  if (failed) process.exit(1);
})();

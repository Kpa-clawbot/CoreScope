/* test-issue-1997-distance-building.js
 *
 * Issue #1997: the Distance tab crashed on the lazy index's first response.
 *
 * The backend answers 202 with {status:"building", retry_after_seconds:5} and
 * no summary until the index (#1011) has been built. Two things went wrong:
 *
 *   1. renderDistanceTab read data.summary.totalHops straight away, so the
 *      page showed "Cannot read properties of undefined (reading 'totalHops')".
 *   2. api() caches any res.ok body, and 202 is ok, so the placeholder was
 *      cached for the analyticsRF TTL. Even a correct retry would then read
 *      the cached "building" body back and stay stuck long after the index
 *      was ready. That is the half that makes the first one permanent.
 *
 * This pins both: the two pure decisions the renderer makes, and api()'s
 * refusal to cache a 202 while still caching a 200.
 */
'use strict';
const fs = require('fs');
const path = require('path');
const vm = require('vm');
const assert = require('assert');

const REPO_ROOT = path.resolve(__dirname, '..', '..');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log('  ✓ ' + name); }
  catch (e) { failed++; console.error('  ✗ ' + name + ': ' + e.message); }
}
async function testAsync(name, fn) {
  try { await fn(); passed++; console.log('  ✓ ' + name); }
  catch (e) { failed++; console.error('  ✗ ' + name + ': ' + e.message); }
}

// ── The renderer's two decisions, loaded from the real analytics.js ─────────
function loadAnalyticsHelpers() {
  const sandbox = {
    console,
    window: null,
    document: {
      documentElement: {},
      createElement: () => ({ style: {}, addEventListener() {} }),
      addEventListener() {}, removeEventListener() {},
      querySelector: () => null, querySelectorAll: () => [], getElementById: () => null,
    },
    localStorage: { getItem: () => null, setItem() {}, removeItem() {} },
    getComputedStyle: () => ({ getPropertyValue: () => '' }),
    registerPage: () => {},
    api: async () => ({}),
    fetch: async () => ({ ok: true, json: async () => ({}) }),
    CLIENT_TTL: {},
    RegionFilter: { getRegionParam: () => '', regionQueryString: () => '' },
    Storage: function () {},
    timeAgo: () => '',
    histogram: () => ({ svg: '' }),
    setTimeout, clearTimeout, setInterval, clearInterval,
    requestAnimationFrame: () => 0,
    cancelAnimationFrame: () => {},
  };
  sandbox.window = sandbox;
  vm.createContext(sandbox);
  vm.runInContext(fs.readFileSync(path.join(REPO_ROOT, 'public/analytics.js'), 'utf8'), sandbox);
  return sandbox;
}

console.log('\n=== #1997: the Distance tab and the lazy index\'s 202 ===');

const a = loadAnalyticsHelpers();

test('the helpers are exposed for testing', () => {
  assert.strictEqual(typeof a._distanceIsBuilding, 'function', '_distanceIsBuilding');
  assert.strictEqual(typeof a._distanceRetryDelayMs, 'function', '_distanceRetryDelayMs');
});

test('the 202 body is recognised as still building', () => {
  const body = { status: 'building', retry_after_seconds: 5, detail: 'distance index is being computed' };
  assert.strictEqual(a._distanceIsBuilding(body), true);
});

test('a real payload is not treated as building', () => {
  assert.strictEqual(a._distanceIsBuilding({ summary: { totalHops: 12 }, catStats: {} }), false);
  assert.strictEqual(a._distanceIsBuilding({}), false);
  assert.strictEqual(a._distanceIsBuilding(null), false);
  assert.strictEqual(a._distanceIsBuilding(undefined), false);
});

test('a body carrying both a status and a summary is data, not a placeholder', () => {
  // Defensive: if the contract ever grows a status field on real payloads,
  // the presence of a summary decides, so the tab renders instead of looping.
  assert.strictEqual(a._distanceIsBuilding({ status: 'building', summary: { totalHops: 1 } }), false);
});

test('the retry honours the server interval, and is clamped at both ends', () => {
  assert.strictEqual(a._distanceRetryDelayMs({ retry_after_seconds: 5 }), 5000, 'uses what the server asked for');
  assert.strictEqual(a._distanceRetryDelayMs({}), 5000, 'defaults to the server default when absent');
  assert.strictEqual(a._distanceRetryDelayMs({ retry_after_seconds: 0 }), 5000, 'zero would be a hot loop');
  assert.strictEqual(a._distanceRetryDelayMs({ retry_after_seconds: -3 }), 5000, 'negative likewise');
  assert.strictEqual(a._distanceRetryDelayMs({ retry_after_seconds: 'soon' }), 5000, 'non-numeric likewise');
  assert.strictEqual(a._distanceRetryDelayMs({ retry_after_seconds: 0.2 }), 1000, 'clamped up to 1s');
  assert.strictEqual(a._distanceRetryDelayMs({ retry_after_seconds: 600 }), 30000, 'clamped down to 30s');
});

// ── api() must not cache a 202 ──────────────────────────────────────────────
function loadApi(responses) {
  const calls = [];
  const sandbox = {
    console,
    window: null,
    document: {
      documentElement: { style: {}, setAttribute() {}, getAttribute: () => null, classList: { add() {}, remove() {}, contains: () => false } },
      createElement: () => ({ style: {}, classList: { add() {}, remove() {} }, appendChild() {}, addEventListener() {} }),
      addEventListener() {}, removeEventListener() {},
      querySelector: () => null, querySelectorAll: () => [], getElementById: () => null,
      body: { appendChild() {}, classList: { add() {}, remove() {} } },
    },
    localStorage: { getItem: () => null, setItem() {}, removeItem() {} },
    location: { hash: '', search: '', pathname: '/' },
    history: { replaceState() {}, pushState() {} },
    performance: { now: () => 0 },
    navigator: { userAgent: 'node' },
    matchMedia: () => ({ matches: false, addEventListener() {}, removeEventListener() {} }),
    getComputedStyle: () => ({ getPropertyValue: () => '' }),
    setTimeout: (fn) => { fn(); return 0; },   // no wall-clock waiting
    clearTimeout() {}, setInterval: () => 0, clearInterval() {},
    requestAnimationFrame: () => 0, cancelAnimationFrame() {},
    WebSocket: function () { this.close = function () {}; this.addEventListener = function () {}; },
    addEventListener() {}, removeEventListener() {}, dispatchEvent: () => true,
    CustomEvent: function () {},
    URLSearchParams,
    // app.js issues its own fetches while it loads (version banner, config,
    // …). Only the distance path draws from the queued responses, and only
    // it is counted: otherwise a load-time fetch consumes the 202 and the
    // assertions below measure the wrong request.
    fetch: async (url) => {
      const mine = String(url).indexOf('/analytics/distance') !== -1;
      if (!mine) return { ok: true, status: 200, headers: { get: () => null }, json: async () => ({}) };
      calls.push(url);
      const r = responses.shift() || responses[responses.length - 1];
      return {
        ok: r.status >= 200 && r.status < 300,
        status: r.status,
        headers: { get: (h) => (h.toLowerCase() === 'retry-after' ? r.retryAfter || null : null) },
        json: async () => r.body,
      };
    },
  };
  sandbox.window = sandbox;
  vm.createContext(sandbox);
  vm.runInContext(fs.readFileSync(path.join(REPO_ROOT, 'public/app.js'), 'utf8'), sandbox);
  return { sandbox, calls };
}

(async () => {
  await testAsync('a 202 body is returned but never cached, so the retry reaches the server', async () => {
    const building = { status: 202, body: { status: 'building', retry_after_seconds: 5 } };
    const ready = { status: 200, body: { summary: { totalHops: 42 }, catStats: {} } };
    const { sandbox, calls } = loadApi([building, ready]);

    const first = await sandbox.api('/analytics/distance', { ttl: 300000 });
    assert.strictEqual(first.status, 'building', 'the placeholder is handed to the caller, not swallowed');

    const second = await sandbox.api('/analytics/distance', { ttl: 300000 });
    assert.ok(second.summary, 'the retry must reach the server and get the real payload');
    assert.strictEqual(second.summary.totalHops, 42);
    assert.strictEqual(calls.length, 2, 'the second call must not have been served from cache');
  });

  await testAsync('a 200 body is still cached, so the fix does not disable caching', async () => {
    const ready = { status: 200, body: { summary: { totalHops: 7 }, catStats: {} } };
    const { sandbox, calls } = loadApi([ready, ready]);

    await sandbox.api('/analytics/distance', { ttl: 300000 });
    await sandbox.api('/analytics/distance', { ttl: 300000 });
    assert.strictEqual(calls.length, 1, 'the second call should have been served from cache');
  });

  console.log(`\nTotal: ${passed} passed, ${failed} failed`);
  if (failed) process.exit(1);
})();

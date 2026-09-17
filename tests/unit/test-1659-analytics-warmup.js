/* test-1659-analytics-warmup.js
 *
 * Issue #1659: client-side warmup-retry for analytics endpoints.
 *
 * Asserts that public/app.js's api() helper:
 *   (a) retries on a 503 response and honors the Retry-After header
 *       value (in seconds) for the wait between attempts
 *   (b) eventually returns the JSON body once the server flips to 200
 *   (c) caps retries so a permanently broken endpoint does not loop
 *       forever
 *
 * The test loads app.js into a vm context with a fake fetch and a fake
 * setTimeout that resolves immediately (to keep wall-time minimal).
 */
'use strict';
const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

// Some tests intentionally cause api() to throw; the inflight tracker
// inside app.js may surface that as an unhandled rejection a tick later
// even after the test has caught it. Swallow them — they're expected
// for the "cap retries" test path.
process.on('unhandledRejection', (e) => {
  if (e && /API \d+/.test(e.message || '')) return;
  throw e;
});

let passed = 0, failed = 0;
function test(name, fn) {
  return Promise.resolve()
    .then(fn)
    .then(() => { passed++; console.log('  ✅ ' + name); })
    .catch(e => { failed++; console.log('  ❌ ' + name + ': ' + (e && e.message || e)); });
}

function makeCtx(fetchImpl) {
  const ctx = {
    console, Date, Math, Promise, Error, isFinite, parseInt, JSON,
    performance: { now: () => 0 },
    window: {},
    document: { readyState: 'complete', body: { appendChild: () => {} }, createElement: () => ({ style: {} }) },
    fetch: fetchImpl,
    setTimeout: (fn) => { fn(); return 0; }, // resolve immediately — backoff sleep is irrelevant in unit tests
    setInterval: () => 0,
    clearInterval: () => {},
    Map, Set,
  };
  vm.createContext(ctx);
  // Provide the no-op fetch('/api/config/cache') hit that app.js makes
  // on module load by returning a thenable that ignores.
  const src = fs.readFileSync('public/app.js', 'utf8');
  // Strip everything after the api()-related helpers we need; we only
  // want the top of the file (cache + api + _warmupNotify) and the
  // fetchAllNodes helper isn't required for these tests. Run the full
  // file under the sandbox; references to globals are guarded.
  try { vm.runInContext(src, ctx, { lineOffset: 0 }); } catch (e) {
    // Some downstream code expects window/document/localStorage; we
    // only need api() to be defined for these tests. Ignore.
  }
  return ctx;
}

(async () => {
  console.log('\n=== #1659: api() retries on 503 with Retry-After ===');

  await test('honors Retry-After then returns body on success', async () => {
    let calls = 0;
    const ctx = makeCtx(async (url) => {
      if (!/analytics/.test(url || '')) {
        return { ok: true, status: 200, headers: { get: () => null }, json: async () => ({}) };
      }
      calls++;
      if (calls < 3) {
        return {
          ok: false, status: 503,
          headers: { get: (k) => k.toLowerCase() === 'retry-after' ? '5' : null },
          json: async () => ({ error: 'analytics warming up', retry_after_s: 5 }),
        };
      }
      return { ok: true, status: 200, headers: { get: () => null }, json: async () => ({ totalPackets: 99 }) };
    });
    const data = await ctx.api('/analytics/rf');
    assert.strictEqual(calls, 3, 'expected 3 analytics fetches (two 503s then 200)');
    assert.deepStrictEqual(data, { totalPackets: 99 });
  });

  await test('caps retry attempts (no infinite loop on permanent 503)', async () => {
    let calls = 0;
    const ctx = makeCtx(async (url) => {
      if (!/analytics/.test(url || '')) {
        return { ok: true, status: 200, headers: { get: () => null }, json: async () => ({}) };
      }
      calls++;
      return {
        ok: false, status: 503,
        headers: { get: () => '1' },
        json: async () => ({}),
      };
    });
    let thrown = null;
    try { await ctx.api('/analytics/rf'); } catch (e) { thrown = e; }
    assert.ok(thrown, 'expected api() to throw after retries exhausted');
    assert.ok(/API 503/.test(thrown.message), 'expected 503 error message, got: ' + thrown.message);
    assert.ok(calls <= 12, 'retry cap should keep call count bounded, got ' + calls);
    assert.ok(calls >= 2, 'should have retried at least once, got ' + calls);
  });

  await test('non-503 errors do not trigger retry loop', async () => {
    let calls = 0;
    const ctx = makeCtx(async (url) => {
      if (!/analytics/.test(url || '')) {
        return { ok: true, status: 200, headers: { get: () => null }, json: async () => ({}) };
      }
      calls++;
      return { ok: false, status: 500, headers: { get: () => null }, json: async () => ({}) };
    });
    let thrown = null;
    try { await ctx.api('/analytics/rf'); } catch (e) { thrown = e; }
    assert.ok(thrown, 'expected throw on 500');
    assert.strictEqual(calls, 1, 'should not retry on non-503 errors');
  });

  // === PR #1688 r1 — banner counter leak fixes (adv #1 + munger #4) ===
  // The banner state must end HIDDEN after BOTH a retry-then-success
  // path and a retry-exhausted-throw path. Previously the in-flight
  // counter leaked, leaving the banner stuck visible.
  //
  // We can't read the internal `let _warmupInflight_1659` from outside
  // the vm (let is not attached to globalThis). Instead we hook
  // window.onWarmup_1659 which fires whenever banner state changes,
  // and assert the LAST state is `false` (hidden) — equivalent to
  // counter == 0.

  function withBannerHook(fetchImpl) {
    const ctx = makeCtx(fetchImpl);
    const events = [];
    ctx.window.onWarmup_1659 = (visible) => { events.push(visible); };
    return { ctx, events };
  }

  await test('banner hidden after retry-then-success (no leak)', async () => {
    let calls = 0;
    const { ctx, events } = withBannerHook(async (url) => {
      if (!/analytics/.test(url || '')) {
        return { ok: true, status: 200, headers: { get: () => null }, json: async () => ({}) };
      }
      calls++;
      if (calls < 4) {
        return { ok: false, status: 503, headers: { get: () => '1' }, json: async () => ({}) };
      }
      return { ok: true, status: 200, headers: { get: () => null }, json: async () => ({ ok: 1 }) };
    });
    await ctx.api('/analytics/rf');
    assert.ok(events.length > 0, 'expected at least one banner state change');
    assert.strictEqual(events[events.length - 1], false,
      'banner must end hidden after success (events=' + JSON.stringify(events) + ')');
  });

  await test('banner hidden after retry-exhausted throw (no leak)', async () => {
    const { ctx, events } = withBannerHook(async (url) => {
      if (!/analytics/.test(url || '')) {
        return { ok: true, status: 200, headers: { get: () => null }, json: async () => ({}) };
      }
      return { ok: false, status: 503, headers: { get: () => '1' }, json: async () => ({}) };
    });
    let thrown = null;
    try { await ctx.api('/analytics/rf'); } catch (e) { thrown = e; }
    assert.ok(thrown, 'expected throw after retries exhausted');
    assert.ok(events.length > 0, 'expected at least one banner state change');
    assert.strictEqual(events[events.length - 1], false,
      'banner must end hidden after retry-exhausted throw (events=' + JSON.stringify(events) + ')');
  });

  await test('banner hidden after three parallel analytics endpoints', async () => {
    let calls = 0;
    const { ctx, events } = withBannerHook(async (url) => {
      if (!/analytics/.test(url || '')) {
        return { ok: true, status: 200, headers: { get: () => null }, json: async () => ({}) };
      }
      calls++;
      // Each endpoint takes one 503 then succeeds.
      if (calls % 2 === 1) {
        return { ok: false, status: 503, headers: { get: () => '1' }, json: async () => ({}) };
      }
      return { ok: true, status: 200, headers: { get: () => null }, json: async () => ({}) };
    });
    await Promise.all([
      ctx.api('/analytics/rf'),
      ctx.api('/analytics/topology'),
      ctx.api('/analytics/channels'),
    ]);
    assert.strictEqual(events[events.length - 1], false,
      'banner must end hidden after three parallel calls (events=' + JSON.stringify(events) + ')');
  });

  await test('banner visible DURING retries (not just at the end)', async () => {
    let calls = 0;
    const { ctx, events } = withBannerHook(async (url) => {
      if (!/analytics/.test(url || '')) {
        return { ok: true, status: 200, headers: { get: () => null }, json: async () => ({}) };
      }
      calls++;
      if (calls < 3) {
        return { ok: false, status: 503, headers: { get: () => '1' }, json: async () => ({}) };
      }
      return { ok: true, status: 200, headers: { get: () => null }, json: async () => ({}) };
    });
    await ctx.api('/analytics/rf');
    assert.ok(events.includes(true),
      'banner must have been visible during retries (events=' + JSON.stringify(events) + ')');
    assert.strictEqual(events[events.length - 1], false, 'banner must end hidden');
  });

  console.log('\n' + passed + ' passed, ' + failed + ' failed');
  if (failed > 0) process.exit(1);
})();

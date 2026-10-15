'use strict';

const assert = require('assert');
const fs = require('fs');
const path = require('path');
const vm = require('vm');

const source = fs.readFileSync(path.join(__dirname, '../../public/app.js'), 'utf8');
const start = source.indexOf('// --- Utilities ---');
const end = source.indexOf('// Fetch the COMPLETE /api/nodes set', start);
assert.ok(start >= 0 && end > start, 'locate the real API/cache/warmup helpers');

function response(status, data = {}) {
  return { status, ok: status >= 200 && status < 300, headers: { get: () => '1' }, json: async () => data };
}

function makeContext(fetchImpl) {
  const events = [];
  const clock = { now: 1000 };
  const context = vm.createContext({
    console,
    Date: { now: () => clock.now },
    performance: { now: () => 0 },
    window: { onWarmup_1659: visible => events.push(visible) },
    fetch: url => url === '/api/config/cache' ? Promise.resolve(response(200)) : fetchImpl(url),
    setTimeout: fn => { fn(); return 0; },
  });
  // Execute the actual helpers, including their maps and warmup counter, without SPA startup.
  vm.runInContext(source.slice(start, end), context, { filename: 'public/app.js' });
  return { context, events, clock };
}

function assertSettled(context) {
  assert.strictEqual(vm.runInContext('_inflight.size', context), 0, 'settled requests leave the inflight map');
  assert.strictEqual(vm.runInContext('_warmupInflight_1659', context), 0, 'warmup counter stays balanced');
}

let passed = 0;
let failed = 0;
async function test(name, fn) {
  const unhandled = [];
  const capture = reason => unhandled.push(reason);
  process.on('unhandledRejection', capture);
  let failure;
  try { await fn(); } catch (error) { failure = error; }
  // Node reports unhandled rejections after the promise microtasks have drained.
  await new Promise(setImmediate);
  process.removeListener('unhandledRejection', capture);
  try {
    if (failure) throw failure;
    assert.strictEqual(unhandled.length, 0,
      'handled API requests must not create unhandled rejections: ' + unhandled.map(error => error.message).join(', '));
    passed++;
    console.log('  PASS ' + name);
  } catch (error) {
    failed++;
    console.error('  FAIL ' + name + ': ' + error.message);
  }
}

(async () => {
  console.log('\n=== API inflight cleanup ===');

  await test('success shares one request, clears inflight, and preserves cache TTL and bust', async () => {
    let release;
    const pending = new Promise(resolve => { release = resolve; });
    let calls = 0;
    const { context, clock } = makeContext(async () => {
      calls++;
      return calls === 1 ? pending : response(200, { call: calls });
    });
    const first = context.api('/cleanup', { ttl: 100 });
    const second = context.api('/cleanup', { ttl: 100 });
    assert.strictEqual(calls, 1, 'concurrent callers share one fetch');
    assert.strictEqual(vm.runInContext('_inflight.size', context), 1);
    const body = { call: 1 };
    release(response(200, body));
    const results = await Promise.all([first, second]);
    results.forEach(result => assert.strictEqual(result, body));
    assertSettled(context);
    clock.now += 99;
    assert.strictEqual(await context.api('/cleanup', { ttl: 100 }), body);
    assert.strictEqual(calls, 1, 'unexpired cached data avoids another fetch');
    clock.now++;
    assert.deepStrictEqual(await context.api('/cleanup', { ttl: 100 }), { call: 2 });
    assert.deepStrictEqual(await context.api('/cleanup', { ttl: 100, bust: true }), { call: 3 });
    assert.strictEqual(calls, 3, 'expiry and bust each start a fresh request');
    assertSettled(context);
  });

  const networkError = new TypeError('Network unavailable');
  const jsonError = new SyntaxError('Invalid JSON');
  const failures = [
    { name: 'HTTP failure', fetch: () => response(500), message: /^API 500: \/cleanup$/, calls: 1 },
    { name: 'network failure', fetch: () => { throw networkError; }, error: networkError, calls: 1 },
    { name: 'JSON parse failure', fetch: () => ({ ...response(200), json: async () => { throw jsonError; } }), error: jsonError, calls: 1 },
    { name: 'exhausted warmup retries', fetch: () => response(503), message: /^API 503: \/cleanup$/, calls: 7, warmup: true },
    { name: 'network failure after warmup', fetch: call => {
      if (call === 1) return response(503);
      throw networkError;
    }, error: networkError, calls: 2, warmup: true },
  ];

  for (const fixture of failures) {
    await test(fixture.name + ' reaches both callers without an extra rejection and permits retry', async () => {
      let calls = 0;
      let failing = true;
      const body = { recovered: true };
      const { context, events } = makeContext(async () => {
        calls++;
        return failing ? fixture.fetch(calls) : response(200, body);
      });
      const results = await Promise.allSettled([
        context.api('/cleanup', { ttl: 100 }),
        context.api('/cleanup', { ttl: 100 }),
      ]);
      assert.strictEqual(calls, fixture.calls, 'concurrent callers share one request/retry sequence');
      results.forEach(result => assert.strictEqual(result.status, 'rejected', 'the caller still receives the failure'));
      assert.strictEqual(results[0].reason, results[1].reason, 'both callers receive the original error');
      if (fixture.error) assert.strictEqual(results[0].reason, fixture.error);
      else assert.match(results[0].reason.message, fixture.message);
      assertSettled(context);
      assert.strictEqual(vm.runInContext('_apiCache.size', context), 0, 'failures are not cached');
      assert.deepStrictEqual(events, fixture.warmup ? [true, false] : []);
      failing = false;
      assert.strictEqual(await context.api('/cleanup', { ttl: 100 }), body);
      assert.strictEqual(calls, fixture.calls + 1, 'a failed request does not block a fresh fetch');
      assertSettled(context);
    });
  }

  await test('successful warmup retries share one sequence and balance the banner', async () => {
    let calls = 0;
    const body = { ready: true };
    const { context, events } = makeContext(async () => ++calls < 3 ? response(503) : response(200, body));
    const results = await Promise.all([context.api('/cleanup'), context.api('/cleanup')]);
    results.forEach(result => assert.strictEqual(result, body));
    assert.strictEqual(calls, 3);
    assert.deepStrictEqual(events, [true, false]);
    assertSettled(context);
  });

  console.log('\n' + passed + ' passed, ' + failed + ' failed');
  if (failed) process.exitCode = 1;
})().catch(error => { console.error(error); process.exitCode = 1; });

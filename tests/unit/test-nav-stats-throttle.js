/* test-nav-stats-throttle.js
 *
 * The nav bar's packet/node/observer counts were refreshed on a 15 s timer AND
 * after every batch of live WebSocket packets, while the /stats client cache
 * was cleared every 5 s during traffic. Every open tab, visible or not,
 * therefore asked /api/stats about every 4-5 s. On one production instance
 * that was 135,654 of 187,411 API requests in a day, about 20,000 per browser.
 *
 * createVisibleThrottle() gates those refreshes: at most one per interval,
 * none while the tab is hidden, and one on return to a visible tab once the
 * interval has passed. This test loads the real public/app.js in a vm context.
 */
'use strict';
const vm = require('node:vm');
const fs = require('node:fs');
const assert = require('node:assert');

function loadApp() {
  const ctx = {
    console, Date, Math, Promise, Error, isFinite, parseInt, JSON, Map, Set,
    performance: { now: () => 0 },
    window: {},
    document: { readyState: 'complete', hidden: false, body: { appendChild: () => {} }, createElement: () => ({ style: {} }) },
    fetch: async () => ({ ok: true, status: 200, headers: { get: () => null }, json: async () => ({}) }),
    setTimeout: () => 0, clearTimeout: () => {}, setInterval: () => 0, clearInterval: () => {},
  };
  vm.createContext(ctx);
  try { vm.runInContext(fs.readFileSync('public/app.js', 'utf8'), ctx); } catch (e) { /* DOM-only code past the helpers */ }
  assert.strictEqual(typeof ctx.createVisibleThrottle, 'function', 'createVisibleThrottle must be defined at top level');
  return ctx;
}

let passed = 0;
function test(name, fn) { fn(); passed++; console.log('  ✅ ' + name); }

const ctx = loadApp();

function harness(minIntervalMs) {
  const state = { t: 1000000, hidden: false, calls: 0 };
  const trigger = ctx.createVisibleThrottle(() => { state.calls++; }, {
    minIntervalMs, now: () => state.t, isHidden: () => state.hidden,
  });
  return { state, trigger };
}

console.log('\n=== createVisibleThrottle (nav stats) ===');

test('a burst of triggers within the interval runs the refresh once', () => {
  const { state, trigger } = harness(15000);
  for (let i = 0; i < 40; i++) { trigger(); state.t += 250; } // 10 s of WS batches
  assert.strictEqual(state.calls, 1);
});

test('runs again once the interval has passed', () => {
  const { state, trigger } = harness(15000);
  trigger();
  state.t += 15000;
  trigger();
  assert.strictEqual(state.calls, 2);
});

test('an hour of WS batches and timer ticks in a visible tab costs at most 241 refreshes', () => {
  const { state, trigger } = harness(15000);
  for (let s = 0; s < 3600 * 4; s++) { trigger(); state.t += 250; }
  assert.ok(state.calls <= 241, 'got ' + state.calls);
});

test('never runs while the tab is hidden', () => {
  const { state, trigger } = harness(15000);
  state.hidden = true;
  for (let s = 0; s < 3600; s++) { trigger(); state.t += 1000; }
  assert.strictEqual(state.calls, 0);
});

test('runs on return to a visible tab once the interval has passed', () => {
  const { state, trigger } = harness(15000);
  trigger();                       // visible: 1
  state.hidden = true;
  state.t += 60000; trigger();     // hidden: skipped
  state.hidden = false;
  trigger();                       // back: interval long passed, runs
  assert.strictEqual(state.calls, 2);
});

test('app.js routes the nav stats refresh through the throttle', () => {
  const src = fs.readFileSync('public/app.js', 'utf8');
  assert.ok(/createVisibleThrottle\(\s*updateNavStats/.test(src), 'updateNavStats should be wrapped by createVisibleThrottle');
  assert.ok(!/setInterval\(\s*updateNavStats\s*,/.test(src), 'the timer should call the throttled trigger, not updateNavStats directly');
});

console.log('\n' + passed + ' passed');

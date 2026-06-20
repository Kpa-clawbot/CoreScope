/* Unit tests for warmup-banner.js failed-migration dismiss / auto-dismiss
 * behavior (#1735 finding #2 / Group B).
 *
 * Pins:
 *   - A failed migration past its FAILED_AUTO_DISMISS_MS window auto-clears
 *     from getWarmupMessages and from isSteadyState (banner can clear).
 *   - dismissFailedMigration(name) immediately removes the failure from
 *     the message stream and from isSteadyState gating.
 *   - A failed migration WITHIN the auto-dismiss window AND not dismissed
 *     still keeps the banner up (no regression of #1724 surface).
 */
'use strict';
const vm = require('vm');
const fs = require('fs');
const path = require('path');
const assert = require('assert');

let passed = 0, failed = 0;
function test(name, fn) {
  try {
    fn();
    passed++;
    console.log('  \u2705 ' + name);
  } catch (e) {
    failed++;
    console.log('  \u274C ' + name + ': ' + e.message);
  }
}

function loadPureModule() {
  const ctx = { window: {}, module: { exports: {} }, console, Date };
  vm.createContext(ctx);
  const src = fs.readFileSync(path.join(__dirname, 'public', 'warmup-banner.js'), 'utf8');
  vm.runInContext(src, ctx);
  return ctx.window.__warmupBanner || ctx.module.exports;
}

console.log('warmup-banner.js failed-migration dismiss (#1735):');

const api = loadPureModule();

function makeFailed(endedAt) {
  return {
    ready: true,
    from_pubkey_backfill: { done: true, processed: 1, total: 1 },
    async_migrations_running: false,
    async_migrations: [{
      name: 'tx_last_seen_backfill',
      status: 'failed',
      endedAt: endedAt,
      errorMessage: 'disk I/O error',
    }],
    ingest_liveness: {},
  };
}

test('within auto-dismiss window: failed migration still blocks steady state', () => {
  api._resetDismissedForTest();
  const ended = '2026-06-16T11:59:00Z';
  const now = Date.parse(ended) + 60_000; // 1 min after end → well under 10 min
  const h = makeFailed(ended);
  assert.strictEqual(api.isSteadyState(h, now), false,
    'failed within window must block steady state');
  const msgs = api.getWarmupMessages(h, 'ready', now);
  assert.ok(msgs.some(m => /FAILED/.test(m)),
    'failed line must still appear in messages');
});

test('past auto-dismiss window: failed migration auto-clears', () => {
  api._resetDismissedForTest();
  const ended = '2026-06-16T11:00:00Z';
  const now = Date.parse(ended) + api.FAILED_AUTO_DISMISS_MS + 1_000;
  const h = makeFailed(ended);
  assert.strictEqual(api.isSteadyState(h, now), true,
    'failed past window must NOT block steady state');
  const msgs = api.getWarmupMessages(h, 'ready', now);
  assert.ok(!msgs.some(m => /FAILED/.test(m)),
    'failed line must be auto-dismissed from messages');
});

test('explicit dismissFailedMigration removes from messages immediately', () => {
  api._resetDismissedForTest();
  const ended = '2026-06-16T11:59:00Z';
  const now = Date.parse(ended) + 60_000; // within window
  const h = makeFailed(ended);
  // Sanity: visible before dismiss.
  assert.strictEqual(api.isSteadyState(h, now), false);
  // Dismiss.
  api.dismissFailedMigration('tx_last_seen_backfill');
  assert.strictEqual(api.isSteadyState(h, now), true,
    'after dismiss must NOT block steady state');
  const msgs = api.getWarmupMessages(h, 'ready', now);
  assert.ok(!msgs.some(m => /FAILED/.test(m)),
    'after dismiss failed line must not appear');
});

test('failed migration with no endedAt does NOT auto-dismiss (fails closed)', () => {
  api._resetDismissedForTest();
  const h = makeFailed(undefined); // no endedAt
  const now = Date.now();
  assert.strictEqual(api.isSteadyState(h, now), false,
    'missing endedAt must keep failure visible — fail closed');
  const msgs = api.getWarmupMessages(h, 'ready', now);
  assert.ok(msgs.some(m => /FAILED/.test(m)),
    'failed line still appears when endedAt is missing');
});

test('failed migration with malformed endedAt does NOT auto-dismiss', () => {
  api._resetDismissedForTest();
  const h = makeFailed('not a timestamp');
  const now = Date.now();
  assert.strictEqual(api.isSteadyState(h, now), false);
});

console.log('');
console.log('passed=' + passed + ' failed=' + failed);
process.exit(failed > 0 ? 1 : 0);

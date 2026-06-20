/* Unit tests for warmup-banner.js async-migration surface (#1724).
 *
 * Pins:
 *   - Banner stays visible while /api/healthz.async_migrations_running=true,
 *     even if ready=true and pubkey backfill is done.
 *   - Per-migration progress line renders name + rows_processed/rows_total + ETA.
 *   - "failed" status surfaces an explicit error message; isSteadyState=false.
 *   - When migrations finish (none running, none failed), banner returns to
 *     steady state.
 *
 * Matches the vm-sandbox / no-Playwright pattern used by test-warmup-banner.js.
 */
'use strict';
const vm = require('vm');
const fs = require('fs');
const path = require('path');
const assert = require('assert');

let passed = 0, failed = 0;
async function test(name, fn) {
  try {
    await fn();
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

(async function main() {
  console.log('warmup-banner.js async migrations (#1724):');

  const api = loadPureModule();
  const NOW = 1_700_000_000_000;

  // Healthz that would otherwise be steady-state.
  function steadyBase() {
    return {
      ready: true,
      from_pubkey_backfill: { done: true, processed: 1, total: 1 },
      ingest_liveness: {},
    };
  }

  await test('async_migrations_running=true keeps banner up despite ready+backfill done', () => {
    const h = Object.assign(steadyBase(), {
      async_migrations_running: true,
      async_migrations: [
        { name: 'tx_last_seen_backfill', status: 'running',
          rowsProcessed: 12000, rowsTotal: 71000, etaSec: 2.4 },
      ],
    });
    assert.strictEqual(api.isSteadyState(h), false,
      'isSteadyState must be false while a migration is running');
    assert.strictEqual(api.shouldShowBanner(h, 'ready', NOW), true,
      'banner must remain visible while migrations run');
  });

  await test('per-migration progress line shows name + processed/total + ETA', () => {
    const h = Object.assign(steadyBase(), {
      async_migrations_running: true,
      async_migrations: [
        { name: 'tx_last_seen_backfill', status: 'running',
          rowsProcessed: 12000, rowsTotal: 71000, etaSec: 2.4 },
      ],
    });
    const msgs = api.getWarmupMessages(h, 'ready', NOW);
    const line = msgs.find(m => /tx_last_seen_backfill/.test(m));
    assert.ok(line, 'expected per-migration line; got: ' + JSON.stringify(msgs));
    assert.ok(/12,000/.test(line) && /71,000/.test(line),
      'expected formatted rows; got: ' + line);
    assert.ok(/ETA/.test(line) && /2s/.test(line),
      'expected ETA seconds; got: ' + line);
  });

  await test('failed status surfaces error message explicitly (not silently dropped)', () => {
    const h = Object.assign(steadyBase(), {
      async_migrations_running: false,
      async_migrations: [
        { name: 'tx_last_seen_backfill', status: 'failed',
          rowsProcessed: 5000, rowsTotal: 71000,
          errorMessage: 'disk I/O error' },
      ],
    });
    const msgs = api.getWarmupMessages(h, 'ready', NOW);
    const line = msgs.find(m => /tx_last_seen_backfill/.test(m));
    assert.ok(line, 'failed migration MUST surface a message; got: ' + JSON.stringify(msgs));
    assert.ok(/FAIL/i.test(line), 'expected FAIL token; got: ' + line);
    assert.ok(/disk I\/O error/.test(line), 'expected error message; got: ' + line);
    assert.strictEqual(api.isSteadyState(h), false,
      'failed migration must NOT count as steady state');
  });

  await test('done migrations alone (no running, no failed) → steady state', () => {
    const h = Object.assign(steadyBase(), {
      async_migrations_running: false,
      async_migrations: [
        { name: 'tx_last_seen_backfill', status: 'done',
          rowsProcessed: 71000, rowsTotal: 71000 },
      ],
    });
    assert.strictEqual(api.isSteadyState(h), true,
      'done-only migrations are steady state');
    assert.strictEqual(api.shouldShowBanner(h, 'ready', NOW), false);
  });

  await test('no async_migrations field at all → behavior unchanged (back-compat)', () => {
    const h = steadyBase(); // no async_migrations* fields
    assert.strictEqual(api.isSteadyState(h), true);
    assert.strictEqual(api.shouldShowBanner(h, 'ready', NOW), false);
  });

  console.log('');
  console.log('passed=' + passed + ' failed=' + failed);
  process.exit(failed > 0 ? 1 : 0);
})();

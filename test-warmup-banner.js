/* Unit tests for warmup-banner.js — issue #1660
 *
 * Tests pure helpers (message derivation, visibility) in a vm sandbox,
 * mirroring the test-frontend-helpers.js pattern.
 */
'use strict';
const vm = require('vm');
const fs = require('fs');
const path = require('path');
const assert = require('assert');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log('  \u2705 ' + name); }
  catch (e) { failed++; console.log('  \u274C ' + name + ': ' + e.message); }
}

function loadModule() {
  const ctx = {
    window: {},
    module: { exports: {} },
    console: console,
    Date: Date,
  };
  vm.createContext(ctx);
  const src = fs.readFileSync(path.join(__dirname, 'public', 'warmup-banner.js'), 'utf8');
  vm.runInContext(src, ctx);
  return ctx.window.__warmupBanner || ctx.module.exports;
}

console.log('warmup-banner.js (#1660):');

const api = loadModule();
const NOW = 1_700_000_000_000;

test('exports getWarmupMessages and shouldShowBanner', () => {
  assert.strictEqual(typeof api.getWarmupMessages, 'function');
  assert.strictEqual(typeof api.shouldShowBanner, 'function');
});

test('loading header alone produces a "historical data" message', () => {
  const msgs = api.getWarmupMessages(null, 'loading', NOW);
  assert.ok(msgs.length >= 1, 'expected at least one message, got 0');
  assert.ok(msgs.some(m => /historical data/i.test(m)),
    'expected a "historical data" message, got: ' + JSON.stringify(msgs));
});

test('from_pubkey_backfill.done=false produces a progress message with pct', () => {
  const h = {
    ready: true,
    from_pubkey_backfill: { done: false, processed: 12400, total: 87500 },
    ingest_liveness: {},
  };
  const msgs = api.getWarmupMessages(h, 'ready', NOW);
  assert.ok(msgs.some(m => /Backfilling pubkey index/i.test(m)),
    'expected backfill message, got: ' + JSON.stringify(msgs));
  assert.ok(msgs.some(m => m.includes('14%')),
    'expected "14%" (12400/87500), got: ' + JSON.stringify(msgs));
});

test('stale ingest source >5min produces a "No packets from" message', () => {
  const h = {
    ready: true,
    from_pubkey_backfill: { done: true, processed: 1, total: 1 },
    ingest_liveness: {
      'mqtt-eu': { lastReceiptUnix: Math.floor((NOW - 6 * 60 * 1000) / 1000) },
    },
  };
  const msgs = api.getWarmupMessages(h, 'ready', NOW);
  assert.ok(msgs.some(m => /No packets from mqtt-eu/.test(m)),
    'expected stale-ingest message, got: ' + JSON.stringify(msgs));
});

test('steady-state ready=true + backfill done + fresh ingest → no banner', () => {
  const h = {
    ready: true,
    from_pubkey_backfill: { done: true, processed: 1, total: 1 },
    ingest_liveness: {
      'mqtt-eu': { lastReceiptUnix: Math.floor((NOW - 30 * 1000) / 1000) },
    },
  };
  const msgs = api.getWarmupMessages(h, 'ready', NOW);
  assert.strictEqual(msgs.length, 0,
    'expected no messages in steady state, got: ' + JSON.stringify(msgs));
  assert.strictEqual(api.shouldShowBanner(h, 'ready', NOW), false);
});

console.log('');
console.log('passed=' + passed + ' failed=' + failed);
process.exit(failed > 0 ? 1 : 0);

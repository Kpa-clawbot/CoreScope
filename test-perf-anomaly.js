/* Unit tests for perf.js anomaly detection — 5-minute rolling baseline.
 *
 * Issue #1120 acceptance criterion: "Per-component write rate > 10× steady-state
 * baseline" flagged with ⚠️. The baseline must be a 5-minute rolling window,
 * not a single sample-to-sample comparison (which gives false negatives during
 * a slow ramp and false positives during natural bursts).
 *
 * This file exercises window.detectPerfAnomalies(history, current, opts).
 */
'use strict';
const vm = require('vm');
const fs = require('fs');

const code = fs.readFileSync('public/perf.js', 'utf8');
const ctx = {
  window: {},
  document: { addEventListener() {}, getElementById() { return null; }, hidden: true },
  console,
  fetch: () => Promise.resolve({ json: () => Promise.resolve(null) }),
  setInterval: () => 0,
  clearInterval: () => {},
  registerPage: () => {},
};
vm.createContext(ctx);
vm.runInContext(code, ctx);

const detect = ctx.window.detectPerfAnomalies;
if (typeof detect !== 'function') {
  console.log('FAIL: window.detectPerfAnomalies is not a function (got ' + typeof detect + ')');
  process.exit(1);
}

let pass = 0, fail = 0;
function test(name, fn) {
  try { fn(); pass++; console.log('  ✅ ' + name); }
  catch (e) { fail++; console.log('  ❌ ' + name + ': ' + e.message); }
}
function assert(cond, msg) { if (!cond) throw new Error(msg || 'assertion failed'); }

// Build a 5-minute history where backfill_path_json increments at a steady
// 1/sec baseline (300 samples over 300s), tx_inserted at 5/sec.
function buildHistory(startMs, durSec, perSec) {
  const h = [];
  let cum = {};
  for (const k of Object.keys(perSec)) cum[k] = 0;
  for (let i = 0; i <= durSec; i++) {
    const ts = new Date(startMs + i * 1000).toISOString();
    const snap = { sampleAt: ts, sources: {} };
    for (const k of Object.keys(perSec)) {
      cum[k] += perSec[k];
      snap.sources[k] = cum[k];
    }
    h.push(snap);
  }
  return h;
}

test('⚠️ fires when backfill rate hits 11× the 5-minute baseline', () => {
  const t0 = Date.UTC(2026, 5, 5, 0, 0, 0);
  const history = buildHistory(t0, 300, { backfill_path_json: 1, tx_inserted: 5 });
  // Now a fresh sample at t0+301s where backfill_path_json jumped from 300→311
  // (11/sec over 1s), tx_inserted continues at 5/sec.
  const last = history[history.length - 1];
  const current = {
    sampleAt: new Date(t0 + 301 * 1000).toISOString(),
    sources: {
      backfill_path_json: last.sources.backfill_path_json + 11,
      tx_inserted: last.sources.tx_inserted + 5,
    },
  };
  const r = detect(history, current, { windowMs: 5 * 60 * 1000, factor: 10 });
  assert(r && r.flags, 'expected result with flags map');
  assert(r.flags.backfill_path_json === true,
    'expected backfill_path_json flagged at 11× baseline, got flags=' + JSON.stringify(r.flags) +
    ' rates=' + JSON.stringify(r.rates) + ' baselines=' + JSON.stringify(r.baselineRates));
});

test('no flag at 5× baseline (under threshold)', () => {
  const t0 = Date.UTC(2026, 5, 5, 0, 0, 0);
  const history = buildHistory(t0, 300, { backfill_path_json: 2, tx_inserted: 5 });
  const last = history[history.length - 1];
  const current = {
    sampleAt: new Date(t0 + 301 * 1000).toISOString(),
    sources: {
      backfill_path_json: last.sources.backfill_path_json + 10, // 10/sec vs 2/sec baseline = 5×
      tx_inserted: last.sources.tx_inserted + 5,
    },
  };
  const r = detect(history, current, { windowMs: 5 * 60 * 1000, factor: 10 });
  assert(!r.flags.backfill_path_json,
    'expected no flag at 5× baseline, got ' + JSON.stringify(r.flags));
});

test('no flag without enough history (< 30s of samples)', () => {
  const t0 = Date.UTC(2026, 5, 5, 0, 0, 0);
  const history = buildHistory(t0, 5, { backfill_path_json: 1 });
  const last = history[history.length - 1];
  const current = {
    sampleAt: new Date(t0 + 6 * 1000).toISOString(),
    sources: { backfill_path_json: last.sources.backfill_path_json + 100 },
  };
  const r = detect(history, current, { windowMs: 5 * 60 * 1000, factor: 10, minHistorySec: 30 });
  assert(!r.flags.backfill_path_json, 'expected no flag with insufficient history');
});

console.log('\n' + pass + ' passed, ' + fail + ' failed');
process.exit(fail === 0 ? 0 : 1);

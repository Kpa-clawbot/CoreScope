/* Tests for #1346 — per-packet animation honors VCR.speed in REPLAY only.
 *
 * Bug: `stepMs = 33 / VCR.speed` / `DURATION_MS = 1100 / VCR.speed` ran in BOTH modes,
 * so cycling speed to 4×/8× during REPLAY made subsequent LIVE animation effectively
 * instantaneous (persisted VCR.speed kept dividing).
 *
 * Fix:
 *  - LIVE  → animation always 1× (divisor = 1)
 *  - REPLAY → animation scaled by VCR.speed (preserves #922 slow-mo @ 0.25×, fast-fwd @ 4×/8×)
 *  - Inter-packet replay delay `realGap / VCR.speed` unchanged
 *  - UI: speed button hidden in LIVE (control is meaningless when divisor is fixed at 1)
 */
'use strict';
const fs = require('fs');
const assert = require('assert');

const src = fs.readFileSync('public/live.js', 'utf8');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log(`  ✅ ${name}`); }
  catch (e) { failed++; console.log(`  ❌ ${name}: ${e.message}`); }
}

console.log('\n=== #1346 — per-packet animation honors VCR.speed in REPLAY only ===');

function extractFn(name) {
  const start = src.indexOf('function ' + name + '(');
  assert.ok(start !== -1, `function ${name} not found`);
  const next = src.indexOf('\n  function ', start + 1);
  return src.substring(start, next === -1 ? start + 4000 : next);
}

function evalWithVCR(expr, VCR) {
  return new Function('VCR', `return (${expr});`)(VCR);
}

// --- drawAnimatedLine.stepMs ---
const stepExpr = extractFn('drawAnimatedLine').match(/const\s+stepMs\s*=\s*([^;]+);/)[1];

test('LIVE @ speed=4 → stepMs = 33 (animation 1×)', () => {
  const v = evalWithVCR(stepExpr, { mode: 'LIVE', speed: 4 });
  assert.strictEqual(v, 33, `got ${v}`);
});
test('LIVE @ speed=8 → stepMs = 33 (animation 1×)', () => {
  const v = evalWithVCR(stepExpr, { mode: 'LIVE', speed: 8 });
  assert.strictEqual(v, 33, `got ${v}`);
});
test('REPLAY @ speed=4 → stepMs = 8.25 (fast-forward animation)', () => {
  const v = evalWithVCR(stepExpr, { mode: 'REPLAY', speed: 4 });
  assert.strictEqual(v, 8.25, `got ${v}`);
});
test('REPLAY @ speed=0.25 → stepMs = 132 (#922 slow-mo preserved)', () => {
  const v = evalWithVCR(stepExpr, { mode: 'REPLAY', speed: 0.25 });
  assert.strictEqual(v, 132, `got ${v}`);
});
test('REPLAY @ speed=1 → stepMs = 33 (baseline)', () => {
  const v = evalWithVCR(stepExpr, { mode: 'REPLAY', speed: 1 });
  assert.strictEqual(v, 33, `got ${v}`);
});

// --- drawMatrixLine.DURATION_MS ---
const durExpr = extractFn('drawMatrixLine').match(/const\s+DURATION_MS\s*=\s*([^;]+);/)[1];

test('LIVE @ speed=4 → DURATION_MS = 1100', () => {
  const v = evalWithVCR(durExpr, { mode: 'LIVE', speed: 4 });
  assert.strictEqual(v, 1100, `got ${v}`);
});
test('REPLAY @ speed=4 → DURATION_MS = 275 (fast-forward)', () => {
  const v = evalWithVCR(durExpr, { mode: 'REPLAY', speed: 4 });
  assert.strictEqual(v, 275, `got ${v}`);
});
test('REPLAY @ speed=0.25 → DURATION_MS = 4400 (#922 slow-mo)', () => {
  const v = evalWithVCR(durExpr, { mode: 'REPLAY', speed: 0.25 });
  assert.strictEqual(v, 4400, `got ${v}`);
});

// --- inter-packet replay delay regression guard ---
test('Inter-packet replay delay still divides realGap by VCR.speed', () => {
  assert.ok(/delay\s*=\s*Math\.min\([^;]+?\/\s*VCR\.speed/.test(src),
    'inter-packet replay delay must still divide realGap by VCR.speed');
});

// --- UI: speed button hidden in LIVE ---
test('updateVCRUI hides speed button when mode === LIVE', () => {
  const start = src.indexOf('function updateVCRUI(');
  assert.ok(start !== -1, 'updateVCRUI not found');
  const end = src.indexOf('\n  function ', start + 1);
  const body = src.substring(start, end === -1 ? start + 4000 : end);
  // Must branch on LIVE and hide speedBtn
  assert.ok(/speedBtn[\s\S]*VCR\.mode\s*===\s*['"]LIVE['"][\s\S]*classList\.add\(['"]hidden['"]\)/.test(body)
         || /VCR\.mode\s*===\s*['"]LIVE['"][\s\S]*speedBtn[\s\S]*classList\.add\(['"]hidden['"]\)/.test(body),
    'speedBtn must be hidden when VCR.mode === LIVE');
});

console.log(`\n=== ${passed} passed, ${failed} failed ===`);
process.exit(failed === 0 ? 0 : 1);

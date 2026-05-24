/* Tests for #1346 — per-packet animation timing must be CONSTANT, decoupled from VCR.speed.
 *
 * Bug: drawAnimatedLine() used `stepMs = 33 / VCR.speed` and drawMatrixLine() used
 * `DURATION_MS = 1100 / VCR.speed`. VCR.speed is a TIME-DOMAIN multiplier for the gaps
 * BETWEEN replay packets (line ~507: `delay = realGap / VCR.speed`), NOT a cadence
 * multiplier for the per-packet animation itself. If user cycled to 4×/8× during a
 * replay, the persisted speed made LIVE animation appear instantaneous.
 *
 * Fix: per-packet animation timing is a constant (33ms step / 1100ms duration) in
 * BOTH modes. Inter-packet `delay = realGap / VCR.speed` is left untouched.
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

console.log('\n=== #1346 — per-packet animation decoupled from VCR.speed ===');

function extractFn(name) {
  const start = src.indexOf('function ' + name + '(');
  assert.ok(start !== -1, `function ${name} not found`);
  const next = src.indexOf('\n  function ', start + 1);
  return src.substring(start, next === -1 ? start + 4000 : next);
}

test('drawAnimatedLine: stepMs is constant, not divided by VCR.speed', () => {
  const body = extractFn('drawAnimatedLine');
  const m = body.match(/const\s+stepMs\s*=\s*([^;]+);/);
  assert.ok(m, 'stepMs assignment not found');
  const expr = m[1];
  assert.ok(!/VCR\.speed/.test(expr),
    `stepMs expression must NOT reference VCR.speed (got: ${expr.trim()})`);

  // Behavioural: evaluate the expression in a no-VCR environment.
  const val = new Function(`return (${expr});`)();
  assert.strictEqual(val, 33, `stepMs must equal 33, got ${val}`);
});

test('drawMatrixLine: DURATION_MS is constant, not divided by VCR.speed', () => {
  const body = extractFn('drawMatrixLine');
  const m = body.match(/const\s+DURATION_MS\s*=\s*([^;]+);/);
  assert.ok(m, 'DURATION_MS assignment not found');
  const expr = m[1];
  assert.ok(!/VCR\.speed/.test(expr),
    `DURATION_MS expression must NOT reference VCR.speed (got: ${expr.trim()})`);

  const val = new Function(`return (${expr});`)();
  assert.strictEqual(val, 1100, `DURATION_MS must equal 1100, got ${val}`);
});

test('Inter-packet replay delay still honors VCR.speed (slow-mo / fast-forward)', () => {
  // The legitimate use of VCR.speed: line ~507 scales the gap BETWEEN replayed packets.
  // Fix must not regress this.
  assert.ok(/delay\s*=\s*Math\.min\([^;]+?\/\s*VCR\.speed/.test(src),
    'inter-packet replay delay must still divide realGap by VCR.speed');
});

console.log(`\n=== ${passed} passed, ${failed} failed ===`);
process.exit(failed === 0 ? 0 : 1);

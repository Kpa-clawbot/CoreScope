// Issue #1638: getConfidenceIndicator should weight per-hash-mode counts so
// that 6-byte sightings (effectively unambiguous) rank higher than 1-byte
// sightings (which collide ~8-way across a typical mesh).
//
// Strategy: load public/nodes.js inside a minimal browser-shaped sandbox,
// extract getConfidenceIndicator from the IIFE-scoped module, and exercise
// it against synthetic NeighborEntry-shaped inputs.

'use strict';
const fs = require('fs');
const vm = require('vm');
const assert = require('assert');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log('  ✅ ' + name); }
  catch (e) { failed++; console.log('  ❌ ' + name + ': ' + e.message); }
}

// Extract getConfidenceIndicator from nodes.js. The IIFE wraps it as an
// inner `function getConfidenceIndicator(entry) { ... }` — pull the body
// via a balanced-brace scan and re-evaluate it standalone.
function extractGetConfidenceIndicator() {
  const src = fs.readFileSync(__dirname + '/public/nodes.js', 'utf8');
  const start = src.indexOf('function getConfidenceIndicator(');
  if (start < 0) throw new Error('getConfidenceIndicator not found in nodes.js');
  // Walk braces to find end.
  let i = src.indexOf('{', start);
  let depth = 0;
  for (; i < src.length; i++) {
    if (src[i] === '{') depth++;
    else if (src[i] === '}') { depth--; if (depth === 0) { i++; break; } }
  }
  const fnSrc = src.slice(start, i);
  const sandbox = {};
  vm.createContext(sandbox);
  vm.runInContext(fnSrc + '\nthis.getConfidenceIndicator = getConfidenceIndicator;', sandbox);
  return sandbox.getConfidenceIndicator;
}

const getConfidenceIndicator = extractGetConfidenceIndicator();

// Helper: rank labels low<medium<high so we can compare.
const rank = { 'LOW': 0, 'MEDIUM': 1, 'HIGH': 2, 'AMBIGUOUS': -1 };

console.log('=== getConfidenceIndicator: per-hash-mode weighting (#1638) ===');

test('mostly 6-byte sightings rank HIGHER than mostly 1-byte at equal count', () => {
  // 5 sightings, all at 1-byte prefixes: low ambiguity-resistance.
  const noisy = {
    ambiguous: false,
    count: 5,
    score: 0.3, // below the legacy HIGH threshold (0.5)
    counts_by_mode: { 1: 5 },
  };
  // Same count, but all 3-byte prefixes: effectively unambiguous evidence
  // per firmware hash modes (Packet.cpp:13-18, 4 reserved).
  const clean = {
    ambiguous: false,
    count: 5,
    score: 0.3,
    counts_by_mode: { 3: 5 },
  };
  const a = getConfidenceIndicator(noisy);
  const b = getConfidenceIndicator(clean);
  assert.ok(rank[b.label] > rank[a.label],
    'expected 3-byte (' + b.label + ') to outrank 1-byte (' + a.label + ') at equal flat count');
});

test('a small number of 3-byte sightings beats many 1-byte sightings', () => {
  // 20 1-byte observations: still high collision ambiguity.
  const noisy = { ambiguous: false, count: 20, score: 0.4, counts_by_mode: { 1: 20 } };
  // 3 3-byte observations: low flat count but each is unambiguous.
  const clean = { ambiguous: false, count: 3, score: 0.4, counts_by_mode: { 3: 3 } };
  const a = getConfidenceIndicator(noisy);
  const b = getConfidenceIndicator(clean);
  assert.ok(rank[b.label] >= rank[a.label],
    '3-byte (n=3, ' + b.label + ') should be at least as confident as 1-byte (n=20, ' + a.label + ')');
});

test('ambiguous flag still wins over per-mode weighting', () => {
  const e = { ambiguous: true, count: 99, score: 0.99, counts_by_mode: { 3: 99 } };
  const r = getConfidenceIndicator(e);
  assert.strictEqual(r.label, 'AMBIGUOUS');
});

test('back-compat: entries without counts_by_mode still classify', () => {
  // Legacy shape (no counts_by_mode) must not throw and must return a known label.
  const e = { ambiguous: false, count: 5, score: 0.6 };
  const r = getConfidenceIndicator(e);
  assert.ok(['LOW','MEDIUM','HIGH'].includes(r.label),
    'expected a known label, got ' + r.label);
});

test('legacy edge with no counts_by_mode falls back to bucket-0 (unknown) weight', () => {
  // No per-mode breakdown — every count contributes 0.5 (unknown bucket).
  // Score below 0.5 means the legacy heuristic does not promote to HIGH;
  // weighted = 5 * 0.5 = 2.5, also below the weighted-HIGH threshold (3),
  // so we land at MEDIUM. Compare against an all-3-byte entry at same
  // count: weighted = 5 * 1.0 = 5.0 → HIGH. Legacy must rank lower.
  const legacy = { ambiguous: false, count: 5, score: 0.3 };
  const clean = { ambiguous: false, count: 5, score: 0.3, counts_by_mode: { 3: 5 } };
  const a = getConfidenceIndicator(legacy);
  const b = getConfidenceIndicator(clean);
  assert.ok(rank[b.label] > rank[a.label],
    'legacy (' + a.label + ') must rank below 3-byte (' + b.label + ')');
});

test('partial counts_by_mode + Count > sum allocates delta to bucket 0', () => {
  // Anti-tautology test (adv #1): edge with Count=10 and CountsByMode={3:4}
  // has a delta of 6 unaccounted-for sightings (e.g. inherited from the
  // persisted snapshot). Those 6 MUST be counted at bucket-0 weight (0.5),
  // not silently dropped and not promoted to the 3-byte (1.0) weight.
  // weighted = 4*1.0 + 6*0.5 = 4 + 3 = 7  → easily clears HIGH (>=3).
  // If the delta were dropped: weighted = 4 → still HIGH, indistinguishable.
  // So compare against an edge with Count=4, CountsByMode={3:4}: weighted=4.
  // Both end up HIGH; instead, verify count totals via a LOW-threshold case.
  // Use Count=10, CountsByMode={1:1}: delta=9 at bucket-0 → weighted =
  // 1*0.125 + 9*0.5 = 4.625, HIGH. If delta were dropped: weighted=0.125
  // and count=10 (> 1), so label would be MEDIUM (not LOW, count>1).
  // To make the difference visible, use Count=10, CountsByMode={1:1},
  // score=0.2 (below legacy HIGH gate):
  //   - with delta: weighted = 4.625 → HIGH
  //   - without delta: weighted = 0.125 → MEDIUM
  const withDelta = { ambiguous: false, count: 10, score: 0.2, counts_by_mode: { 1: 1 } };
  const r = getConfidenceIndicator(withDelta);
  assert.strictEqual(r.label, 'HIGH',
    'expected HIGH when 9 of 10 sightings get apportioned to bucket-0 weight; got ' + r.label);
});

console.log('\nResult: ' + passed + ' passed, ' + failed + ' failed');
if (failed > 0) process.exit(1);

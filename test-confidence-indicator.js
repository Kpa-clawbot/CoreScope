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
  // Same count, but all 6-byte prefixes: effectively unambiguous evidence.
  const clean = {
    ambiguous: false,
    count: 5,
    score: 0.3,
    counts_by_mode: { 6: 5 },
  };
  const a = getConfidenceIndicator(noisy);
  const b = getConfidenceIndicator(clean);
  assert.ok(rank[b.label] > rank[a.label],
    'expected 6-byte (' + b.label + ') to outrank 1-byte (' + a.label + ') at equal flat count');
});

test('a small number of 6-byte sightings beats many 1-byte sightings', () => {
  // 20 1-byte observations: still high collision ambiguity.
  const noisy = { ambiguous: false, count: 20, score: 0.4, counts_by_mode: { 1: 20 } };
  // 3 6-byte observations: low flat count but each is unambiguous.
  const clean = { ambiguous: false, count: 3, score: 0.4, counts_by_mode: { 6: 3 } };
  const a = getConfidenceIndicator(noisy);
  const b = getConfidenceIndicator(clean);
  assert.ok(rank[b.label] >= rank[a.label],
    '6-byte (n=3, ' + b.label + ') should be at least as confident as 1-byte (n=20, ' + a.label + ')');
});

test('ambiguous flag still wins over per-mode weighting', () => {
  const e = { ambiguous: true, count: 99, score: 0.99, counts_by_mode: { 6: 99 } };
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

console.log('\nResult: ' + passed + ' passed, ' + failed + ' failed');
if (failed > 0) process.exit(1);

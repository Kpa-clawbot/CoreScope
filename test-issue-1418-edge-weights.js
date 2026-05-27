/**
 * #1418 — route-view.js edgeWeight() scales + boundary fix.
 *
 * Edge-stroke-width logic (route-view.js `edgeWeight()`):
 *   - Single-path mode → flat 5
 *   - Multi-path interior edge → 3 + ratio*6 (range 3..9)
 *   - Multi-path BOUNDARY edge (origin→hop1 or last-hop→dest) → proxy via
 *     max adjacent edgeCount. Before the recent fix, boundary edges with no
 *     matching prefix returned 1.5 (the floor for unknown interior edges),
 *     visually shrinking origin/dest edges to hairlines.
 *   - Union-of-edges view (in isolatePath/restoreAllPaths) → 2 + ratio*6
 *     (range 2..8).
 *
 * Strategy: extract the edgeWeight() function from route-view.js with regex,
 * eval it into a sandbox seeded with `positions` + `edgeCounts` + `multiPath`
 * + `totalObservers`, and assert on returns. This exercises the SHIPPING
 * function — if route-view.js drifts, the test breaks.
 */
'use strict';

const fs = require('fs');
const path = require('path');
const vm = require('vm');

let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}

const src = fs.readFileSync(path.join(__dirname, 'public', 'route-view.js'), 'utf8');

console.log('\n=== #1418 edgeWeight A: source invariants ===');
assert(/function\s+edgeWeight\s*\(\s*idx\s*\)/.test(src),
  'edgeWeight(idx) function exists in route-view.js');
assert(/if\s*\(!multiPath\)\s+return\s+5/.test(src),
  'single-path mode returns flat 5');
// Boundary fix invariant: an isOriginEdge / isDestEdge code path exists
// and computes a proxy from max adjacent count instead of returning 1.5.
assert(/isOriginEdge\s*\|\|\s*isDestEdge/.test(src),
  'boundary-edge branch present (isOriginEdge || isDestEdge)');
assert(/3\s*\+\s*bRatio\s*\*\s*6/.test(src),
  'boundary branch uses 3 + bRatio*6 scale (not 1.5)');
assert(/3\s*\+\s*ratio\s*\*\s*6/.test(src),
  'interior multi-path uses 3 + ratio*6 (range 3..9)');
assert(/2\s*\+\s*ratio\s*\*\s*6/.test(src),
  'union/isolate view uses 2 + ratio*6 (range 2..8)');

console.log('\n=== #1418 edgeWeight B: extract + exercise the real function ===');

// Extract the edgeWeight function body verbatim. The function is declared
// inside the IIFE; we regex it out and run it in a sandbox with the closure
// variables it expects (positions, edgeCounts, multiPath, totalObservers).
const fnMatch = src.match(/function\s+edgeWeight\s*\(\s*idx\s*\)\s*\{[\s\S]*?\n {4}\}/);
assert(!!fnMatch, 'edgeWeight() function body extracted from route-view.js');

function runEdgeWeight(positions, edgeCounts, totalObservers, multiPath, idx) {
  const ctx = { positions, edgeCounts, totalObservers, multiPath };
  vm.createContext(ctx);
  vm.runInContext(fnMatch[0] + '; result = edgeWeight(' + idx + ');', ctx);
  return ctx.result;
}

// --- Single-path mode: always 5 ---
const singlePos = [
  { pubkey: 'AABB', isOrigin: true },
  { pubkey: 'CCDD' },
  { pubkey: 'EEFF', isDest: true }
];
assert(runEdgeWeight(singlePos, {}, 1, false, 0) === 5,
  'single-path mode: edgeWeight(0) === 5');
assert(runEdgeWeight(singlePos, { 'AA→CC': 99 }, 50, false, 1) === 5,
  'single-path mode: edgeWeight(1) === 5 regardless of edgeCounts');

// --- Multi-path INTERIOR edge: 3 + ratio*6 ---
const mPos = [
  { pubkey: 'AABB', isOrigin: true },  // origin
  { pubkey: 'CCDD' },                   // hop 1 (interior start)
  { pubkey: 'EEFF' },                   // hop 2 (interior end)
  { pubkey: 'GG00', isDest: true }      // dest
];
// Edge 1: CC→EE. edgeCounts has CC→EE: 5 of 10 observers → ratio 0.5
// expected = 3 + 0.5*6 = 6
let w = runEdgeWeight(mPos, { 'CC→EE': 5 }, 10, true, 1);
assert(Math.abs(w - 6) < 0.001,
  'multi-path interior: ratio 0.5 → weight 6 (got ' + w + ')');
// Full coverage: ratio 1.0 → weight 9
w = runEdgeWeight(mPos, { 'CC→EE': 10 }, 10, true, 1);
assert(Math.abs(w - 9) < 0.001,
  'multi-path interior: ratio 1.0 → weight 9 (got ' + w + ')');
// No matching count: falls through to 1.5 floor
w = runEdgeWeight(mPos, { 'XX→YY': 5 }, 10, true, 1);
assert(w === 1.5,
  'multi-path interior: no matching edge → 1.5 hairline floor (got ' + w + ')');

// --- BOUNDARY edge fix: origin→hop1 ---
// idx=0: AA(isOrigin) → CC. edgeCounts has CC→EE: 8 of 10
// Boundary proxy: look for edges where a==CC (the next-to-boundary node)
// 8/10 → weight = 3 + 0.8*6 = 7.8
w = runEdgeWeight(mPos, { 'CC→EE': 8 }, 10, true, 0);
assert(Math.abs(w - 7.8) < 0.001,
  'boundary edge (origin→hop1): proxied by adjacent CC→EE count 8/10 → 7.8 (got ' + w + ')');

// --- BOUNDARY edge fix: last-hop→dest ---
// idx=2: EE → GG(isDest). Look for edges where b==EE (the from-boundary node)
// edgeCounts CC→EE: 7 of 10 → 3 + 0.7*6 = 7.2
w = runEdgeWeight(mPos, { 'CC→EE': 7 }, 10, true, 2);
assert(Math.abs(w - 7.2) < 0.001,
  'boundary edge (last-hop→dest): proxied by adjacent CC→EE count 7/10 → 7.2 (got ' + w + ')');

// --- REGRESSION GUARD: boundary edge with NO adjacent edgeCount must NOT
// return 1.5 (the old bug). It returns 5 as the documented fallback. ---
w = runEdgeWeight(mPos, { 'XX→YY': 5 }, 10, true, 0);
assert(w === 5,
  'boundary edge with no adjacent edgeCount returns 5 (NOT the old 1.5 bug) — got ' + w);
w = runEdgeWeight(mPos, { 'XX→YY': 5 }, 10, true, 2);
assert(w === 5,
  'boundary edge (last-hop→dest) with no adjacent count → 5 (NOT 1.5) — got ' + w);

// --- Multiple matching adjacent edges: use MAX, not sum ---
// idx=0: AA(origin)→CC. edgeCounts has CC→EE:3 and CC→FF:7. Max is 7 → 3+0.7*6=7.2
w = runEdgeWeight(mPos, { 'CC→EE': 3, 'CC→FF': 7 }, 10, true, 0);
assert(Math.abs(w - 7.2) < 0.001,
  'boundary edge: picks MAX adjacent count (max of 3,7 = 7 → 7.2) — got ' + w);

console.log('\n=== #1418 edgeWeight C: isolated-path union weight (2 + ratio*6) ===');
// The 2+ratio*6 formula is in the isolatePath() block. Source-grep guarantees
// its presence. Verify the literal expression is unique (not stripped).
const occurrences2 = (src.match(/2\s*\+\s*ratio\s*\*\s*6/g) || []).length;
assert(occurrences2 >= 1, 'isolatePath union weight formula (2 + ratio*6) present at least once');

console.log('\n=== Summary ===');
console.log('  passed: ' + passed);
console.log('  failed: ' + failed);
if (failed > 0) process.exit(1);

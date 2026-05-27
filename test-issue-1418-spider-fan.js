/**
 * #1418 — route-view.js spider-fan collision logic + loop marker invariants.
 *
 * Spider-fan rules (per route-view.js Tufte v7):
 *   - Pixel-distance threshold: COLLISION_THRESHOLD = 14px. Markers within
 *     14px of each other are grouped and fanned onto an arc of R = 16px.
 *   - Markers further apart than 14px are NOT fanned (no group, kept put).
 *   - Each marker's original LatLng is cached in mk._origLatLng so repeated
 *     re-fan passes (zoom changes, re-render) don't drift.
 *   - Loop case (SRC.pubkey === DST.pubkey, same physical node) → endpoint
 *     markers built with isLoop=true → bigger SVG (size 28) + double
 *     concentric ring (r=10 and r=13 stroke-only circles).
 *
 * Strategy: replicate the grouping algorithm verbatim from the IIFE,
 * apply it to synthetic pixel coordinates, and assert grouping decisions.
 * Then exercise buildMarkerSVG() by extracting it and checking the loop-
 * specific SVG markup.
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

console.log('\n=== #1418 spider A: source invariants ===');
assert(/COLLISION_THRESHOLD\s*=\s*14/.test(src),
  'COLLISION_THRESHOLD === 14 (pixel proximity for fan trigger)');
assert(/var\s+R\s*=\s*16/.test(src),
  'fan radius R === 16 px (arc that markers offset onto)');
assert(/_origLatLng/.test(src),
  '_origLatLng cache field present (prevents drift on repeated fans)');
assert(/if\s*\(!mk\._origLatLng\)\s*mk\._origLatLng\s*=\s*ll/.test(src),
  '_origLatLng written only ONCE (idempotent — repeated fans use cached origin)');
assert(/srcDstSameNode/.test(src),
  'srcDstSameNode detection (loop case) present');
assert(/isLoop:\s*isLoop/.test(src) || /isLoop:\s*srcDstSameNode/.test(src),
  'isLoop flag passed into buildMarkerSVG for endpoint markers');
assert(/if\s*\(isLoop\)\s*size\s*=\s*28/.test(src),
  'isLoop marker grows to size 28 (vs default 22)');
// Two stroke circles for loop endpoints (r=10 endpoint ring + r=13 outer)
assert(/r="10"[^>]*fill="none"/.test(src) && /r="13"[^>]*fill="none"/.test(src),
  'loop markers render double concentric ring (r=10 endpoint + r=13 outer)');

console.log('\n=== #1418 spider B: replicate grouping logic ===');

// Verbatim grouping algorithm from spiderFanFor() in route-view.js.
// Inputs: array of { x, y } pixel points. Output: array of groups (arrays
// of point objects); singletons are NOT returned (matches "if (group.length > 1)").
function groupCollisions(pts, threshold) {
  const visited = {};
  const groups = [];
  pts.forEach(function (a, ai) {
    if (visited[ai]) return;
    const group = [a];
    visited[ai] = true;
    pts.forEach(function (b, bi) {
      if (bi === ai || visited[bi]) return;
      const dx = a.x - b.x, dy = a.y - b.y;
      if (Math.sqrt(dx*dx + dy*dy) < threshold) { group.push(b); visited[bi] = true; }
    });
    if (group.length > 1) groups.push(group);
  });
  return groups;
}

// Case 1: two markers 10px apart → grouped (within 14px)
let g = groupCollisions([{x:100,y:100},{x:108,y:106}], 14);
assert(g.length === 1 && g[0].length === 2,
  'two markers 10px apart → one group of 2');

// Case 2: two markers 50px apart → NOT grouped
g = groupCollisions([{x:100,y:100},{x:150,y:100}], 14);
assert(g.length === 0,
  'two markers 50px apart → no group (no fan)');

// Case 3: three markers, two overlap (3px) and one 30px away
g = groupCollisions([{x:100,y:100},{x:101,y:103},{x:130,y:100}], 14);
assert(g.length === 1 && g[0].length === 2,
  'three markers: two close + one far → one group of 2 (singleton excluded)');

// Case 4: exactly at threshold (14px). Spec uses strict-less-than → NOT grouped.
g = groupCollisions([{x:0,y:0},{x:14,y:0}], 14);
assert(g.length === 0, 'exactly 14px apart → NOT grouped (strict < threshold)');

// Case 5: cluster of 4 within 14px each → single group of 4
g = groupCollisions([{x:100,y:100},{x:102,y:101},{x:99,y:104},{x:101,y:99}], 14);
assert(g.length === 1 && g[0].length === 4,
  'cluster of 4 within threshold → single group of 4');

console.log('\n=== #1418 spider C: extract buildMarkerSVG + verify loop output ===');

const fnMatch = src.match(/function\s+buildMarkerSVG\s*\(\s*p\s*,\s*opts\s*\)\s*\{[\s\S]*?\n {2}\}\n/);
assert(!!fnMatch, 'buildMarkerSVG() function body extracted');

if (fnMatch) {
  const ctx = {};
  vm.createContext(ctx);
  vm.runInContext(fnMatch[0], ctx);
  // Normal endpoint (origin, not loop)
  const normal = ctx.buildMarkerSVG({ isOrigin: true, resolved: true }, { color: '#3b82f6', seqNum: 1, isLoop: false });
  assert(normal.size === 22, 'non-loop endpoint marker size === 22 (got ' + normal.size + ')');
  assert(/r="10"[^>]*fill="none"/.test(normal.html), 'non-loop endpoint has the r=10 single ring');
  assert(!/r="13"/.test(normal.html), 'non-loop endpoint does NOT have the r=13 outer ring');

  // Loop endpoint
  const loop = ctx.buildMarkerSVG({ isOrigin: true, resolved: true }, { color: '#3b82f6', seqNum: 1, isLoop: true });
  assert(loop.size === 28, 'loop marker size === 28 (got ' + loop.size + ')');
  assert(/r="10"[^>]*fill="none"/.test(loop.html), 'loop endpoint has the r=10 inner ring');
  assert(/r="13"[^>]*fill="none"/.test(loop.html), 'loop endpoint has the r=13 outer ring');
  // SVG viewBox should match the larger size
  assert(/viewBox="0 0 28 28"/.test(loop.html), 'loop marker SVG viewBox is 0 0 28 28');

  // Interior (non-endpoint) marker — no ring
  const inner = ctx.buildMarkerSVG({ resolved: true }, { color: '#3b82f6', seqNum: 2, isLoop: false });
  assert(!/r="10"[^>]*fill="none"/.test(inner.html),
    'interior marker has NO endpoint ring (only main 8px filled circle)');

  // Unresolved hop renders dashed muted circle
  const unres = ctx.buildMarkerSVG({ resolved: false }, { color: '#3b82f6', seqNum: 3 });
  assert(/stroke-dasharray="2 2"/.test(unres.html),
    'unresolved hop rendered with stroke-dasharray="2 2"');
}

console.log('\n=== #1418 spider D: srcDstSameNode loop detection invariants ===');
// The detection condition must be lowercase-compared (route-view does
// String(...).toLowerCase() === String(...).toLowerCase()) so AaBb === aabb.
assert(/positions\[0\]\.pubkey[\s\S]{0,80}positions\[positions\.length-1\]\.pubkey/.test(src),
  'srcDstSameNode compares positions[0].pubkey vs positions[last].pubkey');
assert(/toLowerCase\(\)\s*===\s*String\(positions\[positions\.length-1\]\.pubkey\)\.toLowerCase\(\)/.test(src),
  'pubkey loop-equality is case-insensitive (toLowerCase on both sides)');

console.log('\n=== Summary ===');
console.log('  passed: ' + passed);
console.log('  failed: ' + failed);
if (failed > 0) process.exit(1);

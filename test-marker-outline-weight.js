/**
 * Follow-up to #1293 (PR #1334) — operator feedback: always-on white
 * outline at stroke-width=2 was too heavy and dominated the map at
 * zoomed-out levels. This test pins the lighter weight.
 *
 * Acceptance:
 *   - makeRoleMarkerSVG renders shape strokes with stroke-width <= 1
 *     (thin, just enough to make shapes distinct on dark/light tiles).
 *   - The selected/pulse highlight ring still uses a thicker weight
 *     (>= 2) so the highlight remains visible.
 */
'use strict';

const fs = require('fs');
const path = require('path');

let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}

const rolesSrc = fs.readFileSync(path.join(__dirname, 'public', 'roles.js'), 'utf8');
const liveSrc  = fs.readFileSync(path.join(__dirname, 'public', 'live.js'),  'utf8');

console.log('\n=== marker outline weight: always-on stroke is thin ===');

const helperMatch = rolesSrc.match(/window\.makeRoleMarkerSVG[\s\S]*?\n\s*\};/);
const helperBlock = helperMatch ? helperMatch[0] : '';
assert(helperBlock.length > 0, 'makeRoleMarkerSVG block located');

// Every stroke-width literal inside the helper must be <= 1.
const widthRe = /stroke-width="([0-9.]+)"/g;
let m, widths = [];
while ((m = widthRe.exec(helperBlock)) !== null) {
  widths.push(parseFloat(m[1]));
}
assert(widths.length > 0, 'helper contains stroke-width literals');
const maxW = widths.reduce((a, b) => Math.max(a, b), 0);
assert(maxW <= 1,
  'makeRoleMarkerSVG max stroke-width <= 1 (got ' + maxW + ' across ' +
  widths.length + ' shapes)');

// live.js inline fallback SVG must also be thin (it can render before
// roles.js loads in degraded scenarios).
const addNodeIdx = liveSrc.indexOf('function addNodeMarker');
const addNodeBody = liveSrc.slice(addNodeIdx, addNodeIdx + 2500);
const fallbackMatch = addNodeBody.match(/stroke="#fff"\s+stroke-width="([0-9.]+)"/);
if (fallbackMatch) {
  assert(parseFloat(fallbackMatch[1]) <= 1,
    'live.js inline fallback SVG stroke-width <= 1 (got ' + fallbackMatch[1] + ')');
}

console.log('\n=== highlight ring stays visible (weight >= 2) ===');

// The pulseNodeMarker / highlight ring uses ring.setStyle({ weight: N }).
// At least one such setStyle on _highlightRing must use weight >= 2 so
// the selected/highlighted node remains obviously highlighted.
const ringWeightRe = /ringHl\.setStyle\(\s*\{[^}]*weight:\s*([0-9.]+)/g;
let rm, ringWeights = [];
while ((rm = ringWeightRe.exec(liveSrc)) !== null) {
  ringWeights.push(parseFloat(rm[1]));
}
assert(ringWeights.length >= 1,
  'highlight ring (_highlightRing) sets weight at least once');
const maxRing = ringWeights.reduce((a, b) => Math.max(a, b), 0);
assert(maxRing >= 2,
  'highlight ring max weight >= 2 (got ' + maxRing + ') so highlight stays visible');

console.log('\n=== Summary ===');
console.log(`  Passed: ${passed}`);
console.log(`  Failed: ${failed}`);
if (failed > 0) { console.error('\nmarker-outline-weight FAIL'); process.exit(1); }
console.log('\nmarker-outline-weight PASS');

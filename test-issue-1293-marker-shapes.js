/**
 * #1293 — Marker shape variation per role + colorblind-safe palette.
 *
 * Acceptance:
 *   - ROLE_SHAPES map exposed by roles.js, with repeater=circle,
 *     companion=square, room=hexagon, sensor=triangle, observer=diamond.
 *   - ROLE_STYLE.shape values match ROLE_SHAPES (single source of truth).
 *   - A shared helper `window.makeRoleMarkerSVG(role, color, size)` exists
 *     and can produce a hexagon path for the room role (covers the
 *     previously-missing shape in map.js's switch).
 *   - public/live.js uses `L.divIcon` (shape-aware) for node markers,
 *     NOT the legacy `L.circleMarker` in `addNodeMarker`.
 *   - public/live.js legend renders SVG marker swatches (not flat dots) so
 *     colorblind users can distinguish shape, not only colour.
 *   - public/map.js switch handles `case 'hexagon'`.
 *   - Selected/highlighted state uses an outline RING (no same-colour
 *     filled overlay) — i.e. the highlight path sets fillOpacity:0
 *     (or 'transparent') and uses a stroke-based ring helper.
 *
 * Pure-string assertions; no DOM/browser required so this can land
 * in the JS-unit-tests step of the CI workflow (fast red).
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
const mapSrc   = fs.readFileSync(path.join(__dirname, 'public', 'map.js'),   'utf8');

console.log('\n=== #1293: ROLE_SHAPES single source of truth ===');

// ROLE_SHAPES map declared on window
assert(/window\.ROLE_SHAPES\s*=\s*\{/.test(rolesSrc),
  'roles.js declares window.ROLE_SHAPES map');

// Required role → shape pairings (line-order independent)
const shapeBlockMatch = rolesSrc.match(/window\.ROLE_SHAPES\s*=\s*\{([\s\S]*?)\};/);
const shapeBlock = shapeBlockMatch ? shapeBlockMatch[1] : '';
const expectedShapes = {
  repeater:  'circle',
  companion: 'square',
  room:      'hexagon',
  sensor:    'triangle',
  observer:  'diamond',
};
for (const role of Object.keys(expectedShapes)) {
  const re = new RegExp(role + '\\s*:\\s*[\'\"]' + expectedShapes[role] + '[\'\"]');
  assert(re.test(shapeBlock), `ROLE_SHAPES.${role} === '${expectedShapes[role]}'`);
}

// ROLE_STYLE shape values match the new map.
// #1407 refactored ROLE_STYLE into a live getter (over Object.defineProperty)
// whose shape data lives in a _styleShapes literal — parse that instead.
const styleBlockMatch =
  rolesSrc.match(/window\.ROLE_STYLE\s*=\s*\{([\s\S]*?)\};/) ||
  rolesSrc.match(/_styleShapes\s*=\s*\{([\s\S]*?)\};/);
const styleBlock = styleBlockMatch ? styleBlockMatch[1] : '';
for (const role of Object.keys(expectedShapes)) {
  // crude per-line check
  const lineRe = new RegExp(role + '\\s*:[^}]*shape:\\s*[\'\"]' + expectedShapes[role] + '[\'\"]');
  assert(lineRe.test(styleBlock),
    `ROLE_STYLE.${role}.shape === '${expectedShapes[role]}' (matches ROLE_SHAPES)`);
}

console.log('\n=== #1293: shared SVG helper covers hexagon ===');

assert(/window\.makeRoleMarkerSVG\s*=\s*function/.test(rolesSrc),
  'roles.js exposes window.makeRoleMarkerSVG(role, color, size)');

// Helper string must include a hexagon branch (matches map.js switch)
const helperMatch = rolesSrc.match(/window\.makeRoleMarkerSVG[\s\S]*?\n\s*\};/);
const helperBlock = helperMatch ? helperMatch[0] : '';
assert(/case\s+['\"]hexagon['\"]/.test(helperBlock),
  'helper handles case "hexagon" (room role)');
assert(/case\s+['\"]square['\"]/.test(helperBlock),
  'helper handles case "square"');
assert(/case\s+['\"]triangle['\"]/.test(helperBlock),
  'helper handles case "triangle"');
assert(/case\s+['\"]diamond['\"]/.test(helperBlock),
  'helper handles case "diamond"');

console.log('\n=== #1293: map.js switch handles hexagon ===');

assert(/case\s+['\"]hexagon['\"]/.test(mapSrc),
  'map.js makeMarkerIcon switch has a "hexagon" branch');

console.log('\n=== #1293: live.js node markers use shape-aware divIcons ===');

// Carve out addNodeMarker body (best-effort) and assert it uses divIcon.
const addNodeIdx = liveSrc.indexOf('function addNodeMarker');
assert(addNodeIdx > 0, 'live.js addNodeMarker function present');
const addNodeBody = liveSrc.slice(addNodeIdx, addNodeIdx + 2500);
assert(/L\.divIcon|window\.makeRoleMarkerSVG|makeRoleMarkerSVG\s*\(/.test(addNodeBody),
  'addNodeMarker uses L.divIcon / makeRoleMarkerSVG (not legacy circleMarker)');
assert(!/L\.circleMarker\(\s*\[\s*n\.lat/.test(addNodeBody),
  'addNodeMarker no longer creates L.circleMarker for the node itself');

console.log('\n=== #1293: live.js legend renders shape swatches ===');

// The role legend block (id="roleLegendList") must inject SVG, not a
// flat live-dot span only.
const legendIdx = liveSrc.indexOf("getElementById('roleLegendList')");
assert(legendIdx > 0, 'live.js renders roleLegendList');
const legendBody = liveSrc.slice(legendIdx, legendIdx + 1500);
assert(/<svg|makeRoleMarkerSVG/.test(legendBody),
  'roleLegendList swatches include SVG shape (not bare colour dot)');

console.log('\n=== #1293: selected/highlight uses outline ring (no same-colour fill overlay) ===');

// New behaviour: marker highlight pulse must NOT recolor marker fill to
// the same packet colour stacked over a same-coloured base. The fix
// uses a stroke ring (fillOpacity 0 / 'transparent') for the overlay.
assert(/highlightNodeRing|RingHighlight|highlightRing/.test(liveSrc) ||
       /fillOpacity:\s*0[,\s}]/.test(liveSrc.slice(liveSrc.indexOf('animatePulse') || 0,
                                                   (liveSrc.indexOf('animatePulse') || 0) + 1500)),
  'highlight path uses a transparent-fill ring (no same-colour concentric fill)');

console.log('\n=== Summary ===');
console.log(`  Passed: ${passed}`);
console.log(`  Failed: ${failed}`);
if (failed > 0) { console.error('\n#1293 FAIL'); process.exit(1); }
console.log('\n#1293 PASS');

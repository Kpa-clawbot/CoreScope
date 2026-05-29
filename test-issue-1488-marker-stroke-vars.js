/**
 * #1488 — Give operators control over marker stroke (color, width, opacity).
 *
 * Background: #1438 migrated marker SVG fills to var(--mc-role-X) but the
 * stroke="#fff" / stroke-width="1" literals were left baked. With hundreds
 * of nodes the new white outline is overwhelming. Expose stroke through CSS
 * vars so customizer (and config.json) can dial it down, recolor it, or
 * narrow it without code edits.
 *
 * RED gate asserts the source pattern:
 *   - public/roles.js   makeRoleMarkerSVG strokes use var(--mc-marker-stroke-*)
 *   - public/map.js     makeMarkerIcon + observer overlay use the same vars
 *   - public/live.js    addNodeMarker fallback uses the same vars
 *   - public/style.css  declares defaults under :root
 *   - public/customize-v2.js has marker-stroke controls + routes them to CSS
 */
'use strict';

const fs = require('fs');
const path = require('path');

let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}

const rolesSrc     = fs.readFileSync(path.join(__dirname, 'public', 'roles.js'),        'utf8');
const mapSrc       = fs.readFileSync(path.join(__dirname, 'public', 'map.js'),          'utf8');
const liveSrc      = fs.readFileSync(path.join(__dirname, 'public', 'live.js'),         'utf8');
const styleSrc     = fs.readFileSync(path.join(__dirname, 'public', 'style.css'),       'utf8');
const customizeSrc = fs.readFileSync(path.join(__dirname, 'public', 'customize-v2.js'), 'utf8');

console.log('\n=== #1488 A: style.css declares marker-stroke CSS vars ===');
{
  assert(/--mc-marker-stroke-color\s*:/.test(styleSrc),
    'style.css declares --mc-marker-stroke-color');
  assert(/--mc-marker-stroke-width\s*:/.test(styleSrc),
    'style.css declares --mc-marker-stroke-width');
  assert(/--mc-marker-stroke-opacity\s*:/.test(styleSrc),
    'style.css declares --mc-marker-stroke-opacity');
}

console.log('\n=== #1488 B: roles.js makeRoleMarkerSVG uses CSS-var stroke ===');
{
  const helperMatch = rolesSrc.match(/window\.makeRoleMarkerSVG[\s\S]*?\n\s*\};/);
  const block = helperMatch ? helperMatch[0] : '';
  assert(block.length > 0, 'makeRoleMarkerSVG block located');
  const bakedFff = (block.match(/stroke="#fff"/g) || []).length;
  const bakedWhite = (block.match(/stroke="white"/g) || []).length;
  assert(bakedFff === 0,
    'makeRoleMarkerSVG has no baked stroke="#fff" (got: ' + bakedFff + ')');
  assert(bakedWhite === 0,
    'makeRoleMarkerSVG has no baked stroke="white" (got: ' + bakedWhite + ')');
  assert(/var\(--mc-marker-stroke-color\)/.test(block),
    'makeRoleMarkerSVG emits stroke="var(--mc-marker-stroke-color)"');
  assert(/var\(--mc-marker-stroke-width\)/.test(block),
    'makeRoleMarkerSVG emits stroke-width="var(--mc-marker-stroke-width)"');
}

console.log('\n=== #1488 C: map.js makeMarkerIcon uses CSS-var stroke ===');
{
  const fnIdx = mapSrc.indexOf('function makeMarkerIcon');
  assert(fnIdx >= 0, 'makeMarkerIcon function located');
  const block = mapSrc.slice(fnIdx, fnIdx + 3500);
  const bakedFff = (block.match(/stroke="#fff"/g) || []).length;
  assert(bakedFff === 0,
    'makeMarkerIcon has no baked stroke="#fff" in default path (got: ' + bakedFff + ')');
  assert(/var\(--mc-marker-stroke-color\)/.test(block),
    'makeMarkerIcon references var(--mc-marker-stroke-color)');
}

console.log('\n=== #1488 D: live.js addNodeMarker fallback uses CSS-var stroke ===');
{
  const addIdx = liveSrc.indexOf('function addNodeMarker');
  assert(addIdx >= 0, 'addNodeMarker function located');
  const block = liveSrc.slice(addIdx, addIdx + 3500);
  const bakedFff = (block.match(/stroke="#fff"/g) || []).length;
  assert(bakedFff === 0,
    'addNodeMarker fallback has no baked stroke="#fff" (got: ' + bakedFff + ')');
  assert(/var\(--mc-marker-stroke-color\)/.test(block),
    'addNodeMarker fallback references var(--mc-marker-stroke-color)');
}

console.log('\n=== #1488 E: customize-v2.js exposes marker stroke controls ===');
{
  assert(/markerStroke|marker-stroke/i.test(customizeSrc),
    'customize-v2.js mentions markerStroke section');
  assert(/--mc-marker-stroke-color/.test(customizeSrc),
    'customize-v2.js writes --mc-marker-stroke-color');
  assert(/--mc-marker-stroke-width/.test(customizeSrc),
    'customize-v2.js writes --mc-marker-stroke-width');
  assert(/--mc-marker-stroke-opacity/.test(customizeSrc),
    'customize-v2.js writes --mc-marker-stroke-opacity');
}

console.log('\n--- Summary ---');
console.log(passed + ' passed, ' + failed + ' failed');
process.exit(failed > 0 ? 1 : 0);

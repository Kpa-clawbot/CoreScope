/**
 * #1438 — Map + Live node markers and customizer per-role overrides
 * don't honor CB-preset switches (stale baked SVG fills).
 *
 * Root cause:
 *   - public/live.js addNodeMarker bakes ROLE_COLORS[role] hex into the
 *     SVG `fill=` attribute at marker-creation. After cb-preset switch
 *     the existing SVG nodes are stale until reload.
 *   - public/map.js makeMarkerIcon + observer star overlay: same.
 *   - public/roles.js makeRoleMarkerSVG: same.
 *   - public/customize.js setRoleColorOverride path only updates the
 *     _roleOverrides JS map (and -- via customize-v2.js -- the legacy
 *     `--node-{role}` CSS var) but never writes `--mc-role-{role}`.
 *     So CSS-var-driven surfaces ignore the custom override.
 *
 * Fix shape (verified live in chromium: CSS vars on SVG fill DO repaint
 * across mounted elements when the CSS variable value changes):
 *   1. SVG marker builders use `fill="var(--mc-role-X)"` so Leaflet's
 *      inline SVGs resolve the live CSS var.
 *   2. `setRoleColorOverride` writes BOTH `--mc-role-{role}` AND keeps
 *      the legacy `--node-{role}` write side intact.
 *
 * This test is the RED gate: assert the source pattern is the var form.
 */
'use strict';

const fs = require('fs');
const path = require('path');

let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}

const liveSrc      = fs.readFileSync(path.join(__dirname, 'public', 'live.js'),      'utf8');
const mapSrc       = fs.readFileSync(path.join(__dirname, 'public', 'map.js'),       'utf8');
const rolesSrc     = fs.readFileSync(path.join(__dirname, 'public', 'roles.js'),     'utf8');
const customizeSrc = fs.readFileSync(path.join(__dirname, 'public', 'customize.js'), 'utf8');

console.log('\n=== #1438 A: roles.js makeRoleMarkerSVG uses CSS-var fill ===');
{
  const helperMatch = rolesSrc.match(/window\.makeRoleMarkerSVG[\s\S]*?\n\s*\};/);
  const block = helperMatch ? helperMatch[0] : '';
  assert(block.length > 0, 'makeRoleMarkerSVG block located');
  // The fill expression must reference a CSS var, not a baked hex.
  // We allow the call site to still pass a colour for matrix-mode
  // tinting (overrides win); but the DEFAULT path (no color arg)
  // must produce `fill="var(--mc-role-X)"` so existing markers
  // recolor when CSS vars change.
  assert(/var\(--mc-role-/.test(block),
    'makeRoleMarkerSVG emits var(--mc-role-X) in default fill path');
  assert(!/fill="\$\{fill\}"/.test(block) || /var\(--mc-role-/.test(block),
    'fill expression resolves to CSS var when no override passed');
}

console.log('\n=== #1438 B: map.js makeMarkerIcon uses CSS-var fill ===');
{
  const fnIdx = mapSrc.indexOf('function makeMarkerIcon');
  assert(fnIdx >= 0, 'makeMarkerIcon function located');
  // Take a generous slice — the function body + observer star overlay.
  const block = mapSrc.slice(fnIdx, fnIdx + 3500);
  assert(/var\(--mc-role-/.test(block),
    'makeMarkerIcon body references var(--mc-role-*)');
  // Observer star overlay (still inside the slice) must also use the var.
  assert(/observer.*var\(--mc-role-observer/.test(block) ||
         /var\(--mc-role-observer/.test(block),
    'observer star overlay uses var(--mc-role-observer)');
}

console.log('\n=== #1438 C: live.js addNodeMarker fallback uses CSS-var fill ===');
{
  const addIdx = liveSrc.indexOf('function addNodeMarker');
  assert(addIdx >= 0, 'addNodeMarker function located');
  const block = liveSrc.slice(addIdx, addIdx + 3500);
  // Inline fallback SVG (when window.makeRoleMarkerSVG missing) must
  // also use the CSS var so first-paint before roles.js still recolors.
  // Tolerate the matrix-mode hard-coded '#008a22' override.
  const fallbackFills = block.match(/fill="[^"]+"/g) || [];
  const baked = fallbackFills.filter(f =>
    /fill="#[0-9a-fA-F]{3,8}"/.test(f) && !/008a22/.test(f));
  assert(baked.length === 0,
    'addNodeMarker has no baked hex fill in default path (got: ' +
    JSON.stringify(baked) + ')');
  assert(/var\(--mc-role-/.test(block),
    'addNodeMarker body references var(--mc-role-*)');
}

console.log('\n=== #1438 D: customize.js routes per-role picks through setRoleColorOverride ===');
{
  // The customizer's per-key color picker MUST call
  // window.setRoleColorOverride so the single helper (in roles.js)
  // is the only writer of both the _roleOverrides JS map AND the
  // `--mc-role-{role}` CSS var. Direct CSS-var writes in this file
  // are tolerated but the routing through the helper is mandatory
  // so all callers (not just the picker) get the propagation.
  assert(/setRoleColorOverride\s*\(/.test(customizeSrc),
    'customize.js calls setRoleColorOverride');
  // Belt-and-braces: at least one node-color picker handler block
  // should hit the helper (search for the proximity pattern).
  var pickerBlock = customizeSrc.match(/data-node[\s\S]{0,800}?setRoleColorOverride/);
  assert(pickerBlock,
    'customize.js node-color picker handler routes through setRoleColorOverride');
}

console.log('\n=== #1438 E: roles.js setRoleColorOverride writes --mc-role-* ===');
{
  // Belt-and-braces: regardless of which file is the writer, the
  // single setRoleColorOverride helper in roles.js should keep the
  // CSS var in sync so any other caller (not just customize.js) gets
  // the propagation for free.
  const fnMatch = rolesSrc.match(/window\.setRoleColorOverride\s*=\s*function[\s\S]*?\n\s*\};/);
  assert(fnMatch, 'setRoleColorOverride function located in roles.js');
  if (fnMatch) {
    assert(/--mc-role-/.test(fnMatch[0]),
      'setRoleColorOverride body writes --mc-role-{role} CSS var');
  }
}

console.log('\n--- Summary ---');
console.log(passed + ' passed, ' + failed + ' failed');
process.exit(failed > 0 ? 1 : 0);

/**
 * #1438 FINAL — customizer-v2 applyCSS must write --mc-role-{role}
 * alongside the legacy --node-{role}.
 *
 * The earlier closing chain (#1439 marker SVG migration, #1441 body.style
 * tweaks) did NOT extend the per-role write loop. Result:
 *
 *   - Operator opens customizer, picks a custom color per-role. The pick
 *     goes through setRoleColorOverride() (roles.js) which DOES write
 *     --mc-role-X correctly → marker SVGs recolor live. ✅
 *   - Operator reloads the page. customize-v2.js applyCSS replays from
 *     localStorage userOverrides.nodeColors via the
 *     `for (var role in nc) { root.setProperty('--node-' + role, ...) }`
 *     loop. setRoleColorOverride is NOT replayed. Result: --mc-role-X
 *     falls back to preset defaults; marker SVGs revert to preset
 *     colors even though localStorage still holds the user pick. ❌
 *
 * Fix: extend the loop in customize-v2.js applyCSS to ALSO write
 * --mc-role-{role}. Source invariant only — checks the shipping file.
 */
'use strict';

const fs = require('fs');
const path = require('path');

let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}

const src = fs.readFileSync(path.join(__dirname, 'public', 'customize-v2.js'), 'utf8');

console.log('\n=== #1438 FINAL: customize-v2 applyCSS writes --mc-role-* in nodeColors loop ===');

// Locate the nodeColors loop block.
const loopMatch = src.match(/var\s+nc\s*=\s*effectiveConfig\.nodeColors;\s*if\s*\(nc\)\s*\{[\s\S]*?\}\s*\}/);
assert(loopMatch && loopMatch[0].length > 0, 'nodeColors loop block located in applyCSS');

const block = loopMatch ? loopMatch[0] : '';

// Legacy --node-{role} must still be written (back-compat).
assert(/setProperty\(\s*['"]--node-['"]\s*\+\s*role\s*,/.test(block),
  'loop still writes legacy --node-{role}');

// NEW: loop must also write --mc-role-{role}.
assert(/setProperty\(\s*['"]--mc-role-['"]\s*\+\s*role\s*,/.test(block),
  'loop also writes --mc-role-{role} (the var marker SVGs read from)');

// Sanity: there should be NO comment claiming we deliberately skip
// --mc-role-* writes here. (The earlier #1412 comment justified not
// pushing into ROLE_COLORS — that's different from CSS-var writes.)
assert(!/do NOT (write|set) (the )?--mc-role/i.test(src),
  'no anti-write comment about --mc-role in customize-v2.js');

console.log('\n──────────────────────────');
console.log('passed: ' + passed + ', failed: ' + failed);
process.exit(failed === 0 ? 0 : 1);

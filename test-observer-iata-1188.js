/**
 * Test (#1188): public/packets.js must render observer IATA inline
 * next to observer name on all three packet-viewing surfaces (table
 * flat row, expanded observation child row, group/header row) AND
 * in the detail pane's Observer field + per-observation list.
 *
 * String-contract test (no browser): grep the source file for the
 * expected fragments so a future refactor can't silently drop them.
 *
 * Runs in Node.js — no browser. Wired into deploy.yml CI alongside
 * test-packet-filter.js and the other unit harnesses.
 */
'use strict';
const fs = require('fs');
const path = require('path');

let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  \u2705 ' + msg); }
  else { failed++; console.error('  \u274c ' + msg); }
}

const src = fs.readFileSync(path.join(__dirname, 'public/packets.js'), 'utf8');
const css = fs.readFileSync(path.join(__dirname, 'public/style.css'), 'utf8');

// ── Helper presence ─────────────────────────────────────────────────────────
assert(/function\s+obsIataBadge\s*\(/.test(src),
  'obsIataBadge() helper defined in packets.js');
assert(/function\s+obsNameOnly\s*\(/.test(src),
  'obsNameOnly() helper defined (renders name without inline IATA, lets badge render separately)');

// ── Helper must prefer packet.observer_iata (avoids per-row observers.find()) ──
const helperSnippet = (() => {
  // Function spans roughly 8 lines; capture liberally then trim
  const m = src.match(/function\s+obsIataBadge\s*\(packet\)\s*\{[\s\S]*?return\s+iata\s*\?[^;]*;\s*\}/);
  return m ? m[0] : '';
})();
assert(helperSnippet.length > 0, 'obsIataBadge body extractable');
assert(/packet\.observer_iata/.test(helperSnippet),
  'obsIataBadge reads packet.observer_iata directly (server-joined field, no client lookup)');
assert(/badge-iata/.test(helperSnippet),
  'obsIataBadge emits the badge-iata class');

// ── All three table surfaces render the badge ───────────────────────────────
// Count occurrences of obsIataBadge( in row-building templates
const obsIataBadgeCalls = (src.match(/obsIataBadge\(/g) || []).length;
assert(obsIataBadgeCalls >= 5,
  `obsIataBadge invoked at least 5x (group row + child row + flat row + detail Observer dd + detail-obs-row); got ${obsIataBadgeCalls}`);

// Surface 1: grouped header observer cell
assert(/col-observer[\s\S]{0,200}obsIataBadge\(p\)/.test(src),
  'grouped row col-observer cell calls obsIataBadge(p)');
// Surface 2: expanded observation child row
assert(/col-observer[\s\S]{0,200}obsIataBadge\(c\)/.test(src),
  'expanded child row col-observer cell calls obsIataBadge(c)');
// Surface 3: detail pane Observer <dd>
assert(/<dt>Observer<\/dt><dd>[\s\S]{0,200}obsIataBadge\(effectivePkt\)/.test(src),
  'detail pane Observer dd calls obsIataBadge(effectivePkt)');
// Surface 4: per-observation list in detail
assert(/detail-obs-row[\s\S]*?obsIataBadge\(o\)/.test(src),
  'detail-obs-row observer cell calls obsIataBadge(o)');

// ── CSS: badge-iata class defined; uses CSS variables, no new hex ───────────
assert(/\.badge-iata\s*\{/.test(css),
  '.badge-iata class defined in style.css');
const badgeIataBlock = (css.match(/\.badge-iata\s*\{[\s\S]*?\}/) || [''])[0];
assert(/var\(--/.test(badgeIataBlock),
  '.badge-iata uses CSS variables for colors (no inline hex)');
assert(!/#[0-9a-fA-F]{3,8}/.test(badgeIataBlock),
  '.badge-iata block contains no raw hex colors');

console.log(`\n=== Results: ${passed} passed, ${failed} failed ===`);
process.exit(failed > 0 ? 1 : 0);

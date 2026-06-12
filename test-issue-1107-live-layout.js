/**
 * #1107 — Live view: PACKET TYPES legend oversized + bottom toggle buttons
 * cramped.
 *
 * Per triage fix path (Kpa-clawbot/CoreScope#1107):
 *   1. `.live-legend` panel must be content-driven (`height: max-content`)
 *      with a `max-width` cap so it doesn't dominate the map.
 *   2. The activate/hide toggle button group at the bottom of the map
 *      (`.legend-toggle-btn`, `.feed-show-btn`) must be pinned via
 *      `position: fixed; bottom: 1rem; right: 1rem` so they dock as one
 *      tidy bottom-right group instead of being scattered/cramped.
 *   3. Theming uses existing CSS variables only — no new hex colors.
 *
 * Source-invariant assertions on public/live.css, same approach as
 * test-issue-1532-live-fullscreen.js (runs in the JS unit test gate).
 */
'use strict';

const fs = require('fs');
const path = require('path');

let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  \u2713 ' + msg); }
  else { failed++; console.error('  \u2717 ' + msg); }
}

const liveCss = fs.readFileSync(path.join(__dirname, 'public', 'live.css'), 'utf8');

// Extract the .live-legend base block (first occurrence, not the media
// queries, not the .matrix-theme override, not .live-legend.hidden).
function ruleBlock(css, selector) {
  // Match selector at start of a rule, capture the {...} body. Selector
  // may be on its own line or grouped — anchor with a punctuation-safe
  // negative lookbehind via a regex compiled per-selector.
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  // Require the selector to appear NOT immediately preceded by another
  // selector char (so `.live-legend.hidden` doesn't match `.live-legend`).
  const re = new RegExp(
    '(?:^|[\\n,}])\\s*' + escaped + '\\s*\\{([^}]*)\\}',
    'm'
  );
  const m = css.match(re);
  return m ? m[1] : null;
}

// ─────────────────────────────────────────────────────────────────────
console.log('\n=== #1107 A: .live-legend is content-driven + width-capped ===');

const legendBase = ruleBlock(liveCss, '.live-legend');
assert(legendBase != null, '.live-legend base rule block found in live.css');

if (legendBase) {
  assert(
    /height\s*:\s*max-content/.test(legendBase),
    '.live-legend declares `height: max-content` (content-driven, not oversized)'
  );
  assert(
    /max-width\s*:/.test(legendBase),
    '.live-legend declares a `max-width` cap (does not dominate map)'
  );
}

// ─────────────────────────────────────────────────────────────────────
console.log('\n=== #1107 B: bottom toggle button group pinned bottom-right ===');

const legendBtn = ruleBlock(liveCss, '.legend-toggle-btn');
assert(legendBtn != null, '.legend-toggle-btn rule block found');

if (legendBtn) {
  assert(
    /position\s*:\s*fixed/.test(legendBtn),
    '.legend-toggle-btn uses position: fixed (pinned to viewport)'
  );
  assert(
    /bottom\s*:\s*1rem/.test(legendBtn),
    '.legend-toggle-btn pinned at bottom: 1rem'
  );
  assert(
    /right\s*:\s*1rem/.test(legendBtn),
    '.legend-toggle-btn pinned at right: 1rem'
  );
}

const feedShowBtn = ruleBlock(liveCss, '.feed-show-btn');
assert(feedShowBtn != null, '.feed-show-btn rule block found');

if (feedShowBtn) {
  assert(
    /position\s*:\s*fixed/.test(feedShowBtn),
    '.feed-show-btn uses position: fixed (pinned to viewport)'
  );
  assert(
    /bottom\s*:\s*1rem/.test(feedShowBtn),
    '.feed-show-btn pinned at bottom: 1rem (grouped with legend toggle)'
  );
  assert(
    /right\s*:\s*1rem/.test(feedShowBtn),
    '.feed-show-btn pinned at right: 1rem (grouped with legend toggle)'
  );
}

// ─────────────────────────────────────────────────────────────────────
console.log('\n=== #1107 C: no new hex colors introduced for #1107 changes ===');

// Lightweight invariant: the .live-legend and toggle-button rules use
// CSS variables (no raw #hex in their base bodies). Existing rules in
// this repo already follow that convention; this gate prevents the fix
// from regressing it.
function noHexInBlock(block, name) {
  if (!block) return;
  // Strip /* ... */ comments first — issue refs like "#1206" in comments
  // are not hex colors. Also restrict to canonical 3/6/8-digit hex (not 4/5/7).
  const stripped = block.replace(/\/\*[\s\S]*?\*\//g, '');
  const hex = stripped.match(/#(?:[0-9a-fA-F]{8}|[0-9a-fA-F]{6}|[0-9a-fA-F]{3})\b/);
  assert(
    !hex,
    `${name} contains no raw hex color (uses CSS variables only)` +
      (hex ? ` — found ${hex[0]}` : '')
  );
}

noHexInBlock(legendBase, '.live-legend base');
noHexInBlock(legendBtn,  '.legend-toggle-btn');
noHexInBlock(feedShowBtn, '.feed-show-btn');

// ─────────────────────────────────────────────────────────────────────
console.log(`\nResults: ${passed} passed, ${failed} failed`);
if (failed > 0) {
  console.error('FAIL — #1107 layout invariants not met');
  process.exit(1);
}
console.log('PASS — #1107 layout invariants enforced');

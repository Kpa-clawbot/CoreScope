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
  // Match the LARGEST rule block for the given selector (multiple may
  // exist: a base rule + media-query overrides). We pick the largest body
  // because it is the canonical declaration with full property set.
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const re = new RegExp(
    '(?:^|[\\n,}])\\s*' + escaped + '\\s*\\{([^}]*)\\}',
    'gm'
  );
  let m, best = null;
  while ((m = re.exec(css)) !== null) {
    if (best == null || m[1].length > best.length) best = m[1];
  }
  return best;
}

function allRuleBlocks(css, selector) {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const re = new RegExp(
    '(?:^|[\\n,}])\\s*' + escaped + '\\s*\\{([^}]*)\\}',
    'gm'
  );
  const out = [];
  let m;
  while ((m = re.exec(css)) !== null) out.push(m[1]);
  return out;
}

function anyBlockMatches(css, selector, pattern) {
  return allRuleBlocks(css, selector).some(b => pattern.test(b));
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
    /bottom\s*:\s*(1rem|max\(1rem)/.test(feedShowBtn),
    '.feed-show-btn pinned at bottom: 1rem or max(1rem,...) (grouped with legend toggle)'
  );
  assert(
    /right\s*:\s*1rem/.test(feedShowBtn),
    '.feed-show-btn pinned at right: 1rem (grouped with legend toggle)'
  );
}

// ─────────────────────────────────────────────────────────────────────
console.log('\n=== #1107 B2: cascade-final bottom invariant (no !important conflict) ===');

// Scan ALL .feed-show-btn rule blocks. If any declares `bottom` with
// `!important`, there must NOT be a separate block setting a different
// `bottom` without `!important` — otherwise the cascade silently wins
// and the buttons don't actually dock together.
(function cascadeFinalCheck() {
  const feedBlocks = allRuleBlocks(liveCss, '.feed-show-btn');
  const legendBlocks = allRuleBlocks(liveCss, '.legend-toggle-btn');

  // Collect all bottom declarations from .feed-show-btn blocks
  const feedBottoms = [];
  for (const block of feedBlocks) {
    const m = block.match(/bottom\s*:\s*([^;]+)/);
    if (m) {
      feedBottoms.push({
        value: m[1].trim(),
        important: /!important/.test(m[1])
      });
    }
  }

  // If any .feed-show-btn block uses !important on bottom, then EITHER:
  //   (a) it is the ONLY bottom declaration (canonical), OR
  //   (b) all bottom declarations agree (no cascade conflict)
  const importantBottoms = feedBottoms.filter(b => b.important);
  const nonImportantBottoms = feedBottoms.filter(b => !b.important);

  if (importantBottoms.length > 0 && nonImportantBottoms.length > 0) {
    // Cascade conflict: an !important override coexists with a non-!important
    // declaration. The non-!important block (the PR's docking fix) will LOSE.
    assert(false,
      '.feed-show-btn has NO cascade conflict: found ' +
      importantBottoms.length + ' !important bottom declaration(s) AND ' +
      nonImportantBottoms.length + ' non-!important bottom declaration(s) — ' +
      'the !important wins at runtime, breaking docking');
  } else {
    assert(true, '.feed-show-btn bottom declarations have no !important cascade conflict');
  }

  // Both buttons must resolve to the same `right` value across all blocks
  const feedRights = [];
  for (const block of feedBlocks) {
    const m = block.match(/right\s*:\s*([^;]+)/);
    if (m) feedRights.push(m[1].trim().replace(/\s*!important/, ''));
  }
  const legendRights = [];
  for (const block of legendBlocks) {
    const m = block.match(/right\s*:\s*([^;]+)/);
    if (m) legendRights.push(m[1].trim().replace(/\s*!important/, ''));
  }

  // All right values should be identical across both selectors
  const allRights = [...new Set([...feedRights, ...legendRights])];
  assert(
    allRights.length === 1,
    '.feed-show-btn and .legend-toggle-btn share identical `right` value' +
    (allRights.length !== 1 ? ' — found: ' + allRights.join(', ') : ' (both ' + allRights[0] + ')')
  );
})();

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

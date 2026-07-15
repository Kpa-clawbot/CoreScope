/**
 * Regression (#1833): the .legend-toggle-btn on the Live view was hardcoded
 * to `bottom: 1rem`, so it sat underneath the VCR playback bar and was
 * unreachable. Sister overlays (.live-feed, .live-overlay[data-position="br"],
 * .feed-show-btn) all offset their bottom by `var(--vcr-bar-height, ...)`.
 *
 * Same class of regression as #685 / #1206 / #1107. Test asserts the CSS rule
 * uses --vcr-bar-height so bottom-pinned Live overlays stay pinned above the
 * VCR bar. Grep-based per AGENTS.md E2E-DOM-grep exemption.
 *
 * Run: node test-issue-1833-legend-toggle-vcr-offset.js
 */
'use strict';
const fs = require('fs');
const path = require('path');

const cssPath = path.join(__dirname, 'public', 'live.css');
const css = fs.readFileSync(cssPath, 'utf8');

let passed = 0, failed = 0;
function step(name, fn) {
  try { fn(); passed++; console.log('  \u2713 ' + name); }
  catch (e) { failed++; console.error('  \u2717 ' + name + ': ' + e.message); }
}
function assert(c, m) { if (!c) throw new Error(m || 'assertion failed'); }

// Extract a rule body whose selector list is EXACTLY the given selector
// (avoids matching compound rules like `.live-feed .panel-content`).
function extractRule(source, selector) {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const re = new RegExp('(?:^|})\\s*' + escaped + '\\s*\\{', 'm');
  const m = re.exec(source);
  assert(m, 'selector not found (exact match): ' + selector);
  const open = source.indexOf('{', m.index);
  const close = source.indexOf('}', open);
  assert(open !== -1 && close !== -1, 'malformed rule for: ' + selector);
  return source.slice(open + 1, close);
}

console.log('#1833: .legend-toggle-btn must offset for --vcr-bar-height');

step('.legend-toggle-btn rule exists', () => {
  assert(/\.legend-toggle-btn\s*\{/.test(css), '.legend-toggle-btn rule missing from live.css');
});

step('.legend-toggle-btn bottom uses var(--vcr-bar-height)', () => {
  // There may be several `.legend-toggle-btn { ... }` blocks (media queries,
  // fullscreen). Only the one that declares `bottom` matters here.
  const re = /\.legend-toggle-btn\s*\{([^}]*)\}/g;
  let m, bottomValue = null;
  while ((m = re.exec(css))) {
    const bodyMatch = m[1].match(/(?:^|;|\s)bottom\s*:\s*([^;]+);/);
    if (bodyMatch) { bottomValue = bodyMatch[1].trim(); break; }
  }
  assert(bottomValue !== null, 'no .legend-toggle-btn rule declares `bottom`');
  assert(
    /var\(\s*--vcr-bar-height/.test(bottomValue),
    '.legend-toggle-btn bottom does not reference --vcr-bar-height (got: ' + bottomValue + ')'
  );
});

step('sister overlays still track --vcr-bar-height (guard against regression sweep)', () => {
  // Sanity check: the pattern we mirror is the same one already used by
  // .live-feed, .live-overlay[data-position="br"], and .feed-show-btn.
  // These selectors each have at least one rule whose body sets
  // `bottom: calc(var(--vcr-bar-height, ...) ...)`.
  const patterns = [
    /\.live-feed\s*\{[^}]*var\(\s*--vcr-bar-height/,
    /\.live-overlay\[data-position="br"\][^{]*\{[^}]*var\(\s*--vcr-bar-height/,
    /\.feed-show-btn\s*\{[^}]*var\(\s*--vcr-bar-height/,
  ];
  for (const re of patterns) {
    assert(re.test(css), 'sister overlay pattern missing: ' + re.source);
  }
});

console.log('\nResults: ' + passed + ' passed, ' + failed + ' failed');
if (failed > 0) process.exit(1);

/**
 * Issue #1646 — Polish follow-ups for the #1644/#1645 observer-comparison
 * redesign. Behavioral CSS+markup assertions only (Node-only, no Playwright).
 *
 * The aesthetic items (font weights, vertical centering, removing decorative
 * bars) are verified visually via screenshots in the PR — this file gates
 * the few items that ARE behaviorally testable so a regression future-proofs
 * the polish.
 *
 *   1) `input[type=checkbox]` has a GLOBAL `accent-color: var(--accent)`
 *      rule (not only the per-page `.col-compare-select` rule that misses
 *      the rest of the surface). Both light + dark must theme.
 *   2) The dark theme block sets `color-scheme: dark` so UA-native widgets
 *      (checkboxes, scrollbars, native selects) render in dark mode.
 *   3) `.compare-vs` font-size is smaller than `.compare-select` font-size
 *      (the parent finding #3: the "vs" label was at parity with the
 *      surrounding dropdowns).
 *   4) `.compare-strip-mid-count` (the SHARED tally count) is NOT the
 *      largest text in the mid cell. A new `.compare-strip-mid-pct`
 *      class is the largest element and uses var(--fs-xl); the count
 *      uses a smaller scale (var(--fs-lg)); the label stays at 10px.
 *   5) The "Compare" CTA inside `.compare-selector` is rendered with the
 *      `.btn-ghost` class — not `.compare-btn` / `.btn-primary` heavy
 *      visual weight (parent finding #2). Markup in compare.js.
 *   6) `.compare-asym-line` no longer paints a decorative left accent
 *      border (chartjunk encoding nothing). Tufte finding.
 *   7) `.compare-type-summary` no longer paints a decorative left green
 *      border (chartjunk encoding nothing). Tufte finding.
 *   8) compare.js renders a `.compare-controls` element with an
 *      `data-collapsed` attribute / `is-collapsed` class once both
 *      observers are selected. Parent finding #5: the strip must
 *      yield "look here first" attention to the headline strip.
 */
'use strict';
const fs = require('fs');
const path = require('path');

const CSS = fs.readFileSync(path.join(__dirname, 'public/style.css'), 'utf8');
const COMPARE_JS = fs.readFileSync(path.join(__dirname, 'public/compare.js'), 'utf8');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log('  \u2705 ' + name); }
  catch (e) { failed++; console.error('  \u274c ' + name + ': ' + e.message); }
}
function assert(c, m) { if (!c) throw new Error(m || 'assertion failed'); }

function ruleBlock(css, selectorRegex) {
  // returns the {...} block body for the first matching selector list
  const re = new RegExp('(?:^|[\\s,}])(' + selectorRegex.source + ')[^{}]*\\{([^}]*)\\}', 'm');
  const m = css.match(re);
  return m ? m[2] : null;
}

console.log('\n#1646 compare-polish — behavioral assertions\n');

// ── 1) global checkbox accent-color ───────────────────────────────────
test('global input[type=checkbox] accent-color rule uses var(--accent)', () => {
  // Match any top-level rule whose selector list includes input[type=checkbox]
  // (not scoped under .col-compare-select etc.) AND declares accent-color.
  const re = /(?:^|})\s*input\[type=["']?checkbox["']?\][^{]*\{[^}]*accent-color\s*:\s*var\(--accent\)/m;
  assert(re.test(CSS),
    'missing top-level `input[type=checkbox] { accent-color: var(--accent); }`');
});

// ── 2) color-scheme on dark theme ─────────────────────────────────────
test('dark theme sets color-scheme so UA widgets render dark', () => {
  // Look for any rule selecting the dark theme that declares color-scheme.
  const re = /(?:\.theme-dark|data-theme=["']dark["']|prefers-color-scheme:\s*dark)[^{]*\{[^}]*color-scheme\s*:\s*dark/m;
  assert(re.test(CSS),
    'missing color-scheme: dark on dark-theme rule (UA checkbox stays light otherwise)');
});

// ── 3) .compare-vs smaller than .compare-select ───────────────────────
test('.compare-vs font-size < .compare-select font-size', () => {
  const vsBlock = ruleBlock(CSS, /\.compare-vs/);
  const selBlock = ruleBlock(CSS, /\.compare-select(?![a-zA-Z-])/);
  assert(vsBlock, '.compare-vs block missing');
  assert(selBlock, '.compare-select block missing');
  // extract font-size values
  // Comparable rank scale (px-equivalent at smallest viewport):
  //   --fs-xs ≈ 11, --fs-sm ≈ 12, --fs-md ≈ 14, --fs-lg ≈ 15, --fs-xl ≈ 18
  function px(block) {
    const m = block.match(/font-size\s*:\s*([^;]+);/);
    if (!m) return null;
    const v = m[1].trim();
    const tokenMap = {
      'var(--fs-xs)': 11, 'var(--fs-sm)': 12, 'var(--fs-md)': 14,
      'var(--fs-lg)': 15, 'var(--fs-xl)': 18,
    };
    if (tokenMap[v] != null) return tokenMap[v];
    const num = v.match(/^(\d+(?:\.\d+)?)px$/);
    return num ? parseFloat(num[1]) : null;
  }
  const vsSize = px(vsBlock);
  const selSize = px(selBlock);
  assert(vsSize != null && selSize != null, 'could not parse font-size');
  assert(vsSize < selSize, `.compare-vs (${vsSize}) must be smaller than .compare-select (${selSize})`);
});

// ── 4) middle column hierarchy: pct > count > label ───────────────────
test('.compare-strip-mid-pct exists and is var(--fs-xl)', () => {
  const pctBlock = ruleBlock(CSS, /\.compare-strip-mid-pct/);
  assert(pctBlock, '.compare-strip-mid-pct rule missing (needed for inverted hierarchy)');
  assert(/font-size\s*:\s*var\(--fs-xl\)/.test(pctBlock),
    '.compare-strip-mid-pct must be var(--fs-xl) (the largest)');
});

test('.compare-strip-mid-count is smaller than .compare-strip-mid-pct', () => {
  const countBlock = ruleBlock(CSS, /\.compare-strip-mid-count/);
  assert(countBlock, '.compare-strip-mid-count rule missing');
  // must NOT be --fs-xl any more
  assert(!/font-size\s*:\s*var\(--fs-xl\)/.test(countBlock),
    '.compare-strip-mid-count must not be --fs-xl after hierarchy inversion');
});

// ── 5) Compare CTA uses btn-ghost ─────────────────────────────────────
test('compare.js renders #compareBtn with class containing btn-ghost (not compare-btn primary)', () => {
  // Loose: button id=compareBtn with btn-ghost in the class attr.
  const re = /id=["']compareBtn["'][^>]*class=["'][^"']*btn-ghost/;
  assert(re.test(COMPARE_JS),
    '#compareBtn must use .btn-ghost (low-emphasis) instead of .compare-btn / .btn-primary');
});

// ── 6) decorative asym-line border-left removed ───────────────────────
test('.compare-asym-line no longer paints a decorative left accent bar', () => {
  const block = ruleBlock(CSS, /\.compare-asym-line(?!-)/);
  assert(block, '.compare-asym-line rule missing');
  // Either no border-left declared, or border-left: none / 0
  const m = block.match(/border-left\s*:\s*([^;]+);/);
  if (m) {
    const v = m[1].trim();
    assert(/^(none|0|unset|initial)\b/.test(v),
      '.compare-asym-line still paints border-left: ' + v);
  }
});

// ── 7) decorative type-summary border-left removed ────────────────────
test('.compare-type-summary no longer paints a decorative left green bar', () => {
  const block = ruleBlock(CSS, /\.compare-type-summary(?!-)/);
  assert(block, '.compare-type-summary rule missing');
  const m = block.match(/border-left\s*:\s*([^;]+);/);
  if (m) {
    const v = m[1].trim();
    assert(/^(none|0|unset|initial)\b/.test(v),
      '.compare-type-summary still paints border-left: ' + v);
  }
});

// ── 8) controls collapse when both observers picked ───────────────────
test('compare.js toggles a collapsed state on the controls when both observers selected', () => {
  // Implementation freedom: either an `is-collapsed` class or
  // `data-collapsed="true"` attribute on #compareControls / .compare-controls.
  const hasMarker = /(is-collapsed|data-collapsed)/i.test(COMPARE_JS);
  assert(hasMarker,
    'compare.js must mark #compareControls with `is-collapsed` class or `data-collapsed` attr');
  // And there must be CSS that responds to it
  const hasCss = /(\.is-collapsed|\[data-collapsed\b)/i.test(CSS);
  assert(hasCss,
    'style.css must define a rule keyed to .is-collapsed / [data-collapsed]');
});

console.log(`\n  ${passed} passed, ${failed} failed\n`);
process.exit(failed === 0 ? 0 : 1);

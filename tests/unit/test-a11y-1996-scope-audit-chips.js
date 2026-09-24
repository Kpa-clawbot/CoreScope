/* test-a11y-1996-scope-audit-chips.js
 *
 * Issue #1996: the Scope Audit observed/verified chips failed light-theme
 * contrast. They tint their OWN background with
 * `color-mix(in srgb, var(--status-green) 16%, transparent)`, which darkens
 * whatever surface is behind them, and the text was --status-green-text
 * (green-700, #15803d). Measured: 4.38:1 on white and 4.04:1 on --surface-0,
 * against the 4.5:1 the #1719 gate requires for normal text. The reporter
 * measured 4.04:1 on the populated page; this reproduces that number exactly,
 * which is how we know the model here matches the browser's.
 *
 * Why this is a separate suite rather than a case in
 * test-a11y-1719-contrast-root-causes-e2e.js: that suite's parseColor handles
 * hex and rgb() only. Teaching it color-mix() is a larger change than the fix,
 * and these chips are the only current user of the function on a contrast-
 * critical surface. If a second one appears, move this in there.
 *
 * The default Scope Audit fixture renders no chips at all, which is why the
 * existing browser coverage missed this. Deriving the values from the
 * stylesheets rather than from a rendered page keeps that gap from mattering.
 */
'use strict';

const REPO_ROOT = require('path').resolve(__dirname, '..', '..');
const fs = require('fs');
const path = require('path');
const assert = require('assert');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log('  ✓ ' + name); }
  catch (e) { failed++; console.error('  ✗ ' + name + ': ' + e.message); }
}

const STYLE = fs.readFileSync(path.join(REPO_ROOT, 'public/style.css'), 'utf8');
const AUDIT = fs.readFileSync(path.join(REPO_ROOT, 'public/scope-audit.css'), 'utf8');

const REQUIRED = 4.5;

// --- colour maths ----------------------------------------------------------

function hex(s) {
  const m = String(s).trim().match(/^#([0-9a-f]{6})$/i);
  if (!m) return null;
  const n = parseInt(m[1], 16);
  return [(n >> 16) & 255, (n >> 8) & 255, n & 255];
}
function lum(rgb) {
  const f = (c) => { c /= 255; return c <= 0.03928 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4); };
  return 0.2126 * f(rgb[0]) + 0.7152 * f(rgb[1]) + 0.0722 * f(rgb[2]);
}
function contrast(fg, bg) {
  const a = lum(fg), b = lum(bg);
  return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05);
}
// color-mix(in srgb, C p%, transparent) over a surface is a plain alpha
// composite of C at p% onto that surface.
function mixOver(colour, pct, surface) {
  return [0, 1, 2].map((i) => Math.round(colour[i] * pct + surface[i] * (1 - pct)));
}

// --- reading the stylesheets ----------------------------------------------

// Variable values per theme. The light values come from the first :root block,
// the dark ones from the prefers-color-scheme block, which is what a visitor on
// the default setting gets.
function varValue(name, theme) {
  const scope = theme === 'dark'
    ? STYLE.slice(STYLE.indexOf('prefers-color-scheme: dark'))
    : STYLE;
  // Follow one level of var(--x, #fallback) indirection, which is how the
  // palette is referenced.
  const re = new RegExp('--' + name + '\\s*:\\s*([^;]+);');
  const m = scope.match(re);
  if (!m) return null;
  const raw = m[1].trim();
  const direct = hex(raw);
  if (direct) return direct;
  const via = raw.match(/var\(\s*--([\w-]+)\s*(?:,\s*(#[0-9a-f]{6})\s*)?\)/i);
  if (!via) return null;
  return varValue(via[1], theme) || (via[2] ? hex(via[2]) : null);
}

function chipTintPct() {
  const m = AUDIT.match(/\.sa-chip-observed\s*\{[^}]*color-mix\(in srgb,\s*var\(--status-green\)\s*(\d+)%/);
  assert.ok(m, '.sa-chip-observed must still tint with color-mix on --status-green');
  return Number(m[1]) / 100;
}

function chipFgVar() {
  const m = AUDIT.match(/\.sa-chip-observed\s*\{[^}]*[^-\w]color\s*:\s*var\(\s*--([\w-]+)/);
  assert.ok(m, '.sa-chip-observed must set its colour from a variable, never a literal');
  return m[1];
}

console.log('\n=== #1996: Scope Audit chip contrast ===');

test('the chip still tints its own background, so this test is testing something', () => {
  const pct = chipTintPct();
  assert.ok(pct > 0 && pct < 1, 'tint percentage: ' + pct);
});

test('the chip takes its colour from a variable, not a literal', () => {
  // A literal would not follow the customizer and would sidestep every theme
  // check in this repo.
  const name = chipFgVar();
  assert.ok(name.length > 0);
});

// The surfaces a chip can sit on. --surface-0 is the worse of the two in light
// and is what the report measured, so both are checked rather than the kinder one.
for (const theme of ['light', 'dark']) {
  for (const surfaceVar of ['surface-0', 'card-bg']) {
    test(`${theme}: chip meets ${REQUIRED}:1 on --${surfaceVar}`, () => {
      const green = varValue('status-green', theme);
      const surface = varValue(surfaceVar, theme);
      const fg = varValue(chipFgVar(), theme);
      assert.ok(green, 'could not resolve --status-green for ' + theme);
      assert.ok(surface, 'could not resolve --' + surfaceVar + ' for ' + theme);
      assert.ok(fg, 'could not resolve --' + chipFgVar() + ' for ' + theme);

      const composited = mixOver(green, chipTintPct(), surface);
      const ratio = contrast(fg, composited);
      assert.ok(
        ratio >= REQUIRED,
        `got ${ratio.toFixed(2)}:1, need ${REQUIRED}:1 ` +
        `(text rgb(${fg}) on composited rgb(${composited}))`
      );
    });
  }
}

test('the regression itself: green-700 on the light chip would still fail', () => {
  // Guards the fix from being quietly reverted to --status-green-text. If this
  // ever passes, either the tint changed or the palette did, and the fix above
  // is no longer doing the work this issue needed.
  const green = varValue('status-green', 'light');
  const surface = varValue('surface-0', 'light');
  const old = varValue('palette-green-700', 'light');
  const ratio = contrast(old, mixOver(green, chipTintPct(), surface));
  assert.ok(ratio < REQUIRED,
    `green-700 now reaches ${ratio.toFixed(2)}:1 — re-check whether this suite still guards anything`);
});

console.log(`\nTotal: ${passed} passed, ${failed} failed`);
if (failed) process.exit(1);

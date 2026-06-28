#!/usr/bin/env node
/**
 * test-a11y-1715-dark-role-swatches.js — Issue #1715 regression gate.
 *
 * Locks in the per-theme `--role-*` custom properties used by the
 * /analytics?tab=neighbor-graph role swatches (`#ngRoleChecks` labels).
 *
 * Background: PR #1720 fixed LIGHT theme by bumping `customize.js`
 * `DEFAULTS.nodeColors` to palette-700 shades that clear 4.5:1 on white.
 * But the role swatches in the neighbor-graph filter row read those hex
 * values via inline `style="color:#..."`, so the DARK theme swatches were
 * still painted with the same shades and failed against `#1a1a2e`:
 *
 *   repeater  #dc2626  3.53:1
 *   companion #2563eb  3.30:1
 *   observer  #8b5cf6  4.02:1   (also borderline on light)
 *
 * Fix (per issue triage's #1715 path):
 *   - Define `--role-{repeater,companion,room,sensor,observer}` in
 *     `:root` (light) and override them in the dark theme block in
 *     public/style.css so each value clears WCAG AA (≥4.5:1) against
 *     the matching surface (#ffffff light, #1a1a2e dark).
 *   - Refactor the neighbor-graph swatches from inline
 *     `<span style="color:${role_hex}">` to class-based DOM
 *     (`<span class="role-swatch role-swatch--{role}">`) so the CSS
 *     custom properties are the single source of truth across themes.
 *
 * This test is CSS-driven (parses public/style.css) so it runs without
 * a browser. The umbrella axe gate (test-a11y-axe-1668.js) is the
 * live-browser net.
 *
 * Usage:  node test-a11y-1715-dark-role-swatches.js
 */
'use strict';

const fs = require('fs');
const path = require('path');
const assert = require('assert');

const ROOT = __dirname;
const STYLE_CSS = process.env.STYLE_CSS_PATH || path.join(ROOT, 'public', 'style.css');

const ROLES = ['repeater', 'companion', 'room', 'sensor', 'observer'];

// Light theme surface = .analytics-card on white; dark theme surface
// = the analytics card-bg #1a1a2e (per :root[data-theme="dark"]).
const SURFACES = {
  light: { r: 0xff, g: 0xff, b: 0xff, a: 1 },
  dark:  { r: 0x1a, g: 0x1a, b: 0x2e, a: 1 },
};

const MIN_RATIO = 4.5;

// -------------------- Color helpers (WCAG composite-contrast) --------------------

function parseColor(s) {
  if (!s) return null;
  s = String(s).trim();
  let m = s.match(/^#([0-9a-f]{3})$/i);
  if (m) { const h = m[1]; return { r: parseInt(h[0]+h[0],16), g: parseInt(h[1]+h[1],16), b: parseInt(h[2]+h[2],16), a: 1 }; }
  m = s.match(/^#([0-9a-f]{6})$/i);
  if (m) { const h = m[1]; return { r: parseInt(h.slice(0,2),16), g: parseInt(h.slice(2,4),16), b: parseInt(h.slice(4,6),16), a: 1 }; }
  return null;
}
function srgbToLin(c) { c /= 255; return c <= 0.03928 ? c/12.92 : Math.pow((c+0.055)/1.055, 2.4); }
function relLum(rgb) { return 0.2126*srgbToLin(rgb.r) + 0.7152*srgbToLin(rgb.g) + 0.0722*srgbToLin(rgb.b); }
function contrast(a, b) {
  const L1 = relLum(a), L2 = relLum(b);
  const [lo, hi] = L1 < L2 ? [L1, L2] : [L2, L1];
  return (hi + 0.05) / (lo + 0.05);
}

// -------------------- CSS block extractor (brace-balanced) --------------------

function extractBlocks(css, selectorPattern) {
  const re = new RegExp(`(?:^|[^a-zA-Z0-9_-])(${selectorPattern})\\s*\\{`, 'g');
  const blocks = [];
  let m;
  while ((m = re.exec(css))) {
    const start = m.index + m[0].length;
    let depth = 1, i = start;
    while (i < css.length && depth > 0) { const ch = css[i]; if (ch === '{') depth++; else if (ch === '}') depth--; i++; }
    if (depth === 0) blocks.push(css.slice(start, i - 1));
  }
  return blocks;
}

function readVarFromBlocks(blocks, name) {
  // Last write wins (preserves cascade order across multiple matching blocks).
  let v = null;
  for (const b of blocks) {
    const m = b.match(new RegExp(`(?:^|[^-\\w])--${name}\\s*:\\s*([^;\\n]+);`));
    if (m) v = m[1].trim().replace(/\s*!important\s*$/, '').trim();
  }
  return v;
}

// -------------------- Main --------------------

function main() {
  if (!fs.existsSync(STYLE_CSS)) {
    console.error(`a11y-1715: style.css missing at ${STYLE_CSS}`);
    process.exit(1);
  }
  const css = fs.readFileSync(STYLE_CSS, 'utf8');
  const rootBlocks = extractBlocks(css, ':root');
  const darkBlocks = extractBlocks(css, '\\[data-theme="dark"\\]')
    .concat(extractBlocks(css, ':root:not\\(\\[data-theme="light"\\]\\)'));
  if (rootBlocks.length === 0) throw new Error('a11y-1715: no :root blocks');
  if (darkBlocks.length === 0) throw new Error('a11y-1715: no dark theme blocks');

  let failures = 0;
  console.log(`a11y-1715: probing --role-* tokens for neighbor-graph swatches (WCAG AA ≥${MIN_RATIO}:1)`);

  // For each role: light token must resolve and pass on white; dark
  // override must resolve and pass on #1a1a2e.
  for (const role of ROLES) {
    const tokenName = `role-${role}`;

    const lightRaw = readVarFromBlocks(rootBlocks, tokenName);
    if (!lightRaw) {
      console.log(`  FAIL light  --${tokenName}  (token not defined in :root)`);
      failures++;
    } else {
      const fg = parseColor(lightRaw);
      if (!fg) { console.log(`  FAIL light  --${tokenName}  (unparsable: ${lightRaw})`); failures++; }
      else {
        const r = contrast(fg, SURFACES.light);
        const ok = r >= MIN_RATIO;
        console.log(`  ${ok ? 'PASS' : 'FAIL'} light  --${tokenName}  fg=${lightRaw} bg=#ffffff  ratio=${r.toFixed(2)}:1`);
        if (!ok) failures++;
      }
    }

    // Dark override may either redeclare in [data-theme="dark"] or
    // inherit from :root if the light value also clears the dark bar.
    const darkRaw = readVarFromBlocks(darkBlocks, tokenName) || lightRaw;
    if (!darkRaw) {
      console.log(`  FAIL dark   --${tokenName}  (no value resolvable)`);
      failures++;
      continue;
    }
    const dfg = parseColor(darkRaw);
    if (!dfg) { console.log(`  FAIL dark   --${tokenName}  (unparsable: ${darkRaw})`); failures++; continue; }
    const dr = contrast(dfg, SURFACES.dark);
    const ok = dr >= MIN_RATIO;
    console.log(`  ${ok ? 'PASS' : 'FAIL'} dark   --${tokenName}  fg=${darkRaw} bg=#1a1a2e  ratio=${dr.toFixed(2)}:1`);
    if (!ok) failures++;
  }

  // Markup invariant: neighbor-graph swatches must use class-based DOM,
  // not inline `style="color:#..."`. If anyone reintroduces a hardcoded
  // hex on the swatch, the fix regresses silently — gate it here.
  const analyticsJs = fs.readFileSync(path.join(ROOT, 'public', 'analytics.js'), 'utf8');
  // Locate the ngRoleChecks block: lines that build the role checkbox rows.
  const ngBlockStart = analyticsJs.indexOf("getElementById('ngRoleChecks')");
  if (ngBlockStart < 0) {
    console.log('  FAIL markup ngRoleChecks block not found in analytics.js');
    failures++;
  } else {
    // Inspect ~80 lines after the lookup — covers the roles.forEach and
    // the observer-checkbox blocks.
    const slice = analyticsJs.slice(ngBlockStart, ngBlockStart + 4000);
    const hasInlineColorSpan = /<span\s+style=["'][^"']*color\s*:/i.test(slice);
    const hasRoleSwatchClass = /role-swatch--/.test(slice);
    if (hasInlineColorSpan) {
      console.log('  FAIL markup ngRoleChecks swatch still uses inline style="color:..." span');
      failures++;
    } else {
      console.log('  PASS markup ngRoleChecks swatch has no inline color span');
    }
    if (!hasRoleSwatchClass) {
      console.log('  FAIL markup ngRoleChecks swatch missing role-swatch-- class');
      failures++;
    } else {
      console.log('  PASS markup ngRoleChecks swatch uses role-swatch-- class');
    }
  }

  if (failures > 0) {
    console.error(`\nFAIL: ${failures} probe(s) below WCAG AA ${MIN_RATIO}:1 or markup invariant broken (issue #1715)`);
    assert.strictEqual(failures, 0, `${failures} probe(s) below WCAG AA ${MIN_RATIO}:1 or markup invariant broken (issue #1715)`);
  }
  console.log(`\nPASS: all 5 role swatches ≥ ${MIN_RATIO}:1 in both themes + class-based markup (issue #1715)`);
}

if (require.main === module) main();

module.exports = { parseColor, contrast, relLum, extractBlocks, readVarFromBlocks };

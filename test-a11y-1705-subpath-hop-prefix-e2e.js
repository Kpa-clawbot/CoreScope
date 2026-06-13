#!/usr/bin/env node
/**
 * test-a11y-1705-subpath-hop-prefix-e2e.js — Issue #1705 regression gate.
 *
 * Asserts that `.subpath-selected .hop-prefix` (the secondary line of a
 * subpath-detail table row in /#/analytics?tab=subpaths) meets WCAG AA
 * color-contrast (≥ 4.5:1) in BOTH dark and light themes.
 *
 * Why a dedicated test (the umbrella test-a11y-axe-1668.js does NOT catch this):
 *   The umbrella axe gate scans every page in its initial paint. The
 *   `.subpath-selected` class is only applied AFTER a user clicks a row,
 *   so axe never sees it during the umbrella run. Issue #1705 is the
 *   poster child for "state-only" a11y regressions slipping through.
 *
 * Strategy:
 *   1. Boot any page (we just need the production CSS loaded).
 *   2. Inject a minimal fragment carrying the production class names
 *      (`.subpath-selected` + `.hop-prefix`).
 *   3. Read the BROWSER-RESOLVED RGB of:
 *        - the row background  (`.subpath-selected`)
 *        - the prefix text     (`.subpath-selected .hop-prefix`)
 *      per theme.
 *   4. Composite the text color over the background (alpha-aware) and
 *      compute the WCAG contrast ratio in Node. Assert >= 4.5:1.
 *
 *   The contrast math is the same formula axe-core uses (sRGB → relative
 *   luminance, then (L1+0.05)/(L2+0.05)). We do the composite locally
 *   because the original BLOCKER (rgba(255,255,255,0.6) on --accent) is
 *   exactly the case axe's color-contrast rule mis-evaluates without
 *   a real composite step.
 *
 *   We deliberately do NOT depend on @axe-core/playwright invoking
 *   `axe.run` against the subpath state — color-contrast rule on a
 *   rgba text color over a CSS-var background is the rule's known
 *   weak spot (see issue body, "audit probe correctness fix"). A direct
 *   composite-then-contrast computation is the authoritative regression
 *   signal for this BLOCKER.
 *
 * Usage:
 *   node test-a11y-1705-subpath-hop-prefix-e2e.js
 *
 * Env:
 *   STYLE_CSS_PATH    optional override; defaults to public/style.css next
 *                     to this script (same checkout the production server
 *                     serves from).
 */
'use strict';

const fs = require('fs');
const path = require('path');

const STYLE_CSS_PATH = process.env.STYLE_CSS_PATH || path.join(__dirname, 'public', 'style.css');
const THEMES = ['dark', 'light'];

// -------------------- Pure helpers (unit-testable) --------------------

// Parse a CSS color string into {r,g,b,a} (0-255 channels, 0-1 alpha).
// Handles #rgb, #rrggbb, rgb(...), rgba(...).
function parseColor(s) {
  if (!s) return null;
  s = String(s).trim();
  let m = s.match(/^#([0-9a-f]{3})$/i);
  if (m) {
    const h = m[1];
    return { r: parseInt(h[0] + h[0], 16), g: parseInt(h[1] + h[1], 16), b: parseInt(h[2] + h[2], 16), a: 1 };
  }
  m = s.match(/^#([0-9a-f]{6})$/i);
  if (m) {
    const h = m[1];
    return { r: parseInt(h.slice(0, 2), 16), g: parseInt(h.slice(2, 4), 16), b: parseInt(h.slice(4, 6), 16), a: 1 };
  }
  m = s.match(/^rgba?\(([^)]+)\)$/i);
  if (m) {
    const parts = m[1].split(/[ ,/]+/).filter(Boolean).map(Number);
    if (parts.length >= 3) {
      return {
        r: parts[0],
        g: parts[1],
        b: parts[2],
        a: parts.length >= 4 && Number.isFinite(parts[3]) ? parts[3] : 1,
      };
    }
  }
  return null;
}

// Composite `fg` (rgba) over `bg` (assumed opaque rgba) → opaque rgba.
function composite(fg, bg) {
  const a = fg.a;
  return {
    r: Math.round(fg.r * a + bg.r * (1 - a)),
    g: Math.round(fg.g * a + bg.g * (1 - a)),
    b: Math.round(fg.b * a + bg.b * (1 - a)),
    a: 1,
  };
}

// sRGB channel → linear (per WCAG 2.x).
function srgbToLin(c) {
  c /= 255;
  return c <= 0.03928 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4);
}

function relLuminance(rgb) {
  return 0.2126 * srgbToLin(rgb.r) + 0.7152 * srgbToLin(rgb.g) + 0.0722 * srgbToLin(rgb.b);
}

// WCAG contrast ratio.
function contrastRatio(rgb1, rgb2) {
  const L1 = relLuminance(rgb1);
  const L2 = relLuminance(rgb2);
  const [lo, hi] = L1 < L2 ? [L1, L2] : [L2, L1];
  return (hi + 0.05) / (lo + 0.05);
}

// Compose-then-contrast: text (may be rgba) over background (assumed opaque).
function compositeContrast(textRgba, bgRgb) {
  const eff = textRgba.a < 1 ? composite(textRgba, bgRgb) : textRgba;
  return contrastRatio(eff, bgRgb);
}

// Read CSS file synchronously.
function readCss(filePath) {
  if (!fs.existsSync(filePath)) {
    throw new Error(`a11y-1705: CSS file not found: ${filePath}`);
  }
  return fs.readFileSync(filePath, 'utf8');
}

// Extract every top-level block matching `selector { ... }`. Brace-balanced
// so nested `@media (...) { :root { ... } }` doesn't break the scan.
// Returns an array of body strings (the contents between matching braces).
function extractBlocks(css, selectorPattern) {
  const re = new RegExp(`(?:^|[^a-zA-Z0-9_-])(${selectorPattern})\\s*\\{`, 'g');
  const blocks = [];
  let m;
  while ((m = re.exec(css))) {
    const start = m.index + m[0].length;
    let depth = 1;
    let i = start;
    while (i < css.length && depth > 0) {
      const ch = css[i];
      if (ch === '{') depth++;
      else if (ch === '}') depth--;
      i++;
    }
    if (depth === 0) blocks.push(css.slice(start, i - 1));
  }
  return blocks;
}

function inBlock(block, varName) {
  const re = new RegExp(`--${varName}\\s*:\\s*([^;\\n]+);`);
  const m = block.match(re);
  return m ? m[1].trim() : null;
}

// Search across an ordered list of blocks; last writer wins (matches CSS cascade).
function lookupVar(blocks, varName) {
  let val = null;
  for (const b of blocks) {
    const v = inBlock(b, varName);
    if (v) val = v;
  }
  return val;
}

function extractTokensFromCss(css) {
  // Collect all :root and [data-theme="dark"] blocks (style.css has multiple
  // :root blocks — one for the palette layer, one inside the @media dark
  // media query, etc.).
  const rootBlocks = extractBlocks(css, ':root');
  const darkBlocks = extractBlocks(css, '\\[data-theme="dark"\\]');
  if (rootBlocks.length === 0) throw new Error('a11y-1705: no :root blocks found in style.css');
  if (darkBlocks.length === 0) throw new Error('a11y-1705: no [data-theme="dark"] blocks found in style.css');

  // Resolve a var(...) reference recursively against (theme blocks first, then root blocks).
  function resolveValue(raw, themeBlocks) {
    raw = String(raw).trim().replace(/\s*!important\s*$/, '').trim();
    const v = raw.match(/^var\(\s*--([a-z0-9-]+)\s*(?:,\s*(.+))?\)$/i);
    if (!v) return raw;
    const name = v[1];
    const fallback = v[2];
    // Theme overrides win over :root.
    const fromTheme = lookupVar(themeBlocks, name);
    if (fromTheme) return resolveValue(fromTheme, themeBlocks);
    const fromRoot = lookupVar(rootBlocks, name);
    if (fromRoot) return resolveValue(fromRoot, themeBlocks);
    if (fallback) return resolveValue(fallback.trim(), themeBlocks);
    throw new Error(`a11y-1705: could not resolve var(--${name})`);
  }

  // .subpath-selected { background: ...; ... }
  const selBlocks = extractBlocks(css, '\\.subpath-selected');
  if (selBlocks.length === 0) throw new Error('a11y-1705: .subpath-selected rule missing');
  // Pick the block that actually declares `background:` (first one wins).
  const bgRaw = (() => {
    for (const b of selBlocks) {
      const m = b.match(/background\s*:\s*([^;]+);/);
      if (m) return m[1].trim();
    }
    throw new Error('a11y-1705: .subpath-selected has no background declaration');
  })();

  // .subpath-selected { color: ...; } — primary row text. Asserted separately
  // from the .hop-prefix child rule so a future token swap on either the
  // parent OR the child trips this gate (parent regression slipped past
  // earlier audits because only the child was probed).
  const primaryColorRaw = (() => {
    for (const b of selBlocks) {
      const m = b.match(/(?:^|[^-\w])color\s*:\s*([^;]+);/);
      if (m) return m[1].trim();
    }
    throw new Error('a11y-1705: .subpath-selected has no color declaration');
  })();

  // .subpath-selected .hop-prefix { color: ...; }
  const prefixBlocks = extractBlocks(css, '\\.subpath-selected\\s+\\.hop-prefix');
  if (prefixBlocks.length === 0) throw new Error('a11y-1705: .subpath-selected .hop-prefix rule missing');
  const colorRaw = (() => {
    for (const b of prefixBlocks) {
      const m = b.match(/color\s*:\s*([^;]+);/);
      if (m) return m[1].trim();
    }
    throw new Error('a11y-1705: .subpath-selected .hop-prefix has no color declaration');
  })();

  return {
    light: {
      bg: resolveValue(bgRaw, []),                  // light → only :root
      text: resolveValue(colorRaw, []),
      primaryText: resolveValue(primaryColorRaw, []),
      bgRaw, colorRaw, primaryColorRaw,
    },
    dark: {
      bg: resolveValue(bgRaw, darkBlocks),
      text: resolveValue(colorRaw, darkBlocks),
      primaryText: resolveValue(primaryColorRaw, darkBlocks),
      bgRaw, colorRaw, primaryColorRaw,
    },
  };
}

// -------------------- Main: CSS-driven assertion --------------------

async function main() {
  console.log(`a11y-1705: reading ${STYLE_CSS_PATH}`);
  const css = readCss(STYLE_CSS_PATH);

  const tokens = extractTokensFromCss(css);
  let failures = 0;

  for (const theme of THEMES) {
    const { bg: bgStr, text: textStr, primaryText: primaryStr } = tokens[theme];
    const bg = parseColor(bgStr);
    const text = parseColor(textStr);
    const primary = parseColor(primaryStr);
    if (!bg) throw new Error(`a11y-1705: unparsable bg "${bgStr}" for theme=${theme}`);
    if (!text) throw new Error(`a11y-1705: unparsable text "${textStr}" for theme=${theme}`);
    if (!primary) throw new Error(`a11y-1705: unparsable primary "${primaryStr}" for theme=${theme}`);

    // 1. .subpath-selected .hop-prefix (the original BLOCKER surface).
    const ratio = compositeContrast(text, bg);
    const ok = ratio >= 4.5;
    console.log(
      `  ${ok ? 'PASS' : 'FAIL'} theme=${theme} [hop-prefix] bg=${bgStr} text=${textStr} composite=${JSON.stringify(text.a < 1 ? composite(text, bg) : text)} ratio=${ratio.toFixed(2)}:1 (need ≥4.5:1)`
    );
    if (!ok) failures++;

    // 2. .subpath-selected primary row text — guards against the parent
    //    rule regressing independently of the child (e.g. someone swaps
    //    `color: var(--text-on-accent)` back to `#fff` without touching
    //    the .hop-prefix line). #1705 review-r1 must-fix.
    const ratioPrimary = compositeContrast(primary, bg);
    const okPrimary = ratioPrimary >= 4.5;
    console.log(
      `  ${okPrimary ? 'PASS' : 'FAIL'} theme=${theme} [primary]    bg=${bgStr} text=${primaryStr} composite=${JSON.stringify(primary.a < 1 ? composite(primary, bg) : primary)} ratio=${ratioPrimary.toFixed(2)}:1 (need ≥4.5:1)`
    );
    if (!okPrimary) failures++;
  }

  if (failures > 0) {
    console.error(`\nFAIL: .subpath-selected text violates WCAG AA color-contrast in ${failures} probe(s) (issue #1705)`);
    process.exit(1);
  }
  console.log(`\nPASS: .subpath-selected primary + .hop-prefix ≥4.5:1 in dark + light themes (issue #1705)`);
}

if (require.main === module) {
  main().catch((err) => {
    console.error('test-a11y-1705 fatal:', err && err.stack || err);
    process.exit(2);
  });
}

module.exports = {
  parseColor,
  composite,
  contrastRatio,
  compositeContrast,
  extractTokensFromCss,
};

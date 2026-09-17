/* Issue #1668 M2 / #1671 — Palette indirection + WCAG AA token bumps.
 *
 * This test:
 *  1. Parses public/style.css and extracts CSS custom-property values per
 *     theme (:root = light, [data-theme="dark"] = dark).
 *  2. Resolves var(...) indirection (one level deep is enough for our
 *     two-tier palette → semantic mapping).
 *  3. Computes WCAG relative-luminance contrast ratios for the foreground/
 *     background pairs that were flagged as BLOCKER in the M1 a11y audit
 *     (a11y-audit/reports/violations-summary.md).
 *  4. Asserts each pair meets WCAG AA (≥4.5:1 for body text).
 *
 * Source for contrast formula: https://www.w3.org/WAI/WCAG21/Techniques/general/G18
 */
'use strict';
const fs = require('fs');
const assert = require('assert');

const CSS_PATH = 'public/style.css';
const css = fs.readFileSync(CSS_PATH, 'utf8');

// ── Token extraction ──────────────────────────────────────────────────────
function extractBlockTokens(blockRegex) {
  const tokens = {};
  // Use a /g regex; same selector may appear in multiple blocks (e.g. two
  // `:root { ... }` blocks: palette + semantic). Later definitions win,
  // mirroring CSS cascade order.
  const flagged = new RegExp(blockRegex.source, blockRegex.flags.includes('g') ? blockRegex.flags : blockRegex.flags + 'g');
  let m;
  while ((m = flagged.exec(css)) !== null) {
    const body = m[1];
    const re = /^\s*(--[a-z0-9-]+)\s*:\s*([^;]+);/gim;
    let mm;
    while ((mm = re.exec(body)) !== null) {
      tokens[mm[1]] = mm[2].trim();
    }
  }
  return tokens;
}

// Light theme = :root block (the FIRST :root, lines ~99-247)
const lightTokens = extractBlockTokens(/:root\s*\{([\s\S]*?)\n\}/);
// Dark theme = [data-theme="dark"] block
const darkTokens  = extractBlockTokens(/\[data-theme="dark"\]\s*\{([\s\S]*?)\n\}/);

function resolveToken(name, theme) {
  const map = theme === 'dark' ? { ...lightTokens, ...darkTokens } : lightTokens;
  let val = map[name];
  if (!val) return null;
  // resolve up to 5 levels of var() indirection
  for (let i = 0; i < 5; i++) {
    const m = val.match(/^var\(\s*(--[a-z0-9-]+)\s*(?:,\s*([^)]+))?\)\s*$/);
    if (!m) break;
    const next = map[m[1]];
    val = next || (m[2] ? m[2].trim() : null);
    if (!val) return null;
  }
  return val;
}

// ── Color parsing + contrast ──────────────────────────────────────────────
function parseColor(s) {
  if (!s) return null;
  s = s.trim();
  // #rgb / #rrggbb
  let m = s.match(/^#([0-9a-f]{3})$/i);
  if (m) {
    return [
      parseInt(m[1][0] + m[1][0], 16),
      parseInt(m[1][1] + m[1][1], 16),
      parseInt(m[1][2] + m[1][2], 16),
    ];
  }
  m = s.match(/^#([0-9a-f]{6})$/i);
  if (m) {
    return [parseInt(m[1].slice(0,2),16), parseInt(m[1].slice(2,4),16), parseInt(m[1].slice(4,6),16)];
  }
  m = s.match(/^rgba?\(\s*(\d+)\s*,\s*(\d+)\s*,\s*(\d+)/i);
  if (m) return [+m[1], +m[2], +m[3]];
  return null;
}

function relLum([r,g,b]) {
  const f = (c) => {
    c /= 255;
    return c <= 0.03928 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4);
  };
  return 0.2126*f(r) + 0.7152*f(g) + 0.0722*f(b);
}

function contrast(fg, bg) {
  const L1 = relLum(fg), L2 = relLum(bg);
  const [hi, lo] = L1 >= L2 ? [L1, L2] : [L2, L1];
  return (hi + 0.05) / (lo + 0.05);
}

function ratioFromTokens(fgToken, bgToken, theme) {
  const fg = parseColor(resolveToken(fgToken, theme));
  const bg = parseColor(resolveToken(bgToken, theme));
  assert.ok(fg, `token ${fgToken} (${theme}) did not resolve to a color: got ${resolveToken(fgToken, theme)}`);
  assert.ok(bg, `token ${bgToken} (${theme}) did not resolve to a color: got ${resolveToken(bgToken, theme)}`);
  return { fg, bg, ratio: contrast(fg, bg) };
}

// ── Palette indirection: existence assertions (closes #1671) ─────────────
const PALETTE_PREFIXES = ['gray', 'blue', 'green', 'amber', 'red', 'purple'];
for (const p of PALETTE_PREFIXES) {
  const re = new RegExp(`--palette-${p}-\\d+\\s*:`);
  assert.ok(re.test(css), `missing palette family --palette-${p}-* (closes #1671)`);
}
// At least 5 stops per family
for (const p of PALETTE_PREFIXES) {
  const re = new RegExp(`--palette-${p}-\\d+\\s*:`, 'g');
  const n = (css.match(re) || []).length;
  assert.ok(n >= 5, `palette family --palette-${p}-* needs ≥5 stops, got ${n}`);
}

// ── M1-BLOCKER contrast assertions ───────────────────────────────────────
// Each row: [label, fgToken, bgToken, theme, minRatio]
// AA body text = 4.5:1; large text (≥18px or ≥14px+700) = 3:1. Most flagged
// surfaces are body text (11-13px @ 600), so 4.5:1 is the floor.
const CASES = [
  // Operator-reported: .hop-named.hop-link chip — was #fff on var(--accent)
  // ≈ #4a9eff = 2.75:1. Must use --text-on-accent on --accent-strong (or
  // an equivalent darker blue) in BOTH themes.
  ['hop-named chip (dark)',  '--text-on-accent', '--accent-strong', 'dark',  4.5],
  ['hop-named chip (light)', '--text-on-accent', '--accent-strong', 'light', 4.5],

  // .skip-link / .btn.active — same #fff on --accent surface, also a BLOCKER
  // in M1. Bumping --accent-strong fixes them all. Verified via the same
  // token pair (they all rebind to --accent-strong in the patched CSS).
  ['btn.active (dark)',      '--text-on-accent', '--accent-strong', 'dark',  4.5],

  // Body muted text on common surfaces.
  ['text-muted on surface (dark)',     '--text-muted', '--surface-1', 'dark',  4.5],
  ['text-muted on content-bg (dark)',  '--text-muted', '--surface-0', 'dark',  4.5],
  ['text-muted on card-bg (dark)',     '--text-muted', '--card-bg',   'dark',  4.5],
  ['text-muted on surface (light)',    '--text-muted', '--surface-1', 'light', 4.5],
  ['text-muted on content-bg (light)', '--text-muted', '--surface-0', 'light', 4.5],

  // Body text on the canonical page background.
  ['text on content-bg (dark)',  '--text', '--surface-0', 'dark',  7.0],
  ['text on content-bg (light)', '--text', '--surface-0', 'light', 7.0],
];

let failures = 0;
console.log('\n#1668 M2 contrast audit\n' + '─'.repeat(60));
for (const [label, fgT, bgT, theme, min] of CASES) {
  try {
    const { fg, bg, ratio } = ratioFromTokens(fgT, bgT, theme);
    const ok = ratio >= min;
    const fgHex = `#${fg.map(v=>v.toString(16).padStart(2,'0')).join('')}`;
    const bgHex = `#${bg.map(v=>v.toString(16).padStart(2,'0')).join('')}`;
    console.log(
      `${ok ? '✓' : '✗'} ${label.padEnd(42)} ${ratio.toFixed(2).padStart(5)}:1  (need ${min})  ${fgHex} on ${bgHex}`
    );
    if (!ok) failures++;
    assert.ok(ok, `${label}: contrast ${ratio.toFixed(2)}:1 < ${min}:1 (fg ${fgHex} on bg ${bgHex})`);
  } catch (e) {
    failures++;
    console.log(`✗ ${label.padEnd(42)} ERROR: ${e.message}`);
    throw e;
  }
}
console.log('─'.repeat(60));
console.log(failures === 0 ? `All ${CASES.length} contrast cases pass.` : `${failures} failure(s)`);

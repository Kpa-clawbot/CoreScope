// test-issue-1668-m2-contrast.js
// RED test for #1668 M2: top BLOCKER contrast-ratio failures must hit WCAG AA (>=4.5:1).
//
// Parses public/style.css, extracts CSS-variable values for :root (light) and the
// dark-theme block, then computes WCAG contrast ratios for the foreground/background
// pairs that the M1 audit flagged as the highest-count BLOCKERs (rgb(255,255,255)
// on var(--accent), rgb(255,255,255) on var(--status-green), --text-muted on bg).
//
// The thresholds are WCAG AA (>=4.5:1 for normal text). We DO NOT touch font sizes
// (that's M3). We DO NOT add per-route overrides (that's M4).
//
// On master before the M2 token bumps this test FAILS on every assertion below.

const fs = require('fs');
const path = require('path');
const assert = require('assert');

const CSS = fs.readFileSync(path.join(__dirname, 'public', 'style.css'), 'utf8');

// --- color utils ---
function hexToRgb(h) {
  h = h.replace('#', '').trim();
  if (h.length === 3) h = h.split('').map(c => c + c).join('');
  const n = parseInt(h, 16);
  return [(n >> 16) & 0xff, (n >> 8) & 0xff, n & 0xff];
}
function relLum([r, g, b]) {
  const f = v => {
    v /= 255;
    return v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4);
  };
  return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b);
}
function contrast(a, b) {
  const la = relLum(a), lb = relLum(b);
  const hi = Math.max(la, lb), lo = Math.min(la, lb);
  return (hi + 0.05) / (lo + 0.05);
}

// --- extract token blocks (light :root, dark :root) ---
// Light theme: first :root { ... } block (skip @media). Dark: the block immediately
// after the prefers-color-scheme: dark @media, OR the [data-theme="dark"] :root.
function sliceBlock(src, headerRegex) {
  const m = src.match(headerRegex);
  if (!m) return null;
  let i = m.index + m[0].length;
  let depth = 1;
  while (i < src.length && depth > 0) {
    if (src[i] === '{') depth++;
    else if (src[i] === '}') depth--;
    i++;
  }
  return src.slice(m.index + m[0].length, i - 1);
}
function tokens(block) {
  const out = {};
  if (!block) return out;
  const re = /--([a-z0-9-]+)\s*:\s*([^;]+);/gi;
  let m;
  while ((m = re.exec(block)) !== null) {
    out[m[1]] = m[2].trim();
  }
  return out;
}

// First :root in file = light theme defaults.
const lightBlock = sliceBlock(CSS, /:root\s*\{/);
// Dark theme: prefer the explicit [data-theme="dark"] :root if present, else fall back to media.
let darkBlock = sliceBlock(CSS, /\[data-theme=["']dark["']\]\s*:root\s*\{|\[data-theme=["']dark["']\]\s*\{/);
if (!darkBlock) {
  const mediaIdx = CSS.search(/@media\s*\(\s*prefers-color-scheme\s*:\s*dark\s*\)/);
  if (mediaIdx >= 0) darkBlock = sliceBlock(CSS.slice(mediaIdx), /:root\s*\{/);
}

const light = tokens(lightBlock);
const dark = tokens(darkBlock);

function resolve(theme, name, fallbackTheme) {
  let v = theme[name] || (fallbackTheme && fallbackTheme[name]);
  if (!v) return null;
  let guard = 0;
  while (v && v.startsWith('var(') && guard++ < 10) {
    const inner = v.slice(4, v.lastIndexOf(')'));
    const [ref, fb] = inner.split(',').map(s => s.trim());
    const refName = ref.replace(/^--/, '');
    v = theme[refName] || (fallbackTheme && fallbackTheme[refName]) || (fb || null);
  }
  return v;
}
function asRgb(v) {
  if (!v) return null;
  v = v.trim();
  if (v.startsWith('#')) return hexToRgb(v);
  const m = v.match(/rgba?\(\s*([0-9]+)\s*,\s*([0-9]+)\s*,\s*([0-9]+)/);
  if (m) return [+m[1], +m[2], +m[3]];
  return null;
}

const WHITE = [255, 255, 255];

// --- assertions: per-theme contrast for the M1 top-BLOCKER pairs ---
const cases = [];
for (const [themeName, theme] of [['light', light], ['dark', dark]]) {
  const accent = asRgb(resolve(theme, 'accent', light));
  const green = asRgb(resolve(theme, 'status-green', light));
  const muted = asRgb(resolve(theme, 'text-muted', light));
  const surface0 = asRgb(resolve(theme, 'surface-0', light)) || asRgb(resolve(theme, 'bg', light));
  const surface1 = asRgb(resolve(theme, 'surface-1', light)) || asRgb(resolve(theme, 'surface', light));
  const surface2 = asRgb(resolve(theme, 'surface-2', light));

  // 1. white on --accent (path-hops .hop-named, hop-link chips, tab-btn.active,
  //    skip-link, button#fGroup.active — top BLOCKER pair, 649 hits)
  if (accent) {
    cases.push({
      name: `[${themeName}] #fff on --accent (${resolve(theme, 'accent', light)})`,
      ratio: contrast(WHITE, accent),
      min: 4.5,
    });
  }
  // 2. white on --status-green (.hash-cell-taken, .skew-badge--ok, ~755 hits)
  if (green) {
    cases.push({
      name: `[${themeName}] #fff on --status-green (${resolve(theme, 'status-green', light)})`,
      ratio: contrast(WHITE, green),
      min: 4.5,
    });
  }
  // 3. --text-muted on each surface (textmuted top selector by count; needs
  //    headroom across surface variants)
  for (const [bgName, bg] of [['surface-0', surface0], ['surface-1', surface1], ['surface-2', surface2]]) {
    if (muted && bg) {
      cases.push({
        name: `[${themeName}] --text-muted on --${bgName}`,
        ratio: contrast(muted, bg),
        min: 4.5,
      });
    }
  }
}

let failed = 0;
for (const c of cases) {
  const ok = c.ratio >= c.min;
  console.log(`${ok ? 'PASS' : 'FAIL'}  ${c.name.padEnd(60)} ratio=${c.ratio.toFixed(2)} (min ${c.min})`);
  if (!ok) failed++;
}
if (failed > 0) {
  console.error(`\n${failed} contrast assertion(s) failed.`);
  process.exit(1);
}
console.log(`\nAll ${cases.length} contrast assertions passed.`);

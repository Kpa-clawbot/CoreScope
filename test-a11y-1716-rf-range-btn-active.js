#!/usr/bin/env node
/**
 * test-a11y-1716-rf-range-btn-active.js — Issue #1716 regression gate.
 *
 * Scope: a single, focused assertion that `.rf-range-btn.active`
 * (rendered as `button[data-range="..."]` on the rf-health analytics tab)
 * resolves to a WCAG-AA contrast pair in BOTH themes.
 *
 * Why a dedicated test (separate from #1719's umbrella probe)?
 * The #1716 axe allowlist entry was scoped to exactly this selector on
 * exactly this route. PR #1720 subsumed the fix via the shared
 * `.btn-active-accent` consolidation. Before dropping the allowlist line,
 * we want a per-issue regression gate so a future refactor that breaks
 * just this one selector (e.g. someone splitting the rf-range-btn rule
 * back out and inlining `background:var(--accent)`) fails LOUDLY and
 * cites #1716, not just the omnibus #1719 banner.
 *
 * Asserts:
 *   1. `.rf-range-btn.active` is part of the consolidated active-button
 *      group (resolves background → --accent-strong AND color → --text-on-accent).
 *   2. The resolved pair achieves ≥ 4.5:1 in both light and dark themes.
 *   3. The legacy white-on-`--accent` pair (#fff on #4a9eff = 2.75:1) is
 *      NOT present anywhere targeting `.rf-range-btn.active`.
 *
 * If PR #1720's consolidation is reverted, assertion #1 or #2 trips
 * before any code reaches the allowlist.
 *
 * Pure CSS parse — no browser required. The live axe gate
 * (test-a11y-axe-1668.js) remains the umbrella net.
 *
 * Usage:  node test-a11y-1716-rf-range-btn-active.js
 */
'use strict';

const fs = require('fs');
const path = require('path');

const ROOT = __dirname;
const STYLE_CSS = process.env.STYLE_CSS_PATH || path.join(ROOT, 'public', 'style.css');

// ---- WCAG helpers (copied small, no shared import) ----
function parseColor(s) {
  if (!s) return null;
  s = String(s).trim();
  const NAMED = { white: '#ffffff', black: '#000000' };
  if (NAMED[s.toLowerCase()]) s = NAMED[s.toLowerCase()];
  let m = s.match(/^#([0-9a-f]{3})$/i);
  if (m) { const h = m[1]; return { r: parseInt(h[0]+h[0],16), g: parseInt(h[1]+h[1],16), b: parseInt(h[2]+h[2],16) }; }
  m = s.match(/^#([0-9a-f]{6})$/i);
  if (m) { const h = m[1]; return { r: parseInt(h.slice(0,2),16), g: parseInt(h.slice(2,4),16), b: parseInt(h.slice(4,6),16) }; }
  return null;
}
function srgbToLin(c) { c /= 255; return c <= 0.03928 ? c/12.92 : Math.pow((c+0.055)/1.055, 2.4); }
function relLum(rgb) { return 0.2126*srgbToLin(rgb.r) + 0.7152*srgbToLin(rgb.g) + 0.0722*srgbToLin(rgb.b); }
function contrast(a, b) {
  const L1 = relLum(a), L2 = relLum(b);
  const [lo, hi] = L1 < L2 ? [L1, L2] : [L2, L1];
  return (hi + 0.05) / (lo + 0.05);
}

// ---- CSS block scanner ----
function extractBlocks(css, selectorRegex) {
  // Find all CSS rule blocks whose selector list contains selectorRegex.
  const blocks = [];
  const ruleRe = /([^{}]+)\{([^{}]*)\}/g;
  let m;
  while ((m = ruleRe.exec(css)) !== null) {
    const selectors = m[1];
    const body = m[2];
    if (new RegExp(selectorRegex).test(selectors)) blocks.push({ selectors, body });
  }
  return blocks;
}
function extractVarDeclsFromBlocks(css, selectorRegex) {
  const out = {};
  for (const b of extractBlocks(css, selectorRegex)) {
    const re = /--([a-z0-9-]+)\s*:\s*([^;]+);/gi;
    let m;
    while ((m = re.exec(b.body)) !== null) out[m[1]] = m[2].trim();
  }
  return out;
}
function resolveVar(name, rootVars, themeVars) {
  const seen = new Set();
  function rec(raw) {
    raw = String(raw).trim().replace(/\s*!important\s*$/, '').trim();
    const m = raw.match(/^var\(\s*--([a-z0-9-]+)\s*(?:,\s*(.+))?\)$/i);
    if (!m) return raw;
    const n = m[1], fallback = m[2];
    if (seen.has(n)) throw new Error(`1716: var cycle on --${n}`);
    seen.add(n);
    if (themeVars[n]) return rec(themeVars[n]);
    if (rootVars[n]) return rec(rootVars[n]);
    if (fallback) return rec(fallback.trim());
    throw new Error(`1716: could not resolve var(--${n})`);
  }
  return rec(`var(--${name})`);
}

// ---- Test ----
function main() {
  const css = fs.readFileSync(STYLE_CSS, 'utf8');
  const failures = [];
  const passes = [];

  // Assertion 1: the `.rf-range-btn.active` selector must appear inside a
  // rule block whose body sets `background: var(--accent-strong)` and
  // `color: var(--text-on-accent)` — i.e. the consolidated active-button
  // pair from PR #1720.
  const blocks = extractBlocks(css, '\\.rf-range-btn\\.active');
  if (blocks.length === 0) {
    failures.push('A1: no CSS rule found targeting `.rf-range-btn.active`');
  } else {
    let consolidated = false;
    for (const b of blocks) {
      const bg = (b.body.match(/(?:^|[^-\w])background(?:-color)?\s*:\s*([^;]+);/) || [])[1];
      const fg = (b.body.match(/(?:^|[^-\w])color\s*:\s*([^;]+);/) || [])[1];
      if (bg && fg && /var\(--accent-strong\)/.test(bg) && /var\(--text-on-accent\)/.test(fg)) {
        consolidated = true;
        passes.push(`A1: .rf-range-btn.active routes to var(--accent-strong) + var(--text-on-accent)`);
        break;
      }
    }
    if (!consolidated) {
      failures.push('A1: .rf-range-btn.active is NOT routed through the consolidated (--accent-strong / --text-on-accent) pair — PR #1720 regression');
    }
  }

  // Assertion 2: legacy pair MUST NOT reappear — any block listing
  // `.rf-range-btn.active` whose body sets `background: var(--accent)`
  // (the legacy 2.75:1 token) + `color: #fff` is a regression.
  for (const b of blocks) {
    const bg = (b.body.match(/(?:^|[^-\w])background(?:-color)?\s*:\s*([^;]+);/) || [])[1];
    const fg = (b.body.match(/(?:^|[^-\w])color\s*:\s*([^;]+);/) || [])[1];
    if (!bg || !fg) continue;
    // Match var(--accent) (NOT --accent-strong) + literal white.
    if (/var\(--accent\)(?!-strong)/.test(bg) && /^\s*(#fff|#ffffff|white)\s*$/i.test(fg)) {
      failures.push(`A2: legacy 2.75:1 pair re-emerged on .rf-range-btn.active (bg=${bg.trim()} fg=${fg.trim()})`);
    }
  }
  if (failures.filter(f => f.startsWith('A2')).length === 0) passes.push('A2: no legacy var(--accent) + #fff pair on .rf-range-btn.active');

  // Assertion 3: numeric contrast probe (both themes).
  const rootVars = extractVarDeclsFromBlocks(css, ':root');
  const darkVars = extractVarDeclsFromBlocks(css, '\\.dark\\b');
  for (const [theme, themeVars] of [['light', {}], ['dark', darkVars]]) {
    let bgHex, fgHex;
    try {
      bgHex = resolveVar('accent-strong', rootVars, themeVars);
      fgHex = resolveVar('text-on-accent', rootVars, themeVars);
    } catch (e) {
      failures.push(`A3[${theme}]: ${e.message}`);
      continue;
    }
    const bg = parseColor(bgHex), fg = parseColor(fgHex);
    if (!bg || !fg) {
      failures.push(`A3[${theme}]: unparsable resolved colors bg=${bgHex} fg=${fgHex}`);
      continue;
    }
    const ratio = contrast(fg, bg);
    if (ratio < 4.5) {
      failures.push(`A3[${theme}]: contrast ${ratio.toFixed(2)}:1 < 4.5 (fg=${fgHex} bg=${bgHex})`);
    } else {
      passes.push(`A3[${theme}]: ${ratio.toFixed(2)}:1 (fg=${fgHex} bg=${bgHex})`);
    }
  }

  for (const p of passes) console.log('  PASS', p);
  for (const f of failures) console.log('  FAIL', f);
  if (failures.length) {
    console.log(`\nFAIL: ${failures.length} assertion(s) tripped on .rf-range-btn.active (issue #1716)`);
    process.exit(1);
  }
  console.log('\nPASS: .rf-range-btn.active gated by consolidated --accent-strong / --text-on-accent pair (issue #1716)');
}

main();

/**
 * test-issue-1668-m3-typography.js
 *
 * Milestone 3 of #1668 — enforce a readable typography floor on chips,
 * badges, table cells and meta labels flagged by the M1 a11y audit as
 * `thin-small` (font-size < 14px AND font-weight < 500).
 *
 * Floor (operator-locked):
 *   - body text >= 14px, OR
 *   - chip/badge/meta >= 12px AND weight >= 500
 *
 * This test scans the **last** CSS rule for each target selector in
 * public/style.css (and the inline <style> in public/audio-lab.js for
 * .alab-pkt) and asserts the M3 floor.
 */

'use strict';

const fs = require('fs');
const path = require('path');
const assert = require('assert');

const repoRoot = __dirname;
const cssPath = path.join(repoRoot, 'public', 'style.css');
const audioLabJsPath = path.join(repoRoot, 'public', 'audio-lab.js');
const css = fs.readFileSync(cssPath, 'utf8');
const audioLabJs = fs.readFileSync(audioLabJsPath, 'utf8');

// --- helpers ----------------------------------------------------------------

/**
 * Resolve a CSS length token (e.g. `12px`, `0.875rem`, `var(--fs-md)`,
 * `clamp(12px, ..., 14px)`) to a px floor used for the M3 assertion.
 *  - bare px / rem -> direct
 *  - clamp(a,b,c)  -> the FLOOR `a` (worst-case readability)
 *  - var(--token)  -> resolved from :root in style.css (we look up the
 *                     same value via clamp/raw conversion)
 *  - returns NaN if unresolvable.
 */
function resolveFontSizePx(value, allCss) {
  if (!value) return NaN;
  const v = value.trim();
  let m = v.match(/^([0-9.]+)px$/);
  if (m) return parseFloat(m[1]);
  m = v.match(/^([0-9.]+)rem$/);
  if (m) return parseFloat(m[1]) * 16;
  m = v.match(/^clamp\(\s*([0-9.]+)(px|rem)\s*,/);
  if (m) return m[2] === 'rem' ? parseFloat(m[1]) * 16 : parseFloat(m[1]);
  m = v.match(/^var\(\s*(--[a-z0-9-]+)\s*\)$/i);
  if (m) {
    const tok = m[1];
    // look up the token definition in :root
    const re = new RegExp(`${tok}\\s*:\\s*([^;]+);`);
    const def = allCss.match(re);
    if (def) return resolveFontSizePx(def[1].trim(), allCss);
  }
  return NaN;
}

function resolveFontWeight(value, allCss) {
  if (!value) return NaN;
  const v = value.trim();
  if (/^\d+$/.test(v)) return parseInt(v, 10);
  if (v === 'normal') return 400;
  if (v === 'bold') return 700;
  const m = v.match(/^var\(\s*(--[a-z0-9-]+)\s*\)$/i);
  if (m) {
    const tok = m[1];
    const re = new RegExp(`${tok}\\s*:\\s*([^;]+);`);
    const def = allCss.match(re);
    if (def) return resolveFontWeight(def[1].trim(), allCss);
  }
  return NaN;
}

/**
 * Find the LAST occurrence of `selector { ... }` in cssText and return its
 * raw body. Match selectors that contain the literal selector token; we
 * deliberately match across all rules so cascade-last wins.
 */
function lastRuleBody(cssText, selectorLiteral) {
  // Escape regex metachars in literal
  const esc = selectorLiteral.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  // Match a selector list that contains the literal as a whole token.
  // We allow combinators/parents/pseudo siblings but require the literal
  // to be present.
  const re = new RegExp(`([^{}]*${esc}[^{},]*)\\{([^}]*)\\}`, 'g');
  let last = null;
  let m;
  while ((m = re.exec(cssText)) !== null) {
    last = m[2];
  }
  return last;
}

function parseDecl(body, prop) {
  if (!body) return null;
  const re = new RegExp(`(?:^|;|\\{|\\s)${prop}\\s*:\\s*([^;}]+)`, 'i');
  // collect ALL declarations and return the LAST one (later wins in CSS)
  const all = [];
  const reAll = new RegExp(`${prop}\\s*:\\s*([^;}]+)`, 'gi');
  let m;
  while ((m = reAll.exec(body)) !== null) all.push(m[1].trim());
  return all.length ? all[all.length - 1] : null;
}

function effective(selector, cssText) {
  // Strict-match: only consider rules whose selector list contains the
  // literal as a STANDALONE selector (not as part of a descendant chain
  // like ".compare-tabs .tab-btn"). This makes the test deterministic and
  // forces the BASE rule for each chip/badge to hit the floor — which is
  // what we want: the M3 floor must hold even without contextual overrides.
  const esc = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const re = new RegExp(`([^{}]*)\\{([^}]*)\\}`, 'g');
  let fsRaw = null;
  let fwRaw = null;
  let m;
  while ((m = re.exec(cssText)) !== null) {
    let selList = m[1].trim();
    // strip leading/embedded /* ... */ block comments — they are not selectors
    selList = selList.replace(/\/\*[\s\S]*?\*\//g, '').trim();
    if (!selList || selList.startsWith('@')) continue;
    // split on commas, treat each selector independently
    const sels = selList.split(',').map((s) => s.trim());
    const hit = sels.some((s) => {
      // EXACT selector match only. Pseudo-class state (:hover/:focus) is
      // accepted because it doesn't change the resting visual; .modifier
      // and --bem-modifier chains are NOT accepted because they only apply
      // in some states and we must enforce the floor on the BASE rule too.
      if (s === selector) return true;
      if (s.startsWith(selector + ':')) return true;
      return false;
    });
    if (!hit) continue;
    const fs = parseDecl(m[2], 'font-size');
    const fw = parseDecl(m[2], 'font-weight');
    if (fs) fsRaw = fs;
    if (fw) fwRaw = fw;
  }
  return {
    fontSize: resolveFontSizePx(fsRaw, cssText),
    fontWeight: resolveFontWeight(fwRaw, cssText),
    rawFontSize: fsRaw,
    rawFontWeight: fwRaw,
  };
}

function passesFloor(fs, fw) {
  if (Number.isNaN(fs)) return false;
  if (fs >= 14) return true;
  if (fs >= 12 && !Number.isNaN(fw) && fw >= 500) return true;
  return false;
}

// --- target selectors (sourced from M1 audit thin-small findings) -----------

/**
 * Each entry: [selector, sourceCss, note]
 * sourceCss is either the main stylesheet (`css`) or an inline <style> in JS.
 */
const TARGETS = [
  // Top-10 by violation count from M1 audit reports/violations-summary.md
  ['.nav-link', css, 'global navbar link — 12.8px/400 on master'],
  ['.tab-btn', css, 'analytics + compare tabs — 13px/400 on master'],
  ['.alab-pkt', audioLabJs, 'audio-lab packet rows — 12px/400 on master'],
  ['.ch-item-time', css, 'channels list timestamps — 11px/400 on master'],
  ['.ch-item-preview', css, 'channels list preview line — 12px/400 on master'],
  ['.payload-bar-label', css, 'analytics payload labels — 12px/400 on master'],
  ['.stat-label', css, 'analytics stat label — 12px/400 on master'],
  ['.col-hidden-pill', css, 'nodes hidden-columns pill — 10px/700 on master'],
  ['.skew-badge', css, 'nodes clock-skew badge — 10px/600 on master'],
  ['.filter-group .btn', css, 'filter chips — 12px/400 on master'],
  // High-volume body-text offenders (593 + 1380+ M1 violations combined)
  ['.timestamp-text', css, 'every row timestamp — inherited 12px/400 on master'],
  ['.data-table', css, 'baseline for td / td.mono / td.col-pubkey — 12px/400 on master'],
];

// --- run --------------------------------------------------------------------

let failed = 0;
const rows = [];
for (const [sel, src, note] of TARGETS) {
  const e = effective(sel, src);
  const ok = passesFloor(e.fontSize, e.fontWeight);
  rows.push({ sel, fs: e.fontSize, fw: e.fontWeight, ok, note });
  if (!ok) failed++;
}

console.log('M3 typography floor check (>=14px OR >=12px+500):');
console.log('--------------------------------------------------');
for (const r of rows) {
  const status = r.ok ? 'PASS' : 'FAIL';
  console.log(
    `  [${status}] ${r.sel.padEnd(28)}  fs=${String(r.fs).padEnd(6)}  fw=${String(r.fw).padEnd(5)}  (${r.note})`
  );
}
console.log('--------------------------------------------------');

assert.strictEqual(
  failed,
  0,
  `M3 typography floor: ${failed}/${TARGETS.length} selectors below floor (see table above)`
);

console.log('OK — all targets satisfy the M3 typography floor.');

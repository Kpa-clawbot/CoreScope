#!/usr/bin/env node
/**
 * test-a11y-1719-contrast-root-causes-e2e.js — Issue #1719 regression gate.
 *
 * Asserts WCAG AA color-contrast (≥4.5:1 body text, ≥3:1 large) for the FOUR
 * recurring root-cause patterns identified in #1719 that were generating ~320
 * axe violations across the expanded axe gate (PR #1707/#1706):
 *
 *   Pattern 1 — Active button white-on-`--accent` (#4a9eff): 2.75:1.
 *               Surfaces: .rf-range-btn.active, .clock-filter-btn.active,
 *               .subpath-jump-nav a, #ptCheckBtn / #ptGenBtn (inline).
 *               Fix: consolidate to `--accent-strong` (#2563eb) +
 *               `--text-on-accent` (#f9fafb) — 8.59:1 (AA pass).
 *
 *   Pattern 2 — .skew-badge--no_clock: #fff on --text-muted (#a8b8cc dark
 *               theme legacy → bumped to #d1d5db) = 2.02:1 historic / still
 *               below AA against #fff. Fix: switch to a dedicated darker
 *               token so #fff text reads cleanly in both themes.
 *
 *   Pattern 3 — Neighbor-graph role swatches: room/sensor/observer hues that
 *               drop below AA when rendered as small label text on white
 *               (light theme). The fix uses the customizer's role hex map,
 *               so the assertion targets the customizer defaults directly.
 *
 *   Pattern 4 — `--status-green` (#22c55e) used as TEXT color on white
 *               .analytics-stat-card → 2.27:1. Fix: introduce
 *               `--status-green-text` (darker, AA-passing on light) and
 *               route text usages to it. The background swatch token
 *               (`--status-green`) is unchanged.
 *
 * This test is CSS-driven (parses public/style.css + scans the customizer
 * defaults) so it runs without a browser. The umbrella axe gate
 * (test-a11y-axe-1668.js) is the live-browser net; this test exists so the
 * four patterns above stay tracked even when CI chromium is broken in the
 * sandbox.
 *
 * Usage:  node test-a11y-1719-contrast-root-causes-e2e.js
 */
'use strict';

const fs = require('fs');
const path = require('path');

const ROOT = __dirname;
const STYLE_CSS = process.env.STYLE_CSS_PATH || path.join(ROOT, 'public', 'style.css');
const CUSTOMIZE_JS = path.join(ROOT, 'public', 'customize.js');

// -------------------- Pure helpers (WCAG composite-contrast) --------------------

function parseColor(s) {
  if (!s) return null;
  s = String(s).trim();
  // Minimal CSS named-color set we expect on active-button surfaces.
  const NAMED = { white: '#ffffff', black: '#000000', transparent: 'rgba(0,0,0,0)' };
  if (NAMED[s.toLowerCase()]) s = NAMED[s.toLowerCase()];
  let m = s.match(/^#([0-9a-f]{3})$/i);
  if (m) { const h = m[1]; return { r: parseInt(h[0]+h[0],16), g: parseInt(h[1]+h[1],16), b: parseInt(h[2]+h[2],16), a: 1 }; }
  m = s.match(/^#([0-9a-f]{6})$/i);
  if (m) { const h = m[1]; return { r: parseInt(h.slice(0,2),16), g: parseInt(h.slice(2,4),16), b: parseInt(h.slice(4,6),16), a: 1 }; }
  m = s.match(/^rgba?\(([^)]+)\)$/i);
  if (m) {
    const parts = m[1].split(/[ ,/]+/).filter(Boolean).map(Number);
    if (parts.length >= 3) return { r: parts[0], g: parts[1], b: parts[2], a: parts.length >= 4 && Number.isFinite(parts[3]) ? parts[3] : 1 };
  }
  return null;
}
function composite(fg, bg) {
  const a = fg.a;
  return { r: Math.round(fg.r*a + bg.r*(1-a)), g: Math.round(fg.g*a + bg.g*(1-a)), b: Math.round(fg.b*a + bg.b*(1-a)), a: 1 };
}
function srgbToLin(c) { c /= 255; return c <= 0.03928 ? c/12.92 : Math.pow((c+0.055)/1.055, 2.4); }
function relLum(rgb) { return 0.2126*srgbToLin(rgb.r) + 0.7152*srgbToLin(rgb.g) + 0.0722*srgbToLin(rgb.b); }
function contrast(a, b) {
  const L1 = relLum(a), L2 = relLum(b);
  const [lo, hi] = L1 < L2 ? [L1, L2] : [L2, L1];
  return (hi + 0.05) / (lo + 0.05);
}
function compositeContrast(fg, bg) {
  const eff = fg.a < 1 ? composite(fg, bg) : fg;
  return contrast(eff, bg);
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
function inBlock(b, name) { const m = b.match(new RegExp(`--${name}\\s*:\\s*([^;\\n]+);`)); return m ? m[1].trim() : null; }
function lookupVar(blocks, name) { let v = null; for (const b of blocks) { const x = inBlock(b, name); if (x) v = x; } return v; }

function makeResolver(rootBlocks, themeBlocks) {
  return function resolve(raw) {
    raw = String(raw).trim().replace(/\s*!important\s*$/, '').trim();
    const m = raw.match(/^var\(\s*--([a-z0-9-]+)\s*(?:,\s*(.+))?\)$/i);
    if (!m) return raw;
    const name = m[1], fallback = m[2];
    const fromTheme = lookupVar(themeBlocks, name);
    if (fromTheme) return resolve(fromTheme);
    const fromRoot = lookupVar(rootBlocks, name);
    if (fromRoot) return resolve(fromRoot);
    if (fallback) return resolve(fallback.trim());
    throw new Error(`a11y-1719: could not resolve var(--${name})`);
  };
}

// -------------------- Per-pattern probes --------------------

function probeP1_activeButton(css, rootBlocks, darkBlocks) {
  // Consolidation invariant: the shared button-active style (or every
  // legacy duplicate) must paint background:--accent-strong + color:
  // --text-on-accent, NOT background:--accent + color:#fff.
  //
  // We check every selector listed in the issue. If ANY of them still
  // resolves to white-on-#4a9eff (~2.75:1) the assertion trips.
  const selectors = [
    '\\.rf-range-btn\\.active',
    '\\.clock-filter-btn\\.active',
    '\\.subpath-jump-nav a',          // sp-pairs/triples/quads/long jump pills
    '\\.btn-active-accent',           // shared class introduced by this fix
    // Round-1 polish: three additional surfaces still emitting #fff on
    // var(--accent) (=2.75:1). Added to the consolidated selector group.
    '\\.node-filter-option\\.node-filter-active',
    '\\.subpath-selected',
    '\\.analytics-time-range button\\.active',
  ];
  const probes = [];
  for (const sel of selectors) {
    const blocks = extractBlocks(css, sel);
    if (blocks.length === 0) continue; // not all selectors must exist
    let bgRaw = null, colorRaw = null;
    for (const b of blocks) {
      const bm = b.match(/(?:^|[^-\w])background(?:-color)?\s*:\s*([^;]+);/);
      if (bm) bgRaw = bm[1].trim();
      const cm = b.match(/(?:^|[^-\w])color\s*:\s*([^;]+);/);
      if (cm) colorRaw = cm[1].trim();
    }
    if (!bgRaw || !colorRaw) continue;
    probes.push({ sel: sel.replace(/\\/g, ''), bgRaw, colorRaw });
  }
  if (probes.length === 0) throw new Error('a11y-1719 P1: no active-button selectors found');

  const results = [];
  for (const theme of ['light', 'dark']) {
    const resolve = makeResolver(rootBlocks, theme === 'dark' ? darkBlocks : []);
    for (const p of probes) {
      const bg = parseColor(resolve(p.bgRaw));
      const fg = parseColor(resolve(p.colorRaw));
      if (!bg || !fg) throw new Error(`a11y-1719 P1: unparsable color for ${p.sel} theme=${theme}`);
      results.push({ pattern: 'P1', sel: p.sel, theme, bg: resolve(p.bgRaw), fg: resolve(p.colorRaw), ratio: compositeContrast(fg, bg) });
    }
  }
  return results;
}

function probeP2_skewBadge(css, rootBlocks, darkBlocks) {
  // .skew-badge--no_clock { background: var(--text-muted); color: #fff; }
  // Need ≥4.5:1 in both themes (small text 12px).
  const blocks = extractBlocks(css, '\\.skew-badge--no_clock');
  if (blocks.length === 0) throw new Error('a11y-1719 P2: .skew-badge--no_clock missing');
  let bgRaw = null, colorRaw = null;
  for (const b of blocks) {
    const bm = b.match(/background(?:-color)?\s*:\s*([^;]+);/); if (bm) bgRaw = bm[1].trim();
    const cm = b.match(/(?:^|[^-\w])color\s*:\s*([^;]+);/); if (cm) colorRaw = cm[1].trim();
  }
  if (!bgRaw || !colorRaw) throw new Error('a11y-1719 P2: no bg/color in .skew-badge--no_clock');
  const results = [];
  for (const theme of ['light', 'dark']) {
    const resolve = makeResolver(rootBlocks, theme === 'dark' ? darkBlocks : []);
    const bg = parseColor(resolve(bgRaw));
    const fg = parseColor(resolve(colorRaw));
    results.push({ pattern: 'P2', sel: '.skew-badge--no_clock', theme, bg: resolve(bgRaw), fg: resolve(colorRaw), ratio: compositeContrast(fg, bg) });
  }
  return results;
}

function probeP3_roleSwatches() {
  // Neighbor-graph role checkboxes: <span style="color:${ROLE_COLOR}">.
  //
  // In LIGHT theme the customizer's nodeColors map (public/customize.js)
  // is the first-paint default — its hex values (#16a34a / #d97706 /
  // #8b5cf6) were the measured BLOCKERs against the white analytics
  // surface (#ffffff).
  //
  // In DARK theme the live ROLE_COLORS getter resolves from the CB-safe
  // --mc-role-* palette (style.css :root + dark overrides), NOT from the
  // customizer's static JS defaults — so probing the customize.js values
  // against a dark card-bg would be a false probe. We assert only against
  // the light-theme rendering surface where axe found the violations.
  const js = fs.readFileSync(CUSTOMIZE_JS, 'utf8');
  const m = js.match(/nodeColors\s*:\s*\{([^}]+)\}/);
  if (!m) throw new Error('a11y-1719 P3: nodeColors block not found in customize.js');
  const body = m[1];
  const get = (k) => { const r = body.match(new RegExp(`${k}\\s*:\\s*['"](#[0-9a-fA-F]{3,8})['"]`)); return r ? r[1] : null; };
  const colors = { room: get('room'), sensor: get('sensor'), observer: get('observer') };
  for (const k of ['room', 'sensor', 'observer']) {
    if (!colors[k]) throw new Error(`a11y-1719 P3: missing ${k} in nodeColors`);
  }
  const lightBg = { r: 255, g: 255, b: 255, a: 1 };
  const results = [];
  for (const k of ['room', 'sensor', 'observer']) {
    const fg = parseColor(colors[k]);
    results.push({ pattern: 'P3', sel: `nodeColors.${k} on white`, theme: 'light', fg: colors[k], bg: '#ffffff', ratio: contrast(fg, lightBg) });
  }
  return results;
}

function probeP4_statusGreenText(css, rootBlocks, darkBlocks) {
  // The text usages (analytics.js inline `color:var(--status-green)` on a
  // white .analytics-stat-card) should now resolve to a darker token.
  // We probe `--status-green-text` if defined; otherwise fall back to
  // `--status-green` (which is the current broken state — triggers FAIL).
  const tokenName = lookupVar(rootBlocks, 'status-green-text') ? 'status-green-text' : 'status-green';
  const results = [];
  for (const theme of ['light', 'dark']) {
    const resolve = makeResolver(rootBlocks, theme === 'dark' ? darkBlocks : []);
    const fg = parseColor(resolve(`var(--${tokenName})`));
    // Card bg per theme.
    const cardBg = resolve(`var(--card-bg)`);
    const cardBgC = parseColor(cardBg) || (theme === 'dark' ? { r: 0x23, g: 0x23, b: 0x40, a: 1 } : { r: 255, g: 255, b: 255, a: 1 });
    results.push({ pattern: 'P4', sel: `.analytics-stat-card text color (--${tokenName})`, theme, fg: resolve(`var(--${tokenName})`), bg: cardBg, ratio: contrast(fg, cardBgC) });
  }
  return results;
}

// -------------------- Main --------------------

function probeP5_themeMapHasStatusGreenText() {
  // Round-1 polish MAJOR 2: customize.js THEME_CSS_MAP must include
  // status-green-text so operators can override the new token from the
  // customize panel and themed previews track it. Pure structural assertion
  // on the JS source — no execution.
  const js = fs.readFileSync(CUSTOMIZE_JS, 'utf8');
  const m = js.match(/THEME_CSS_MAP\s*=\s*\{([\s\S]*?)\n\s*\};/);
  if (!m) throw new Error('a11y-1719 P5: THEME_CSS_MAP block not found in customize.js');
  const body = m[1];
  const ok = /['"]?--status-green-text['"]?/.test(body) || /statusGreenText\s*:\s*['"]--status-green-text['"]/.test(body);
  return [{ pattern: 'P5', sel: 'THEME_CSS_MAP entry for --status-green-text', theme: 'n/a', fg: '-', bg: '-', ratio: ok ? 999 : 0, _structural: true }];
}

function probeP6_btnActiveAccentClassApplied() {
  // Round-1 polish MAJOR 3: ensure #ptCheckBtn and #ptGenBtn carry the
  // shared `btn-active-accent` class wherever they are emitted in
  // public/*.js (analytics.js is the current owner). Future refactors
  // that drop the class will be caught here.
  const ANALYTICS_JS = path.join(ROOT, 'public', 'analytics.js');
  const src = fs.readFileSync(ANALYTICS_JS, 'utf8');
  const results = [];
  for (const id of ['ptCheckBtn', 'ptGenBtn']) {
    // Find any <button ... id="ID" ...> emission and verify the class attr
    // includes btn-active-accent.
    const re = new RegExp(`<button\\b[^>]*\\bid=["']${id}["'][^>]*>`, 'g');
    let m, found = 0, withClass = 0;
    while ((m = re.exec(src))) {
      found++;
      if (/\bclass=["'][^"']*\bbtn-active-accent\b/.test(m[0])) withClass++;
    }
    if (found === 0) throw new Error(`a11y-1719 P6: no <button id="${id}"> emission found in public/analytics.js`);
    const ok = found === withClass;
    results.push({ pattern: 'P6', sel: `<button id="${id}"> has class="btn-active-accent"`, theme: 'n/a', fg: '-', bg: '-', ratio: ok ? 999 : 0, _structural: true, _detail: `${withClass}/${found} emissions` });
  }
  return results;
}

function main() {
  const css = fs.readFileSync(STYLE_CSS, 'utf8');
  const rootBlocks = extractBlocks(css, ':root');
  const darkBlocks = extractBlocks(css, '\\[data-theme="dark"\\]')
    .concat(extractBlocks(css, ':root:not\\(\\[data-theme="light"\\]\\)'));
  if (rootBlocks.length === 0) throw new Error('a11y-1719: no :root blocks');
  if (darkBlocks.length === 0) throw new Error('a11y-1719: no dark theme blocks');

  const all = []
    .concat(probeP1_activeButton(css, rootBlocks, darkBlocks))
    .concat(probeP2_skewBadge(css, rootBlocks, darkBlocks))
    .concat(probeP3_roleSwatches())
    .concat(probeP4_statusGreenText(css, rootBlocks, darkBlocks))
    .concat(probeP5_themeMapHasStatusGreenText())
    .concat(probeP6_btnActiveAccentClassApplied());

  let failures = 0;
  for (const r of all) {
    if (r._structural) {
      const ok = r.ratio >= 999;
      const tag = ok ? 'PASS' : 'FAIL';
      console.log(`  ${tag} [${r.pattern}] ${r.sel}${r._detail ? '  ' + r._detail : ''}`);
      if (!ok) failures++;
      continue;
    }
    // Small text (≥body) → AA needs 4.5:1.
    const ok = r.ratio >= 4.5;
    const tag = ok ? 'PASS' : 'FAIL';
    console.log(`  ${tag} [${r.pattern}] theme=${r.theme} ${r.sel}  fg=${r.fg} bg=${r.bg}  ratio=${r.ratio.toFixed(2)}:1`);
    if (!ok) failures++;
  }
  if (failures > 0) {
    console.error(`\nFAIL: ${failures} contrast probe(s) below WCAG AA 4.5:1 (issue #1719)`);
    process.exit(1);
  }
  console.log(`\nPASS: all 4 root-cause patterns ≥ 4.5:1 in both themes (issue #1719)`);
}

if (require.main === module) {
  try { main(); }
  catch (e) { console.error('test-a11y-1719 fatal:', e && e.stack || e); process.exit(2); }
}

module.exports = { parseColor, contrast, compositeContrast, extractBlocks };

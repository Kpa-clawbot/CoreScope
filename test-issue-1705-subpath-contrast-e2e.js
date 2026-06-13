#!/usr/bin/env node
/* Issue #1705 — WCAG AA contrast E2E guard for `.subpath-selected .hop-prefix`.
 *
 * Companion to test-issue-1705-subpath-contrast.js (parser-only). The
 * parser test is a fast pre-CI smoke; it inspects DECLARED cascade in
 * style.css text but cannot resolve real browser specificity, `!important`
 * ordering, or `color: inherit` resolution. A higher-specificity
 * `.subpath-selected .hop-prefix` rule could win in the real browser
 * while the parser test still happily asserts the canonical token pair.
 *
 * This test loads public/style.css into a real (headless) Chromium and
 * uses getComputedStyle() on a `<tr class="subpath-selected">` with a
 * `<span class="hop-prefix">` child — the exact DOM shape rendered by
 * public/analytics.js. It then computes the sRGB-composited WCAG ratio
 * between the resolved foreground and the resolved background in both
 * light (`:root`) and dark (`[data-theme="dark"]`) themes and asserts
 * >=4.5:1.
 *
 * Why this catches what the parser test cannot:
 *   - `getComputedStyle` resolves `color: inherit` through the real
 *     cascade (parser fakes it).
 *   - Any later rule that overrides `.subpath-selected` color/background
 *     at higher specificity is observed live (parser only sees the
 *     selector-shaped match).
 *   - rgba()/transparent foregrounds are composited the same way the
 *     browser would render them.
 *
 * Designed to be runnable in CI; degrades to SKIP if Chromium cannot
 * launch (mirrors test-logo-theme-e2e.js policy). Set CHROMIUM_REQUIRE=1
 * to convert SKIP -> FAIL.
 */
'use strict';

const fs = require('fs');
const path = require('path');
const { chromium } = require('playwright');

const CSS_PATH = path.resolve(__dirname, 'public', 'style.css');

function fail(msg) {
  console.error(`test-issue-1705-subpath-contrast-e2e.js: FAIL — ${msg}`);
  process.exit(1);
}

// WCAG sRGB relative luminance + contrast.
function relLum([r, g, b]) {
  const f = (c) => {
    c /= 255;
    return c <= 0.03928 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4);
  };
  return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b);
}
function contrast(fg, bg) {
  const L1 = relLum(fg), L2 = relLum(bg);
  const [hi, lo] = L1 >= L2 ? [L1, L2] : [L2, L1];
  return (hi + 0.05) / (lo + 0.05);
}
// Parse "rgb(r, g, b)" / "rgba(r, g, b, a)" from getComputedStyle.
function parseRGB(s) {
  const m = s && s.match(/^rgba?\(\s*([0-9.]+)\s*,\s*([0-9.]+)\s*,\s*([0-9.]+)\s*(?:,\s*([0-9.]+))?\s*\)$/);
  if (!m) return null;
  return [Math.round(+m[1]), Math.round(+m[2]), Math.round(+m[3]), m[4] != null ? parseFloat(m[4]) : 1];
}
function composite(fg, bg) {
  const a = fg[3];
  return [
    Math.round(fg[0] * a + bg[0] * (1 - a)),
    Math.round(fg[1] * a + bg[1] * (1 - a)),
    Math.round(fg[2] * a + bg[2] * (1 - a)),
    1,
  ];
}
const hex = (c) => `#${c.slice(0, 3).map(v => v.toString(16).padStart(2, '0')).join('')}`;

async function main() {
  if (!fs.existsSync(CSS_PATH)) {
    fail(`style.css not found at ${CSS_PATH}`);
  }

  const requireChromium = process.env.CHROMIUM_REQUIRE === '1';
  let browser;
  try {
    browser = await chromium.launch({
      headless: true,
      executablePath: process.env.CHROMIUM_PATH || undefined,
      args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
    });
  } catch (err) {
    if (requireChromium) {
      fail(`Chromium required but unavailable: ${err.message}`);
    }
    console.log(`test-issue-1705-subpath-contrast-e2e.js: SKIP (Chromium unavailable: ${err.message.split('\n')[0]})`);
    process.exit(0);
  }

  // Reproduce the DOM rendered by public/analytics.js (renderTable +
  // click handler that adds `.subpath-selected` to a <tr data-hops>).
  // The .subpaths-table class is mirrored from the section structure in
  // analytics.js (the table itself uses .analytics-table but the
  // class-name regression class is identical: any future rule scoped to
  // either selector is exercised).
  const cssHref = 'file://' + CSS_PATH;
  const html = `<!doctype html>
<html><head>
  <meta charset="utf-8">
  <link rel="stylesheet" href="${cssHref}">
  <title>1705 e2e</title>
</head>
<body>
  <table class="analytics-table subpaths-table">
    <thead><tr><th>#</th><th>Route</th><th>Count</th></tr></thead>
    <tbody>
      <tr data-hops="aa,bb" class="subpath-selected">
        <td>1</td>
        <td>node-a → node-b<br><span class="hop-prefix mono" id="probe-prefix">aa → bb</span></td>
        <td>42</td>
      </tr>
    </tbody>
  </table>
</body></html>`;

  let failures = 0;
  try {
    for (const theme of ['light', 'dark']) {
      const context = await browser.newContext({ viewport: { width: 1280, height: 720 } });
      const page = await context.newPage();
      // Use file:// origin so the file:// stylesheet link is allowed.
      await page.goto('about:blank');
      await page.setContent(html, { waitUntil: 'load' });
      // setContent loads with about:blank origin which cannot fetch file://.
      // Inline the stylesheet contents instead — same correctness because
      // we read public/style.css directly off disk.
      const cssText = fs.readFileSync(CSS_PATH, 'utf8');
      await page.evaluate((css) => {
        const styleEl = document.createElement('style');
        styleEl.textContent = css;
        document.head.appendChild(styleEl);
      }, cssText);
      // Apply the theme attribute the same way customize.js does.
      await page.evaluate((t) => {
        if (t === 'dark') document.documentElement.setAttribute('data-theme', 'dark');
        else document.documentElement.removeAttribute('data-theme');
      }, theme);
      // Force a layout flush.
      await page.evaluate(() => document.body.getBoundingClientRect());

      const probe = await page.evaluate(() => {
        const tr = document.querySelector('tr.subpath-selected');
        const span = document.getElementById('probe-prefix');
        if (!tr || !span) return { error: 'fixture DOM missing' };
        const trStyle = getComputedStyle(tr);
        const spanStyle = getComputedStyle(span);
        return {
          trBg: trStyle.backgroundColor,
          trColor: trStyle.color,
          spanColor: spanStyle.color,
          spanBg: spanStyle.backgroundColor,
        };
      });
      if (probe.error) fail(`[${theme}] ${probe.error}`);

      const fgRaw = parseRGB(probe.spanColor);
      const bgRaw = parseRGB(probe.trBg);
      if (!fgRaw) fail(`[${theme}] could not parse computed span color "${probe.spanColor}"`);
      if (!bgRaw) fail(`[${theme}] could not parse computed tr background "${probe.trBg}"`);

      // If parent bg is transparent (no .subpath-selected background
      // declared / overridden away), walk up to body to find an opaque
      // ancestor. Browsers paint against the next opaque layer.
      let bg = bgRaw;
      if (bg[3] < 1) {
        const opaque = await page.evaluate(() => {
          let n = document.querySelector('tr.subpath-selected').parentElement;
          while (n) {
            const s = getComputedStyle(n).backgroundColor;
            const m = s.match(/^rgba?\(\s*([0-9.]+)\s*,\s*([0-9.]+)\s*,\s*([0-9.]+)\s*(?:,\s*([0-9.]+))?\s*\)$/);
            if (m && (m[4] == null || parseFloat(m[4]) >= 1)) return s;
            n = n.parentElement;
          }
          // Fall back to white if we run off the top.
          return 'rgb(255, 255, 255)';
        });
        bg = parseRGB(opaque) || [255, 255, 255, 1];
      }
      const fg = fgRaw[3] < 1 ? composite(fgRaw, bg) : fgRaw;
      const ratio = contrast(fg, bg);
      const ok = ratio >= 4.5;
      console.log(
        `${ok ? '✓' : '✗'} ${theme.padEnd(5)} computed span color=${probe.spanColor} on tr bg=${probe.trBg}  ` +
        `composed=${hex(fg)} on ${hex(bg)}  ratio=${ratio.toFixed(2)}:1  (need ≥4.5)`
      );
      if (!ok) {
        failures++;
        console.error(
          `#1705 regression: real browser getComputedStyle in ${theme} theme gives ${ratio.toFixed(2)}:1, below WCAG AA 4.5:1 ` +
          `(composed ${hex(fg)} on ${hex(bg)})`
        );
      }
      await context.close();
    }
  } finally {
    await browser.close();
  }

  if (failures > 0) process.exit(1);
  console.log(`\n✅ PASS — .subpath-selected .hop-prefix ≥4.5:1 in both themes (real browser getComputedStyle)`);
}

main().catch((err) => {
  console.error(`test-issue-1705-subpath-contrast-e2e.js: ERROR — ${err && err.stack || err}`);
  process.exit(1);
});

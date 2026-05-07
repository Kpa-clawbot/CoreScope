/**
 * #1146 — "Paths Through This Node" path-link contrast E2E.
 *
 * Bug: Path entries inside the node-detail "Paths Through This Node"
 * section are rendered as <div> blocks, not a <table>. The existing
 * `.node-detail-section .data-table td a { color: var(--accent) }`
 * rule (style.css:1231) doesn't apply, so the path-hop <a> elements
 * fall back to UA-default `rgb(0,0,238)` blue. On dark theme, that
 * blue against `var(--card-bg)` (#1a1a2e) computes to ~3.0:1 — a
 * WCAG AA failure (4.5:1 required for body text).
 *
 * This test loads a node detail page, mocks the /paths API to return
 * a deterministic chain with at least one named hop, switches to dark
 * theme, then asserts the computed link colour vs. its background
 * yields a contrast ratio ≥ 4.5:1.
 *
 * Currently FAILS (link color resolves to rgb(0,0,238)).
 * After the style.css fix it PASSES.
 *
 * Usage: BASE_URL=http://localhost:13581 node test-issue-1146-path-link-contrast-e2e.js
 */
'use strict';
const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';

let passed = 0, failed = 0;
async function step(name, fn) {
  try { await fn(); passed++; console.log('  ✓ ' + name); }
  catch (e) { failed++; console.error('  ✗ ' + name + ': ' + e.message); }
}
function assert(c, m) { if (!c) throw new Error(m || 'assertion failed'); }

// WCAG 2.1 relative luminance + contrast ratio.
function srgbToLin(c) {
  c = c / 255;
  return c <= 0.03928 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4);
}
function lum(rgb) {
  return 0.2126 * srgbToLin(rgb[0]) + 0.7152 * srgbToLin(rgb[1]) + 0.0722 * srgbToLin(rgb[2]);
}
function contrast(fg, bg) {
  const L1 = lum(fg), L2 = lum(bg);
  const hi = Math.max(L1, L2), lo = Math.min(L1, L2);
  return (hi + 0.05) / (lo + 0.05);
}
function parseRgb(s) {
  // Accept "rgb(r, g, b)" or "rgba(r, g, b, a)".
  const m = s.match(/rgba?\(\s*(\d+)\s*,\s*(\d+)\s*,\s*(\d+)/);
  if (!m) throw new Error('Cannot parse colour: ' + s);
  return [parseInt(m[1], 10), parseInt(m[2], 10), parseInt(m[3], 10)];
}
// Walk up parent chain to find the first non-transparent backgroundColor.
async function effectiveBgFor(page, selector) {
  return await page.evaluate((sel) => {
    let el = document.querySelector(sel);
    if (!el) return null;
    while (el) {
      const cs = getComputedStyle(el);
      const bg = cs.backgroundColor;
      const m = bg && bg.match(/rgba?\((\d+),\s*(\d+),\s*(\d+)(?:,\s*([\d.]+))?\)/);
      if (m) {
        const a = m[4] !== undefined ? parseFloat(m[4]) : 1;
        if (a > 0.01) return bg;
      }
      el = el.parentElement;
    }
    // Fallback: html background.
    return getComputedStyle(document.documentElement).backgroundColor || 'rgb(255,255,255)';
  }, selector);
}

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  const page = await ctx.newPage();
  page.setDefaultTimeout(15000);
  page.on('pageerror', (e) => console.error('[pageerror]', e.message));

  console.log(`\n=== #1146 path-link contrast E2E against ${BASE} ===`);

  const hopPubkey = 'a1b2c3d4e5f60718293a4b5c6d7e8f9001122334455667788990aabbccddeeff';

  // Mock paths API for ANY node so test is deterministic.
  await page.route('**/api/nodes/*/paths*', (route) => {
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        totalPaths: 1,
        totalTransmissions: 5,
        paths: [{
          hops: [
            { pubkey: hopPubkey, prefix: 'a1', name: 'TestHop' },
          ],
          count: 5,
          lastSeen: new Date().toISOString(),
          sampleHash: 'deadbeef00',
        }],
      }),
    });
  });

  await step('Load nodes page and force dark theme', async () => {
    await page.goto(BASE + '/#/nodes', { waitUntil: 'domcontentloaded' });
    await page.evaluate(() => {
      document.documentElement.setAttribute('data-theme', 'dark');
    });
    await page.waitForSelector('#nodesBody tr[data-key]', { timeout: 15000 });
  });

  await step('Open side panel for first node and wait for paths', async () => {
    await page.click('#nodesBody tr[data-key]');
    await page.waitForSelector('#pathsContent', { timeout: 10000 });
    await page.waitForFunction(
      () => {
        const el = document.getElementById('pathsContent');
        return el && el.querySelector('a[href^="#/nodes/"]');
      },
      { timeout: 15000 }
    );
  });

  await step('Path link contrast (#pathsContent a) ≥ 4.5:1 in dark mode', async () => {
    const linkColor = await page.$eval('#pathsContent a[href^="#/nodes/"]', (el) => getComputedStyle(el).color);
    const bgColor = await effectiveBgFor(page, '#pathsContent a[href^="#/nodes/"]');
    const fg = parseRgb(linkColor);
    const bg = parseRgb(bgColor);
    const ratio = contrast(fg, bg);
    console.log(`    link=${linkColor}  bg=${bgColor}  ratio=${ratio.toFixed(2)}:1`);
    assert(ratio >= 4.5,
      `Expected contrast ≥ 4.5:1 (WCAG AA), got ${ratio.toFixed(2)}:1 ` +
      `(link ${linkColor} on ${bgColor}). The path-link <a> elements are not ` +
      `covered by the .data-table td a rule and inherit UA blue.`);
  });

  await browser.close();

  console.log(`\n${passed} passed, ${failed} failed`);
  process.exit(failed === 0 ? 0 : 1);
})().catch((e) => { console.error(e); process.exit(1); });

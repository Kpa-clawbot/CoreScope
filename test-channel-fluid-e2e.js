/**
 * Issue #1057 — Channels page fluid layout E2E.
 *
 * For each viewport asserts:
 *   - No horizontal scroll on the body.
 *   - At ≥768px wide: both .ch-sidebar and .ch-main are visible AND occupy
 *     non-overlapping horizontal regions (true side-by-side).
 *   - At narrow (<700px) widths: layout stacks (sidebar above OR overlay).
 *
 * Usage: BASE_URL=http://localhost:13581 node test-channel-fluid-e2e.js
 */
'use strict';

const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:3000';

let passed = 0, failed = 0;
async function step(name, fn) {
  try { await fn(); passed++; console.log(`  ✅ ${name}`); }
  catch (e) { failed++; console.error(`  ❌ ${name}: ${e.message}`); }
}
function assert(c, m) { if (!c) throw new Error(m || 'assertion failed'); }

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });
  const ctx = await browser.newContext();
  const page = await ctx.newPage();
  page.setDefaultTimeout(8000);
  page.on('pageerror', (e) => console.error('[pageerror]', e.message));

  console.log(`\n=== #1057 Channels fluid layout E2E against ${BASE} ===`);

  async function loadChannels(w, h) {
    await page.setViewportSize({ width: w, height: h });
    await page.goto(BASE + '/#/channels', { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('.ch-sidebar', { timeout: 8000 });
    // Allow CSS layout/paint to settle.
    await page.waitForTimeout(150);
  }

  async function noBodyHScroll() {
    return page.evaluate(() => {
      // Allow ≤1px tolerance for sub-pixel rounding.
      return (document.documentElement.scrollWidth - document.documentElement.clientWidth) <= 1;
    });
  }

  async function rectOf(sel) {
    return page.evaluate((s) => {
      const el = document.querySelector(s);
      if (!el) return null;
      const r = el.getBoundingClientRect();
      const cs = window.getComputedStyle(el);
      return {
        x: r.x, y: r.y, w: r.width, h: r.height,
        visible: r.width > 0 && r.height > 0 && cs.display !== 'none' && cs.visibility !== 'hidden',
      };
    }, sel);
  }

  // Wide viewports — true side-by-side.
  for (const [w, h] of [[768, 900], [1080, 900], [1440, 900], [1920, 1080]]) {
    await step(`viewport ${w}×${h}: no horizontal scroll`, async () => {
      await loadChannels(w, h);
      assert(await noBodyHScroll(), 'document scrollWidth > clientWidth (horizontal scroll)');
    });

    await step(`viewport ${w}×${h}: sidebar AND message area both visible`, async () => {
      const sb = await rectOf('.ch-sidebar');
      const main = await rectOf('.ch-main');
      assert(sb && sb.visible, '.ch-sidebar not visible');
      assert(main && main.visible, '.ch-main not visible');
      // Sidebar should not consume more than ~45% of viewport width on wide screens.
      assert(sb.w <= w * 0.45 + 1,
        `sidebar too wide: ${sb.w}px / ${w}px viewport (>45%)`);
      // Message area should occupy meaningful remaining width (≥40% of viewport).
      assert(main.w >= w * 0.40,
        `message area too narrow: ${main.w}px / ${w}px viewport (<40%)`);
      // Side-by-side: main starts at/after sidebar's right edge (no overlap).
      assert(main.x + 1 >= sb.x + sb.w,
        `sidebar (x=${sb.x},w=${sb.w}) overlaps main (x=${main.x})`);
    });
  }

  // Narrow viewport — stacking (sidebar above main, or overlay/single-pane).
  await step('viewport 480×800: layout stacks (no side-by-side overflow)', async () => {
    await loadChannels(480, 800);
    assert(await noBodyHScroll(), 'narrow viewport caused horizontal scroll');
    const sb = await rectOf('.ch-sidebar');
    const main = await rectOf('.ch-main');
    assert(sb, '.ch-sidebar missing');
    // Either main is hidden/overlayed (single-pane mobile mode), OR
    // main is stacked below the sidebar (main.y >= sb.y + sb.h - tolerance).
    if (main && main.visible) {
      const stacked = main.y + 1 >= sb.y + sb.h
                   || sb.y + 1 >= main.y + main.h;
      const overlay = Math.abs(main.x - sb.x) < 5 && Math.abs(main.w - sb.w) < 5;
      assert(stacked || overlay,
        `narrow layout not stacked/overlayed: sb=${JSON.stringify(sb)} main=${JSON.stringify(main)}`);
    }
  });

  console.log(`\n${passed} passed, ${failed} failed`);
  await browser.close();
  process.exit(failed ? 1 : 0);
})().catch((e) => { console.error(e); process.exit(1); });

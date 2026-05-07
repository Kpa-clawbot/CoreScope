#!/usr/bin/env node
/* Issue #1139 Bug B — desktop "More ▾" overflow menu degenerate at 1101–1278px.
 *
 * Above 1100px the Priority+ measurement loop in public/app.js
 * (applyNavPriority) iteratively pushes one link at a time into the
 * overflow set until the inline strip "fits". At intermediate widths
 * (~1101-1278px) the loop terminates with exactly ONE link in
 * overflow — producing a degenerate "More ▾" dropdown that contains
 * just one item (often the active route).
 *
 * Acceptance for Bug B:
 *   For viewport widths 1101, 1150, 1200, 1240, 1278, 1280:
 *     navMoreMenu.children.length is either 0 (everything fits inline,
 *     More button hidden) or >= 2. NEVER exactly 1.
 *
 * This test FAILS on master @ origin/master (>=1 viewport returns 1)
 * and PASSES once the floor check is added to applyNavPriority().
 */
'use strict';

const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';
const VIEWPORTS = [1101, 1150, 1200, 1240, 1278, 1280, 1340, 1500, 1600, 1700];
const HEIGHT = 800;

async function main() {
  let browser;
  try {
    browser = await chromium.launch({
      headless: true,
      executablePath: process.env.CHROMIUM_PATH || undefined,
      args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
    });
  } catch (err) {
    if (process.env.CHROMIUM_REQUIRE === '1') {
      console.error(`test-nav-more-floor-1139-e2e.js: FAIL — Chromium required but unavailable: ${err.message}`);
      process.exit(1);
    }
    console.log(`test-nav-more-floor-1139-e2e.js: SKIP (Chromium unavailable: ${err.message.split('\n')[0]})`);
    process.exit(0);
  }

  let failures = 0;
  let passes = 0;
  const ctx = await browser.newContext();
  const page = await ctx.newPage();
  page.setDefaultTimeout(15000);

  for (const w of VIEWPORTS) {
    await page.setViewportSize({ width: w, height: HEIGHT });
    await page.goto(`${BASE}/#/home`, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('.top-nav .nav-links');
    await page.evaluate(() => document.fonts && document.fonts.ready ? document.fonts.ready : null);
    // Don't mutate stats text — leave whatever the page rendered. The
    // bug reproduces deterministically against fixture stats at ~1600px
    // (only "🎵 Lab" overflows → degenerate 1-item More menu) and the
    // sweep covers the prod-reported 1101-1278px band as well.
    await page.evaluate(() => { window.dispatchEvent(new Event('resize')); });
    // Two rAFs to let the rAF-debounced resize handler run + settle.
    await page.evaluate(() => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r))));
    await page.evaluate(() => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r))));
    await page.waitForFunction(() => {
      const el = document.querySelector('.top-nav .nav-right');
      if (!el) return false;
      const r1 = el.getBoundingClientRect();
      return new Promise((resolve) => {
        requestAnimationFrame(() => requestAnimationFrame(() => {
          const r2 = el.getBoundingClientRect();
          resolve(r1.right === r2.right && r1.left === r2.left);
        }));
      });
    }, null, { timeout: 5000 });

    const data = await page.evaluate(() => {
      const menu = document.getElementById('navMoreMenu');
      const wrap = document.querySelector('.nav-more-wrap');
      const moreVisible = wrap ? getComputedStyle(wrap).display !== 'none' : false;
      const items = menu ? menu.children.length : -1;
      const overflowInline = Array.from(document.querySelectorAll('.nav-links .nav-link.is-overflow'))
        .map(a => a.getAttribute('href'));
      return { items, moreVisible, overflowInline };
    });

    // Acceptance: items === 0 (everything fits) OR items >= 2.
    // items === 1 is the bug.
    const ok = data.items === 0 || data.items >= 2;
    if (ok) {
      passes++;
      console.log(`  ✅ ${w}px: more menu items=${data.items} moreVisible=${data.moreVisible}`);
    } else {
      failures++;
      console.log(`  ❌ ${w}px: degenerate More menu (items=${data.items}, overflow=[${data.overflowInline.join(', ')}])`);
    }
  }

  await browser.close();
  console.log(`\ntest-nav-more-floor-1139-e2e.js: ${failures === 0 ? 'OK' : 'FAIL'} — ${passes}/${VIEWPORTS.length} passed`);
  process.exit(failures === 0 ? 0 : 1);
}

main().catch(err => { console.error(err); process.exit(1); });

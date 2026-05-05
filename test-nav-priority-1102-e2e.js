#!/usr/bin/env node
/* Issue #1102 — Nav Priority+ at very wide widths and "More" menu correctness.
 *
 * Regression from PR #1097 polish: at all widths >=768px the CSS rule
 *   .nav-links a:not([data-priority="high"]) { display: none; }
 * unconditionally hides 6 of 11 links, even at 2560px where there is
 * plenty of room for everything. The "More" menu is built once on load
 * from the same selector, so it correctly shows the hidden links — but
 * the bug here is the SET being hidden is wrong (way too aggressive).
 *
 * Acceptance:
 *  - At 2560px: ALL 11 links visible inline AND "More ▾" hidden.
 *  - At 1920px: at least 9 links visible (room for most).
 *  - At 1080px: 5 high-priority links visible AND More menu contains
 *    every link not currently visible inline.
 *  - At 768px (just above hamburger threshold): 5 high-priority links
 *    visible AND More menu non-empty.
 */
'use strict';

const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';

// [width, expected behavior]
const CASES = [
  // viewport, minVisible, moreVisible, label
  { w: 2560, minVisible: 11, moreVisible: false, label: '2560px — all visible' },
  { w: 1920, minVisible: 9,  moreVisible: null,  label: '1920px — most visible' },
  { w: 1080, minVisible: 5,  moreVisible: true,  label: '1080px — collapsed' },
  { w: 800,  minVisible: 5,  moreVisible: true,  label: '800px — collapsed' },
];

const HEIGHT = 900;

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
      console.error(`test-nav-priority-1102-e2e.js: FAIL — Chromium required but unavailable: ${err.message}`);
      process.exit(1);
    }
    console.log(`test-nav-priority-1102-e2e.js: SKIP (Chromium unavailable: ${err.message.split('\n')[0]})`);
    process.exit(0);
  }

  let failures = 0;
  let passes = 0;
  const ctx = await browser.newContext();
  const page = await ctx.newPage();
  page.setDefaultTimeout(15000);

  for (const c of CASES) {
    await page.setViewportSize({ width: c.w, height: HEIGHT });
    await page.goto(`${BASE}/#/home`, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('.top-nav .nav-links');
    await page.evaluate(() => document.fonts && document.fonts.ready ? document.fonts.ready : null);
    // Settle layout (two consecutive frames identical for nav-right).
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
      const links = Array.from(document.querySelectorAll('.nav-links .nav-link'));
      const visible = links.filter(a => getComputedStyle(a).display !== 'none');
      const visibleHrefs = visible.map(a => a.getAttribute('href'));
      const allHrefs = links.map(a => a.getAttribute('href'));
      const hiddenInline = allHrefs.filter(h => !visibleHrefs.includes(h));
      const moreWrap = document.querySelector('.nav-more-wrap');
      const moreVisible = moreWrap ? getComputedStyle(moreWrap).display !== 'none' : false;
      const moreMenuLinks = Array.from(document.querySelectorAll('#navMoreMenu .nav-link'))
        .map(a => a.getAttribute('href'));
      return { totalLinks: links.length, visibleCount: visible.length,
               visibleHrefs, hiddenInline, moreVisible, moreMenuLinks };
    });

    const reasons = [];
    if (data.visibleCount < c.minVisible) {
      reasons.push(`only ${data.visibleCount}/${data.totalLinks} links visible (need >=${c.minVisible})`);
    }
    if (c.moreVisible === true && !data.moreVisible) {
      reasons.push(`"More" button should be visible but is hidden`);
    }
    if (c.moreVisible === false && data.moreVisible) {
      reasons.push(`"More" button should be HIDDEN at ${c.w}px (all links fit) but is visible`);
    }
    // More menu MUST contain every link not currently visible inline.
    if (data.moreVisible) {
      const missing = data.hiddenInline.filter(h => !data.moreMenuLinks.includes(h));
      if (missing.length) {
        reasons.push(`More menu missing hidden links: ${missing.join(', ')} ` +
                     `(menu has ${data.moreMenuLinks.length}, expected ${data.hiddenInline.length})`);
      }
    }

    const tag = c.label;
    if (reasons.length === 0) {
      passes++;
      console.log(`  ✅ ${tag}: visible=${data.visibleCount}/${data.totalLinks} more=${data.moreVisible} menu=${data.moreMenuLinks.length}`);
    } else {
      failures++;
      console.log(`  ❌ ${tag}: ${reasons.join(' | ')} ` +
                  `(visible=${data.visibleCount}/${data.totalLinks} more=${data.moreVisible} menu=${data.moreMenuLinks.length})`);
    }
  }

  await browser.close();
  console.log(`\ntest-nav-priority-1102-e2e.js: ${failures === 0 ? 'OK' : 'FAIL'} — ${passes}/${CASES.length} passed`);
  process.exit(failures === 0 ? 0 : 1);
}

main().catch((err) => {
  console.error('test-nav-priority-1102-e2e.js: fatal', err);
  process.exit(1);
});

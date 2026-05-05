#!/usr/bin/env node
/* Issue #1055 — Nav fluid Priority+ adaptation at all widths.
 *
 * Asserts the top-nav never overflows the viewport at common widths:
 * the right edge of `.nav-right` MUST be ≤ document.documentElement.clientWidth.
 *
 * Pre-fix behavior: the Priority+ collapse rule was scoped to
 * `(min-width: 768px) and (max-width: 1279px)`, so at 1280/1440/1920 the
 * full link strip + nav-stats + nav-right buttons could push past the
 * viewport's right edge (no collapse happened above 1279px).
 *
 * Post-fix: Priority+ collapses at all widths >=768px when needed.
 *
 * Run against a CoreScope server (defaults to localhost:13581 with the
 * E2E fixture DB, matching the playwright job in .github/workflows/deploy.yml).
 */
'use strict';

const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';
// Common widths the nav must stay clean at. 1280/1440 are the historic
// failure window: the Priority+ rule used to stop at 1279px but the full
// link strip + nav-right buttons don't fit on one row until ~1600px+.
const VIEWPORTS = [768, 1024, 1280, 1440, 1920];
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
    if (process.env.NAV_FLUID_REQUIRE === '1') throw err;
    console.log(`test-nav-fluid-1055-e2e.js: SKIP (Chromium unavailable: ${err.message.split('\n')[0]})`);
    process.exit(0);
  }

  let failures = 0;
  let passes = 0;
  const context = await browser.newContext();
  const page = await context.newPage();
  page.setDefaultTimeout(15000);

  for (const w of VIEWPORTS) {
    await page.setViewportSize({ width: w, height: HEIGHT });
    await page.goto(`${BASE}/#/home`, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('.top-nav .nav-right');
    // Give layout / web fonts a beat to settle.
    await page.evaluate(() => document.fonts && document.fonts.ready ? document.fonts.ready : null);
    await page.waitForTimeout(150);

    const data = await page.evaluate(() => {
      const navRight = document.querySelector('.top-nav .nav-right');
      const navLeft  = document.querySelector('.top-nav .nav-left');
      const topNav   = document.querySelector('.top-nav');
      const more     = document.querySelector('.nav-more-wrap');
      const moreCs   = more ? getComputedStyle(more) : null;
      const links    = Array.from(document.querySelectorAll('.nav-links .nav-link'));
      const visible  = links.filter(a => getComputedStyle(a).display !== 'none');
      const lastVisible = visible[visible.length - 1] || null;
      return {
        clientW:    document.documentElement.clientWidth,
        navScroll:  topNav.scrollWidth,
        navClient:  topNav.clientWidth,
        navRight:   navRight.getBoundingClientRect().right,
        navRightL:  navRight.getBoundingClientRect().left,
        navLeftR:   navLeft.getBoundingClientRect().right,
        lastLinkR:  lastVisible ? lastVisible.getBoundingClientRect().right : -1,
        moreVisible: moreCs ? moreCs.display !== 'none' : false,
        visibleLinks: visible.length,
        totalLinks: links.length,
      };
    });

    const tag = `viewport ${w}px`;
    const reasons = [];
    // 1. .nav-right must not extend past the viewport's right edge.
    if (data.navRight > data.clientW + 0.5) {
      reasons.push(`nav-right.right=${data.navRight.toFixed(1)} > clientWidth=${data.clientW} ` +
                   `(excess ${(data.navRight - data.clientW).toFixed(1)}px)`);
    }
    // 2. The visible link strip must not overlap .nav-right (parent overflow:hidden
    //    masks this visually but it still hides the rightmost links — the actual bug).
    if (data.lastLinkR > data.navRightL + 0.5) {
      reasons.push(`last visible link right=${data.lastLinkR.toFixed(1)} > nav-right.left=${data.navRightL.toFixed(1)} ` +
                   `(${(data.lastLinkR - data.navRightL).toFixed(1)}px overlap)`);
    }
    // 3. The nav row itself must not require horizontal scrolling.
    if (data.navScroll > data.navClient + 0.5) {
      reasons.push(`top-nav scrollWidth=${data.navScroll} > clientWidth=${data.navClient}`);
    }

    if (reasons.length === 0) {
      passes++;
      console.log(`  ✅ ${tag}: clean (visible links ${data.visibleLinks}/${data.totalLinks}, more=${data.moreVisible})`);
    } else {
      failures++;
      console.log(`  ❌ ${tag}: ${reasons.join(' | ')} ` +
                  `(visible links ${data.visibleLinks}/${data.totalLinks}, more=${data.moreVisible})`);
    }
  }

  await browser.close();

  console.log(`\ntest-nav-fluid-1055-e2e.js: ${failures === 0 ? 'OK' : 'FAIL'} — ${passes}/${VIEWPORTS.length} passed`);
  process.exit(failures === 0 ? 0 : 1);
}

main().catch((err) => {
  console.error('test-nav-fluid-1055-e2e.js: fatal', err);
  process.exit(1);
});

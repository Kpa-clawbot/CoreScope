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
 *
 * CI gating: when CHROMIUM_REQUIRE=1 (set by the GH Actions workflow) a
 * missing/broken Chromium is a HARD FAIL — no soft-skip. Locally the
 * test is allowed to skip so devs without Playwright browsers installed
 * can still run other tests.
 */
'use strict';

const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';
// Common widths the nav must stay clean at. 1280/1440 are the historic
// failure window: the Priority+ rule used to stop at 1279px but the full
// link strip + nav-right buttons don't fit on one row until ~1600px+.
const VIEWPORTS = [768, 1024, 1280, 1440, 1920];
// Routes asserted at every viewport. The pre-#1097 version only checked
// /#/home, but the bug reproduces on every top-level page since they
// all share the same .top-nav. Cover the four primary routes.
const ROUTES = ['/#/home', '/#/packets', '/#/nodes', '/#/map'];
const HEIGHT = 900;
// Whitespace tolerance (px) for the overflow/overlap assertions.
// Browsers occasionally hand back layout coordinates with sub-pixel
// rounding noise (≈0.1–0.4px) even when the box model is clean. We
// allow up to 0.5px so the test doesn't false-fail on rounding while
// still catching real overlaps (the bug this guards against was
// ~20px). Tighter than 0.5 caused intermittent CI flakes; looser
// would risk masking 1px regressions.
const SUBPIXEL_TOL = 0.5;

async function main() {
  const requireChromium = process.env.CHROMIUM_REQUIRE === '1' ||
                          process.env.NAV_FLUID_REQUIRE === '1';
  let browser;
  try {
    browser = await chromium.launch({
      headless: true,
      executablePath: process.env.CHROMIUM_PATH || undefined,
      args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
    });
  } catch (err) {
    if (requireChromium) {
      console.error(`test-nav-fluid-1055-e2e.js: FAIL — Chromium required (CHROMIUM_REQUIRE=1) but unavailable: ${err.message}`);
      process.exit(1);
    }
    console.log(`test-nav-fluid-1055-e2e.js: SKIP (Chromium unavailable: ${err.message.split('\n')[0]})`);
    process.exit(0);
  }

  let failures = 0;
  let passes = 0;
  const context = await browser.newContext();
  const page = await context.newPage();
  page.setDefaultTimeout(15000);

  for (const route of ROUTES) {
    for (const w of VIEWPORTS) {
      await page.setViewportSize({ width: w, height: HEIGHT });
      await page.goto(`${BASE}${route}`, { waitUntil: 'domcontentloaded' });
      await page.waitForSelector('.top-nav .nav-right');
      // Wait for fonts (which affect text measurement) AND for the nav
      // layout to settle: the .nav-right bounding box must hold steady
      // for two consecutive animation frames at the same coordinates.
      // This replaces a magic 150ms sleep with a deterministic gate
      // that asserts what we actually care about (layout has stopped
      // moving) before measuring.
      await page.evaluate(() => document.fonts && document.fonts.ready ? document.fonts.ready : null);
      await page.waitForFunction(() => {
        const el = document.querySelector('.top-nav .nav-right');
        if (!el) return false;
        const r1 = el.getBoundingClientRect();
        return new Promise((resolve) => {
          requestAnimationFrame(() => requestAnimationFrame(() => {
            const r2 = el.getBoundingClientRect();
            resolve(r1.right === r2.right && r1.left === r2.left && r1.top === r2.top);
          }));
        });
      }, null, { timeout: 5000 });

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

      const tag = `${route} @ ${w}px`;
      const reasons = [];
      // 1. .nav-right must not extend past the viewport's right edge.
      if (data.navRight > data.clientW + SUBPIXEL_TOL) {
        reasons.push(`nav-right.right=${data.navRight.toFixed(1)} > clientWidth=${data.clientW} ` +
                     `(excess ${(data.navRight - data.clientW).toFixed(1)}px)`);
      }
      // 2. The visible link strip must not overlap .nav-right (parent overflow:hidden
      //    masks this visually but it still hides the rightmost links — the actual bug).
      if (data.lastLinkR > data.navRightL + SUBPIXEL_TOL) {
        reasons.push(`last visible link right=${data.lastLinkR.toFixed(1)} > nav-right.left=${data.navRightL.toFixed(1)} ` +
                     `(${(data.lastLinkR - data.navRightL).toFixed(1)}px overlap)`);
      }
      // 3. The nav row itself must not require horizontal scrolling.
      if (data.navScroll > data.navClient + SUBPIXEL_TOL) {
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
  }

  await browser.close();

  const total = ROUTES.length * VIEWPORTS.length;
  console.log(`\ntest-nav-fluid-1055-e2e.js: ${failures === 0 ? 'OK' : 'FAIL'} — ${passes}/${total} passed`);
  process.exit(failures === 0 ? 0 : 1);
}

main().catch((err) => {
  console.error('test-nav-fluid-1055-e2e.js: fatal', err);
  process.exit(1);
});

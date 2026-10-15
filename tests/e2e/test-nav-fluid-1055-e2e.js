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
// #1061: bottom-nav activates at max-width:768px and hides the top-nav.
// This test asserts top-nav layout stability — start at 769 to stay above
// that breakpoint. Below 768 the top nav is intentionally display:none.
const VIEWPORTS = [769, 900, 1024, 1101, 1250, 1280, 1440, 1920, 2560];
// Routes asserted at every viewport. The pre-#1097 version only checked
// /#/home, but the bug reproduces on every top-level page since they
// all share the same .top-nav. Include non-primary active routes too.
const ROUTES = ['/#/home', '/#/packets', '/#/nodes', '/#/map', '/#/analytics?tab=nodes', '/#/scope-audit'];
const HIGH_PRIORITY_HREFS = ['#/home', '#/packets', '#/map', '#/live', '#/nodes'];
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
  // Include tablet touch targets while checking the desktop nav breakpoint.
  const context = await browser.newContext({ hasTouch: true });
  // Account controls are optional in the fixture server but consume real nav
  // space on enabled sites. Keep counts wide enough to exercise that layout.
  await context.route('**/api/config/client', async route => {
    const response = await route.fetch();
    const config = await response.json();
    config.userManagement = { enabled: true };
    await route.fulfill({ json: config });
  });
  await context.route('**/api/auth/me', route => route.fulfill({
    status: 401, json: { error: 'Not logged in' },
  }));
  await context.route('**/api/stats', async route => {
    const response = await route.fetch();
    const stats = await response.json();
    Object.assign(stats, { totalPackets: 30000, totalNodes: 2500, totalObservers: 213 });
    await route.fulfill({ json: stats });
  });
  const page = await context.newPage();
  page.setDefaultTimeout(15000);

  for (const route of ROUTES) {
    for (const w of VIEWPORTS) {
      await page.setViewportSize({ width: w, height: HEIGHT });
      await page.goto(`${BASE}${route}`, { waitUntil: 'domcontentloaded' });
      await page.waitForSelector('.top-nav .nav-right');
      await page.waitForSelector('#accountToggle');
      await page.waitForFunction(() => document.getElementById('navStats').textContent.includes('30000'));
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
        return {
          clientW:    document.documentElement.clientWidth,
          navScroll:  topNav.scrollWidth,
          navClient:  topNav.clientWidth,
          navRight:   navRight.getBoundingClientRect().right,
          navRightL:  navRight.getBoundingClientRect().left,
          navLeftR:   navLeft.getBoundingClientRect().right,
          lastLinkR:  Math.max(...visible.map(a => a.getBoundingClientRect().right)),
          moreVisible: moreCs ? moreCs.display !== 'none' : false,
          moreLeft: more ? more.getBoundingClientRect().left : 0,
          moreRight: more ? more.getBoundingClientRect().right : 0,
          visibleHrefs: visible.map(a => a.getAttribute('href')),
          activeInline: visible.some(a => a.classList.contains('active')),
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
      if (data.moreVisible && data.lastLinkR > data.moreLeft + SUBPIXEL_TOL) {
        reasons.push(`last visible link right=${data.lastLinkR.toFixed(1)} overlaps More left=${data.moreLeft.toFixed(1)}`);
      }
      if (data.moreVisible && data.moreRight > data.navRightL + SUBPIXEL_TOL) {
        reasons.push(`More right=${data.moreRight.toFixed(1)} overlaps nav-right left=${data.navRightL.toFixed(1)}`);
      }
      if (!data.activeInline || HIGH_PRIORITY_HREFS.some(href => !data.visibleHrefs.includes(href))) {
        reasons.push('active route or primary navigation was hidden');
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

  // Auth changes happen without resize/hashchange. The new account label must
  // trigger the same layout pass immediately, without waiting for stats polling.
  await page.setViewportSize({ width: 1366, height: HEIGHT });
  // Scope Audit is the widest pinned non-primary route in the stock header.
  await page.goto(`${BASE}/#/scope-audit`, { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('#accountToggle');
  await page.waitForFunction(() => document.getElementById('navStats').textContent.includes('30000'));
  await page.evaluate(() => window.CSAuth.ready());
  await page.evaluate(() => window.CSAuth.setUser({ displayName: 'Long account name', role: 'member' }));
  await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
  const accountLayout = await page.evaluate(() => {
    const links = Array.from(document.querySelectorAll('.nav-links .nav-link'))
      .filter(a => getComputedStyle(a).display !== 'none');
    return {
      lastRight: Math.max(...links.map(a => a.getBoundingClientRect().right)),
      moreLeft: document.querySelector('.nav-more-wrap').getBoundingClientRect().left,
      name: document.querySelector('.nav-account-label').textContent,
    };
  });
  if (accountLayout.lastRight <= accountLayout.moreLeft + SUBPIXEL_TOL && accountLayout.name === 'Long account name') {
    passes++;
    console.log('  ✅ account label change refits navigation and preserves its full name');
  } else {
    failures++;
    console.log(`  ❌ account label change overlaps More: ${JSON.stringify(accountLayout)}`);
  }

  const resizeWidths = [769, 1280, 900, 1440, 1101, 1920, 1250];
  for (const width of resizeWidths) {
    await page.setViewportSize({ width, height: HEIGHT });
    await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
    await page.evaluate(() => Promise.all(document.querySelector('.top-nav')
      .getAnimations({ subtree: true }).map(animation => animation.finished.catch(() => {}))));
    const layout = await page.evaluate(() => {
      const visible = Array.from(document.querySelectorAll('.nav-links .nav-link'))
        .filter(a => getComputedStyle(a).display !== 'none');
      const more = document.querySelector('.nav-more-wrap');
      return {
        lastRight: Math.max(...visible.map(a => a.getBoundingClientRect().right)),
        moreLeft: more.getBoundingClientRect().left,
        moreVisible: getComputedStyle(more).display !== 'none',
        rightLeft: document.querySelector('.nav-right').getBoundingClientRect().left,
        controlsFit: Array.from(document.querySelectorAll('.nav-btn, .nav-account-btn'))
          .filter(e => e.getBoundingClientRect().width > 0)
          .every(e => e.getBoundingClientRect().width >= 48 && e.getBoundingClientRect().height >= 48),
      };
    });
    const boundary = layout.moreVisible ? layout.moreLeft : layout.rightLeft;
    const namedAccount = await page.getByRole('button', { name: 'Long account name', exact: true }).isVisible();
    if (layout.lastRight <= boundary + SUBPIXEL_TOL && layout.controlsFit && namedAccount) {
      passes++;
      console.log(`  ✅ resize to ${width}px: links fit, controls retain their size and account name`);
    } else {
      failures++;
      console.log(`  ❌ resize to ${width}px: ${JSON.stringify(layout)}, namedAccount=${namedAccount}`);
    }
  }
  await page.locator('#navMoreBtn').focus();
  await page.keyboard.press('Enter');
  const menuFocused = await page.evaluate(() => document.activeElement === document.querySelector('#navMoreMenu .nav-link'));
  await page.keyboard.press('Escape');
  const menuClosed = await page.locator('#navMoreBtn').getAttribute('aria-expanded') === 'false';
  if (menuFocused && menuClosed) {
    passes++;
    console.log('  ✅ More retains keyboard focus and Escape dismissal');
  } else {
    failures++;
    console.log(`  ❌ More keyboard interaction: focused=${menuFocused}, closed=${menuClosed}`);
  }

  await browser.close();

  const total = ROUTES.length * VIEWPORTS.length + 2 + resizeWidths.length;
  console.log(`\ntest-nav-fluid-1055-e2e.js: ${failures === 0 ? 'OK' : 'FAIL'} — ${passes}/${total} passed`);
  process.exit(failures === 0 ? 0 : 1);
}

main().catch((err) => {
  console.error('test-nav-fluid-1055-e2e.js: fatal', err);
  process.exit(1);
});

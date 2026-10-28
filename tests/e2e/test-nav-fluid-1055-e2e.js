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
const assert = require('node:assert/strict');

const BASE = process.env.BASE_URL || 'http://localhost:13581';
// Common widths the nav must stay clean at. 1280/1440 are the historic
// failure window: the Priority+ rule used to stop at 1279px but the full
// link strip + nav-right buttons don't fit on one row until ~1600px+.
// #1061: bottom-nav activates at max-width:768px and hides the top-nav.
// This test asserts top-nav layout stability — start at 769 to stay above
// that breakpoint. Below 768 the top nav is intentionally display:none.
const VIEWPORTS = [769, 1024, 1101, 1250, 1280, 1440, 1920];
// Routes asserted at every viewport. The pre-#1097 version only checked
// /#/home, but the bug reproduces on every top-level page since they
// all share the same .top-nav. Cover the four primary routes.
const ROUTES = ['/#/home', '/#/packets', '/#/nodes', '/#/map', '/#/analytics', '/#/scope-audit'];
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
  const context = await browser.newContext({ hasTouch: true });
  let configGate = Promise.resolve();
  let authGate = Promise.resolve({ status: 401, json: {} });
  await context.route('**/api/config/client', async route => {
    const response = await route.fetch();
    const config = await response.json();
    await configGate;
    config.userManagement = { enabled: true };
    await route.fulfill({ json: config });
  });
  await context.route('**/api/auth/me', async route => {
    const response = await authGate;
    if (response === 'offline') await route.abort('failed');
    else await route.fulfill(response);
  });
  await context.route('**/api/stats', async route => {
    const response = await route.fetch();
    const stats = await response.json();
    Object.assign(stats, { totalPackets: 30000, totalNodes: 2500, totalObservers: 213 });
    await route.fulfill({ json: stats });
  });
  let page = await context.newPage();
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
        reasons.push(`last link right=${data.lastLinkR.toFixed(1)} overlaps More left=${data.moreLeft.toFixed(1)}`);
      }
      if (data.moreVisible && data.moreRight > data.navRightL + SUBPIXEL_TOL) {
        reasons.push(`More right=${data.moreRight.toFixed(1)} overlaps controls left=${data.navRightL.toFixed(1)}`);
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

  // #2332: refitting an open menu must preserve the focused destination,
  // even when that destination moves back into the inline navigation.
  async function resizeAndRefit(width) {
    await page.setViewportSize({ width, height: HEIGHT });
    await page.evaluate(() => new Promise(resolve => {
      requestAnimationFrame(() => requestAnimationFrame(resolve));
    }));
  }
  await resizeAndRefit(1250);
  await page.locator('#navMoreBtn').click();
  const focusedHref = await page.locator('#navMoreMenu .nav-link').first().getAttribute('href');
  assert.equal(await page.evaluate(() => document.activeElement.getAttribute('href')), focusedHref);
  await resizeAndRefit(1249);
  assert.equal(await page.evaluate(() => document.activeElement.getAttribute('href')), focusedHref,
    'refitting an open More menu must retain its focused destination');
  assert.equal(await page.evaluate(() => document.getElementById('navMoreMenu').contains(document.activeElement)), true);
  assert.equal(await page.locator('#navMoreBtn').getAttribute('aria-expanded'), 'true');
  passes++;
  console.log('  ✅ More retains its focused destination after a refit');

  await resizeAndRefit(2560);
  assert.equal(await page.evaluate(href => document.activeElement ===
    [...document.querySelectorAll('.nav-links .nav-link')].find(link => link.getAttribute('href') === href), focusedHref), true,
  'focus must follow a More destination that moves inline');
  assert.equal(await page.locator('#navMoreMenu .nav-link').count(), 0);
  assert.equal(await page.locator('#navMoreMenu').evaluate(menu => menu.classList.contains('open')), false);
  assert.equal(await page.locator('#navMoreBtn').getAttribute('aria-expanded'), 'false');
  passes++;
  console.log('  ✅ Focus follows the inline destination and the empty More menu closes');

  await resizeAndRefit(1250);
  await page.locator('#navMoreBtn').click();
  await page.keyboard.press('Tab');
  const secondHref = await page.locator('#navMoreMenu .nav-link').nth(1).getAttribute('href');
  assert.equal(await page.evaluate(() => document.activeElement.getAttribute('href')), secondHref);
  await resizeAndRefit(1249);
  assert.equal(await page.evaluate(() => document.activeElement.getAttribute('href')), secondHref,
    'refitting More must retain a later destination instead of resetting to the first');
  assert.equal(await page.evaluate(() => document.getElementById('navMoreMenu').contains(document.activeElement)), true);
  assert.equal(await page.locator('#navMoreBtn').getAttribute('aria-expanded'), 'true');
  await page.keyboard.press('Escape');
  passes++;
  console.log('  ✅ More retains a later keyboard destination after a refit');

  await resizeAndRefit(1250);
  await page.locator('#navMoreBtn').click();
  await page.locator('#accountToggle').focus();
  await resizeAndRefit(1249);
  assert.equal(await page.evaluate(() => document.activeElement === document.getElementById('accountToggle')), true,
    'refitting More must not steal focus from another control');
  await page.keyboard.press('Escape');
  passes++;
  console.log('  ✅ More refits leave focus on controls outside the menu');

  // Account configuration can arrive after the first fit, without a resize
  // or route change. Exercise the real auth renderer, then change its label.
  let releaseConfig;
  let releaseAuth;
  configGate = new Promise(resolve => { releaseConfig = resolve; });
  authGate = new Promise(resolve => { releaseAuth = resolve; });
  await page.close();
  page = await context.newPage();
  page.setDefaultTimeout(15000);
  await page.setViewportSize({ width: 1250, height: HEIGHT });
  await page.goto(`${BASE}/#/scope-audit`, { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('#navMoreBtn');
  await page.evaluate(() => document.fonts.ready);
  await page.waitForFunction(() => document.getElementById('navStats').textContent.includes('30000'));
  assert.equal(await page.locator('#accountToggle').count(), 0, 'account control must arrive after initial navigation');
  releaseConfig();
  await page.waitForSelector('#accountToggle');

  async function checkFit(tag) {
    try {
      await page.waitForFunction(() => {
        const links = [...document.querySelectorAll('.nav-links .nav-link')]
          .filter(a => a.getBoundingClientRect().width > 0);
        const more = document.querySelector('.nav-more-wrap');
        const right = document.querySelector('.nav-right');
        const edge = more.getBoundingClientRect().width ? more.getBoundingClientRect().left : right.getBoundingClientRect().left;
        return Math.max(...links.map(a => a.getBoundingClientRect().right)) <= edge + 0.5;
      }, null, { timeout: 3000 });
      const controls = await page.locator('.nav-btn, .nav-account-btn').evaluateAll(elements => elements
        .filter(el => el.getBoundingClientRect().width > 0)
        .every(el => el.getBoundingClientRect().width >= 48 && el.getBoundingClientRect().height >= 48));
      assert.ok(controls, 'touch controls must retain 48px bounds');
      passes++;
      console.log(`  ✅ ${tag}: no overlap and touch controls retained`);
    } catch (err) {
      failures++;
      console.log(`  ❌ ${tag}: ${err.message}`);
    }
  }
  await checkFit('late account insertion while authentication is pending');
  assert.equal(await page.locator('.nav-account-label').textContent(), 'Log in');
  releaseAuth('offline');
  await page.evaluate(() => window.CSAuth.ready());
  await checkFit('account control after authentication network failure');
  assert.equal(await page.locator('.nav-account-label').textContent(), 'Log in');
  authGate = Promise.resolve({ status: 200, json: { displayName: 'Long account name', role: 'member' } });
  await page.evaluate(() => window.CSAuth.refreshMe());
  await checkFit('successful authentication retry updates the account label');
  assert.equal(await page.locator('.nav-account-label').textContent(), 'Long account name');

  const resizeWidths = [769, 1280, 1024, 1920, 1101, 2560, 1250];
  for (const width of resizeWidths) {
    await page.setViewportSize({ width, height: HEIGHT });
    await checkFit(`resize to ${width}px`);
  }
  await page.evaluate(() => window.CSAuth.setUser(null));
  await checkFit('account sign-out');
  await page.locator('#navMoreBtn').focus();
  await page.keyboard.press('Enter');
  assert.equal(await page.evaluate(() => document.activeElement === document.querySelector('#navMoreMenu .nav-link')), true);
  await page.keyboard.press('Escape');
  assert.equal(await page.locator('#navMoreBtn').getAttribute('aria-expanded'), 'false');
  passes++;
  console.log('  ✅ More retains keyboard entry and Escape dismissal');

  for (const width of [320, 768]) {
    await page.setViewportSize({ width, height: HEIGHT });
    const tabs = page.locator('.bottom-nav-tab');
    await tabs.first().waitFor({ state: 'visible' });
    assert.ok(await tabs.evaluateAll(elements => elements.every(el => {
      const r = el.getBoundingClientRect();
      return r.width >= 44 && r.height >= 44;
    })), 'phone/tablet navigation must retain 44px touch targets');
    passes++;
    console.log(`  ✅ mobile navigation at ${width}px retains touch targets`);
  }

  await browser.close();

  const total = ROUTES.length * VIEWPORTS.length + resizeWidths.length + 11;
  console.log(`\ntest-nav-fluid-1055-e2e.js: ${failures === 0 ? 'OK' : 'FAIL'} — ${passes}/${total} passed`);
  process.exit(failures === 0 ? 0 : 1);
}

main().catch((err) => {
  console.error('test-nav-fluid-1055-e2e.js: fatal', err);
  process.exit(1);
});

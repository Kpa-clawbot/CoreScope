#!/usr/bin/env node
/* Issue #1109 — Mobile hamburger dropdown is invisible (CSS clip).
 *
 * Symptom: tap the hamburger on mobile, DOM state goes correct
 * (.nav-links.open, body.nav-open, aria-expanded=true) but the
 * dropdown is not visible. Cause: `.top-nav { overflow:hidden;
 * height:52px }` (added in #1066 fluid scaffolding) clips the
 * absolutely-positioned `.nav-links { position:absolute; top:52px }`
 * outside its containing block. Fix: switch to position:fixed at
 * <768px so the dropdown escapes the navbar's overflow trap.
 *
 * Prior tests checked only `.classList.contains('open')` — pure DOM
 * state — and missed the regression entirely. This test asserts
 * PIXEL-LEVEL visibility via `elementFromPoint` AND a getBoundingClientRect
 * sanity check, so a state-only fix can never lie its way past CI.
 *
 * RCA: https://github.com/Kpa-clawbot/CoreScope/issues/1109#issuecomment-4398900387
 *
 * This test FAILS on master @ origin/master (elementFromPoint at the
 * dropdown center returns <body>, not a .nav-link) and PASSES once
 * the position:fixed fix is applied.
 */
'use strict';

const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';
const VIEWPORT = { width: 390, height: 844 }; // iPhone 13 dimensions

function fail(msg) {
  console.error(`test-issue-1109-hamburger-dropdown-visible-e2e.js: FAIL — ${msg}`);
  process.exit(1);
}

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
      console.error(`test-issue-1109-hamburger-dropdown-visible-e2e.js: FAIL — Chromium required but unavailable: ${err.message}`);
      process.exit(1);
    }
    console.log(`test-issue-1109-hamburger-dropdown-visible-e2e.js: SKIP (Chromium unavailable: ${err.message.split('\n')[0]})`);
    process.exit(0);
  }

  try {
    const ctx = await browser.newContext({
      viewport: VIEWPORT,
      hasTouch: true,
      isMobile: true,
      userAgent: 'Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1',
    });
    const page = await ctx.newPage();
    page.setDefaultTimeout(15000);

    await page.goto(`${BASE}/#/home`, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#hamburger');
    await page.evaluate(() => document.fonts && document.fonts.ready ? document.fonts.ready : null);

    // Sanity: hamburger is visible and dropdown is closed by default.
    const initial = await page.evaluate(() => {
      const h = document.getElementById('hamburger');
      const nl = document.querySelector('.nav-links');
      return {
        hamburgerDisplay: h ? getComputedStyle(h).display : null,
        navOpen: nl ? nl.classList.contains('open') : null,
        navDisplay: nl ? getComputedStyle(nl).display : null,
      };
    });
    if (initial.hamburgerDisplay === 'none') fail(`hamburger should be visible at <768px, got display:${initial.hamburgerDisplay}`);
    if (initial.navOpen) fail('nav-links should NOT have .open before tap');
    if (initial.navDisplay !== 'none') fail(`nav-links display should be 'none' before tap, got ${initial.navDisplay}`);

    // Tap (mobile context => Playwright synthesizes touch).
    await page.tap('#hamburger');

    // Step 1: confirm DOM state (the OLD assertion).
    await page.waitForSelector('.nav-links.open', { timeout: 5000 });
    const domState = await page.evaluate(() => {
      const nl = document.querySelector('.nav-links');
      const h = document.getElementById('hamburger');
      return {
        open: nl.classList.contains('open'),
        bodyOpen: document.body.classList.contains('nav-open'),
        ariaExpanded: h.getAttribute('aria-expanded'),
        display: getComputedStyle(nl).display,
      };
    });
    if (!domState.open) fail('.nav-links.open missing after tap');
    if (!domState.bodyOpen) fail('body.nav-open missing after tap');
    if (domState.ariaExpanded !== 'true') fail(`aria-expanded should be 'true', got ${domState.ariaExpanded}`);
    if (domState.display !== 'flex') fail(`nav-links display should be 'flex' after tap, got ${domState.display}`);

    // Step 2: PIXEL-LEVEL visibility (the NEW assertion that gates the bug).
    // Pick a point inside where a nav-link should render: center-x, y=100
    // (well below the 52px navbar). On the bug, this returns <body> because
    // the dropdown is laid out but clipped by .top-nav { overflow:hidden }.
    const probe = await page.evaluate(() => {
      const x = Math.floor(window.innerWidth / 2);
      const y = 100;
      const el = document.elementFromPoint(x, y);
      const navLinks = document.querySelector('.nav-links');
      const firstLink = navLinks ? navLinks.querySelector('.nav-link') : null;
      const linkRect = firstLink ? firstLink.getBoundingClientRect() : null;
      const navLinksRect = navLinks ? navLinks.getBoundingClientRect() : null;
      return {
        x, y,
        hitTag: el ? el.tagName : null,
        hitClass: el ? el.className : null,
        // Walk up to see if hit point belongs to the nav-links subtree.
        hitInsideNavLinks: !!(el && navLinks && navLinks.contains(el)),
        linkRect: linkRect ? {
          top: linkRect.top, bottom: linkRect.bottom,
          left: linkRect.left, right: linkRect.right,
          width: linkRect.width, height: linkRect.height,
        } : null,
        navLinksRect: navLinksRect ? {
          top: navLinksRect.top, bottom: navLinksRect.bottom,
          left: navLinksRect.left, right: navLinksRect.right,
        } : null,
      };
    });

    if (!probe.hitInsideNavLinks) {
      fail(
        `pixel-level visibility check failed: elementFromPoint(${probe.x}, ${probe.y}) returned ` +
        `<${probe.hitTag} class="${probe.hitClass}">, expected an element inside .nav-links. ` +
        `This means the dropdown is laid out but visually clipped (likely by an ancestor with overflow:hidden). ` +
        `linkRect=${JSON.stringify(probe.linkRect)} navLinksRect=${JSON.stringify(probe.navLinksRect)}`
      );
    }
    if (!probe.linkRect) fail('no .nav-link found inside .nav-links');
    if (probe.linkRect.bottom <= 60) fail(`first .nav-link bounding rect bottom (${probe.linkRect.bottom}) should be > 60 (below 52px navbar)`);
    if (probe.linkRect.right <= 0) fail(`first .nav-link bounding rect right (${probe.linkRect.right}) should be > 0`);
    if (probe.linkRect.width <= 0 || probe.linkRect.height <= 0) {
      fail(`first .nav-link rect has zero area: ${JSON.stringify(probe.linkRect)}`);
    }

    // Step 3: tap to close, assert dropdown is no longer rendered.
    await page.tap('#hamburger');
    // Wait for state flip.
    await page.waitForFunction(() => {
      const nl = document.querySelector('.nav-links');
      return nl && !nl.classList.contains('open');
    }, { timeout: 5000 });
    const closed = await page.evaluate(() => {
      const nl = document.querySelector('.nav-links');
      return {
        open: nl.classList.contains('open'),
        display: getComputedStyle(nl).display,
        bodyOpen: document.body.classList.contains('nav-open'),
      };
    });
    if (closed.open) fail('.nav-links.open should be gone after second tap');
    if (closed.display !== 'none') fail(`nav-links display should be 'none' after close, got ${closed.display}`);
    if (closed.bodyOpen) fail('body.nav-open should be cleared after close');

    console.log('test-issue-1109-hamburger-dropdown-visible-e2e.js: PASS');
  } finally {
    await browser.close();
  }
}

main().catch((err) => {
  console.error(`test-issue-1109-hamburger-dropdown-visible-e2e.js: FAIL — ${err.stack || err.message}`);
  process.exit(1);
});

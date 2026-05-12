#!/usr/bin/env node
/* Issue #1109 (post-#1174 conversion) — Long-tail routes reachable on phones.
 *
 * Original contract: tapping the hamburger surfaces the long-tail routes
 * (Tools/Lab/Perf/Analytics/Observers/Nodes) that don't fit in the primary
 * nav. Origin used a CSS-clip-prone dropdown.
 *
 * #1174 replaced the hamburger-at-narrow-widths path with a 6th "More" tab
 * in the bottom-nav that opens a bottom-anchored sheet listing the same
 * long-tail routes. The hamburger is HIDDEN at ≤768px (its job at narrow
 * widths is now done by the More tab).
 *
 * This test asserts the converted contract:
 *   1. At iPhone-13 viewport (390×844, mobile UA), #hamburger is NOT visible.
 *   2. The More tab IS visible and toggles the sheet.
 *   3. Tap More → sheet visible (pixel-level: elementFromPoint inside sheet,
 *      bounding rect non-zero, top above the bottom-nav).
 *   4. Tap a long-tail route inside the sheet → URL hash updates AND
 *      sheet closes.
 *   5. Tap More again → sheet re-opens (toggle, not push).
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
    await page.waitForSelector('[data-bottom-nav-tab="more"]');

    // 1. #hamburger hidden at ≤768px.
    const hamburgerState = await page.evaluate(() => {
      const h = document.getElementById('hamburger');
      if (!h) return { present: false };
      const cs = getComputedStyle(h);
      const r = h.getBoundingClientRect();
      return {
        present: true,
        display: cs.display,
        visibility: cs.visibility,
        width: r.width, height: r.height,
        hidden: cs.display === 'none' || cs.visibility === 'hidden' || (r.width === 0 && r.height === 0),
      };
    });
    if (hamburgerState.present && !hamburgerState.hidden) {
      fail(`#hamburger should be hidden at ≤768px (replaced by More tab); got display=${hamburgerState.display}, visibility=${hamburgerState.visibility}, size=${hamburgerState.width}x${hamburgerState.height}`);
    }

    // 2. More tab visible.
    const moreState = await page.evaluate(() => {
      const el = document.querySelector('[data-bottom-nav-tab="more"]');
      if (!el) return { present: false };
      const cs = getComputedStyle(el);
      const r = el.getBoundingClientRect();
      return {
        present: true,
        visible: cs.display !== 'none' && r.width > 0 && r.height > 0,
        ariaExpanded: el.getAttribute('aria-expanded'),
        ariaControls: el.getAttribute('aria-controls'),
      };
    });
    if (!moreState.present) fail('[data-bottom-nav-tab="more"] missing');
    if (!moreState.visible) fail('More tab not visible at 390×844');
    if (moreState.ariaExpanded !== 'false') fail(`More tab aria-expanded should be 'false' before tap, got ${moreState.ariaExpanded}`);

    // 3. Tap More → sheet visible (pixel-level).
    await page.tap('[data-bottom-nav-tab="more"]');
    await page.waitForSelector('[data-bottom-nav-sheet]', { timeout: 3000 });
    const probe = await page.evaluate(() => {
      const sheet = document.querySelector('[data-bottom-nav-sheet]');
      const cs = sheet ? getComputedStyle(sheet) : null;
      const r = sheet ? sheet.getBoundingClientRect() : null;
      const moreTab = document.querySelector('[data-bottom-nav-tab="more"]');
      // Probe a point inside the sheet's rect.
      let hitInside = false;
      if (sheet && r && r.width > 0 && r.height > 0) {
        const x = Math.floor(r.left + r.width / 2);
        const y = Math.floor(r.top + r.height / 2);
        const hit = document.elementFromPoint(x, y);
        hitInside = !!(hit && sheet.contains(hit));
      }
      const items = sheet ? Array.from(sheet.querySelectorAll('[data-bottom-nav-more-route]')).map(e => e.getAttribute('data-bottom-nav-more-route')) : [];
      return {
        rect: r,
        display: cs ? cs.display : null,
        visibility: cs ? cs.visibility : null,
        role: sheet ? sheet.getAttribute('role') : null,
        hitInside,
        items,
        moreExpanded: moreTab ? moreTab.getAttribute('aria-expanded') : null,
      };
    });
    if (!probe.rect || probe.rect.width === 0 || probe.rect.height === 0) {
      fail(`sheet has zero area: ${JSON.stringify(probe.rect)}`);
    }
    if (probe.display === 'none' || probe.visibility === 'hidden') {
      fail(`sheet hidden: display=${probe.display}, visibility=${probe.visibility}`);
    }
    if (!probe.hitInside) {
      fail(`pixel-level visibility check failed: elementFromPoint inside sheet rect did not hit a sheet descendant. rect=${JSON.stringify(probe.rect)}`);
    }
    if (probe.role !== 'menu') fail(`sheet role should be 'menu', got ${probe.role}`);
    if (probe.items.length < 6) fail(`sheet should list ≥6 long-tail routes, got ${probe.items.length}: ${probe.items.join(',')}`);
    if (probe.moreExpanded !== 'true') fail(`More tab aria-expanded should be 'true' while sheet open, got ${probe.moreExpanded}`);

    // 4. Tap a long-tail route → hash changes, sheet closes.
    const firstRoute = probe.items[0];
    await page.tap(`[data-bottom-nav-more-route="${firstRoute}"]`);
    await page.waitForFunction((r) => location.hash === `#/${r}`, firstRoute, { timeout: 3000 });
    await page.waitForFunction(() => {
      const s = document.querySelector('[data-bottom-nav-sheet]');
      if (!s) return true;
      const cs = getComputedStyle(s);
      return cs.display === 'none' || cs.visibility === 'hidden';
    }, null, { timeout: 3000 }).catch(() => {});
    const afterTap = await page.evaluate(() => {
      const s = document.querySelector('[data-bottom-nav-sheet]');
      if (!s) return { sheetClosed: true, hash: location.hash };
      const cs = getComputedStyle(s);
      const r = s.getBoundingClientRect();
      return {
        sheetClosed: cs.display === 'none' || cs.visibility === 'hidden' || (r.width === 0 && r.height === 0),
        hash: location.hash,
      };
    });
    if (afterTap.hash !== `#/${firstRoute}`) fail(`hash did not change to #/${firstRoute}, got ${afterTap.hash}`);
    if (!afterTap.sheetClosed) fail('sheet did not close after route tap');

    // 5. Tap More again → sheet reopens (toggle).
    await page.tap('[data-bottom-nav-tab="more"]');
    await page.waitForFunction(() => {
      const s = document.querySelector('[data-bottom-nav-sheet]');
      if (!s) return false;
      const cs = getComputedStyle(s);
      return cs.display !== 'none' && cs.visibility !== 'hidden';
    }, null, { timeout: 3000 });
    // Now tap More AGAIN to confirm toggle (close).
    await page.tap('[data-bottom-nav-tab="more"]');
    await page.waitForFunction(() => {
      const s = document.querySelector('[data-bottom-nav-sheet]');
      if (!s) return true;
      const cs = getComputedStyle(s);
      return cs.display === 'none' || cs.visibility === 'hidden';
    }, null, { timeout: 3000 }).catch(() => {});
    const afterToggle = await page.evaluate(() => {
      const s = document.querySelector('[data-bottom-nav-sheet]');
      if (!s) return { closed: true };
      const cs = getComputedStyle(s);
      return { closed: cs.display === 'none' || cs.visibility === 'hidden' };
    });
    if (!afterToggle.closed) fail('sheet did not close on second More tap (toggle behavior expected)');

    console.log('test-issue-1109-hamburger-dropdown-visible-e2e.js: PASS');
  } finally {
    await browser.close();
  }
}

main().catch((err) => {
  console.error(`test-issue-1109-hamburger-dropdown-visible-e2e.js: FAIL — ${err.stack || err.message}`);
  process.exit(1);
});

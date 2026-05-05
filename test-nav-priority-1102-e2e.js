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
 *
 * #1105 MINOR 7: at 1080/800px we now assert the visible set is *exactly*
 * the 5 high-priority links (Home/Packets/Map/Live/Nodes). A buggy queue
 * that hid Home and showed Lab would still pass the cardinality check.
 *
 * #1105 MINOR 9: also asserts that navigating to a route whose link
 * lives in the More menu lights up #navMoreBtn with .active.
 */
'use strict';

const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';

// [width, expected behavior]
// requireExactHighPri: when true, asserts the visible set matches HIGH_PRIORITY_HREFS exactly
const HIGH_PRIORITY_HREFS = ['#/home', '#/packets', '#/map', '#/live', '#/nodes'];
const CASES = [
  // viewport, minVisible, moreVisible, requireExactHighPri, label
  { w: 2560, minVisible: 11, moreVisible: false, requireExactHighPri: false, label: '2560px — all visible' },
  { w: 1920, minVisible: 9,  moreVisible: null,  requireExactHighPri: false, label: '1920px — most visible' },
  { w: 1080, minVisible: 5,  moreVisible: true,  requireExactHighPri: true,  label: '1080px — collapsed' },
  { w: 800,  minVisible: 5,  moreVisible: true,  requireExactHighPri: true,  label: '800px — collapsed' },
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
    // #1105 MINOR 7: identity, not just cardinality. The 5 visible links
    // at the collapsed widths must be EXACTLY the high-priority set
    // (Home/Packets/Map/Live/Nodes). A buggy queue that hid Home and
    // showed Lab would still pass `visibleCount >= 5`.
    if (c.requireExactHighPri) {
      const missingHighPri = HIGH_PRIORITY_HREFS.filter(h => !data.visibleHrefs.includes(h));
      if (missingHighPri.length) {
        reasons.push(`high-priority link(s) NOT visible inline: ${missingHighPri.join(', ')} ` +
                     `(visible=[${data.visibleHrefs.join(', ')}])`);
      }
      const extra = data.visibleHrefs.filter(h => !HIGH_PRIORITY_HREFS.includes(h));
      if (extra.length) {
        reasons.push(`unexpected non-high-priority link(s) visible: ${extra.join(', ')} ` +
                     `(expected exactly [${HIGH_PRIORITY_HREFS.join(', ')}])`);
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

  // #1105 MINOR 9: when at a collapsed width, navigating to a route
  // whose link overflows into the More menu must light up #navMoreBtn
  // with .active. Verifies rebuildMoreMenu() correctly mirrors the
  // active state from the inline (cloned) link to the More button on
  // each hashchange (applyNavPriority is wired to hashchange and runs
  // after the route handler's class toggles).
  await page.setViewportSize({ width: 1080, height: HEIGHT });
  await page.goto(`${BASE}/#/observers`, { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('.top-nav .nav-links');
  await page.evaluate(() => document.fonts && document.fonts.ready ? document.fonts.ready : null);
  // Wait for layout to settle.
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
  // Give the hashchange-triggered applyNavPriority a frame to run.
  await page.evaluate(() => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r))));

  const activeMirror = await page.evaluate(() => {
    const observersInline = document.querySelector('.nav-links .nav-link[href="#/observers"]');
    const inlineHidden = observersInline && observersInline.classList.contains('is-overflow');
    const moreBtn = document.getElementById('navMoreBtn');
    const moreBtnActive = moreBtn ? moreBtn.classList.contains('active') : false;
    const moreMenuActiveHrefs = Array.from(document.querySelectorAll('#navMoreMenu .nav-link.active'))
      .map(a => a.getAttribute('href'));
    return { inlineHidden, moreBtnActive, moreMenuActiveHrefs };
  });

  const mirrorReasons = [];
  if (!activeMirror.inlineHidden) {
    mirrorReasons.push('precondition: #/observers should be in the More menu at 1080px (not visible inline)');
  }
  if (!activeMirror.moreBtnActive) {
    mirrorReasons.push('navMoreBtn missing .active class while #/observers is the active route');
  }
  if (!activeMirror.moreMenuActiveHrefs.includes('#/observers')) {
    mirrorReasons.push(`More-menu clone of #/observers missing .active (active hrefs in menu: [${activeMirror.moreMenuActiveHrefs.join(', ')}])`);
  }
  if (mirrorReasons.length === 0) {
    passes++;
    console.log(`  ✅ active-mirror @1080 #/observers: navMoreBtn.active=true, menu .active=#/observers`);
  } else {
    failures++;
    console.log(`  ❌ active-mirror @1080 #/observers: ${mirrorReasons.join(' | ')}`);
  }

  await browser.close();
  console.log(`\ntest-nav-priority-1102-e2e.js: ${failures === 0 ? 'OK' : 'FAIL'} — ${passes}/${CASES.length + 1} passed`);
  process.exit(failures === 0 ? 0 : 1);
}

main().catch((err) => {
  console.error('test-nav-priority-1102-e2e.js: fatal', err);
  process.exit(1);
});

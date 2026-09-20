#!/usr/bin/env node
/* Issue #1833 (round 2) — the Live legend toggle must be CLICKABLE, not just
 * correctly offset.
 *
 * #1833 r1 fixed a hard-coded `bottom: 1rem` by switching .legend-toggle-btn
 * to `bottom: calc(var(--vcr-bar-height, 58px) + 10px)`. The offset became
 * right, but the ANCHOR stayed wrong: the button was `position: fixed`, so
 * that bottom resolved against the viewport, while the .vcr-bar it is meant
 * to clear is absolute inside .live-page. At <=768 bottom-nav.css sets
 * --bottom-nav-reserve to 56px + safe-area and .live-page becomes exactly
 * that much shorter than the viewport, so the two anchors disagreed by the
 * reserve and the button dropped into the bar's band. .vcr-bar is z-index
 * 1000 to the button's 500 and takes pointer events, so real clicks were
 * swallowed: users could not dismiss the PACKET TYPES legend at all, while
 * `document.querySelector('#legendToggleBtn').click()` from the console
 * still worked (a synthesised click skips hit-testing). That asymmetry is
 * the signature of an occlusion bug, and it is what this test pins.
 *
 * The band that matters is 641..768: at <=640 both the legend and its toggle
 * are display:none, above 768 the reserve is 0 and nothing overlaps. So the
 * bug lived in a window that neither the desktop nor the phone layout tests
 * covered.
 *
 * Asserts, at 720x900 (inside that band):
 *   (a) --bottom-nav-reserve is actually non-zero — i.e. the test is really
 *       exercising the reserve band and not silently passing on a desktop
 *       layout.
 *   (b) #legendToggleBtn is visible.
 *   (c) its bottom edge is at or above the VCR bar's top edge (no overlap).
 *   (d) elementFromPoint at the button's centre IS the button (or a child) —
 *       nothing is painted over it.
 *   (e) a REAL click (page.click(), which hit-tests) toggles #liveLegend
 *       into .hidden, and a second click brings it back.
 *   (f) at 1440x900 the button is still clear of the bar (no desktop
 *       regression from the anchor change).
 *
 * CI gating: when CHROMIUM_REQUIRE=1 a missing/broken Chromium is a HARD FAIL.
 */
'use strict';

const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';

async function gotoLive(page) {
  await page.goto(`${BASE}/#/live`, { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('#liveMap');
  await page.waitForSelector('#vcrBar');
  await page.waitForSelector('#legendToggleBtn');
  // --vcr-bar-height is published by a ResizeObserver on .vcr-bar; the
  // button's offset is meaningless until that first publish lands.
  await page.waitForFunction(() => {
    const v = getComputedStyle(document.querySelector('.live-page'))
      .getPropertyValue('--vcr-bar-height');
    return v && parseFloat(v) > 0;
  }, null, { timeout: 8000 });
  await page.waitForTimeout(150);
}

// Geometry + hit-test of the toggle against the VCR bar, in one evaluate so
// nothing can reflow between the two measurements.
async function probe(page) {
  return page.evaluate(() => {
    const btn = document.getElementById('legendToggleBtn');
    const bar = document.getElementById('vcrBar');
    const legend = document.getElementById('liveLegend');
    const livePage = document.querySelector('.live-page');
    if (!btn || !bar || !livePage) return null;

    const cs = getComputedStyle(btn);
    const r = btn.getBoundingClientRect();
    const barRect = bar.getBoundingClientRect();
    const pageRect = livePage.getBoundingClientRect();
    const hit = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);

    return {
      reserve: getComputedStyle(document.documentElement)
        .getPropertyValue('--bottom-nav-reserve').trim(),
      gapBelowLivePage: Math.round(window.innerHeight - pageRect.bottom),
      btnDisplay: cs.display,
      btnVisible: cs.display !== 'none' && r.width > 0 && r.height > 0,
      btnBottom: Math.round(r.bottom),
      barTop: Math.round(barRect.top),
      hitIsButton: !!hit && (hit === btn || btn.contains(hit)),
      hitTag: hit ? hit.tagName + (hit.id ? '#' + hit.id : '') : null,
      legendHidden: legend ? legend.classList.contains('hidden') : null,
    };
  });
}

async function main() {
  const requireChromium = process.env.CHROMIUM_REQUIRE === '1';
  let browser;
  try {
    browser = await chromium.launch({
      headless: true,
      executablePath: process.env.CHROMIUM_PATH || undefined,
      args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
    });
  } catch (err) {
    if (requireChromium) {
      console.error(`test-issue-1833-legend-toggle-clickable-e2e.js: FAIL — Chromium required (CHROMIUM_REQUIRE=1) but unavailable: ${err.message}`);
      process.exit(1);
    }
    console.log(`test-issue-1833-legend-toggle-clickable-e2e.js: SKIP (Chromium unavailable: ${err.message.split('\n')[0]})`);
    process.exit(0);
  }

  let failures = 0;
  let passes = 0;
  const fail = (msg) => { failures += 1; console.error(`  FAIL: ${msg}`); };
  const pass = (msg) => { passes += 1; console.log(`  PASS: ${msg}`); };

  const ctx = await browser.newContext();
  const page = await ctx.newPage();
  page.setDefaultTimeout(15000);

  // ── 720x900: inside the 641..768 reserve band ──
  await page.setViewportSize({ width: 720, height: 900 });
  await gotoLive(page);

  const narrow = await probe(page);
  if (!narrow) {
    fail('(setup) #legendToggleBtn / #vcrBar / .live-page not found on /#/live');
  } else {
    // (a) prove we are in the band that regressed
    const reservePx = parseFloat(narrow.reserve);
    if (narrow.gapBelowLivePage > 0 && reservePx > 0) {
      pass(`(a) reserve band active (--bottom-nav-reserve=${narrow.reserve}, .live-page ends ${narrow.gapBelowLivePage}px above viewport bottom)`);
    } else {
      fail(`(a) expected a non-zero --bottom-nav-reserve at 720px wide (got "${narrow.reserve}", gap ${narrow.gapBelowLivePage}px) — test is not exercising the regressed band`);
    }

    // (b) visible at all
    if (narrow.btnVisible) pass('(b) #legendToggleBtn is visible at 720px');
    else fail(`(b) #legendToggleBtn not visible at 720px (display=${narrow.btnDisplay})`);

    // (c) geometry: clear of the bar
    if (narrow.btnBottom <= narrow.barTop) {
      pass(`(c) toggle clears the VCR bar (btn.bottom=${narrow.btnBottom} <= bar.top=${narrow.barTop})`);
    } else {
      fail(`(c) toggle overlaps the VCR bar (btn.bottom=${narrow.btnBottom} > bar.top=${narrow.barTop}) — it is ${narrow.btnBottom - narrow.barTop}px inside the bar`);
    }

    // (d) hit-test: nothing painted over it
    if (narrow.hitIsButton) {
      pass('(d) elementFromPoint at the toggle centre resolves to the toggle');
    } else {
      fail(`(d) something is painted over the toggle — elementFromPoint returned ${narrow.hitTag}`);
    }
  }

  // (e) a real, hit-tested click must actually toggle the legend. This is
  // the user-visible contract; a synthesised .click() would pass even with
  // the bug present, so we deliberately use page.click().
  const hiddenBefore = await page.evaluate(() =>
    document.getElementById('liveLegend').classList.contains('hidden'));
  let clickErr = null;
  try {
    await page.click('#legendToggleBtn', { timeout: 4000 });
  } catch (err) {
    clickErr = err.message.split('\n')[0];
  }
  if (clickErr) {
    fail(`(e) real click on #legendToggleBtn did not land: ${clickErr}`);
  } else {
    const hiddenAfter = await page.evaluate(() =>
      document.getElementById('liveLegend').classList.contains('hidden'));
    if (hiddenAfter !== hiddenBefore) {
      pass(`(e) real click toggled the legend (hidden ${hiddenBefore} -> ${hiddenAfter})`);
      await page.click('#legendToggleBtn', { timeout: 4000 }).catch(() => {});
      const hiddenBack = await page.evaluate(() =>
        document.getElementById('liveLegend').classList.contains('hidden'));
      if (hiddenBack === hiddenBefore) pass('(e) second click restores the legend');
      else fail(`(e) second click did not restore the legend (got hidden=${hiddenBack}, expected ${hiddenBefore})`);
    } else {
      fail(`(e) real click was swallowed — #liveLegend.hidden stayed ${hiddenBefore}`);
    }
  }

  // ── (f) desktop must not regress from the anchor change ──
  await page.setViewportSize({ width: 1440, height: 900 });
  await gotoLive(page);
  const wide = await probe(page);
  if (!wide) {
    fail('(f) probe failed at 1440x900');
  } else if (!wide.btnVisible) {
    fail(`(f) #legendToggleBtn not visible at 1440px (display=${wide.btnDisplay})`);
  } else if (wide.btnBottom <= wide.barTop && wide.hitIsButton) {
    pass(`(f) desktop unchanged — toggle clears the bar (btn.bottom=${wide.btnBottom} <= bar.top=${wide.barTop}) and is hit-testable`);
  } else {
    fail(`(f) desktop regression — btn.bottom=${wide.btnBottom}, bar.top=${wide.barTop}, hit=${wide.hitTag}`);
  }

  await browser.close();

  console.log(`\ntest-issue-1833-legend-toggle-clickable-e2e.js: ${passes} passed, ${failures} failed`);
  process.exit(failures > 0 ? 1 : 0);
}

main().catch((err) => {
  console.error('test-issue-1833-legend-toggle-clickable-e2e.js: FAIL —', err);
  process.exit(1);
});

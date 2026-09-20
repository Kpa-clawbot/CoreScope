#!/usr/bin/env node
/* Issue #1833 (round 2) — the Live bottom-right button pair must be
 * CLICKABLE, not just correctly offset.
 *
 * #1833 r1 fixed a hard-coded `bottom: 1rem` by switching .legend-toggle-btn
 * to `bottom: calc(var(--vcr-bar-height, 58px) + 10px)`. The offset became
 * right, but the ANCHOR stayed wrong: both .legend-toggle-btn and its pair
 * .feed-show-btn were `position: fixed`, so that bottom resolved against the
 * viewport, while the .vcr-bar they are meant to clear is absolute inside
 * .live-page. At <=768 bottom-nav.css sets --bottom-nav-reserve to
 * 56px + safe-area and .live-page becomes exactly that much shorter than the
 * viewport, so the two anchors disagreed by the reserve and BOTH buttons
 * dropped by it.
 *
 * Measured consequence (reserve = 56px, bar 50px tall, offsets from
 * .live-page's bottom edge):
 *   .legend-toggle-btn  68 -> 12  — inside the bar's band, clicks swallowed
 *   .feed-show-btn     122 -> 66  — still ~16px clear of the bar, by luck
 * .vcr-bar is z-index 1000 to the buttons' 500 and takes pointer events, so
 * the legend toggle stopped responding entirely: users could not dismiss the
 * PACKET TYPES legend, while `document.querySelector('#legendToggleBtn')
 * .click()` from the console still worked (a synthesised click skips
 * hit-testing). That asymmetry is the signature of an occlusion bug.
 *
 * The band that matters is 641..768: at <=640 the feed, the legend and both
 * buttons are display:none, above 768 the reserve is 0 and nothing moves. So
 * the bug lived in a window that neither the desktop nor the phone layout
 * tests covered.
 *
 * Asserts, at 720x900 (inside that band):
 *   (a) .live-page actually ends short of the viewport bottom — i.e. the
 *       reserve is applied and the run is really exercising the regressed
 *       band, not silently passing on a desktop layout. Measured, not read
 *       off --bottom-nav-reserve: that property is unregistered, so its
 *       computed value is an unevaluated token sequence ("calc(56px + 0px)")
 *       with no number in it to test.
 *   (b) both buttons are visible.
 *   (c) each button's bottom edge is at or above the VCR bar's top edge.
 *   (d) elementFromPoint at each button's centre IS that button (or a child)
 *       — nothing is painted over it.
 *   (e) a REAL click (page.click(), which hit-tests) works on each: the
 *       legend toggle moves #liveLegend in and out of .hidden, and the feed
 *       show button brings #liveFeed back.
 *   (f) ANCHOR INVARIANT — each button's offset from .live-page's bottom
 *       edge, net of the bar height, is the SAME at 720 (reserve on) and
 *       1440 (reserve off). This is what separates the fix from the bug:
 *       under `position: fixed` those offsets differ by the reserve, under
 *       `absolute` they match. It catches .feed-show-btn too, which the
 *       hit-test alone does not, because its drop stopped 16px short of
 *       the bar.
 *   (g) at 1440x900 both buttons still clear the bar and hit-test.
 *
 * CI gating: when CHROMIUM_REQUIRE=1 a missing/broken Chromium is a HARD FAIL.
 */
'use strict';

const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';

// Offsets are compared in CSS px; allow a pixel of sub-pixel rounding.
const TOL = 2;

async function gotoLive(page) {
  await page.goto(`${BASE}/#/live`, { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('#liveMap');
  await page.waitForSelector('#vcrBar');
  await page.waitForSelector('#legendToggleBtn');
  // --vcr-bar-height is published by a ResizeObserver on .vcr-bar; the
  // buttons' offsets are meaningless until that first publish lands.
  await page.waitForFunction(() => {
    const v = getComputedStyle(document.querySelector('.live-page'))
      .getPropertyValue('--vcr-bar-height');
    return v && parseFloat(v) > 0;
  }, null, { timeout: 8000 });
  await page.waitForTimeout(150);
}

// #feedShowBtn ships with .hidden and is revealed by hiding the live feed.
// Returns false when the feed chrome is not available at this viewport.
async function revealFeedShowBtn(page) {
  const hideBtn = await page.$('#feedHideBtn');
  if (!hideBtn) return false;
  const usable = await page.evaluate(() => {
    const b = document.getElementById('feedHideBtn');
    const r = b.getBoundingClientRect();
    return getComputedStyle(b).display !== 'none' && r.width > 0 && r.height > 0;
  });
  if (!usable) return false;
  await page.click('#feedHideBtn', { timeout: 4000 }).catch(() => {});
  await page.waitForTimeout(150);
  return page.evaluate(() => {
    const b = document.getElementById('feedShowBtn');
    return !!b && getComputedStyle(b).display !== 'none';
  });
}

// Geometry + hit-test of one button against the VCR bar, in a single
// evaluate so nothing can reflow between the measurements.
async function probe(page, id) {
  return page.evaluate((btnId) => {
    const btn = document.getElementById(btnId);
    const bar = document.getElementById('vcrBar');
    const livePage = document.querySelector('.live-page');
    if (!btn || !bar || !livePage) return null;

    const cs = getComputedStyle(btn);
    const r = btn.getBoundingClientRect();
    const barRect = bar.getBoundingClientRect();
    const pageRect = livePage.getBoundingClientRect();
    const hit = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);

    // --bottom-nav-reserve is an UNREGISTERED custom property, so
    // getPropertyValue returns the token sequence after var() substitution,
    // not an evaluated length: bottom-nav.css:45 declares it as
    // `calc(56px + env(safe-area-inset-bottom, 0px))` and it comes back as
    // the literal string "calc(56px + 0px)". Both values below are for the
    // failure message only — nothing asserts on them. The band is proven by
    // gapBelowLivePage, which is measured.
    const reserveToken = getComputedStyle(document.documentElement)
      .getPropertyValue('--bottom-nav-reserve').trim();
    let reservePx = null;
    try {
      const probeEl = document.createElement('div');
      probeEl.style.cssText =
        'position:absolute;visibility:hidden;pointer-events:none;width:0;height:var(--bottom-nav-reserve, 0px)';
      document.body.appendChild(probeEl);
      reservePx = Math.round(probeEl.getBoundingClientRect().height);
      probeEl.remove();
    } catch (_) { /* diagnostics only */ }

    return {
      id: btnId,
      position: cs.position,
      reserveToken,
      reservePx,
      gapBelowLivePage: Math.round(window.innerHeight - pageRect.bottom),
      visible: cs.display !== 'none' && r.width > 0 && r.height > 0,
      display: cs.display,
      bottom: Math.round(r.bottom),
      barTop: Math.round(barRect.top),
      barHeight: Math.round(barRect.height),
      // The anchor invariant: distance from .live-page's bottom edge, net of
      // the bar, must not depend on --bottom-nav-reserve.
      slack: Math.round(pageRect.bottom - r.bottom - barRect.height),
      hitIsSelf: !!hit && (hit === btn || btn.contains(hit)),
      hitTag: hit ? hit.tagName + (hit.id ? '#' + hit.id : '') : null,
    };
  }, id);
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

  const narrowLegend = await probe(page, 'legendToggleBtn');
  if (!narrowLegend) {
    fail('(setup) #legendToggleBtn / #vcrBar / .live-page not found on /#/live');
  } else {
    // (a) prove we are in the band that regressed.
    //
    // Assert the EFFECT, not the token. `.live-page` ending short of the
    // viewport is the entire precondition the bug needs, and it is exactly
    // what (f) depends on, so measuring the gap tests the real thing. The
    // reserve's declared value is only reported.
    //
    // Deliberately `> 0` and not `=== 56`: the gap measures 58px on CI
    // against a 56px token, and the extra 2px is unexplained (the height is
    // derived from 100dvh, which need not agree with innerHeight to the
    // pixel). Pinning the exact number would make this assertion fragile
    // for no gain — the bug does not care how far short the box ends, only
    // that it does.
    const diag = `token="${narrowLegend.reserveToken}", resolved=${narrowLegend.reservePx}px, gap=${narrowLegend.gapBelowLivePage}px`;
    if (narrowLegend.gapBelowLivePage > 0) {
      pass(`(a) reserve band active — .live-page ends ${narrowLegend.gapBelowLivePage}px above the viewport bottom (${diag})`);
    } else {
      fail(`(a) .live-page is flush with the viewport bottom at 720px wide (${diag}) — the reserve is not applied, so this run is not exercising the regressed band`);
    }
  }

  // Reveal the second button, then measure both.
  const feedShown = await revealFeedShowBtn(page);
  if (!feedShown) {
    fail('(setup) could not reveal #feedShowBtn at 720px — #feedHideBtn unusable, so the pair cannot be checked');
  }
  const narrowFeed = feedShown ? await probe(page, 'feedShowBtn') : null;

  const narrowPair = [narrowLegend, narrowFeed].filter(Boolean);
  for (const p of narrowPair) {
    // (b) visible at all
    if (p.visible) pass(`(b) #${p.id} is visible at 720px`);
    else fail(`(b) #${p.id} not visible at 720px (display=${p.display})`);

    // (c) geometry: clear of the bar
    if (p.bottom <= p.barTop) {
      pass(`(c) #${p.id} clears the VCR bar (bottom=${p.bottom} <= bar.top=${p.barTop})`);
    } else {
      fail(`(c) #${p.id} overlaps the VCR bar (bottom=${p.bottom} > bar.top=${p.barTop}) — ${p.bottom - p.barTop}px inside it`);
    }

    // (d) hit-test: nothing painted over it
    if (p.hitIsSelf) {
      pass(`(d) elementFromPoint at #${p.id}'s centre resolves to it`);
    } else {
      fail(`(d) something is painted over #${p.id} — elementFromPoint returned ${p.hitTag}`);
    }
  }

  // (e) real, hit-tested clicks. A synthesised .click() would pass even with
  // the bug present, so page.click() is the point.
  if (narrowFeed && narrowFeed.visible) {
    let feedClickErr = null;
    try {
      await page.click('#feedShowBtn', { timeout: 4000 });
    } catch (err) {
      feedClickErr = err.message.split('\n')[0];
    }
    if (feedClickErr) {
      fail(`(e) real click on #feedShowBtn did not land: ${feedClickErr}`);
    } else {
      const feedBack = await page.evaluate(() =>
        !document.getElementById('liveFeed').classList.contains('hidden'));
      if (feedBack) pass('(e) real click on #feedShowBtn restored the live feed');
      else fail('(e) real click on #feedShowBtn was swallowed — #liveFeed stayed hidden');
    }
  }

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

  // ── 1440x900: reserve off. Desktop regression check + anchor invariant ──
  await page.setViewportSize({ width: 1440, height: 900 });
  await gotoLive(page);

  const wideLegend = await probe(page, 'legendToggleBtn');
  const wideFeedShown = await revealFeedShowBtn(page);
  const wideFeed = wideFeedShown ? await probe(page, 'feedShowBtn') : null;

  // (f) the anchor invariant — the whole point of the fix.
  const pairs = [[narrowLegend, wideLegend], [narrowFeed, wideFeed]];
  for (const [n, w] of pairs) {
    if (!n || !w || !n.visible || !w.visible) continue;
    if (w.position === 'fixed' || n.position === 'fixed') {
      fail(`(f) #${n.id} is position:fixed — it resolves its offset against the viewport, not .live-page`);
      continue;
    }
    if (Math.abs(n.slack - w.slack) <= TOL) {
      pass(`(f) #${n.id} offset is reserve-independent (slack ${n.slack}px at 720 vs ${w.slack}px at 1440)`);
    } else {
      fail(`(f) #${n.id} offset moves with --bottom-nav-reserve: slack ${n.slack}px at 720 vs ${w.slack}px at 1440 (delta ${Math.abs(n.slack - w.slack)}px) — still anchored to the viewport`);
    }
  }

  // (g) desktop still fine
  for (const p of [wideLegend, wideFeed].filter(Boolean)) {
    if (!p.visible) {
      fail(`(g) #${p.id} not visible at 1440px (display=${p.display})`);
    } else if (p.bottom <= p.barTop && p.hitIsSelf) {
      pass(`(g) desktop unchanged — #${p.id} clears the bar (bottom=${p.bottom} <= bar.top=${p.barTop}) and is hit-testable`);
    } else {
      fail(`(g) desktop regression on #${p.id} — bottom=${p.bottom}, bar.top=${p.barTop}, hit=${p.hitTag}`);
    }
  }

  await browser.close();

  console.log(`\ntest-issue-1833-legend-toggle-clickable-e2e.js: ${passes} passed, ${failures} failed`);
  process.exit(failures > 0 ? 1 : 0);
}

main().catch((err) => {
  console.error('test-issue-1833-legend-toggle-clickable-e2e.js: FAIL —', err);
  process.exit(1);
});

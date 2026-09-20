#!/usr/bin/env node
/* PR #1490 — the live map's canvas animation engine.
 *
 * The engine draws in-flight packets on its own canvas, drains the queue
 * when each animation finishes, then goes back to sleep. Three things can
 * regress without any visible error: the queue never drains (the map keeps
 * repainting forever), the engine never sleeps (a permanent rAF loop), or
 * the fading trails pile up past the cap of 5.
 *
 * Also pinned here (#1514 M2): the animation canvas and the fading polylines
 * live on animationsPane (z=625), not on Leaflet's default overlayPane
 * (z=400), where they would be drawn underneath the markers.
 *
 * Ported from the @playwright/test version of this file, which was written
 * against a runner this project does not install and so had never run
 * (#2037). The polling the original expressed with expect.poll is done here
 * with waitForFunction. CHROMIUM_REQUIRE=1 makes Chromium-launch failure a
 * HARD FAIL.
 */
'use strict';

const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';

// #1514 S5: 20, not 5, so the `while (recentPaths.length > 5)` prune at
// public/live.js:4285 actually runs and the cap assertion means something.
const PACKET_COUNT = 20;
const RECENT_PATHS_CAP = 5;
// One animation steps at scaledDt/660 (live.js:4154), so 660ms at 1x. The
// @playwright/test original allowed 1500ms, but that figure was never tested:
// the file had no runner. Measured against a populated instance, 20 queued
// animations do not all finish inside it. What this asserts is that the queue
// drains at all and the engine then sleeps, not that it does so within twice
// one animation's duration, so the budget is generous on purpose.
const DRAIN_TIMEOUT_MS = 8000;

let passes = 0, failures = 0;
function pass(msg) { console.log(`  ✓ ${msg}`); passes++; }
function fail(msg) { console.error(`  ✗ ${msg}`); failures++; }

async function main() {
  const requireChromium = process.env.CHROMIUM_REQUIRE === '1';
  let browser;
  try {
    browser = await chromium.launch({ headless: true });
  } catch (err) {
    if (requireChromium) {
      console.error(`HARD FAIL — Chromium unavailable: ${err.message}`);
      process.exit(1);
    }
    console.warn(`SKIP — Chromium unavailable: ${err.message}`);
    process.exit(0);
  }

  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  const page = await ctx.newPage();
  await page.goto(`${BASE}/#/live`, { waitUntil: 'domcontentloaded' });

  try {
    await page.waitForSelector('#liveMap', { timeout: 15000 });
    await page.waitForFunction(
      () => !!window._liveDrawAnimatedLine && !!window._liveTestSeams,
      null,
      { timeout: 15000 },
    );
  } catch {
    fail('the live map never exposed its animation seams within 15s');
    await browser.close();
    console.log(`\ntest-pr-1490-live-map-gpu-animations-e2e: ${passes} passed, ${failures} failed`);
    process.exit(1);
  }

  // (1) The animation canvas is on animationsPane, not on overlayPane.
  // Leaflet's own preferCanvas:true renderer also creates a canvas, on
  // overlayPane, so an unqualified `canvas` selector would match the wrong one.
  const canvasOnPane = await page.evaluate(() => {
    const pane = document.querySelector('.leaflet-pane.leaflet-animations-pane');
    if (!pane) return { pane: false, canvases: 0 };
    return { pane: true, canvases: pane.querySelectorAll('canvas').length };
  });
  if (canvasOnPane.pane && canvasOnPane.canvases >= 1) {
    pass('(1) the animation canvas is attached to animationsPane');
  } else {
    fail(`(1) animationsPane ${canvasOnPane.pane ? 'has no canvas' : 'is missing'} (canvases: ${canvasOnPane.canvases})`);
  }

  // (2) Firing packets queues exactly that many animations and wakes the engine.
  const afterFire = await page.evaluate((count) => {
    if (window._liveVcrSetMode) window._liveVcrSetMode('LIVE');
    for (let i = 0; i < count; i++) {
      window._liveDrawAnimatedLine([37.4, -122.0], [37.5, -122.1], '#00ff00', null, null, '00AA', 'test-hash-' + i);
    }
    return { count: window._liveTestSeams.getAnimCount(), animating: window._liveTestSeams.isAnimating() };
  }, PACKET_COUNT);

  if (afterFire.count === PACKET_COUNT) pass(`(2) all ${PACKET_COUNT} animations were queued`);
  else fail(`(2) queued ${afterFire.count} animations, expected ${PACKET_COUNT}`);

  if (afterFire.animating) pass('(3) the engine woke up');
  else fail('(3) the engine did not wake up, so nothing would be drawn');

  // (4) The queue drains.
  try {
    await page.waitForFunction(() => window._liveTestSeams.getAnimCount() === 0, null, { timeout: DRAIN_TIMEOUT_MS, polling: 50 });
    pass(`(4) the queue drained to 0 within ${DRAIN_TIMEOUT_MS}ms`);
  } catch {
    const left = await page.evaluate(() => window._liveTestSeams.getAnimCount());
    fail(`(4) ${left} animation(s) still queued after ${DRAIN_TIMEOUT_MS}ms`);
  }

  // (5) The engine goes back to sleep. There is one rAF tick between the
  // queue emptying and renderAnimations flipping isAnimating to false, so
  // poll rather than read once (#1514).
  try {
    await page.waitForFunction(() => window._liveTestSeams.isAnimating() === false, null, { timeout: 1000, polling: 50 });
    pass('(5) the engine went back to sleep');
  } catch {
    fail('(5) isAnimating stayed true with an empty queue, so the rAF loop never stops');
  }

  // (6) The fading trails respect the cap.
  const paths = await page.evaluate(() => window._liveTestSeams.getPathCount());
  if (paths <= RECENT_PATHS_CAP) pass(`(6) recentPaths held at ${paths}, within the cap of ${RECENT_PATHS_CAP}`);
  else fail(`(6) recentPaths grew to ${paths}, past the cap of ${RECENT_PATHS_CAP}`);

  await browser.close();
  console.log(`\ntest-pr-1490-live-map-gpu-animations-e2e: ${passes} passed, ${failures} failed`);
  process.exit(failures ? 1 : 0);
}

main().catch((e) => { console.error(e); process.exit(1); });

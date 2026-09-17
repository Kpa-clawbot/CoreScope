#!/usr/bin/env node
/* Issue #1599 — Replay from packets sidebar must not freeze the live map.
 *
 * Reproduces the freeze caused by the replay handoff calling vcrPause() to
 * silence live WS traffic. vcrPause() sets VCR.mode='PAUSED', and
 * renderAnimations() gates `anim.progress` advancement on `!isPaused`, so the
 * replayed animation never advances and the map appears frozen.
 *
 * Repro:
 *   1. Seed sessionStorage['replay-packet'] with a synthetic packet (the
 *      packets-sidebar "Replay" button does this before navigating to /#/live).
 *   2. Load /#/live. live.js init reads the key and (currently) calls
 *      vcrPause().
 *   3. Push an animation via the existing _liveDrawAnimatedLine test seam
 *      AFTER the replay handoff has run.
 *   4. Wait 2x the canvas-engine duration (660ms base → 1500ms total).
 *
 * Expected behaviour after fix: the animation drains to 0 (engine still
 * running because VCR stays in LIVE mode; live WS suppression is handled by a
 * dedicated flag, not by entering PAUSED).
 *
 * Pre-fix behaviour: VCR.mode is 'PAUSED' → animation progress stays 0 →
 * activeAnimations never drains → test fails on the drain assertion.
 */
'use strict';

const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';

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
      console.error(`test-issue-1599-replay-freeze-e2e.js: FAIL — Chromium required but unavailable: ${err.message}`);
      process.exit(1);
    }
    console.log(`test-issue-1599-replay-freeze-e2e.js: SKIP (Chromium unavailable: ${err.message.split('\n')[0]})`);
    process.exit(0);
  }

  let failures = 0, passes = 0;
  const fail = (m) => { failures++; console.error('  FAIL: ' + m); };
  const pass = (m) => { passes++; console.log('  PASS: ' + m); };

  const ctx = await browser.newContext();
  const page = await ctx.newPage();
  page.setDefaultTimeout(15000);

  // Seed sessionStorage BEFORE the page script runs (mirrors the packets
  // sidebar "Replay" button which sets the key then navigates to /#/live).
  await page.addInitScript(() => {
    try {
      const syntheticPacket = {
        hash: 'test1599deadbeef',
        decoded: {
          header: { payloadTypeName: 'ADVERT' },
          payload: {},
          path: { hops: [] },
        },
      };
      sessionStorage.setItem('replay-packet', JSON.stringify([syntheticPacket]));
    } catch (_) {}
  });

  try {
    await page.goto(`${BASE}/#/live`, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#liveMap', { timeout: 15000 });
    await page.waitForFunction(() => !!(window._liveTestSeams && window._liveDrawAnimatedLine), null, { timeout: 15000 });

    // Wait past the 1500ms setTimeout in the replay handoff so VCR mode is
    // settled into whatever state the handoff leaves it in.
    await page.waitForTimeout(1700);

    // Sanity: confirm the handoff actually consumed the sessionStorage key
    // (proves the init path ran the replay branch).
    const replayKeyConsumed = await page.evaluate(() => sessionStorage.getItem('replay-packet') === null);
    if (replayKeyConsumed) pass('replay-packet sessionStorage key was consumed by live.js init');
    else fail('replay-packet sessionStorage key still present — handoff did not run');

    // Push a synthetic animation onto the canvas engine post-handoff.
    await page.evaluate(() => {
      window._liveDrawAnimatedLine(
        [37.4, -122.0],
        [37.5, -122.1],
        '#00ff00',
        null,
        null,
        '00AA',
        'test-1599-anim'
      );
    });

    const initialCount = await page.evaluate(() => window._liveTestSeams.getAnimCount());
    if (initialCount >= 1) pass(`animation queued (count=${initialCount})`);
    else fail(`animation did not queue (count=${initialCount})`);

    // Core assertion: with the replay handoff leaving VCR in LIVE mode
    // (post-fix), the canvas engine advances progress and the animation
    // drains within ~2× the 660ms base duration. Pre-fix the handoff sets
    // VCR.mode='PAUSED' which freezes progress, so the engine never drains.
    //
    // Headless Chromium throttles requestAnimationFrame when no compositing
    // is happening, so we pump rAF callbacks from inside the page to give
    // the engine a deterministic chance to advance.
    const drainedTo = await page.evaluate(async () => {
      const seam = window._liveTestSeams;
      // ~30 frames at ~16ms each ≈ 480ms of simulated time, well over the
      // 660ms / 60fps it takes one animation to drain, but the rAF callback
      // bridge advances by real timestamps so we keep pumping until either
      // the queue drains or we hit a hard cap (90 frames ≈ 1.5s wall-clock).
      for (let i = 0; i < 90; i++) {
        if (seam.getAnimCount() === 0) break;
        await new Promise((r) => requestAnimationFrame(r));
      }
      return seam.getAnimCount();
    });

    if (drainedTo === 0) {
      pass('activeAnimations drained to 0 — canvas engine advanced progress during replay');
    } else {
      fail(`activeAnimations did NOT drain after replay handoff (count=${drainedTo}) — replay freeze regression`);
    }
  } catch (e) {
    fail(`unexpected error: ${e && e.stack || e}`);
  } finally {
    try { await browser.close(); } catch (_) {}
  }

  console.log(`\ntest-issue-1599-replay-freeze-e2e.js: ${passes} pass, ${failures} fail`);
  process.exit(failures === 0 ? 0 : 1);
}

main().catch((e) => { console.error(e); process.exit(1); });

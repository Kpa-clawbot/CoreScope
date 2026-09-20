#!/usr/bin/env node
/* The live map's pulse highlight ring must stay at least 2px wide.
 *
 * The ring is drawn on canvas, so it has no CSS rule an accessibility
 * checker can read: the weight is set in code (public/live.js, where the
 * pulse's hl_weight is stepped down as the highlight fades). A change that
 * let it taper to a hairline would be invisible to every other test in the
 * repo, and to axe, which cannot see inside a canvas.
 *
 * The marker unit suites (#1293 shapes, #1438 and #1488 CSS vars) cover the
 * DOM markers and their custom properties, not this.
 *
 * Method: drive a synthetic pulse through the live page's test seams
 * (window._liveTestSeams, public/live.js) and sample hl_weight every frame
 * for as long as the ring is actually visible (hl_op > 0).
 *
 * Ported from the @playwright/test version of this file, which was written
 * against a runner this project does not install and so had never run
 * (#2037). CHROMIUM_REQUIRE=1 makes Chromium-launch failure a HARD FAIL.
 */
'use strict';

const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';
const MIN_WEIGHT = 2;

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
    await page.waitForFunction(() => window._liveTestSeams && typeof window._liveTestSeams.triggerPulse === 'function', null, { timeout: 15000 });
  } catch {
    fail('the live page never exposed _liveTestSeams.triggerPulse within 15s');
    await browser.close();
    console.log(`\ntest-marker-outline-weight: ${passes} passed, ${failures} failed`);
    process.exit(1);
  }

  const result = await page.evaluate((minWeight) => new Promise((resolve) => {
    const seams = window._liveTestSeams;
    seams.triggerPulse('a11y-test-node', [38.8951, -77.0364], 'ADVERT');

    let minSeen = Infinity;
    let framesWithRing = 0;
    // A pulse that never starts, or never ends, must not hang the suite.
    const deadline = Date.now() + 10000;

    function checkFrame() {
      const pulses = seams.getPulses();
      if (pulses.length === 0 || Date.now() > deadline) {
        resolve({ minSeen, framesWithRing, timedOut: Date.now() > deadline, minWeight });
        return;
      }
      const p = pulses[0];
      if (p.hl_op > 0) {
        framesWithRing++;
        if (p.hl_weight < minSeen) minSeen = p.hl_weight;
      }
      requestAnimationFrame(checkFrame);
    }
    requestAnimationFrame(checkFrame);
  }), MIN_WEIGHT);

  // A pulse that produced no visible ring would leave minSeen at Infinity and
  // make the weight assertion vacuously true, so check that first.
  if (result.framesWithRing > 0) {
    pass(`(1) the pulse rendered a visible highlight ring (${result.framesWithRing} frames)`);
  } else {
    fail(`(1) no frame had hl_op > 0, so the weight below was never actually exercised${result.timedOut ? ' (timed out)' : ''}`);
  }

  if (result.framesWithRing > 0 && result.minSeen >= MIN_WEIGHT) {
    pass(`(2) hl_weight stayed at or above ${MIN_WEIGHT}px (lowest seen: ${result.minSeen})`);
  } else if (result.framesWithRing > 0) {
    fail(`(2) hl_weight dropped to ${result.minSeen}, below the ${MIN_WEIGHT}px minimum`);
  }

  await browser.close();
  console.log(`\ntest-marker-outline-weight: ${passes} passed, ${failures} failed`);
  process.exit(failures ? 1 : 0);
}

main().catch((e) => { console.error(e); process.exit(1); });

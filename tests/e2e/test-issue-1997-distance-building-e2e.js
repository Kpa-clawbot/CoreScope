#!/usr/bin/env node
/* Issue #1997 — Analytics → Distance against the lazy index's 202.
 *
 * /api/analytics/distance answers 202 {status:"building", retry_after_seconds}
 * with no summary until the distance index (#1011) has been built. The tab
 * read data.summary.totalHops straight away and threw
 * "Cannot read properties of undefined (reading 'totalHops')", and api()
 * cached the placeholder (res.ok is true for 202), so every retry read the
 * cached "building" body back and the page never recovered.
 *
 * The unit suite (tests/unit/test-issue-1997-distance-building.js) pins the
 * two pure decisions and api()'s refusal to cache a 202. This one pins what
 * the unit suite cannot: that a real browser, on a real render, shows the
 * building state instead of an exception, retries on its own, replaces it
 * with the data once the server is ready, and stops retrying when the user
 * leaves the tab.
 *
 * Both responses are served by a route interception, so the suite does not
 * depend on whether the server under test has an index built.
 *
 * CHROMIUM_REQUIRE=1 makes Chromium-launch failure a HARD FAIL.
 */
'use strict';

const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';

let passes = 0, failures = 0;
function pass(msg) { console.log(`  ✓ ${msg}`); passes++; }
function fail(msg) { console.error(`  ✗ ${msg}`); failures++; }

const READY = {
  summary: { totalHops: 4242, totalPaths: 77, avgDist: 12.5, maxDist: 88.25 },
  catStats: { 'R↔R': { count: 10, avg: 12, median: 11, min: 1, max: 40 } },
  distHistogram: { bins: [{ x: 1.5, count: 3 }, { x: 2.5, count: 5 }] },
  distOverTime: [],
  topHops: [],
  topPaths: [],
};

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

  // The server answers "building" until this flips, exactly like the lazy
  // index does once its first build finishes.
  let ready = false;
  let distanceCalls = 0;
  await page.route('**/api/analytics/distance*', async (route) => {
    distanceCalls++;
    if (ready) {
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(READY) });
    } else {
      await route.fulfill({
        status: 202,
        contentType: 'application/json',
        body: JSON.stringify({ status: 'building', retry_after_seconds: 1, detail: 'distance index is being computed' }),
      });
    }
  });

  // Any "reading 'totalHops' of undefined" would land here.
  const pageErrors = [];
  page.on('pageerror', (e) => pageErrors.push(e.message));

  await page.goto(`${BASE}/#/analytics?tab=distance`, { waitUntil: 'domcontentloaded' });

  // (1) The 202 renders the building state, not an exception and not a crash.
  try {
    await page.waitForSelector('#distanceBuilding', { timeout: 15000 });
    pass('(1) the 202 renders the building state');
  } catch {
    const shown = await page.evaluate(() => {
      const el = document.getElementById('analyticsContent');
      return el ? (el.innerText || '').slice(0, 300) : '(no #analyticsContent)';
    });
    fail(`(1) the building state never appeared within 15s; the tab showed: ${JSON.stringify(shown)}`);
  }

  // (2) The reported symptom, asserted where it is actually visible.
  // renderDistanceTab wraps its body in try/catch, so the TypeError never
  // reaches window.onerror: it is caught and painted into the tab as
  // "Failed to load distance analytics: Cannot read properties of undefined
  // (reading 'totalHops')". Asserting on pageErrors here would pass on the
  // broken build too, so assert on the text the user sees.
  const buildingText = await page.evaluate(() => {
    const el = document.getElementById('analyticsContent');
    return el ? (el.innerText || '') : '';
  });
  if (!/Failed to load distance analytics|Cannot read properties of undefined/.test(buildingText)) {
    pass('(2) the building body is not rendered as data');
  } else {
    fail(`(2) the building body was rendered as data: ${JSON.stringify(buildingText.slice(0, 160))}`);
  }

  // (3) The tab retries on its own: a second request arrives without any
  // interaction. retry_after_seconds is 1, so this is a short wait.
  const callsAfterFirstRender = distanceCalls;
  // The counter lives in Node (the route handler), so poll it here.
  const deadline = Date.now() + 10000;
  while (distanceCalls <= callsAfterFirstRender && Date.now() < deadline) {
    await page.waitForTimeout(200);
  }
  if (distanceCalls > callsAfterFirstRender) pass(`(3) the tab retried on its own (${distanceCalls} requests)`);
  else fail('(3) no retry arrived within 10s, so a stuck build never recovers');

  // (4) Once the server is ready, the retry replaces the placeholder with
  // the real numbers. This is the half that api()'s 202 caching broke: a
  // cached placeholder would keep the building state on screen.
  ready = true;
  try {
    await page.waitForFunction(() => {
      const el = document.getElementById('analyticsContent');
      return !!el && /4,?242/.test(el.innerText || '');
    }, null, { timeout: 15000 });
    pass('(4) the retry replaces the placeholder with the real payload');
  } catch {
    const shown = await page.evaluate(() => {
      const el = document.getElementById('analyticsContent');
      return el ? (el.innerText || '').slice(0, 300) : '(no #analyticsContent)';
    });
    fail(`(4) the tab never showed the ready payload; it showed: ${JSON.stringify(shown)}`);
  }

  // (5) Leaving the tab stops the retry. Go back to building, let one retry
  // be scheduled, navigate away, and assert the request count stops moving.
  ready = false;
  await page.evaluate(() => { window.location.hash = '#/analytics?tab=overview'; });
  await page.waitForTimeout(500);
  const before = distanceCalls;
  await page.waitForTimeout(3000); // three retry intervals
  if (distanceCalls === before) pass('(5) no retry fires after leaving the tab');
  else fail(`(5) the retry kept running after leaving the tab: ${distanceCalls - before} extra request(s)`);

  if (pageErrors.length) console.log(`  (page errors seen: ${pageErrors.length}) ${pageErrors.slice(0, 3).join(' | ')}`);

  await browser.close();
  console.log(`\ntest-issue-1997-distance-building-e2e: ${passes} passed, ${failures} failed`);
  process.exit(failures ? 1 : 0);
}

main().catch((e) => { console.error(e); process.exit(1); });

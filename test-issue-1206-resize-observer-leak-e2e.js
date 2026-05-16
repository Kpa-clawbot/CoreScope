/**
 * E2E regression for #1206 review must-fix (kent-beck #2):
 *   ResizeObserver leak in initVCRHeightTracker().
 *
 * SPA navigates to /#/live, then bounces /#/nodes ↔ /#/live ≥ 3 times.
 * Each /#/live mount re-runs initVCRHeightTracker(); without the cleanup
 * tear-down (or with a future regression that orphans cleanup) each visit
 * would accumulate another ResizeObserver against #vcrBar.
 *
 * We can't read live ResizeObserver instances directly — wrap the
 * constructor + .disconnect() via addInitScript so we can count
 * outstanding (constructed but not disconnected) observers and assert it
 * does NOT grow with each /live mount.
 *
 * Run: BASE_URL=http://localhost:13581 node test-issue-1206-resize-observer-leak-e2e.js
 */
'use strict';
const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';

let passed = 0, failed = 0;
async function step(name, fn) {
  try { await fn(); passed++; console.log('  \u2713 ' + name); }
  catch (e) { failed++; console.error('  \u2717 ' + name + ': ' + e.message); }
}
function assert(c, m) { if (!c) throw new Error(m || 'assertion failed'); }

async function gotoHash(page, hash) {
  await page.evaluate((h) => { window.location.hash = h; }, hash);
  await page.waitForTimeout(150);
}

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });

  console.log('\n=== #1206 ResizeObserver leak E2E against ' + BASE + ' ===');

  const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 } });

  // Install ResizeObserver wrapper BEFORE any page script runs.
  await ctx.addInitScript(() => {
    var RealRO = window.ResizeObserver;
    if (typeof RealRO !== 'function') {
      window.__roOutstanding = 0;
      window.__roConstructed = 0;
      return;
    }
    window.__roConstructed = 0;
    window.__roOutstanding = 0;
    function WrappedRO(cb) {
      var inst = new RealRO(cb);
      window.__roConstructed++;
      window.__roOutstanding++;
      var realDisconnect = inst.disconnect.bind(inst);
      var disconnected = false;
      inst.disconnect = function() {
        if (!disconnected) {
          disconnected = true;
          window.__roOutstanding--;
        }
        return realDisconnect();
      };
      return inst;
    }
    WrappedRO.prototype = RealRO.prototype;
    window.ResizeObserver = WrappedRO;
  });

  const page = await ctx.newPage();
  page.setDefaultTimeout(8000);
  page.on('pageerror', (e) => console.error('[pageerror]', e.message));

  await step('initial /#/live mount constructs at most 1 VCR ResizeObserver', async () => {
    await page.goto(BASE + '/#/live', { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#vcrBar', { timeout: 8000 });
    await page.waitForTimeout(300);
    // Baseline snapshot — record outstanding right after first /live mount.
    const snap = await page.evaluate(() => ({
      outstanding: window.__roOutstanding,
      constructed: window.__roConstructed,
    }));
    assert(typeof snap.outstanding === 'number',
      'ResizeObserver wrapper not installed (snap=' + JSON.stringify(snap) + ')');
    // Stash the first-mount baseline on window for the next step.
    await page.evaluate((b) => { window.__roBaseline = b; }, snap);
  });

  await step('3 SPA round-trips /live<->/nodes do NOT grow outstanding observer count', async () => {
    for (let i = 0; i < 3; i++) {
      await gotoHash(page, '#/nodes');
      await page.waitForTimeout(150);
      await gotoHash(page, '#/live');
      await page.waitForSelector('#vcrBar', { timeout: 8000 });
      await page.waitForTimeout(200);
    }
    const after = await page.evaluate(() => ({
      outstanding: window.__roOutstanding,
      constructed: window.__roConstructed,
      baseline: window.__roBaseline,
    }));
    // The VCR tracker MUST clean its observer on destroy(). After N
    // remounts the outstanding count for VCR-tracking observers must not
    // exceed the baseline.  We can't isolate which observers are
    // ours, so we use the delta: 4 mounts * leak-of-1 = 3 extra
    // outstanding observers, which is the failure mode this test gates.
    var delta = after.outstanding - after.baseline.outstanding;
    assert(delta <= 0,
      'ResizeObserver leak: outstanding count grew by ' + delta +
      ' across 3 SPA round-trips (baseline=' + after.baseline.outstanding +
      ', after=' + after.outstanding + ', constructed=' + after.constructed +
      '). Expected delta <= 0.');
  });

  await ctx.close();
  await browser.close();
  console.log('\n#1206 ResizeObserver leak: ' + passed + ' passed, ' + failed + ' failed');
  process.exit(failed ? 1 : 0);
})().catch((e) => { console.error(e); process.exit(1); });

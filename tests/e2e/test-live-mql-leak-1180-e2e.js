/**
 * E2E regression for #1180 review must-fix:
 *   MediaQueryList 'change' listener leak in wireLiveCollapseToggles().
 *
 * SPA navigates to /#/live, then bounces /#/explore ↔ /#/live N times.
 * Each /#/live mount re-runs the wiring IIFE; without a guard, every
 * mount calls narrowMql.addEventListener('change', applyForViewport)
 * against a process-global MediaQueryList instance, so listeners
 * accumulate without bound.
 *
 * live.js exposes a debug seam: window.__liveMQLBindCount is incremented
 * exactly when the MQL listener is registered. After 5 round-trips it
 * MUST be ≤ 1.
 *
 * Run: BASE_URL=http://localhost:13581 node test-live-mql-leak-1180-e2e.js
 */
'use strict';
const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';

let passed = 0, failed = 0;
async function step(name, fn) {
  try { await fn(); passed++; console.log('  ✓ ' + name); }
  catch (e) { failed++; console.error('  ✗ ' + name + ': ' + e.message); }
}
function assert(c, m) { if (!c) throw new Error(m || 'assertion failed'); }

async function gotoHash(page, hash) {
  await page.evaluate((h) => { window.location.hash = h; }, hash);
  // Allow router to run
  await page.waitForTimeout(150);
}

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });

  console.log(`\n=== #1180 MQL listener leak E2E against ${BASE} ===`);

  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } });
  const page = await ctx.newPage();
  page.setDefaultTimeout(8000);
  page.on('pageerror', (e) => console.error('[pageerror]', e.message));

  await step('initial /#/live load registers MQL listener at most once', async () => {
    await page.goto(BASE + '/#/live', { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#liveHeader, .live-header', { timeout: 8000 });
    await page.waitForTimeout(300);
    const count = await page.evaluate(() => window.__liveMQLBindCount);
    assert(typeof count === 'number',
      'window.__liveMQLBindCount missing — debug seam not exposed by live.js');
    assert(count <= 1, `expected MQL bind count ≤ 1 after first mount, got ${count}`);
  });

  await step('5 SPA round-trips do NOT accumulate MQL listeners', async () => {
    for (let i = 0; i < 5; i++) {
      await gotoHash(page, '#/packets');
      await page.waitForTimeout(80);
      await gotoHash(page, '#/live');
      await page.waitForSelector('#liveHeader, .live-header', { timeout: 8000 });
      await page.waitForTimeout(120);
    }
    const count = await page.evaluate(() => window.__liveMQLBindCount);
    assert(typeof count === 'number',
      'window.__liveMQLBindCount missing after navigations');
    assert(count <= 1,
      `MQL listener leak: bind count after 5 round-trips = ${count}, expected ≤ 1`);
  });

  await ctx.close();
  await browser.close();
  console.log(`\n=== Results: passed ${passed} failed ${failed} ===`);
  process.exit(failed > 0 ? 1 : 0);
})().catch(e => { console.error(e); process.exit(1); });

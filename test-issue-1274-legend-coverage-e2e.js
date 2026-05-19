/**
 * E2E for #1274 — Live legend must document gray packets (ACK + unknown
 * payload types), the RESPONSE and PATH colors, AND the white-ring
 * repeater convention. See issue #1274 acceptance criteria.
 *
 * Run: BASE_URL=http://localhost:13581 node test-issue-1274-legend-coverage-e2e.js
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

async function gotoLive(page) {
  await page.goto(BASE + '/#/live', { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('#liveLegend', { timeout: 8000, state: 'attached' });
  await page.waitForTimeout(400);
  // Ensure the legend is expanded (it persists collapsed state via localStorage).
  const hidden = await page.evaluate(() => {
    const el = document.getElementById('liveLegend');
    return !!el && el.classList.contains('hidden');
  });
  if (hidden) {
    await page.evaluate(() => {
      try { localStorage.removeItem('live-legend-hidden'); } catch (_) {}
      const el = document.getElementById('liveLegend');
      if (el) el.classList.remove('hidden');
    });
  }
}

async function legendText(page) {
  return page.evaluate(() => {
    const el = document.getElementById('liveLegend');
    return el ? (el.textContent || '').toLowerCase() : '';
  });
}

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });

  console.log(`\n=== #1274 legend documents ACK/RESPONSE/PATH + white-ring — E2E against ${BASE} ===`);

  for (const vp of [
    { w: 1440, h: 900, tag: '[1440x900 desktop]' },
    { w: 375,  h: 800, tag: '[375x800 mobile]' },
  ]) {
    const ctx = await browser.newContext({ viewport: { width: vp.w, height: vp.h } });
    const page = await ctx.newPage();
    page.setDefaultTimeout(8000);
    page.on('pageerror', (e) => console.error('[pageerror]', e.message));
    await step(vp.tag + ' navigate to /live', async () => { await gotoLive(page); });

    await step(vp.tag + ' legend lists ACK row', async () => {
      const t = await legendText(page);
      assert(/\back\b/.test(t), 'legend missing ACK row; text=' + t.slice(0, 400));
    });
    await step(vp.tag + ' legend lists RESPONSE row', async () => {
      const t = await legendText(page);
      assert(/response/.test(t), 'legend missing RESPONSE row');
    });
    await step(vp.tag + ' legend lists PATH row', async () => {
      const t = await legendText(page);
      assert(/path/.test(t), 'legend missing PATH row');
    });
    await step(vp.tag + ' legend documents white-ring / repeater convention', async () => {
      const t = await legendText(page);
      assert(/repeater/.test(t) && /ring/.test(t),
        'legend missing repeater white-ring documentation; text=' + t.slice(0, 600));
    });

    await ctx.close();
  }

  await browser.close();
  console.log(`\n=== ${passed} passed, ${failed} failed ===`);
  process.exit(failed === 0 ? 0 : 1);
})().catch((e) => { console.error(e); process.exit(1); });

/**
 * E2E for #1279 P2 #2 — Live legend covers all remaining named payload types.
 * After PR #1276 the legend already lists Advert/Message/Direct/Request/
 * Response/Trace/Path/Ack; this PR adds Anon Req, Group Data, Multipart,
 * Control and Raw Custom.
 *
 * Run: BASE_URL=http://localhost:13581 node test-issue-1279-legend-p2-e2e.js
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

  console.log(`\n=== #1279 P2 legend covers all 13 payload types — E2E against ${BASE} ===`);

  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } });
  const page = await ctx.newPage();
  page.setDefaultTimeout(8000);
  page.on('pageerror', (e) => console.error('[pageerror]', e.message));

  await step('navigate to /live', async () => { await gotoLive(page); });

  // Types already covered by #1274/#1276: Advert/Message/Direct/Request/
  // Response/Trace/Path/Ack. New ones added by #1279 P2:
  const newRows = ['anon req', 'group data', 'multipart', 'control', 'raw custom'];
  for (const label of newRows) {
    await step(`legend lists "${label}"`, async () => {
      const t = await legendText(page);
      assert(t.indexOf(label) !== -1, 'legend missing row: ' + label);
    });
  }

  await ctx.close();
  await browser.close();
  console.log(`\n=== ${passed} passed, ${failed} failed ===`);
  process.exit(failed === 0 ? 0 : 1);
})().catch((e) => { console.error(e); process.exit(1); });

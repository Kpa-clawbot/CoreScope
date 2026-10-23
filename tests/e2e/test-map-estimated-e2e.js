/**
 * E2E: estimated positions on the map (map-estimated.js, /api/nodes/reported).
 *
 * A repeater known only from its reported name has no advert position. When
 * the server can estimate one, the map draws it as a hollow dashed "≈" marker
 * with a dashed uncertainty circle and a popup that says it is an estimate.
 * The "Estimated positions" toggle hides both, and the choice is remembered.
 * The repeater role filter and route view hide them as well.
 * A reported repeater without an estimate draws nothing.
 *
 * Backend-independent: every /api/* call is mocked via page.route.
 *
 * Run: BASE_URL=http://localhost:13581 node test-map-estimated-e2e.js
 */
'use strict';
const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';
const PLACED = 'c0cdee59e0db981b00000000000000000000000000000000000000000000000a';
const UNPLACED = 'b1b1220000000000000000000000000000000000000000000000000000000000';

let passed = 0, failed = 0;
async function step(name, fn) {
  try { await fn(); passed++; console.log('  ✓ ' + name); }
  catch (e) { failed++; console.error('  ✗ ' + name + ': ' + e.message); }
}
function assert(c, m) { if (!c) throw new Error(m || 'assertion failed'); }

const reported = {
  nodes: [
    { pubkey: PLACED, name: 'Self Named', name_reported_at: '2026-10-16T10:00:00.000Z', reporters: 1, receptions: 15, spots: 4,
      first_heard: '2026-09-18T06:36:06Z', last_heard: '2026-10-16T06:50:48Z', estimate: { lat: 50.99712, lon: 5.37811, radius_m: 2480 } },
    { pubkey: UNPLACED, name: 'Lonely', name_reported_at: '2026-10-16T09:00:00.000Z', reporters: 1, receptions: 2, spots: 1 },
  ],
  method: { spot_m: 100, min_spots: 3, min_spread_m: 200, min_radius_m: 500 },
};

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  const page = await ctx.newPage();
  page.setDefaultTimeout(15000);
  const errors = [];
  page.on('pageerror', (e) => errors.push(e.message));

  console.log(`\n=== Map estimated positions E2E against ${BASE} ===`);

  await page.route('**/api/**', (route) => {
    const path = new URL(route.request().url()).pathname;
    const json = (body) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });
    if (path === '/api/nodes/reported') return json(reported);
    if (path === '/api/nodes') {
      return json({ nodes: [{ public_key: 'aa' + '0'.repeat(62), name: 'Real Repeater', role: 'repeater', lat: 51.0, lon: 5.4, last_seen: new Date().toISOString(), hash_size: 1 }],
        total: 1, has_more: false, counts: { repeaters: 1, rooms: 0, companions: 0, sensors: 0 } });
    }
    if (path === '/api/observers') return json({ observers: [] });
    if (path === '/api/config/map') return json({ center: [51.0, 5.38], zoom: 12 });
    return json({});
  });

  await page.goto(BASE + '/#/map', { waitUntil: 'load', timeout: 60000 });
  await page.waitForSelector('#leaflet-map', { timeout: 15000 });

  await step('the estimate is drawn as a dashed "≈" marker with an uncertainty circle', async () => {
    await page.waitForSelector('.mc-est-marker', { timeout: 15000 });
    const n = await page.$$eval('.mc-est-marker', (els) => els.length);
    assert(n === 1, 'expected 1 estimated marker (the unplaced repeater draws nothing), got ' + n);
    const glyph = await page.$eval('.mc-est-marker', (el) => el.textContent.trim());
    assert(glyph === '≈', 'marker glyph ' + JSON.stringify(glyph));
    const dash = await page.$eval('path.mc-est-area', (el) => getComputedStyle(el).strokeDasharray);
    assert(dash && dash !== 'none', 'uncertainty circle is not dashed: ' + dash);
  });

  await step('the popup says it is an estimate', async () => {
    await page.click('.mc-est-marker');
    await page.waitForSelector('.mc-est-popup');
    const text = await page.$eval('.mc-est-popup', (el) => el.textContent);
    assert(text.includes('Estimated position') && text.includes('No advert position'), 'popup text: ' + text);
    assert(text.includes('2.5 km'), 'popup radius missing: ' + text);
  });

  await step('the toggle hides marker and circle and is remembered', async () => {
    await page.evaluate(() => { const el = document.getElementById('mcEstimated'); el.click(); });
    await page.waitForFunction(() => !document.querySelector('.mc-est-marker') && !document.querySelector('path.mc-est-area'));
    const stored = await page.evaluate(() => localStorage.getItem('meshcore-map-estimated'));
    assert(stored === 'false', 'localStorage meshcore-map-estimated = ' + stored);
    await page.evaluate(() => document.getElementById('mcEstimated').click());
    await page.waitForSelector('.mc-est-marker');
  });

  await step('the repeater role filter hides the estimates too', async () => {
    await page.waitForSelector('input[data-role="repeater"]');
    await page.click('input[data-role="repeater"]');
    await page.waitForFunction(() => !document.querySelector('.mc-est-marker') && !document.querySelector('path.mc-est-area'));
    await page.click('input[data-role="repeater"]');
    await page.waitForSelector('.mc-est-marker');
  });

  await step('route view hides the estimates, closing it brings them back', async () => {
    await page.evaluate(() => window.drawPacketRouteMulti([{ path: ['aa0000'] }], null, {}));
    await page.waitForFunction(() => !document.querySelector('.mc-est-marker') && !document.querySelector('path.mc-est-area'));
    await page.click('a[aria-label="Close route"]');
    await page.waitForSelector('.mc-est-marker');
  });

  await step('no page errors', async () => {
    assert(errors.length === 0, errors.join(' | '));
  });

  await browser.close();
  console.log(`\n${passed} passed, ${failed} failed`);
  process.exit(failed > 0 ? 1 : 0);
})().catch((e) => { console.error(e); process.exit(1); });

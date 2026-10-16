/**
 * E2E: repeaters known only from their reported name are findable in the UI
 * (reported-nodes.js, /api/nodes/reported).
 *
 * - The Nodes page lists them in a "Self-reported repeaters" section, filtered
 *   by the search box, with the position marked as an estimate or unknown.
 * - #/nodes/<pubkey> for such a repeater (where /api/nodes/<pubkey> is 404)
 *   shows a self-reported detail view instead of "Node not found".
 * - The header search returns them, tagged as self-reported.
 *
 * Backend-independent: every /api/* call is mocked via page.route.
 *
 * Run: BASE_URL=http://localhost:13581 node test-reported-nodes-e2e.js
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
    { pubkey: PLACED, name: 'Gent Zuid', name_reported_at: '2026-10-16T10:00:00.000Z', reporters: 1, receptions: 15, spots: 4,
      first_heard: '2026-09-18T06:36:06Z', last_heard: '2026-10-16T06:50:48Z', estimate: { lat: 50.99712, lon: 5.37811, radius_m: 2480 } },
    { pubkey: UNPLACED, name: 'Lonely', name_reported_at: '2026-10-16T09:00:00.000Z', reporters: 1, receptions: 2, spots: 1,
      first_heard: '2026-10-06T10:00:00Z', last_heard: '2026-10-06T10:01:00Z' },
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

  console.log(`\n=== Self-reported repeaters E2E against ${BASE} ===`);

  await page.route('**/api/**', (route) => {
    const path = new URL(route.request().url()).pathname;
    const json = (body, status) => route.fulfill({ status: status || 200, contentType: 'application/json', body: JSON.stringify(body) });
    if (path === '/api/nodes/reported') return json(reported);
    if (path === '/api/nodes') {
      return json({ nodes: [{ public_key: 'aa' + '0'.repeat(62), name: 'Real Repeater', role: 'repeater', lat: 51.0, lon: 5.4, last_seen: new Date().toISOString(), advert_count: 3 }],
        total: 1, has_more: false, counts: { repeaters: 1, rooms: 0, companions: 0, sensors: 0 } });
    }
    if (path === '/api/nodes/clock-skew') return json([]);
    if (path.startsWith('/api/nodes/')) return json({ error: 'Not found' }, 404);
    if (path === '/api/observers') return json({ observers: [] });
    if (path === '/api/channels') return json([]);
    if (path === '/api/packets') return json({ packets: [] });
    return json({});
  });

  await step('the Nodes page lists self-reported repeaters under the table', async () => {
    await page.goto(BASE + '/#/nodes', { waitUntil: 'load', timeout: 60000 });
    await page.waitForSelector('.reported-section');
    const text = await page.$eval('.reported-section', (el) => el.textContent);
    assert(text.includes('Self-reported repeaters') && text.includes('Gent Zuid') && text.includes('Lonely'), text);
    assert(text.includes('≈ estimated, ± 2.5 km'), 'estimate not marked: ' + text);
    assert(text.includes('Unknown, heard at 1 spot (estimate needs 3)'), 'missing reason: ' + text);
  });

  await step('the search box filters the section', async () => {
    await page.fill('#nodeSearch', 'lonely');
    await page.waitForFunction(() => {
      const el = document.querySelector('.reported-section');
      return el && !el.textContent.includes('Gent Zuid') && el.textContent.includes('Lonely');
    });
  });

  await step('a self-reported repeater opens a detail view, not "Node not found"', async () => {
    await page.goto(BASE + '/#/nodes/' + PLACED, { waitUntil: 'load' });
    await page.waitForSelector('.reported-detail');
    const text = await page.$eval('.reported-detail', (el) => el.textContent);
    assert(text.includes('Self-reported repeater') && text.includes('No advert from this repeater'), text);
    assert(text.includes('estimated, within about 2.5 km'), text);
    const title = await page.$eval('.node-full-title', (el) => el.textContent);
    assert(title.includes('Gent Zuid (self-reported)'), 'title: ' + title);
  });

  await step('an unknown pubkey still says "Node not found"', async () => {
    await page.goto(BASE + '/#/nodes/' + 'ff'.repeat(32), { waitUntil: 'load' });
    await page.waitForFunction(() => /Node not found/.test(document.body.textContent));
    assert(!(await page.$('.reported-detail')), 'reported view shown for an unknown key');
  });

  await step('the header search returns them, tagged self-reported', async () => {
    await page.goto(BASE + '/#/home', { waitUntil: 'load' });
    await page.click('#searchToggle');
    await page.fill('#searchInput', 'gent');
    await page.waitForSelector('.search-result-item .reported-tag');
    const item = await page.$eval('.search-result-item .reported-tag', (el) => el.closest('.search-result-item').dataset.href);
    assert(item === '#/nodes/' + PLACED, 'href ' + item);
  });

  await step('no page errors other than the mocked 404s', async () => {
    // The node detail calls 404 by design here (no /api/nodes row); api()
    // surfaces those as rejections. Anything else is a real error.
    const real = errors.filter((m) => !/^API 404: \/nodes\//.test(m));
    assert(real.length === 0, real.join(' | '));
  });

  await browser.close();
  console.log(`\n${passed} passed, ${failed} failed`);
  process.exit(failed > 0 ? 1 : 0);
})().catch((e) => { console.error(e); process.exit(1); });

/**
 * E2E test (#1869): the radio setting a packet was heard on.
 *
 * The fixture's observers report "910.525…,62.5,7,x"; the migrate step seeds
 * their radio history from that, so every packet resolves to an SF7 setting.
 *  - grouped rows (the page's default view) carry `radio`
 *  - the filter language reads it: sf == 7 keeps rows, sf == 12 keeps none
 *  - the packet detail shows a Radio row
 *  - the observer page lists the radio history once it has two stretches
 *    (intercepted: the fixture has one per observer)
 *
 * Usage: BASE_URL=http://localhost:13581 node test-radio-setting-1869-e2e.js
 */
const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:3000';
const RADIO = /^\d+(\.\d+)?,\d+(\.\d+)?,\d+,\d+$/;

async function test(name, fn) {
  try {
    await fn();
    console.log(`  ✅ ${name}`);
  } catch (err) {
    console.log(`  ❌ ${name}: ${err.message}`);
    process.exit(1);
  }
}

function assert(cond, msg) {
  if (!cond) throw new Error(msg || 'Assertion failed');
}

async function filterCount(page, expr) {
  await page.goto(BASE + '#/packets?filter=' + encodeURIComponent(expr), { waitUntil: 'domcontentloaded' });
  await page.waitForFunction(() => {
    const el = document.getElementById('packetFilterCount');
    return el && el.style.display !== 'none' && /Showing/.test(el.textContent);
  }, null, { timeout: 15000 });
  const text = await page.textContent('#packetFilterCount');
  const m = text.replace(/[,\s]/g, ' ').match(/Showing\s+(\d+)\s+of\s+(\d+)/);
  assert(m, 'unparseable count: ' + text);
  return { shown: Number(m[1]), of: Number(m[2]) };
}

async function run() {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage']
  });
  const page = await (await browser.newContext({ viewport: { width: 1400, height: 900 } })).newPage();
  page.setDefaultTimeout(15000);
  const pageErrors = [];
  page.on('pageerror', (e) => pageErrors.push(e.message));
  console.log('#1869 radio setting per observation:');

  let sample;
  await test('grouped packets carry the radio of their header observation', async () => {
    const res = await page.request.get(BASE + '/api/packets?groupByHash=true&limit=20');
    assert(res.ok(), 'status ' + res.status());
    const body = await res.json();
    assert(body.packets.length > 0, 'no packets in the fixture');
    const bad = body.packets.filter((p) => !RADIO.test(p.radio || ''));
    assert(bad.length === 0, bad.length + ' rows without a radio, e.g. ' + JSON.stringify(bad[0] && bad[0].radio));
    sample = body.packets[0];
  });

  await test('filter sf == <fixture SF> keeps rows, sf == 12 keeps none', async () => {
    const sf = sample.radio.split(',')[2];
    const hit = await filterCount(page, 'sf == ' + sf);
    assert(hit.shown > 0, 'sf == ' + sf + ' showed ' + hit.shown + ' of ' + hit.of);
    const miss = await filterCount(page, 'sf == 12');
    assert(miss.shown === 0 && miss.of > 0, 'sf == 12 showed ' + miss.shown + ' of ' + miss.of);
  });

  await test('packet detail shows the Radio row', async () => {
    await page.goto(BASE + '#/packets/' + sample.hash, { waitUntil: 'domcontentloaded' });
    const dd = page.locator('[data-testid="detail-radio"]').first();
    await dd.waitFor({ timeout: 15000 });
    const text = await dd.textContent();
    assert(/MHz · SF\d+ · BW [\d.]+ kHz · CR 4\/\d/.test(text), 'radio row: ' + text);
  });

  await test('observer page lists the radio history', async () => {
    const id = sample.observer_id;
    await page.route(new RegExp('/api/observers/' + id + '(\\?|$)'), async (route) => {
      const res = await route.fetch();
      const obs = await res.json();
      obs.radio_history = [
        { radio_id: '', radio: '869.432,62.5,7,5', first_seen: '2026-10-01T00:00:00Z', last_seen: '2026-10-25T09:00:00Z' },
        { radio_id: '', radio: '869.618,62.5,8,8', first_seen: '2026-10-25T09:30:00Z', last_seen: '2026-10-25T12:00:00Z' },
      ];
      await route.fulfill({ response: res, json: obs });
    });
    await page.goto(BASE + '#/observers/' + encodeURIComponent(id), { waitUntil: 'domcontentloaded' });
    const section = page.locator('[data-testid="observer-radio-history"]');
    await section.waitFor({ timeout: 15000 });
    const rows = await section.locator('tbody tr').allTextContents();
    assert(rows.length === 2, 'rows: ' + rows.length);
    assert(/869\.618 MHz · SF8/.test(rows[0]), 'newest first: ' + rows[0]);
  });

  await test('no page errors', async () => {
    assert(pageErrors.length === 0, pageErrors.join(' | '));
  });

  await browser.close();
}

run().catch((e) => {
  console.error(e);
  process.exit(1);
});

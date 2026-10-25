/**
 * E2E test (#2270, #2271): the latest /neighbors report on the observers
 * list (Neighbors column) and on the observer page (two cards + table).
 *
 * The fixture has no reports, so the observer endpoints are intercepted and
 * the report fields added to the real responses.
 *
 * Usage: BASE_URL=http://localhost:13581 node test-observer-neighbors-2270-e2e.js
 */
const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:3000';

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

async function run() {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage']
  });
  const page = await (await browser.newContext({ viewport: { width: 1600, height: 950 } })).newPage();
  page.setDefaultTimeout(15000);
  const pageErrors = [];
  page.on('pageerror', (e) => pageErrors.push(e.message));
  console.log('#2270/#2271 observer neighbors:');

  const reportAt = new Date(Date.now() - 3 * 3600 * 1000).toISOString();
  let reported;
  await page.route(/\/api\/observers(\?|$)/, async (route) => {
    const res = await route.fetch();
    const body = await res.json();
    reported = body.observers[0].id;
    body.observers[0].neighbors_report_at = reportAt;
    body.observers[0].neighbors_count = 12;
    await route.fulfill({ response: res, json: body });
  });

  await test('observers list shows the Neighbors column', async () => {
    await page.goto(BASE + '#/observers', { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('[data-testid="obs-cell-neighbors"]');
    const header = await page.locator('#obsTable th[data-sort-key="neighbors"]').count();
    assert(header === 1, 'Neighbors header missing');
    const cell = page.locator(`tr[data-observer-id="${reported}"] [data-testid="obs-cell-neighbors"]`);
    const text = (await cell.textContent()).trim();
    assert(/^3h ago · 12$/.test(text), 'reporting observer cell: ' + text);
    const dashes = await page.locator('[data-testid="obs-cell-neighbors"]').allTextContents();
    assert(dashes.filter((t) => t.trim() === '—').length === dashes.length - 1, 'other observers should show a dash');
  });

  await test('observer page shows the report cards and the neighbors table', async () => {
    await page.route(new RegExp('/api/observers/' + encodeURIComponent(reported) + '(\\?|$)'), async (route) => {
      const res = await route.fetch();
      const obs = await res.json();
      obs.neighbors_report_at = reportAt;
      obs.neighbors_count = 3;
      obs.neighbor_report = {
        received_at: reportAt, reported_at: reportAt, neighbor_count: 3, queried_neighbors: 2,
        self_scopes: '#tst', self_default_scope: '*',
        neighbors: [
          { pubkey: 'aa' + '0'.repeat(62), name: 'E2E Neighbor', role: 'repeater', snr: 6.5, heard_secs_ago: 90, status: 'responded', scopes: '#tst' },
          { pubkey: 'bb' + '0'.repeat(62), name: null, role: null, snr: null, heard_secs_ago: null, status: 'timeout' },
        ],
      };
      await route.fulfill({ response: res, json: obs });
    });
    await page.goto(BASE + '#/observers/' + encodeURIComponent(reported), { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('[data-testid="observer-neighbors"]');
    const age = (await page.textContent('[data-testid="obs-stat-neighbors-report"] .stat-value')).trim();
    const count = (await page.textContent('[data-testid="obs-stat-neighbors-count"] .stat-value')).trim();
    assert(age === '3h ago' && count === '3', `cards: ${age} / ${count}`);
    const rows = await page.locator('[data-testid="observer-neighbors"] tbody tr').allTextContents();
    assert(rows.length === 2, 'rows: ' + rows.length);
    assert(/E2E Neighbor/.test(rows[0]) && /6\.5 dB/.test(rows[0]) && /responded/.test(rows[0]), rows[0]);
    assert(/bb000000…/.test(rows[1]) && /timeout/.test(rows[1]), rows[1]);
    const heading = await page.textContent('[data-testid="observer-neighbors"] h4');
    assert(/2 of 3 listed/.test(heading), heading);
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

/**
 * E2E test (#661): rolling 1 h TX airtime per observer.
 *
 * The fixture DB has no observer_metrics inside the last hour, so the
 * summary endpoint is intercepted with page.route and given known values for
 * the fixture's own observer ids: one over the duty-cycle threshold, one
 * under, the rest unknown.
 *
 * Usage: BASE_URL=http://localhost:13581 node test-issue-661-airtime-e2e.js
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

function summaryFor(ids, overId, underId) {
  return {
    duty_cycle_warn_pct: 10,
    observers: ids.map((id) => ({
      observer_id: id,
      observer_name: id,
      current_noise_floor: -110,
      avg_noise_floor_24h: -111,
      max_noise_floor_24h: -105,
      battery_mv: null,
      sample_count: 12,
      sparkline: [-112, -110, -108],
      tx_airtime_pct_1h: id === overId ? 12.5 : (id === underId ? 2 : null),
      rx_airtime_pct_1h: null,
    })),
  };
}

async function run() {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage']
  });
  const ctx = await browser.newContext({ viewport: { width: 1400, height: 900 } });
  const page = await ctx.newPage();
  page.setDefaultTimeout(15000);

  console.log(`\nRunning #661 observer airtime E2E tests against ${BASE}\n`);

  const obsResp = await page.request.get(`${BASE}/api/observers`);
  const ids = ((await obsResp.json()).observers || []).map((o) => o.id);
  assert(ids.length >= 2, `fixture needs >=2 observers, got ${ids.length}`);
  const [overId, underId] = ids;

  await page.route('**/api/observers/metrics/summary*', (route) => route.fulfill({
    status: 200,
    contentType: 'application/json',
    body: JSON.stringify(summaryFor(ids, overId, underId)),
  }));

  await test('observer list has a sortable numeric TX 1h column', async () => {
    await page.goto(`${BASE}/#/observers`, { waitUntil: 'domcontentloaded' });
    await page.evaluate(() => { try { localStorage.removeItem('meshcore-observers-sort'); } catch (_) {} });
    await page.reload({ waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#obsTable tbody tr');
    const th = await page.$('#obsTable thead th[data-sort-key="airtime_1h"]');
    assert(th, 'TX 1h <th> must carry data-sort-key="airtime_1h"');
    assert(await th.getAttribute('data-type') === 'numeric', 'TX 1h <th> must be data-type="numeric"');
  });

  await test('over-threshold observer is flagged, under-threshold is not, unknown is a dash', async () => {
    const cell = (id) => page.$eval(`#obsTable tbody tr[data-observer-id="${id}"] td[data-testid="obs-cell-airtime"]`,
      (td) => ({ html: td.innerHTML, text: td.textContent.trim(), value: td.getAttribute('data-value') }));
    const over = await cell(overId);
    assert(over.html.includes('airtime-over') && over.text === '12.5%', `over cell: ${JSON.stringify(over)}`);
    const under = await cell(underId);
    assert(!under.html.includes('airtime-over') && under.text === '2.0%', `under cell: ${JSON.stringify(under)}`);
    if (ids.length > 2) {
      const unknown = await cell(ids[2]);
      assert(unknown.text === '—' && unknown.value === '', `unknown cell: ${JSON.stringify(unknown)}`);
    }
  });

  await test('clicking TX 1h sorts the highest share first', async () => {
    await page.click('#obsTable thead th[data-sort-key="airtime_1h"]');
    const first = await page.$eval('#obsTable tbody tr', (tr) => tr.getAttribute('data-observer-id'));
    assert(first === overId, `first row after sort = ${first}, want ${overId}`);
  });

  await test('RF Health tiles show the TX 1h share with the warning', async () => {
    await page.goto(`${BASE}/#/analytics?tab=rf-health`, { waitUntil: 'domcontentloaded' });
    // A theme refresh can replace the grid after its first tile appears.
    const over = await page.locator(`.rf-cell[data-observer="${overId}"] [data-testid="rf-cell-airtime"]`).evaluate(
      (el) => ({ html: el.innerHTML, text: el.textContent.trim() }));
    assert(over.html.includes('airtime-over') && over.text === 'TX 1h: 12.5%', `tile: ${JSON.stringify(over)}`);
  });

  await browser.close();
  console.log('\nAll #661 airtime E2E tests passed.');
}

run().catch((e) => { console.error(e); process.exit(1); });

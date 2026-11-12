/**
 * E2E test: the node analytics "Heard by observers" card replaces the SNR
 * Trend and Observer Coverage charts. The 12-row case stubs the analytics
 * response; the stat check uses the real one.
 *
 * Usage: BASE_URL=http://localhost:13581 node test-node-analytics-heard-by-e2e.js
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
  const page = await (await browser.newContext({ viewport: { width: 1400, height: 900 } })).newPage();
  page.setDefaultTimeout(15000);

  console.log(`\nRunning node analytics heard-by E2E tests against ${BASE}\n`);

  const nodes = (await (await page.request.get(`${BASE}/api/nodes?limit=50`)).json()).nodes || [];
  assert(nodes.length > 0, 'fixture has no nodes');
  const pk = nodes[0].public_key;
  const real = await (await page.request.get(`${BASE}/api/nodes/${pk}/analytics?days=7`)).json();
  assert(Array.isArray(real.heardBy), 'analytics response has no heardBy array');

  await test('the Observers stat counts the heardBy rows; no SNR Trend or Observer Coverage chart', async () => {
    await page.goto(`${BASE}/#/nodes/${encodeURIComponent(pk)}/analytics`, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#heardByCard');
    assert(!(await page.$('#snrChart')), '#snrChart still on the page');
    assert(!(await page.$('#observerChart')), '#observerChart still on the page');
    const stat = await page.$$eval('.analytics-stat-card', (cards) => {
      const c = cards.find((el) => el.querySelector('.analytics-stat-label').textContent.trim() === 'Observers');
      return c && c.querySelector('.analytics-stat-value').textContent.trim();
    });
    assert(stat === String(real.heardBy.length), `Observers stat ${stat}, heardBy rows ${real.heardBy.length}`);
  });

  await test('12 observers: 10 rows, then "Show all 12" shows all', async () => {
    const stub = JSON.parse(JSON.stringify(real));
    stub.heardBy = Array.from({ length: 12 }, (_, i) => ({
      observer_id: 'e2eobs' + i, observer_name: 'E2E observer ' + i, count: 20 - i,
      medianSnr: 8 - i, p10Snr: 5 - i, p90Snr: 10 - i, recentMedianSnr: null, earlierMedianSnr: null,
    }));
    stub.computedStats.uniqueObservers = 12;
    await page.route('**/api/nodes/*/analytics?days=30', (route) => route.fulfill({ contentType: 'application/json', body: JSON.stringify(stub) }));
    await page.click('#timeRangeBtns button[data-days="30"]');
    await page.waitForSelector('[data-action="heard-show-all"]');
    assert((await page.$$('[data-testid="heard-by-row"]')).length === 10, 'expected 10 rows before Show all');
    const label = await page.$eval('[data-action="heard-show-all"]', (el) => el.textContent.trim());
    assert(label === 'Show all 12', `button label: ${label}`);
    await page.click('[data-action="heard-show-all"]');
    await page.waitForFunction(() => document.querySelectorAll('[data-testid="heard-by-row"]').length === 12);
    assert(!(await page.$('[data-action="heard-show-all"]')), 'Show all still present after expanding');
    const first = await page.$eval('[data-testid="heard-by-row"] a', (a) => a.getAttribute('href'));
    assert(first === '#/observers/e2eobs0', `first row link: ${first}`);
  });

  await browser.close();
  console.log('\nAll node analytics heard-by E2E tests passed.');
}

run().catch((e) => { console.error(e); process.exit(1); });

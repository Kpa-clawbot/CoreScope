/**
 * E2E test (#748): the node analytics page says from when its data runs when
 * the requested range reaches past the server's in-memory window.
 *
 * The CI server runs without a retention window or memory cap and the
 * fixture's packets span a few months: 7d is fully covered, and "All"
 * (365d) reaches past the oldest packet in the database. That is the
 * "database" case, so the notice must say the database holds no older
 * packets, not that the server keeps them in memory. The memory and
 * loading wordings are covered by the unit tests.
 *
 * Usage: BASE_URL=http://localhost:13581 node test-issue-748-node-analytics-coverage-e2e.js
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

  console.log(`\nRunning #748 node analytics coverage E2E tests against ${BASE}\n`);

  const nodes = (await (await page.request.get(`${BASE}/api/nodes?limit=50`)).json()).nodes || [];
  assert(nodes.length > 0, 'fixture has no nodes');
  const pk = nodes[0].public_key;

  await test('7d inside the in-memory window: not partial, no notice', async () => {
    const r = await (await page.request.get(`${BASE}/api/nodes/${pk}/analytics?days=7`)).json();
    assert(!r.timeRange.partial && !r.timeRange.dataFrom, `7d timeRange: ${JSON.stringify(r.timeRange)}`);
    await page.goto(`${BASE}/#/nodes/${encodeURIComponent(pk)}/analytics`, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#timeRangeBtns');
    assert(!(await page.$('[data-testid="analytics-coverage-notice"]')), 'notice shown for a fully covered range');
  });

  await test('All reaches past the oldest packet: partial, notice names the start date and the database', async () => {
    const r = await (await page.request.get(`${BASE}/api/nodes/${pk}/analytics?days=365`)).json();
    assert(r.timeRange.partial && r.timeRange.dataFrom, `365d timeRange: ${JSON.stringify(r.timeRange)}`);
    assert(r.timeRange.dataFromReason === 'database', `365d reason: ${r.timeRange.dataFromReason}`);
    await page.click('#timeRangeBtns button[data-days="365"]');
    await page.waitForSelector('[data-testid="analytics-coverage-notice"]');
    const text = await page.$eval('[data-testid="analytics-coverage-notice"]', (el) => el.textContent);
    const day = r.timeRange.dataFrom.slice(0, 10);
    assert(text.includes('Data from ' + day + ': the database holds no older packets'), `notice: ${text}`);
    assert(!text.includes('in memory'), `notice claims a memory limit: ${text}`);
  });

  await browser.close();
  console.log('\nAll #748 node analytics coverage E2E tests passed.');
}

run().catch((e) => { console.error(e); process.exit(1); });

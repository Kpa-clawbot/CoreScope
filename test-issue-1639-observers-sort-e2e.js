/**
 * E2E test (#1639): observers table at #/observers must be sortable.
 *
 * Same fix-pattern as #679 (nodes table) — wire TableSort with
 * numeric / time column hints. Clicking a column header MUST reorder
 * tbody rows; this asserts row-swap behavior on the "Total Packets"
 * column (data-sort-key="packet_count", data-type="numeric",
 * data-sort-default="desc").
 *
 * Usage: BASE_URL=http://localhost:13581 node test-issue-1639-observers-sort-e2e.js
 */
const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:3000';

async function test(name, fn) {
  try {
    await fn();
    console.log(`  \u2705 ${name}`);
  } catch (err) {
    console.log(`  \u274c ${name}: ${err.message}`);
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
  const ctx = await browser.newContext({ viewport: { width: 1400, height: 900 } });
  const page = await ctx.newPage();
  page.setDefaultTimeout(15000);

  console.log(`\nRunning #1639 observers-sort E2E tests against ${BASE}\n`);

  await test('observers thead has data-sort-key + data-type on numeric columns', async () => {
    await page.goto(`${BASE}/#/observers`, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#obsTable', { timeout: 10000 });
    await page.waitForSelector('#obsTable tbody tr', { timeout: 10000 });

    const totalPktsTh = await page.$('#obsTable thead th[data-sort-key="packet_count"]');
    assert(totalPktsTh, 'Total Packets <th> must carry data-sort-key="packet_count"');
    const type = await totalPktsTh.getAttribute('data-type');
    assert(type === 'numeric',
      `Total Packets <th> must have data-type="numeric", got "${type}"`);
  });

  await test('clicking Total Packets header reorders rows numerically (desc)', async () => {
    await page.goto(`${BASE}/#/observers`, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#obsTable', { timeout: 10000 });
    await page.waitForSelector('#obsTable tbody tr', { timeout: 10000 });

    // Read numeric packet_count values from each row's Total Packets <td>
    // (data-value attr if present, else parse the rendered text).
    async function readPacketCounts() {
      return await page.$$eval('#obsTable tbody tr', (rows) => rows.map((r) => {
        const td = r.querySelector('td[data-col-key="packet_count"], td:nth-child(7)');
        if (!td) return NaN;
        const dv = td.getAttribute('data-value');
        const raw = dv != null ? dv : td.textContent.replace(/[^0-9.-]/g, '');
        return Number(raw);
      }));
    }

    const before = await readPacketCounts();
    assert(before.length >= 2, `need >=2 rows, got ${before.length}`);

    // Click the Total Packets header (data-sort-key=packet_count, default desc).
    const th = await page.$('#obsTable thead th[data-sort-key="packet_count"]');
    assert(th, 'Total Packets <th> with data-sort-key="packet_count" not found');
    await th.click();
    await page.waitForTimeout(300);

    const after = await readPacketCounts();
    assert(after.length === before.length,
      `row count changed: ${before.length} -> ${after.length}`);

    // After one click on a numeric column with default desc, the first row
    // must have a packet_count >= the last row's. The original order is NOT
    // sorted by packet_count, so this must produce a different order.
    const first = after[0], last = after[after.length - 1];
    assert(Number.isFinite(first) && Number.isFinite(last),
      `non-numeric packet_count: first=${first} last=${last}`);
    assert(first >= last,
      `desc sort failed: first row packet_count=${first}, last row=${last}`);

    // Stronger: order strictly decreased somewhere relative to original,
    // i.e. clicking actually did something.
    const changed = after.some((v, i) => v !== before[i]);
    assert(changed, 'clicking Total Packets header did not change row order');
  });

  await browser.close();
  console.log('\n✅ all #1639 sort tests passed\n');
}

run().catch((e) => {
  console.error(e);
  process.exit(1);
});

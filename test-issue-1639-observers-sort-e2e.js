/**
 * E2E test (#1639): observers table at #/observers must be sortable.
 *
 * Same fix-pattern as #679 (nodes table) — wire TableSort with
 * numeric / time column hints. Clicking a column header MUST reorder
 * tbody rows.
 *
 * Round-1 test additions (PR #1641):
 *   - last_packet_at column reorder (regression guard for the round-0
 *     data-type=date → numeric fix; without this the one-line fix is
 *     unguarded).
 *   - strict monotonic non-increasing assertion (no more .some(changed),
 *     which silently passes even when order is mostly wrong).
 *   - toggle (second-click ascending) on packet_count.
 *   - second-column string sort (Name) to exercise localeCompare.
 *   - cells are addressed via data-testid (added in observers.js for
 *     testability) — finding #8 dropped the broader data-col-key on
 *     <td>s, and data-testid is the less-invasive replacement.
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

// finding #5: strict monotonic non-increasing across ALL rows
function assertNonIncreasing(arr, label) {
  for (let i = 0; i < arr.length - 1; i++) {
    // NaN slots are sorted last by the comparator — accept them at the tail.
    const a = arr[i], b = arr[i + 1];
    if (!Number.isFinite(a)) continue; // NaN tail
    if (!Number.isFinite(b)) continue;
    if (!(a >= b)) {
      throw new Error(`${label}: not non-increasing at idx ${i}: ${a} < ${b}; full=${JSON.stringify(arr)}`);
    }
  }
}

function assertNonDecreasing(arr, label) {
  for (let i = 0; i < arr.length - 1; i++) {
    const a = arr[i], b = arr[i + 1];
    if (!Number.isFinite(a)) continue;
    if (!Number.isFinite(b)) continue;
    if (!(a <= b)) {
      throw new Error(`${label}: not non-decreasing at idx ${i}: ${a} > ${b}; full=${JSON.stringify(arr)}`);
    }
  }
}

async function gotoObservers(page) {
  await page.goto(`${BASE}/#/observers`, { waitUntil: 'domcontentloaded' });
  await page.evaluate(() => { try { localStorage.removeItem('meshcore-observers-sort'); } catch (_) {} });
  await page.reload({ waitUntil: 'domcontentloaded' });
  await page.waitForSelector('#obsTable', { timeout: 10000 });
  await page.waitForSelector('#obsTable tbody tr', { timeout: 10000 });
}

async function readNumeric(page, testid) {
  return await page.$$eval(`#obsTable tbody tr td[data-testid="${testid}"]`, (tds) => tds.map((td) => {
    const dv = td.getAttribute('data-value');
    const raw = dv != null && dv !== '' ? dv : td.textContent.replace(/[^0-9.-]/g, '');
    return Number(raw);
  }));
}

async function readStrings(page, testid) {
  return await page.$$eval(`#obsTable tbody tr td[data-testid="${testid}"]`, (tds) =>
    tds.map((td) => td.getAttribute('data-value') || ''));
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
    await gotoObservers(page);
    const totalPktsTh = await page.$('#obsTable thead th[data-sort-key="packet_count"]');
    assert(totalPktsTh, 'Total Packets <th> must carry data-sort-key="packet_count"');
    const type = await totalPktsTh.getAttribute('data-type');
    assert(type === 'numeric',
      `Total Packets <th> must have data-type="numeric", got "${type}"`);
  });

  await test('clicking Total Packets header reorders rows numerically (desc)', async () => {
    await gotoObservers(page);
    const before = await readNumeric(page, 'obs-cell-packet-count');
    assert(before.length >= 2, `need >=2 rows, got ${before.length}`);

    const th = await page.$('#obsTable thead th[data-sort-key="packet_count"]');
    assert(th, 'Total Packets <th> not found');
    await th.click();
    await page.waitForTimeout(300);

    const after = await readNumeric(page, 'obs-cell-packet-count');
    assert(after.length === before.length, `row count changed`);
    // finding #5: strict non-increasing across ALL rows, not just first/last
    assertNonIncreasing(after, 'packet_count desc');
  });

  // finding #13: toggle (second click on the same header → ascending)
  await test('second click on Total Packets header toggles to ascending', async () => {
    await gotoObservers(page);
    const th = await page.$('#obsTable thead th[data-sort-key="packet_count"]');
    await th.click(); // desc
    await page.waitForTimeout(200);
    await th.click(); // asc
    await page.waitForTimeout(300);
    const after = await readNumeric(page, 'obs-cell-packet-count');
    assert(after.length >= 2, 'need >=2 rows');
    assertNonDecreasing(after, 'packet_count asc (after toggle)');
  });

  // finding #4: regression guard for round-0 data-type=date → numeric fix
  await test('clicking Last Packet header reorders rows by timestamp (desc)', async () => {
    await gotoObservers(page);
    const th = await page.$('#obsTable thead th[data-sort-key="last_packet_at"]');
    assert(th, 'Last Packet <th> not found');
    const type = await th.getAttribute('data-type');
    assert(type === 'numeric',
      `Last Packet <th> must have data-type="numeric" (round-0 fix), got "${type}"`);
    await th.click();
    await page.waitForTimeout(300);
    const after = await readNumeric(page, 'obs-cell-last-packet');
    assert(after.length >= 2, `need >=2 rows`);
    assertNonIncreasing(after, 'last_packet_at desc');
  });

  // finding #13: second-column test — Name string sort via localeCompare
  await test('clicking Name header sorts alphabetically (string localeCompare)', async () => {
    await gotoObservers(page);
    const th = await page.$('#obsTable thead th[data-sort-key="name"]');
    assert(th, 'Name <th> not found');
    await th.click();
    await page.waitForTimeout(300);
    const after = await readStrings(page, 'obs-cell-name');
    assert(after.length >= 2, `need >=2 rows`);
    // Default direction for text columns is asc — sorted A→Z
    for (let i = 0; i < after.length - 1; i++) {
      if (after[i] === '' || after[i + 1] === '') continue;
      assert(after[i].localeCompare(after[i + 1]) <= 0,
        `Name asc broken at idx ${i}: ${JSON.stringify(after[i])} > ${JSON.stringify(after[i + 1])}`);
    }
  });

  await browser.close();
  console.log('\n✅ all #1639 sort tests passed\n');
}

run().catch((e) => {
  console.error(e);
  process.exit(1);
});

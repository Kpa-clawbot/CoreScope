/**
 * E2E test (#1188): observer IATA must render alongside observer name
 * on packets rows and in the detail pane. Plus, the wireshark-style
 * filter grammar must accept `observer_iata` / `iata` expressions.
 *
 * Runs against the e2e fixture (see test-fixtures/e2e-fixture.db).
 * Observers in the fixture carry IATA codes (e.g. SJC, OAK, MRY),
 * so once the UI changes land, at least one rendered packet row must
 * carry one of those codes next to its observer name.
 *
 * Usage: BASE_URL=http://localhost:13581 node test-observer-iata-1188-e2e.js
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

  console.log(`\nRunning observer-IATA E2E tests against ${BASE}\n`);

  await test('Packets table renders an IATA badge in an observer cell', async () => {
    await page.goto(`${BASE}/#/packets`, { waitUntil: 'domcontentloaded' });
    // Wide time window so fixture rows are in scope
    await page.evaluate(() => localStorage.setItem('meshcore-time-window', '525600'));
    await page.reload({ waitUntil: 'load' });
    await page.waitForSelector('[data-loaded="true"]', { timeout: 20000 });
    await page.waitForSelector('table tbody tr:not([id^=vscroll])', { timeout: 15000 });

    // Cells in the Observer column should contain a `.badge-iata` element
    // for at least one row that has a known IATA.
    const iataBadges = await page.$$('td.col-observer .badge-iata');
    assert(iataBadges.length > 0,
      `expected at least one .badge-iata inside a .col-observer cell; got ${iataBadges.length}`);

    // The badge text should be a recognizable IATA code (3 uppercase letters)
    const text = (await iataBadges[0].textContent() || '').trim();
    assert(/^[A-Z]{3}$/.test(text), `expected 3-letter IATA in badge, got "${text}"`);
  });

  await test('Filter grammar: observer_iata == "<code>" narrows the table', async () => {
    await page.goto(`${BASE}/#/packets`, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('[data-loaded="true"]', { timeout: 20000 });
    await page.waitForSelector('table tbody tr:not([id^=vscroll])', { timeout: 15000 });

    // Pick the first IATA shown in any badge in the table
    const firstBadge = await page.$('td.col-observer .badge-iata');
    assert(firstBadge, 'no .badge-iata found to pick a filter value from');
    const iata = (await firstBadge.textContent() || '').trim();
    assert(/^[A-Z]{3}$/.test(iata), `expected IATA, got "${iata}"`);

    // Apply the filter — `iata == "XXX"` should leave rows visible
    const input = await page.$('#packetFilterInput');
    assert(input, 'packet filter input not found');
    await input.fill(`iata == "${iata}"`);
    await page.waitForTimeout(500); // debounce

    const rowsAfter = await page.$$('table tbody tr:not([id^=vscroll])');
    assert(rowsAfter.length > 0, `expected matching rows for iata == "${iata}"`);

    // Every remaining observer cell should carry the same IATA
    const badges = await page.$$('td.col-observer .badge-iata');
    for (const b of badges) {
      const t = (await b.textContent() || '').trim();
      assert(t === iata, `unexpected IATA ${t} in row when filter is iata == "${iata}"`);
    }
  });

  await test('Mobile viewport (375px): observer IATA badge stays visible — not clipped', async () => {
    // #1189 R1 mesh-operator finding: no narrow-viewport validation existed.
    // At 375px (iPhone SE) the .col-observer cell must still render its
    // .badge-iata pill within the viewport bounds. If a future CSS change
    // moves observer behind a hidden column or clips the badge off-screen,
    // this assertion turns red.
    const mobile = await browser.newContext({ viewport: { width: 375, height: 812 } });
    const mpage = await mobile.newPage();
    mpage.setDefaultTimeout(15000);
    await mpage.goto(`${BASE}/#/packets`, { waitUntil: 'domcontentloaded' });
    await mpage.evaluate(() => localStorage.setItem('meshcore-time-window', '525600'));
    await mpage.reload({ waitUntil: 'load' });
    await mpage.waitForSelector('[data-loaded="true"]', { timeout: 20000 });
    await mpage.waitForSelector('table tbody tr:not([id^=vscroll])', { timeout: 15000 });
    const badge = await mpage.$('td.col-observer .badge-iata');
    assert(badge, 'no .badge-iata rendered at 375px viewport');
    // Must be in the viewport horizontally — not clipped beyond the 375px edge.
    const box = await badge.boundingBox();
    assert(box, '.badge-iata has no bounding box (not rendered)');
    assert(box.x >= 0, `.badge-iata x=${box.x} is off the left edge at 375px`);
    assert(box.x + box.width <= 375 + 1,
      `.badge-iata right edge ${box.x + box.width} exceeds 375px viewport`);
    assert(box.width > 0 && box.height > 0,
      `.badge-iata has zero dimensions (collapsed/clipped): ${box.width}x${box.height}`);
    await mobile.close();
  });

  await browser.close();
  console.log(`\nAll observer-IATA E2E tests passed.\n`);
}

run().catch(err => {
  console.error(err);
  process.exit(1);
});

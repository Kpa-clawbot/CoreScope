/**
 * E2E test (#1268): LoRa RF parameters on the Nodes page.
 *
 * The fixture server runs without rfParams configured, so the first check
 * is the real disabled state (no LoRa column). The enabled checks serve
 * /api/rf-params through page.route with values for the fixture's own
 * node keys.
 *
 * Usage: BASE_URL=http://localhost:13581 node test-issue-1268-rf-params-e2e.js
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

async function gotoNodes(page) {
  await page.goto(`${BASE}/#/nodes`, { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('#nodesBody tr[data-key]');
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

  console.log(`\nRunning #1268 RF params E2E tests against ${BASE}\n`);

  await test('feature off: /api/rf-params says disabled and there is no LoRa column', async () => {
    const r = await page.request.get(`${BASE}/api/rf-params`);
    const body = await r.json();
    assert(body.enabled === false && Array.isArray(body.presets) && body.nodes && typeof body.nodes === 'object',
      `disabled body: ${JSON.stringify(body)}`);
    await gotoNodes(page);
    const th = await page.$('#nodesTable th.col-lora');
    assert(th && await th.isHidden(), 'LoRa header must exist but stay hidden');
    assert(!(await page.$('#nodesBody td[data-testid="node-cell-lora"]')), 'no LoRa cells when disabled');
  });

  const keys = await page.$$eval('#nodesBody tr[data-key]', (trs) => trs.map((tr) => tr.getAttribute('data-key')));
  assert(keys.length >= 3, `fixture needs >=3 nodes, got ${keys.length}`);
  const [presetKey, rawKey] = keys;
  const now = new Date().toISOString();
  await page.route('**/api/rf-params*', (route) => route.fulfill({
    status: 200,
    contentType: 'application/json',
    body: JSON.stringify({
      enabled: true,
      fetched_at: now,
      presets: [{ title: 'EU/UK (Narrow)', freq: 869.618, sf: 8, bw: 62.5, cr: 8 }],
      nodes: {
        [presetKey.toLowerCase()]: { freq: 869.618, sf: 8, bw: 62.5, cr: 8, last_advert: now },
        [rawKey.toLowerCase()]: { freq: 869.618, sf: 7, bw: 62.5, cr: 5, last_advert: now },
      },
    }),
  }));

  await test('feature on: LoRa column shows the preset title or the raw values', async () => {
    await page.reload({ waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#nodesBody td[data-testid="node-cell-lora"]');
    assert(await page.isVisible('#nodesTable th.col-lora'), 'LoRa header visible');
    const cell = (k) => page.$eval(`#nodesBody tr[data-key="${k}"] td[data-testid="node-cell-lora"]`, (td) => td.textContent.trim());
    assert(await cell(presetKey) === 'EU/UK (Narrow)', `preset cell: ${await cell(presetKey)}`);
    assert(await cell(rawKey) === '869.618 SF7 BW62.5 CR5', `raw cell: ${await cell(rawKey)}`);
    assert(await cell(keys[2]) === '', `unknown cell: ${await cell(keys[2])}`);
    const tip = await page.$eval(`#nodesBody tr[data-key="${presetKey}"] td[data-testid="node-cell-lora"]`, (td) => td.title);
    assert(tip === '869.618 SF8 BW62.5 CR8', `tooltip: ${tip}`);
  });

  await test('sorting by LoRa keeps nodes without RF params last', async () => {
    await page.click('#nodesTable th.col-lora');
    const texts = await page.$$eval('#nodesBody td[data-testid="node-cell-lora"]', (tds) => tds.map((td) => td.textContent.trim()));
    const firstEmpty = texts.indexOf('');
    assert(firstEmpty === 2, `two filled cells first, got ${JSON.stringify(texts.slice(0, 4))}`);
    assert(texts.slice(firstEmpty).every((t) => t === ''), 'no filled cell after an empty one');
  });

  await test('node detail shows the LoRa row', async () => {
    await page.goto(`${BASE}/#/nodes/${encodeURIComponent(presetKey)}`, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('[data-testid="node-detail-lora"]');
    const text = await page.$eval('[data-testid="node-detail-lora"]', (el) => el.textContent);
    assert(text.includes('869.618 MHz') && text.includes('SF8') && text.includes('EU/UK (Narrow)'), `detail: ${text}`);
  });

  await browser.close();
  console.log('\nAll #1268 RF params E2E tests passed.');
}

run().catch((e) => { console.error(e); process.exit(1); });

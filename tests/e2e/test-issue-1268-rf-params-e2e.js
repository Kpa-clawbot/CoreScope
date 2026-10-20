/**
 * E2E test (#1268): LoRa RF parameters in the node detail views.
 *
 * The fixture server runs without rfParams configured, so the first check
 * is the real disabled state (no LoRa row). The enabled checks serve
 * /api/rf-params through page.route with values for the fixture's own
 * node keys. The Nodes table deliberately has no LoRa column.
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

  await test('feature off: /api/rf-params says disabled and the detail has no LoRa row', async () => {
    const r = await page.request.get(`${BASE}/api/rf-params`);
    const body = await r.json();
    assert(body.enabled === false && Array.isArray(body.presets) && body.nodes && typeof body.nodes === 'object',
      `disabled body: ${JSON.stringify(body)}`);
    await gotoNodes(page);
    const k = await page.$eval('#nodesBody tr[data-key]', (tr) => tr.getAttribute('data-key'));
    await page.goto(`${BASE}/#/nodes/${encodeURIComponent(k)}`, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#node-stats');
    assert(!(await page.$('[data-testid="node-detail-lora"]')), 'no LoRa row when disabled');
  });

  await gotoNodes(page);
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

  await test('feature on: the Nodes table still has no LoRa column', async () => {
    // Reload: api() cached the disabled response from the first check.
    await page.reload({ waitUntil: 'domcontentloaded' });
    await gotoNodes(page);
    await page.waitForTimeout(500);
    const headers = await page.$$eval('#nodesTable thead th', (ths) => ths.map((th) => th.textContent.trim()));
    assert(!headers.includes('LoRa'), `headers: ${JSON.stringify(headers)}`);
  });

  await test('full detail page shows the values and the preset', async () => {
    await page.goto(`${BASE}/#/nodes/${encodeURIComponent(presetKey)}`, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('[data-testid="node-detail-lora"]');
    const text = await page.$eval('[data-testid="node-detail-lora"]', (el) => el.textContent);
    assert(text.includes('869.618 MHz') && text.includes('SF8') && text.includes('EU/UK (Narrow)'), `detail: ${text}`);
  });

  await test('side panel shows raw values without a preset, and a hint for unknown nodes', async () => {
    await gotoNodes(page);
    await page.click(`#nodesBody tr[data-key="${rawKey}"]`);
    await page.waitForSelector('#nodesRight [data-testid="node-detail-lora"]');
    const raw = await page.$eval('#nodesRight [data-testid="node-detail-lora"]', (el) => el.textContent);
    assert(raw.includes('SF7') && raw.includes('CR 4/5') && !raw.includes('EU/UK'), `raw: ${raw}`);
    await page.click(`#nodesBody tr[data-key="${keys[2]}"]`);
    await page.waitForFunction(() => {
      const el = document.querySelector('#nodesRight [data-testid="node-detail-lora"]');
      return el && el.textContent.includes('Not on the MeshCore map');
    });
  });

  await browser.close();
  console.log('\nAll #1268 RF params E2E tests passed.');
}

run().catch((e) => { console.error(e); process.exit(1); });

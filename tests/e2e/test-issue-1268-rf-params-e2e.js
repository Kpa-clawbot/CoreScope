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

  await test('feature off: /api/rf-params says disabled, no LoRa row and no LoRa filter', async () => {
    const r = await page.request.get(`${BASE}/api/rf-params`);
    const body = await r.json();
    assert(body.enabled === false && Array.isArray(body.presets) && body.nodes && typeof body.nodes === 'object',
      `disabled body: ${JSON.stringify(body)}`);
    await gotoNodes(page);
    const k = await page.$eval('#nodesBody tr[data-key]', (tr) => tr.getAttribute('data-key'));
    await page.goto(`${BASE}/#/nodes/${encodeURIComponent(k)}`, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#node-stats');
    assert(!(await page.$('[data-testid="node-detail-lora"]')), 'no LoRa row when disabled');
    await gotoNodes(page);
    assert(await page.$eval('#nodeLoraFilter', (s) => s.hidden), 'LoRa filter hidden when disabled');
    assert(!(await page.$('#nodesBody .badge-lora-diff')), 'no badges when disabled');
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
      deviations: {
        [rawKey.toLowerCase()]: { majority: { freq: 869.618, sf: 8, bw: 62.5 }, agree: 12, peers: 14, fields: ['sf'] },
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

  await test('deviating node gets a badge in the list, others do not', async () => {
    await gotoNodes(page);
    await page.waitForSelector(`#nodesBody tr[data-key="${rawKey}"] .badge-lora-diff`);
    const badge = await page.$eval(`#nodesBody tr[data-key="${rawKey}"] .badge-lora-diff`, (b) => ({ text: b.textContent.trim(), title: b.title }));
    assert(badge.text === 'LoRa SF7' && badge.title.includes('12 of 14 nearby nodes'), `badge: ${JSON.stringify(badge)}`);
    const others = await page.$$eval('#nodesBody .badge-lora-diff', (bs) => bs.length);
    assert(others === 1, `badges in list: ${others}`);
  });

  await test('LoRa filter shows only deviating nodes and goes into the URL', async () => {
    assert(await page.isVisible('#nodeLoraFilter'), 'filter visible when the feature is on');
    const label = await page.$eval('#nodeLoraFilter option[value="differs"]', (o) => o.textContent);
    assert(label.includes('(1)'), `option label: ${label}`);
    await page.selectOption('#nodeLoraFilter', 'differs');
    await page.waitForFunction(() => document.querySelectorAll('#nodesBody tr[data-key]').length === 1);
    const key = await page.$eval('#nodesBody tr[data-key]', (tr) => tr.getAttribute('data-key'));
    assert(key === rawKey, `filtered row: ${key}`);
    assert(page.url().includes('lora=differs'), `url: ${page.url()}`);
    await page.selectOption('#nodeLoraFilter', '');
    await page.waitForFunction(() => document.querySelectorAll('#nodesBody tr[data-key]').length > 1);
    assert(!page.url().includes('lora='), `url after reset: ${page.url()}`);
  });

  await test('the filter is restored from the URL', async () => {
    await page.goto(`${BASE}/#/nodes?lora=differs`, { waitUntil: 'domcontentloaded' });
    await page.reload({ waitUntil: 'domcontentloaded' });
    await page.waitForFunction(() => document.querySelectorAll('#nodesBody tr[data-key]').length === 1);
    assert(await page.$eval('#nodeLoraFilter', (s) => s.value) === 'differs', 'select restored');
  });

  await test('detail of a deviating node explains the difference', async () => {
    await page.goto(`${BASE}/#/nodes/${encodeURIComponent(rawKey)}`, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('[data-testid="lora-deviation"]');
    const text = await page.$eval('[data-testid="lora-deviation"]', (el) => el.textContent);
    assert(text.includes('Differs from 12 of 14 nearby nodes on this band') && text.includes('SF8'), `deviation: ${text}`);
  });

  await browser.close();
  console.log('\nAll #1268 RF params E2E tests passed.');
}

run().catch((e) => { console.error(e); process.exit(1); });

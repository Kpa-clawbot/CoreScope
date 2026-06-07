const { chromium } = require('playwright');
const BASE = process.env.BASE_URL || 'http://localhost:3000';

(async () => {
  const browser = await chromium.launch();
  const page = await browser.newPage();
  // pick any repeater pubkey from the API
  const nodes = await (await page.request.get(BASE + '/api/nodes?role=repeater&limit=1')).json();
  if (!nodes.nodes || !nodes.nodes.length) {
    console.log('node-quality E2E SKIP (no repeater in dataset)');
    await browser.close();
    return;
  }
  const pk = nodes.nodes[0].public_key;

  await page.goto(BASE + '/#/nodes/' + pk + '?section=quality');
  await page.waitForSelector('#nodeQualityContent .nq-stats, #nodeQualityContent .text-muted', { timeout: 15000 });

  const hasStats = await page.locator('#nodeQualityContent .nq-stats').count();
  if (hasStats) {
    await page.waitForSelector('#nqMap', { timeout: 10000 });
    const before = await page.locator('#nqRows tr').count();
    // one-way toggle should never reduce the visible rows below the 2-way set
    await page.check('#nqShowOneWay');
    const after = await page.locator('#nqRows tr').count();
    if (after < before) throw new Error('one-way toggle reduced rows unexpectedly');
  }
  console.log('node-quality E2E OK');
  await browser.close();
})().catch((e) => { console.error(e); process.exit(1); });

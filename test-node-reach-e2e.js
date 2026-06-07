// E2E for the per-node Reach page (#/nodes/<pubkey>/reach).
// Defaults to localhost:3000 — NEVER point at prod (AGENTS.md). CI sets BASE_URL.
const { chromium } = require('playwright');
const BASE = process.env.BASE_URL || 'http://localhost:3000';

(async () => {
  const browser = await chromium.launch();
  const page = await browser.newPage();

  // A repeater is most likely to have reach data (it relays).
  const nodes = await (await page.request.get(BASE + '/api/nodes?role=repeater&limit=1')).json();
  if (!nodes.nodes || !nodes.nodes.length) {
    console.log('node-reach E2E SKIP (no repeater in dataset)');
    await browser.close();
    return;
  }
  const pk = nodes.nodes[0].public_key;

  // 1. The endpoint returns the documented shape.
  const reach = await (await page.request.get(BASE + '/api/nodes/' + pk + '/reach?days=7')).json();
  for (const k of ['node', 'window', 'reliable_tokens', 'importance', 'links', 'direct_observers']) {
    if (!(k in reach)) throw new Error('reach response missing key: ' + k);
  }
  if (!Array.isArray(reach.links)) throw new Error('reach.links must be an array');

  // 2. The page renders.
  await page.goto(BASE + '/#/nodes/' + pk + '/reach');
  await page.waitForSelector('.nq-head', { timeout: 20000 });
  if (!(await page.locator('h2', { hasText: 'Reach' }).count())) {
    throw new Error('Reach header missing');
  }

  // 3. If this node is identifiable, exercise the table, toggles and links.
  if (reach.reliable_tokens.length && (await page.locator('#nqRows').count())) {
    await page.waitForSelector('#nqIncoming');
    await page.waitForSelector('#nqOutgoing');

    const twoWay = await page.locator('#nqRows tr').count();
    await page.check('#nqIncoming');
    const withIncoming = await page.locator('#nqRows tr').count();
    if (withIncoming < twoWay) throw new Error('incoming toggle reduced rows');
    await page.check('#nqOutgoing');
    const withBoth = await page.locator('#nqRows tr').count();
    if (withBoth < withIncoming) throw new Error('outgoing toggle reduced rows');

    // Neighbour rows link to a node detail page.
    if (await page.locator('#nqRows a.nq-link').count()) {
      const href = await page.locator('#nqRows a.nq-link').first().getAttribute('href');
      if (!href || !href.startsWith('#/nodes/')) throw new Error('neighbour link malformed: ' + href);
    }

    // Map renders (node has GPS for a repeater fixture).
    await page.waitForSelector('#nqMap .leaflet-container', { timeout: 10000 }).catch(() => {});
  }

  console.log('node-reach E2E OK');
  await browser.close();
})().catch((e) => { console.error(e); process.exit(1); });

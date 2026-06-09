// E2E for the per-node Reach page (#/nodes/<pubkey>/reach).
// Defaults to localhost:3000 — NEVER point at prod (AGENTS.md). CI sets BASE_URL.
const { chromium } = require('playwright');
const BASE = process.env.BASE_URL || 'http://localhost:3000';

async function getJson(page, url) {
  const resp = await page.request.get(url);
  if (!resp.ok()) throw new Error('GET ' + url + ' → HTTP ' + resp.status());
  return resp.json();
}

(async () => {
  const browser = await chromium.launch();
  const page = await browser.newPage();

  // A repeater is most likely to have reach data (it relays).
  const nodes = await getJson(page, BASE + '/api/nodes?role=repeater&limit=1');
  if (!nodes.nodes || !nodes.nodes.length) {
    console.log('node-reach E2E SKIP (no repeater in dataset)');
    await browser.close();
    return;
  }
  const pk = nodes.nodes[0].public_key;

  // 1. The endpoint returns the documented shape.
  const reach = await getJson(page, BASE + '/api/nodes/' + pk + '/reach?days=7');
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

    // Derive the EXACT expected row counts from the API so the toggles are
    // verified, not just "didn't shrink" (tautology). Base shows two-way only;
    // incoming adds we-only links; +outgoing adds the rest (= all links).
    const twoWayExp = reach.links.filter(l => l.bidir).length;
    const weOnlyExp = reach.links.filter(l => !l.bidir && l.we_hear > 0 && l.they_hear === 0).length;
    const allExp = reach.links.length;

    const base = await page.locator('#nqRows tr').count();
    if (base !== twoWayExp) throw new Error(`base rows ${base} != two-way ${twoWayExp}`);
    await page.check('#nqIncoming');
    const withIncoming = await page.locator('#nqRows tr').count();
    if (withIncoming !== twoWayExp + weOnlyExp) {
      throw new Error(`incoming rows ${withIncoming} != two-way+we-only ${twoWayExp + weOnlyExp}`);
    }
    await page.check('#nqOutgoing');
    const withBoth = await page.locator('#nqRows tr').count();
    if (withBoth !== allExp) throw new Error(`both-toggles rows ${withBoth} != all links ${allExp}`);

    // Neighbour rows link to a node detail page.
    if (await page.locator('#nqRows a.nq-link').count()) {
      const href = await page.locator('#nqRows a.nq-link').first().getAttribute('href');
      if (!href || !href.startsWith('#/nodes/')) throw new Error('neighbour link malformed: ' + href);
    }

    // Map must render whenever at least one link has GPS (no swallowed failure).
    if (reach.links.some(l => l.lat != null && l.lon != null)) {
      await page.waitForSelector('#nqMap .leaflet-container', { timeout: 10000 });
    }
  }

  console.log('node-reach E2E OK');
  await browser.close();
})().catch((e) => { console.error(e); process.exit(1); });

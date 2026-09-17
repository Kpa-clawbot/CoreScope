// E2E for the RX coverage hex layer on the Reach page (#/nodes/<pubkey>/reach?coverage=1).
// Defaults to localhost:3000 — NEVER point at prod (AGENTS.md). CI sets BASE_URL.
const { chromium } = require('playwright');
const BASE = process.env.BASE_URL || 'http://localhost:3000';

(async () => {
  const browser = await chromium.launch();
  const page = await browser.newPage();

  // Coverage is opt-in (config flag, default off). Skip when the deployment under
  // test hasn't enabled it — the endpoints 404 and the UI toggle is absent by design.
  const clientCfg = await (await page.request.get(BASE + '/api/config/client')).json();
  if (clientCfg.clientRxCoverage !== true) {
    console.log('node-reach-coverage E2E SKIP (clientRxCoverage disabled on this deployment)');
    await browser.close();
    return;
  }

  const nodes = await (await page.request.get(BASE + '/api/nodes?role=repeater&limit=1')).json();
  if (!nodes.nodes || !nodes.nodes.length) {
    console.log('node-reach-coverage E2E SKIP (no repeater in dataset)');
    await browser.close();
    return;
  }
  const pk = nodes.nodes[0].public_key;

  // 1. The coverage endpoint returns a GeoJSON FeatureCollection.
  const cov = await (await page.request.get(
    BASE + '/api/nodes/' + pk + '/rx-coverage?bbox=-90,-180,90,180&z=10')).json();
  if (cov.type !== 'FeatureCollection' || !Array.isArray(cov.features)) {
    throw new Error('rx-coverage must return a FeatureCollection with a features array');
  }

  // 2. Bad bbox → 400.
  const bad = await page.request.get(BASE + '/api/nodes/' + pk + '/rx-coverage');
  if (bad.status() !== 400) throw new Error('missing bbox should be 400, got ' + bad.status());

  // 3. The Reach page exposes the coverage toggle.
  await page.goto(BASE + '/#/nodes/' + pk + '/reach');
  await page.waitForSelector('.nq-head', { timeout: 20000 });
  const reach = await (await page.request.get(BASE + '/api/nodes/' + pk + '/reach?days=7')).json();
  if (reach.reliable_tokens && reach.reliable_tokens.length && (await page.locator('#nqRows').count())) {
    await page.waitForSelector('#nqCoverage');

    // 4. Enabling coverage issues a request to rx-coverage, shows the legend, and deep-links.
    const waitCov = page.waitForRequest((r) => r.url().includes('/rx-coverage'), { timeout: 15000 });
    await page.check('#nqCoverage');
    await waitCov;
    await page.waitForSelector('#nqCovLegend', { state: 'visible' });
    if (!/coverage=1/.test(await page.evaluate(() => location.hash))) {
      throw new Error('coverage toggle did not deep-link ?coverage=1');
    }

    // 5. Toggling off hides the legend.
    await page.uncheck('#nqCoverage');
    await page.waitForSelector('#nqCovLegend', { state: 'hidden' });
  }

  const errors = [];
  page.on('console', (m) => { if (m.type() === 'error') errors.push(m.text()); });
  if (errors.length) throw new Error('console errors: ' + errors.join('; '));

  console.log('node-reach-coverage E2E OK');
  await browser.close();
})().catch((e) => { console.error('node-reach-coverage E2E FAIL:', e.message); process.exit(1); });

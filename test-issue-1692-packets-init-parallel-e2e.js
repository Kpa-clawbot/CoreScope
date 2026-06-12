/**
 * E2E (#1692): /packets init() must NOT serialize loadObservers → loadPackets.
 *
 * Before #1692 fix: init() does `await loadObservers(); loadPackets();` so
 * /api/packets cannot start until /api/observers resolves. Under a slow
 * observers fetch (CI load, large fixture DB, slow link) the first table row
 * does not appear until well after the observers call returns.
 *
 * Repro strategy: stub /api/observers with a deterministic 4s delay via
 * page.route(), then measure wallclock time from navigation start to the
 * first `tr[data-hash]` appearance in #pktBody. Serial behavior pushes
 * first-row past 4s. Parallel (fixed) behavior should land first-row well
 * under 3s — the /api/packets call runs in parallel.
 *
 * Acceptance budget: first-row < 3000ms. Headroom of ~1s below the 4s
 * observers-stub delay so a serial implementation reliably trips the gate
 * even with timing jitter, and a parallel implementation comfortably passes.
 *
 * Usage: BASE_URL=http://localhost:13581 node test-issue-1692-packets-init-parallel-e2e.js
 */
'use strict';
const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';
const OBSERVERS_DELAY_MS = 4000;
const FIRST_ROW_BUDGET_MS = 3000;

let passed = 0, failed = 0;
async function step(name, fn) {
  try { await fn(); passed++; console.log('  \u2713 ' + name); }
  catch (e) { failed++; console.error('  \u2717 ' + name + ': ' + e.message); }
}
function assert(c, m) { if (!c) throw new Error(m || 'assertion failed'); }

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });
  const ctx = await browser.newContext({ viewport: { width: 1400, height: 900 } });
  const page = await ctx.newPage();
  page.setDefaultTimeout(15000);
  page.on('pageerror', (e) => console.error('[pageerror]', e.message));

  console.log(`\n=== #1692 packets init() parallel-fetch E2E against ${BASE} ===`);

  // Stub /api/observers with a 4s artificial delay. The real response body
  // is fetched and forwarded so the page still gets a valid observer list —
  // only the latency is amplified. This isolates the init() sequencing
  // behavior from network/IO variance on the CI runner.
  await page.route('**/api/observers**', async (route) => {
    const req = route.request();
    let body = '{"observers":[]}';
    let contentType = 'application/json';
    try {
      const upstream = await page.context().request.fetch(req);
      body = await upstream.text();
      contentType = upstream.headers()['content-type'] || contentType;
    } catch (_) { /* fall through with empty observers */ }
    await new Promise(r => setTimeout(r, OBSERVERS_DELAY_MS));
    await route.fulfill({ status: 200, contentType, body });
  });

  await step(`first table row appears < ${FIRST_ROW_BUDGET_MS}ms despite ${OBSERVERS_DELAY_MS}ms /api/observers stub`, async () => {
    // Clean SPA state — mirrors gotoPackets() pattern from test-e2e-playwright.js.
    await page.goto(BASE, { waitUntil: 'domcontentloaded' });
    await page.evaluate(() => {
      localStorage.removeItem('meshcore-groupbyhash');
      localStorage.setItem('meshcore-time-window', '525600');
    });

    const t0 = Date.now();
    await page.goto(BASE + '/#/packets', { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#pktBody tr[data-hash]', { timeout: 10000 });
    const elapsed = Date.now() - t0;
    console.log(`    first-row elapsed: ${elapsed}ms (budget ${FIRST_ROW_BUDGET_MS}ms, observers stub ${OBSERVERS_DELAY_MS}ms)`);
    assert(elapsed < FIRST_ROW_BUDGET_MS,
      `first tr[data-hash] took ${elapsed}ms, expected < ${FIRST_ROW_BUDGET_MS}ms — packets.js::init() is serializing loadObservers → loadPackets (#1692)`);
  });

  await page.unroute('**/api/observers**');
  await browser.close();

  console.log(`\nResults: ${passed} passed, ${failed} failed`);
  process.exit(failed ? 1 : 0);
})();

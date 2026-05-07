/**
 * E2E (#1136): Live page region filter must NOT wipe all packets and lines.
 *
 * Regression introduced in #1080 — `public/live.js` parsed `/api/observers`
 * as if it were a top-level array, but the endpoint returns
 * `{observers: [...], server_time: ...}`. Result: `observerIataMap` stayed
 * empty and `packetMatchesRegion` returned false for EVERY packet whenever
 * any region was selected — so no markers, no polylines, no feed entries.
 *
 * This test:
 *   1. Loads /#/live against the fixture DB.
 *   2. Waits for the observer roster to load and verifies the live module
 *      has a populated observer_id → IATA map (proves the parse path works).
 *   3. Programmatically selects a region (SJC) that we know maps to fixture
 *      observers (test-fixtures/e2e-fixture.db has multiple observers in
 *      SJC, OAK, MRY, SFO).
 *   4. Synthesizes a packet whose observer_id IS in the SJC region and
 *      pushes it through the same path live websocket packets take.
 *   5. Asserts at least one `.live-feed-item` is rendered for that hash.
 *
 * Before the fix this test FAILS at assertion 2 (map empty) AND at
 * assertion 5 (feed never renders the packet). After the fix both pass.
 *
 * Usage: BASE_URL=http://localhost:13581 node test-issue-1136-live-region-e2e.js
 */
'use strict';
const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';

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

  console.log('\n=== #1136 live region filter E2E against ' + BASE + ' ===');

  // Discover an observer_id in SJC from the API (drives test from real data).
  let sjcObserverId = null;
  let allObservers = [];
  await step('GET /api/observers returns {observers:[...]} shape with SJC entries', async () => {
    const res = await page.request.get(BASE + '/api/observers');
    assert(res.ok(), 'API returned non-OK: ' + res.status());
    const body = await res.json();
    assert(body && Array.isArray(body.observers), 'response must have .observers array (the bug-1136 root cause)');
    allObservers = body.observers;
    const sjc = body.observers.filter(function (o) { return o && o.iata === 'SJC' && o.id; });
    assert(sjc.length > 0, 'fixture must contain at least one SJC observer (got ' + sjc.length + ')');
    sjcObserverId = sjc[0].id;
  });

  await step('navigate to /#/live and wait for live module to register', async () => {
    // Pre-clear region selection so it starts unrestricted.
    await page.addInitScript(() => {
      try { localStorage.removeItem('meshcore-region-filter'); } catch (e) {}
    });
    await page.goto(BASE + '/#/live', { waitUntil: 'domcontentloaded' });
    await page.waitForFunction(() => !!(window._liveBufferPacket && window.RegionFilter), { timeout: 15000 });
  });

  await step('observer iata map is POPULATED after init fetch (regression #1136)', async () => {
    const exposed = await page.evaluate(() => typeof window._liveGetObserverIataMap);
    assert(exposed === 'function', '_liveGetObserverIataMap must be exposed as a function (regression: not wired up)');
    // Wait for fetch + setObserverIataMap to land.
    await page.waitForFunction(() => {
      const m = window._liveGetObserverIataMap && window._liveGetObserverIataMap();
      return m && Object.keys(m).length > 0;
    }, { timeout: 8000 }).catch(() => {});
    const sample = await page.evaluate((oid) => {
      const m = window._liveGetObserverIataMap();
      return { size: Object.keys(m).length, iataForOid: m[oid] || null };
    }, sjcObserverId);
    assert(sample.size > 0, 'observerIataMap should be populated from /api/observers (was empty — #1136 bug)');
    assert(sample.iataForOid === 'SJC', 'observerIataMap[' + sjcObserverId + '] should be "SJC", got ' + sample.iataForOid);
  });

  await step('select SJC region in RegionFilter, verify selection took effect', async () => {
    await page.evaluate(() => {
      window.RegionFilter.setSelected(['SJC']);
    });
    const sel = await page.evaluate(() => window.RegionFilter.getSelected());
    assert(Array.isArray(sel) && sel.indexOf('SJC') !== -1, 'RegionFilter selected should include SJC, got ' + JSON.stringify(sel));
  });

  await step('packet with SJC observer renders to live feed when SJC region selected', async () => {
    const targetHash = 'fixture-1136-' + Date.now().toString(16);
    await page.evaluate(function (args) {
      const pkt = {
        id: 9999991136,
        hash: args.hash,
        raw_hex: '00',
        path_json: '[]',
        observer_id: args.oid,
        observer_name: 'fixture-observer',
        timestamp: new Date().toISOString(),
        snr: 5, rssi: -90,
        decoded: {
          header: { payloadTypeName: 'GRP_TXT' },
          payload: { text: 'region-1136-probe' },
          path: { hops: [] },
        },
      };
      // Push through the same buffer entry point the WS handler uses.
      window._liveBufferPacket(pkt);
    }, { hash: targetHash, oid: sjcObserverId });

    // Allow the (non-realistic-propagation) immediate renderPacketTree to land.
    await page.waitForFunction((h) => {
      return !!document.querySelector('.live-feed-item[data-hash="' + h + '"]');
    }, targetHash, { timeout: 5000 }).catch(() => {});

    const found = await page.evaluate((h) => !!document.querySelector('.live-feed-item[data-hash="' + h + '"]'), targetHash);
    assert(found, 'expected .live-feed-item[data-hash=' + targetHash + '] to render with SJC selected (#1136: filter wiped feed)');
  });

  await page.evaluate(() => { try { window.RegionFilter.setSelected([]); } catch(e) {} });
  await browser.close();

  console.log('\n--- ' + passed + ' passed, ' + failed + ' failed ---\n');
  process.exit(failed > 0 ? 1 : 0);
})().catch((e) => { console.error(e); process.exit(1); });

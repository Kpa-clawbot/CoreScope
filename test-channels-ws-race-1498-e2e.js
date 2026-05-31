/**
 * #1498 — Deterministic regression test for the WS-vs-REST race that
 * makes test-channels-ws-batch-e2e.js flaky.
 *
 * Bug: selectChannel() sets selectedHash + header synchronously, then
 * awaits a REST fetch that unconditionally replaces `messages` with the
 * server response. Any WS messages appended in the window between the
 * header update and the REST resolution are silently wiped.
 *
 * The flaky test on master is a real-world manifestation: the REST
 * fetch usually finishes before the injection, but occasionally the
 * injection lands first and gets stomped.
 *
 * This test forces the race deterministically by stubbing window.fetch
 * to delay the /channels/<hash>/messages response, then injects a WS
 * message DURING that delay. On master HEAD the injected message gets
 * overwritten by the (empty) REST response. After the fix the injected
 * message survives.
 */
'use strict';
const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';
let passed = 0, failed = 0;
async function step(name, fn) {
  try { await fn(); passed++; console.log('  ✓ ' + name); }
  catch (e) { failed++; console.error('  ✗ ' + name + ': ' + e.message); }
}
function assert(c, m) { if (!c) throw new Error(m || 'assertion failed'); }

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 } });
  const page = await ctx.newPage();
  page.on('dialog', (d) => d.accept());
  page.setDefaultTimeout(8000);
  page.on('pageerror', (e) => console.error('[pageerror]', e.message));

  console.log(`\n=== #1498 ws-vs-rest race regression against ${BASE} ===`);

  await page.goto(BASE + '/', { waitUntil: 'domcontentloaded' });
  await page.evaluate(() => { try { localStorage.clear(); } catch (e) {} });
  await page.goto(BASE + '/#/channels', { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('#chList .ch-item', { timeout: 10000 });

  // Pick a channel hash but DO NOT click it yet — we want to stub
  // fetch before selectChannel() fires.
  const firstRow = await page.$('.ch-section-network .ch-item');
  const targetHash = await firstRow.getAttribute('data-hash');

  await step('WS message injected during selectChannel() REST fetch is preserved', async () => {
    // Stub fetch: when the /channels/<hash>/messages request is made,
    // delay 800ms and return an empty messages array. This guarantees
    // the WS injection lands first and the REST response lands second.
    await page.evaluate((h) => {
      window.__chFetchHits = 0;
      const realFetch = window.fetch.bind(window);
      window.fetch = async function (url, opts) {
        const u = typeof url === 'string' ? url : (url && url.url) || '';
        if (u.indexOf('/channels/') !== -1 && u.indexOf('/messages') !== -1) {
          window.__chFetchHits++;
          await new Promise((r) => setTimeout(r, 800));
          return new Response(JSON.stringify({ messages: [] }), {
            status: 200, headers: { 'Content-Type': 'application/json' },
          });
        }
        return realFetch(url, opts);
      };
    }, targetHash);

    // Kick off selectChannel asynchronously; do NOT await it.
    page.evaluate((h) => {
      window._channelsSelectChannelForTest(h);
    }, targetHash);

    // Wait until selectedHash is set (sync part of selectChannel ran)
    // but the REST fetch is still in-flight (delay 800ms not elapsed).
    await page.waitForFunction((h) => {
      const s = window._channelsGetStateForTest();
      return s.selectedHash === h && window.__chFetchHits >= 1;
    }, targetHash, { timeout: 3000 });

    // Now inject the WS message WHILE the REST fetch is delayed.
    await page.evaluate((h) => {
      window._channelsProcessWSBatchForTest([{
        type: 'message',
        data: {
          hash: 'ws-race-1498-1',
          id: 'pkt-race-1',
          decoded: { payload: { channel: h, sender: 'WsRacer', text: 'race-test' } },
        },
      }], []);
    }, targetHash);

    // Verify the WS message landed in `messages` synchronously.
    const seenLive = await page.evaluate(() => {
      const s = window._channelsGetStateForTest();
      return s.messages.some((m) => m.packetHash === 'ws-race-1498-1');
    });
    assert(seenLive, 'WS injection should appear in messages immediately after processWSBatch');

    // Wait for the delayed REST response to land + selectChannel to finish.
    await page.waitForTimeout(1200);

    // The WS message must STILL be present after the REST fetch resolved.
    const survives = await page.evaluate(() => {
      const s = window._channelsGetStateForTest();
      return {
        present: s.messages.some((m) => m.packetHash === 'ws-race-1498-1'),
        count: s.messages.length,
        hashes: s.messages.map((m) => m.packetHash),
      };
    });
    assert(survives.present,
      'WS message stomped by REST fetch — messages after fetch: ' + JSON.stringify(survives));
  });

  await step('WS messages from previous channel do not leak into next channel', async () => {
    // Hard reload to drop the fetch stub from the previous step and reset state.
    await page.goto(BASE + '/', { waitUntil: 'domcontentloaded' });
    await page.goto(BASE + '/#/channels', { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#chList .ch-item', { timeout: 10000 });
    const rows = await page.$$('.ch-section-network .ch-item');
    if (rows.length < 2) throw new Error('need at least 2 network channels in fixture');
    const hashA = await rows[0].getAttribute('data-hash');
    const hashB = await rows[1].getAttribute('data-hash');

    // Select A, wait for REST to settle (observable via messages state),
    // then inject a _fromWS message for A.
    await rows[0].click();
    await page.waitForFunction((h) => window._channelsGetStateForTest().selectedHash === h, hashA, { timeout: 3000 });
    await page.evaluate((h) => {
      window._channelsProcessWSBatchForTest([{
        type: 'message',
        data: {
          hash: 'leak-test-from-A',
          id: 'pkt-leak-A',
          decoded: { payload: { channel: h, sender: 'LeakAlice', text: 'A-only' } },
        },
      }], []);
    }, hashA);
    const inA = await page.evaluate(() =>
      window._channelsGetStateForTest().messages.some((m) => m.packetHash === 'leak-test-from-A'));
    assert(inA, 'precondition: WS message for A should be in messages while A is selected');

    // Switch to B. The A-only WS message MUST NOT appear in B's view
    // even after a REST replacement for B that does NOT contain it.
    await page.evaluate((h) => window._channelsSelectChannelForTest(h), hashB);
    await page.waitForFunction((h) => window._channelsGetStateForTest().selectedHash === h, hashB, { timeout: 3000 });
    // Wait until the REST response for B has populated messages or
    // explicitly returned empty — observable via a fetch-mock-style flag
    // is not available here, so wait on selectedHash + a tick.
    await page.waitForFunction(() => {
      const s = window._channelsGetStateForTest();
      // After selectChannel resolves, either messages was repopulated by
      // REST OR we have an empty array. Both states are observable.
      return Array.isArray(s.messages);
    }, undefined, { timeout: 3000 });
    const leaked = await page.evaluate(() =>
      window._channelsGetStateForTest().messages.some((m) => m.packetHash === 'leak-test-from-A'));
    assert(!leaked, 'WS message from channel A leaked into channel B view');
  });

  await browser.close();
  console.log(`\n=== #1498 race: ${passed} passed, ${failed} failed ===\n`);
  process.exit(failed === 0 ? 0 : 1);
})().catch((e) => { console.error(e); process.exit(1); });

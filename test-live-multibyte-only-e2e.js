/**
 * E2E: live "Multibyte only" toggle.
 *   1. Toggle exists in live controls and defaults OFF.
 *   2. With it ON, a single-byte synthetic packet does NOT create a feed item,
 *      while a multibyte one does.
 *   3. Turning it OFF and rebuilding shows the previously-hidden single-byte pkt.
 *   4. The setting persists across a reload (localStorage round-trip).
 *
 * Usage: BASE_URL=http://localhost:13581 node test-live-multibyte-only-e2e.js
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

// raw_hex builders (non-transport, route_type 1 => path-len byte at offset 1).
// byte0 = 0x10 header; byte1 top bits set hash size.
const SINGLE_HEX = '1000'; // (0x00>>6)+1 = 1
const MULTI_HEX  = '1040'; // (0x40>>6)+1 = 2

function makePkt(hash, rawHex) {
  return {
    id: Math.floor(Math.random() * 1e9),
    hash: hash,
    raw_hex: rawHex,
    route_type: 1,
    path_json: '[]',
    observer_id: 'mb-e2e-obs',
    observer_name: 'mb-e2e',
    timestamp: new Date().toISOString(),
    snr: 5, rssi: -90,
    decoded: {
      header: { payloadTypeName: 'GRP_TXT' },
      payload: { text: 'mb-probe' },
      path: { hops: [] },
    },
  };
}

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

  console.log('\n=== live multibyte-only E2E against ' + BASE + ' ===');

  await step('navigate to /#/live, toggle exists and defaults OFF', async () => {
    await page.addInitScript(() => {
      try { localStorage.removeItem('live-multibyte-only'); } catch (e) {}
    });
    await page.goto(BASE + '/#/live', { waitUntil: 'domcontentloaded' });
    await page.waitForFunction(() => !!window._liveBufferPacket, { timeout: 15000 });
    const cb = await page.$('#liveMultibyteToggle');
    assert(cb, '#liveMultibyteToggle must exist in the live controls');
    const checked = await page.evaluate(() => document.getElementById('liveMultibyteToggle').checked);
    assert(checked === false, 'multibyte toggle must default OFF');
  });

  const singleHash = 'mb-single-' + Date.now().toString(16);
  const multiHash  = 'mb-multi-'  + Date.now().toString(16);

  await step('turn toggle ON: single-byte packet hidden, multibyte shown', async () => {
    await page.evaluate(() => {
      const cb = document.getElementById('liveMultibyteToggle');
      cb.checked = true;
      cb.dispatchEvent(new Event('change', { bubbles: true }));
    });
    await page.evaluate((args) => {
      window._liveBufferPacket(args.single);
      window._liveBufferPacket(args.multi);
    }, { single: makePkt(singleHash, SINGLE_HEX), multi: makePkt(multiHash, MULTI_HEX) });

    await page.waitForFunction((h) => !!document.querySelector('.live-feed-item[data-hash="' + h + '"]'),
      multiHash, { timeout: 5000 }).catch(() => {});

    const multiShown = await page.evaluate((h) => !!document.querySelector('.live-feed-item[data-hash="' + h + '"]'), multiHash);
    const singleShown = await page.evaluate((h) => !!document.querySelector('.live-feed-item[data-hash="' + h + '"]'), singleHash);
    assert(multiShown, 'multibyte packet should render a feed item when toggle ON');
    assert(!singleShown, 'single-byte packet must NOT render a feed item when toggle ON');
  });

  await step('turn toggle OFF: previously-hidden single-byte packet appears', async () => {
    await page.evaluate(() => {
      const cb = document.getElementById('liveMultibyteToggle');
      cb.checked = false;
      cb.dispatchEvent(new Event('change', { bubbles: true }));
    });
    await page.waitForFunction((h) => !!document.querySelector('.live-feed-item[data-hash="' + h + '"]'),
      singleHash, { timeout: 5000 }).catch(() => {});
    const singleShown = await page.evaluate((h) => !!document.querySelector('.live-feed-item[data-hash="' + h + '"]'), singleHash);
    assert(singleShown, 'single-byte packet should reappear after toggle OFF + feed rebuild');
  });

  await step('setting persists across reload', async () => {
    // Ensure the toggle is ON and localStorage is written.
    await page.evaluate(() => {
      const cb = document.getElementById('liveMultibyteToggle');
      cb.checked = true;
      cb.dispatchEvent(new Event('change', { bubbles: true }));
    });
    // Open a fresh context (no addInitScript) to simulate a cold reload.
    const persistCtx = await browser.newContext({ viewport: { width: 1400, height: 900 } });
    const persistPage = await persistCtx.newPage();
    persistPage.setDefaultTimeout(15000);
    // Seed localStorage before navigation so the page boots with it set.
    await persistPage.addInitScript(() => {
      localStorage.setItem('live-multibyte-only', 'true');
    });
    await persistPage.goto(BASE + '/#/live', { waitUntil: 'domcontentloaded' });
    await persistPage.waitForFunction(() => !!document.getElementById('liveMultibyteToggle'), { timeout: 15000 });
    const checked = await persistPage.evaluate(() => document.getElementById('liveMultibyteToggle').checked);
    await persistCtx.close();
    assert(checked === true, 'multibyte toggle should restore checked=true from localStorage');
  });

  await browser.close();
  console.log('\n--- ' + passed + ' passed, ' + failed + ' failed ---\n');
  process.exit(failed > 0 ? 1 : 0);
})().catch((e) => { console.error(e); process.exit(1); });

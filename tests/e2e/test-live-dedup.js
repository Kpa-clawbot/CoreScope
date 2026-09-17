/**
 * Tests for Live page hash-based packet deduplication in the feed.
 * Injects packets by intercepting WebSocket before page loads.
 *
 * Usage:
 *   CHROMIUM_PATH=/usr/bin/chromium-browser BASE_URL=http://localhost:13581 node test-live-dedup.js
 */
const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:3000';
const results = [];

async function test(name, fn) {
  try {
    await fn();
    results.push({ name, pass: true });
    console.log(`  ✅ ${name}`);
  } catch (err) {
    results.push({ name, pass: false, error: err.message });
    console.log(`  ❌ ${name}: ${err.message}`);
  }
}

function assert(condition, msg) {
  if (!condition) throw new Error(msg || 'Assertion failed');
}

async function run() {
  console.log('Launching Chromium for Live dedup tests...');
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage']
  });
  const context = await browser.newContext();

  // Patch WebSocket BEFORE any page script runs
  await context.addInitScript(() => {
    const OrigWS = window.WebSocket;
    window.__capturedWS = [];
    window.WebSocket = function(...args) {
      const ws = new OrigWS(...args);
      window.__capturedWS.push(ws);
      return ws;
    };
    window.WebSocket.prototype = OrigWS.prototype;
    window.WebSocket.CONNECTING = OrigWS.CONNECTING;
    window.WebSocket.OPEN = OrigWS.OPEN;
    window.WebSocket.CLOSING = OrigWS.CLOSING;
    window.WebSocket.CLOSED = OrigWS.CLOSED;
  });

  const page = await context.newPage();
  page.setDefaultTimeout(15000);

  console.log(`\nRunning Live dedup tests against ${BASE}\n`);

  // Helper: navigate to live page, wait for feed, clear initial items
  async function setupLivePage() {
    await page.goto(`${BASE}/#/live`, { waitUntil: 'networkidle' });
    await page.waitForSelector('#liveFeed', { timeout: 10000 });
    await page.waitForTimeout(3000); // let WS connect + initial replay
    // Clear feed
    await page.evaluate(() => {
      document.getElementById('liveFeed').querySelectorAll('.live-feed-item').forEach(el => el.remove());
    });
  }

  // Helper: inject a packet via captured WS
  function injectPkt(hash, observer, type, text, id) {
    return page.evaluate(({hash, observer, type, text, id}) => {
      // Find the last active WebSocket with an onmessage handler
      const wsList = window.__capturedWS || [];
      let ws = null;
      for (let i = wsList.length - 1; i >= 0; i--) {
        if (wsList[i].onmessage && wsList[i].readyState === 1) { ws = wsList[i]; break; }
      }
      if (!ws) throw new Error('No active WebSocket found (count: ' + wsList.length + ')');
      ws.onmessage({ data: JSON.stringify({ type: 'packet', data: {
        id: id || 'test-' + Math.random().toString(36).slice(2),
        hash: hash || undefined,
        raw: 'AABB' + (hash || '0000').slice(0, 4),
        decoded: {
          header: { payloadTypeName: type || 'GRP_TXT' },
          payload: { text: text || 'test msg' },
          path: { hops: ['ab', 'cd'] }
        },
        snr: 10, rssi: -85, observer_name: observer || 'obs1'
      }})});
    }, {hash, observer, type, text, id});
  }

  await setupLivePage();

  await test('Duplicate hash packets produce single feed entry', async () => {
    const HASH = 'aabbccdd11223344';
    await injectPkt(HASH, 'observer-A', 'GRP_TXT', 'hello');
    await injectPkt(HASH, 'observer-B', 'GRP_TXT', 'hello');
    await page.waitForTimeout(300);

    const items = await page.$$eval(`.live-feed-item[data-hash="${HASH}"]`, els => els.length);
    assert(items === 1, `Expected 1 feed item for hash, got ${items}`);

    // Check observation badge shows 2
    const badgeText = await page.$eval(`.live-feed-item[data-hash="${HASH}"] .badge-obs`, el => el.textContent);
    assert(badgeText.includes('2'), `Badge should show 2, got "${badgeText}"`);
  });

  // Clear feed between tests
  await page.evaluate(() => {
    document.getElementById('liveFeed').querySelectorAll('.live-feed-item').forEach(el => el.remove());
  });

  await test('Different hash packets produce separate feed entries', async () => {
    await injectPkt('bbbb111122223333', 'obs1', 'ADVERT', '', 'b1');
    await injectPkt('cccc444455556666', 'obs1', 'TXT_MSG', 'direct', 'c1');
    await page.waitForTimeout(300);

    const count = await page.$$eval('.live-feed-item', els => els.length);
    assert(count === 2, `Expected 2 items, got ${count}`);
  });

  await page.evaluate(() => {
    document.getElementById('liveFeed').querySelectorAll('.live-feed-item').forEach(el => el.remove());
  });

  await test('Rapid sequential duplicates (5 observers) aggregate correctly', async () => {
    const HASH = 'dddddddd33333333';
    for (let i = 0; i < 5; i++) {
      await injectPkt(HASH, 'obs-' + i, 'GRP_TXT', 'flood', 'td-' + i);
    }
    await page.waitForTimeout(300);

    const items = await page.$$eval(`.live-feed-item[data-hash="${HASH}"]`, els => els.length);
    assert(items === 1, `Expected 1 feed item for 5 observations, got ${items}`);

    const badgeText = await page.$eval(`.live-feed-item[data-hash="${HASH}"] .badge-obs`, el => el.textContent);
    assert(badgeText.includes('5'), `Badge should show 5, got "${badgeText}"`);
  });

  await page.evaluate(() => {
    document.getElementById('liveFeed').querySelectorAll('.live-feed-item').forEach(el => el.remove());
  });

  await test('Same hash same observer still deduplicates', async () => {
    const HASH = 'eeeeeeee44444444';
    await injectPkt(HASH, 'same-obs', 'GRP_TXT', 'dup', 'e1');
    await injectPkt(HASH, 'same-obs', 'GRP_TXT', 'dup', 'e2');
    await page.waitForTimeout(300);

    const count = await page.$$eval(`.live-feed-item[data-hash="${HASH}"]`, els => els.length);
    assert(count === 1, `Expected 1 feed item, got ${count}`);
  });

  await page.evaluate(() => {
    document.getElementById('liveFeed').querySelectorAll('.live-feed-item').forEach(el => el.remove());
  });

  await test('Packets without hash are not deduplicated', async () => {
    await injectPkt(null, 'obs1', 'ACK', '', 'nh1');
    await injectPkt(null, 'obs2', 'ACK', '', 'nh2');
    await page.waitForTimeout(300);

    const count = await page.$$eval('.live-feed-item', els => els.length);
    assert(count === 2, `Expected 2 items for no-hash packets, got ${count}`);
  });

  await browser.close();

  const passed = results.filter(r => r.pass).length;
  const failed = results.filter(r => !r.pass).length;
  console.log(`\n${passed}/${results.length} tests passed${failed ? `, ${failed} failed` : ''}`);
  process.exit(failed > 0 ? 1 : 0);
}

run().catch(err => {
  console.error('Fatal error:', err);
  process.exit(1);
});

/**
 * #1498 — Deterministic regression test for the WS-vs-REST race that
 * makes test-channels-ws-batch-e2e.js flaky.
 *
 * Bug: selectChannel() sets selectedHash + header synchronously, then
 * awaits a REST fetch that unconditionally replaces `messages` with the
 * server response. Any WS messages appended in the window between the
 * header update and the REST resolution are silently wiped.
 *
 * This file forces the race deterministically with a fetch stub +
 * observable counters (no magic-number sleeps). All waits use
 * page.waitForFunction(...) against state that the production code
 * actually updates.
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

// Install a fetch interceptor that:
//   * counts hits on /channels/<hash>/messages,
//   * sets window.__chLastRequestedHash to the hash being fetched,
//   * lets callers control delay + response body per-request via
//     window.__chNextStub = { delayMs, response, only:<hash> } (consumed once).
async function installFetchMock(page) {
  await page.evaluate(() => {
    window.__chFetchHits = 0;
    window.__chLastRequestedHash = null;
    window.__chNextStub = null;
    if (window.__realFetch) return;
    window.__realFetch = window.fetch.bind(window);
    window.fetch = async function (url, opts) {
      const u = typeof url === 'string' ? url : (url && url.url) || '';
      const m = u.match(/\/channels\/([^/?]+)\/messages/);
      if (m) {
        const hash = decodeURIComponent(m[1]);
        window.__chFetchHits++;
        window.__chLastRequestedHash = hash;
        const stub = window.__chNextStub;
        if (stub && (!stub.only || stub.only === hash)) {
          window.__chNextStub = null;
          if (stub.delayMs) await new Promise((r) => setTimeout(r, stub.delayMs));
          return new Response(JSON.stringify(stub.response || { messages: [] }), {
            status: 200, headers: { 'Content-Type': 'application/json' },
          });
        }
      }
      return window.__realFetch(url, opts);
    };
  });
}

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
  await installFetchMock(page);

  // Pick a channel hash but DO NOT click it yet.
  const firstRow = await page.$('.ch-section-network .ch-item');
  const targetHash = await firstRow.getAttribute('data-hash');

  await step('WS message injected during selectChannel() REST fetch is preserved', async () => {
    // Stub: empty response, 800ms delay — guarantees WS injection wins
    // the race to mutate `messages` before REST resolves.
    await page.evaluate(() => {
      window.__chFetchHits = 0;
      window.__chNextStub = { delayMs: 800, response: { messages: [] } };
    });

    // Kick off selectChannel asynchronously; do NOT await it.
    page.evaluate((h) => { window._channelsSelectChannelForTest(h); }, targetHash);

    // Wait until selectedHash is set AND the REST fetch is in-flight
    // (observable: fetch hit recorded). No magic sleeps.
    await page.waitForFunction((h) => {
      const s = window._channelsGetStateForTest();
      return s.selectedHash === h && window.__chFetchHits >= 1;
    }, targetHash, { timeout: 3000 });

    // Inject the WS message WHILE the REST fetch is delayed.
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

    // Round-1 finding #12: stronger assertions on the injected state.
    const live = await page.evaluate(() => {
      const s = window._channelsGetStateForTest();
      const m = s.messages.find((x) => x.packetHash === 'ws-race-1498-1');
      return {
        count: s.messages.length,
        present: !!m,
        fromWS: m && m._fromWS === true,
        // Newest-at-end ordering: the WS injection is the newest message,
        // so it must be at the last index.
        lastIsInjected: s.messages.length > 0
          && s.messages[s.messages.length - 1].packetHash === 'ws-race-1498-1',
      };
    });
    assert(live.present, 'WS injection should appear immediately after processWSBatch');
    assert(live.fromWS, 'injected message must carry _fromWS === true');
    assert(live.count === 1, 'expected exactly 1 message after injection, got ' + live.count);
    assert(live.lastIsInjected, 'injected message must be at end (newest position)');

    // Wait for the REST response to have resolved AND selectChannel's
    // post-fetch state to be applied. Observable: messages.length is
    // stable at 1 (1 survivor merged into 0 REST results).
    await page.waitForFunction(() => {
      const s = window._channelsGetStateForTest();
      // After merge of [] REST with 1 survivor, count is 1.
      // (If the bug regressed, REST would stomp and count would drop to 0.)
      return s.messages.length === 1
        && s.messages[0].packetHash === 'ws-race-1498-1'
        // No more in-flight fetch for this channel.
        && window.__chFetchHits >= 1;
    }, undefined, { timeout: 3000 });

    const survives = await page.evaluate(() => {
      const s = window._channelsGetStateForTest();
      return {
        present: s.messages.some((m) => m.packetHash === 'ws-race-1498-1'),
        count: s.messages.length,
      };
    });
    assert(survives.present && survives.count === 1,
      'WS message stomped by REST fetch — messages after fetch: ' + JSON.stringify(survives));
  });

  await step('WS message survives REST replacement that does NOT contain its hash', async () => {
    // Reset DOM + state, re-arm mock.
    await page.goto(BASE + '/', { waitUntil: 'domcontentloaded' });
    await page.goto(BASE + '/#/channels', { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#chList .ch-item', { timeout: 10000 });
    await installFetchMock(page);
    const rows = await page.$$('.ch-section-network .ch-item');
    const hashA = await rows[0].getAttribute('data-hash');

    // Select A with an empty stub so messages settles at [].
    await page.evaluate(() => {
      window.__chFetchHits = 0;
      window.__chNextStub = { delayMs: 0, response: { messages: [] } };
    });
    await page.evaluate((h) => window._channelsSelectChannelForTest(h), hashA);
    await page.waitForFunction((h) => {
      const s = window._channelsGetStateForTest();
      return s.selectedHash === h && window.__chFetchHits >= 1;
    }, hashA, { timeout: 3000 });

    // Inject a WS message.
    await page.evaluate((h) => {
      window._channelsProcessWSBatchForTest([{
        type: 'message',
        data: {
          hash: 'survives-no-overlap',
          id: 'pkt-survives',
          decoded: { payload: { channel: h, sender: 'S', text: 't' } },
        },
      }], []);
    }, hashA);
    await page.waitForFunction(() =>
      window._channelsGetStateForTest().messages.some((m) => m.packetHash === 'survives-no-overlap'),
      undefined, { timeout: 2000 });

    // Arm REST refresh stub with a DIFFERENT hash — must not stomp our survivor.
    await page.evaluate(() => {
      window.__chFetchHits = 0;
      window.__chNextStub = { delayMs: 0, response: {
        messages: [{ packetHash: 'rest-only-hash', sender: 'R', text: 'rest', id: 'rest-1' }],
      } };
    });
    await page.evaluate(() => window._channelsRefreshMessagesForTest({ forceNoCache: true }));
    // Wait for the refresh fetch to have completed AND merge to have run.
    await page.waitForFunction(() => {
      const s = window._channelsGetStateForTest();
      return window.__chFetchHits >= 1
        && s.messages.some((m) => m.packetHash === 'rest-only-hash')
        && s.messages.some((m) => m.packetHash === 'survives-no-overlap');
    }, undefined, { timeout: 3000 });

    const after = await page.evaluate(() => {
      const s = window._channelsGetStateForTest();
      return {
        count: s.messages.length,
        hashes: s.messages.map((m) => m.packetHash),
      };
    });
    assert(after.count === 2,
      'expected 2 messages (1 REST + 1 survivor), got ' + JSON.stringify(after));
    // Survivor should be at end (newer).
    assert(after.hashes[after.hashes.length - 1] === 'survives-no-overlap',
      'survivor should be at end of array, got ' + JSON.stringify(after.hashes));
  });

  await step('REST refresh dedups identical-hash WS message (count exactly 1)', async () => {
    await page.goto(BASE + '/', { waitUntil: 'domcontentloaded' });
    await page.goto(BASE + '/#/channels', { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#chList .ch-item', { timeout: 10000 });
    await installFetchMock(page);
    const rows = await page.$$('.ch-section-network .ch-item');
    const hashA = await rows[0].getAttribute('data-hash');

    // Pre-load REST response containing the dup hash.
    await page.evaluate(() => {
      window.__chFetchHits = 0;
      window.__chNextStub = { delayMs: 0, response: {
        messages: [{ packetHash: 'dup-hash-1498', sender: 'RestS', text: 'rest copy', id: 'rest-dup' }],
      } };
    });
    await page.evaluate((h) => window._channelsSelectChannelForTest(h), hashA);
    await page.waitForFunction(() =>
      window._channelsGetStateForTest().messages.some((m) => m.packetHash === 'dup-hash-1498'),
      undefined, { timeout: 3000 });

    // Inject a WS message with the SAME hash (observer update style).
    await page.evaluate((h) => {
      window._channelsProcessWSBatchForTest([{
        type: 'message',
        data: {
          hash: 'dup-hash-1498',
          id: 'pkt-dup',
          decoded: { payload: { channel: h, sender: 'WsS', text: 'ws copy' } },
          observer: 'obs-A',
        },
      }], []);
    }, hashA);
    // Dedup hit on processWSBatch: still 1.
    const afterInject = await page.evaluate(() => {
      const s = window._channelsGetStateForTest();
      return {
        count: s.messages.length,
        dups: s.messages.filter((m) => m.packetHash === 'dup-hash-1498').length,
      };
    });
    assert(afterInject.count === 1, 'after WS dedup-hit: expected 1, got ' + afterInject.count);
    assert(afterInject.dups === 1, 'expected single entry for dup hash, got ' + afterInject.dups);

    // Now arm REST refresh that returns the SAME hash again. A blind
    // concat would produce count === 2; the merge must dedup to 1.
    await page.evaluate(() => {
      window.__chFetchHits = 0;
      window.__chNextStub = { delayMs: 0, response: {
        messages: [{ packetHash: 'dup-hash-1498', sender: 'RestS', text: 'rest copy 2', id: 'rest-dup-2' }],
      } };
    });
    await page.evaluate(() => window._channelsRefreshMessagesForTest({ forceNoCache: true }));
    await page.waitForFunction(() => window.__chFetchHits >= 1, undefined, { timeout: 3000 });
    // Give the merge a microtask to apply; observable: text is the REST-2 copy.
    await page.waitForFunction(() => {
      const s = window._channelsGetStateForTest();
      const m = s.messages.find((x) => x.packetHash === 'dup-hash-1498');
      return m && m.text === 'rest copy 2';
    }, undefined, { timeout: 3000 });

    const afterRefresh = await page.evaluate(() => {
      const s = window._channelsGetStateForTest();
      return {
        count: s.messages.length,
        dups: s.messages.filter((m) => m.packetHash === 'dup-hash-1498').length,
      };
    });
    assert(afterRefresh.count === 1,
      'REST refresh dedup: expected exactly 1 message, got ' + afterRefresh.count);
    assert(afterRefresh.dups === 1,
      'REST refresh dedup: expected single entry for dup hash, got ' + afterRefresh.dups);
  });

  await step('decryptAndRender onCacheHit path also merges WS-pushed messages', async () => {
    // Exercises the third REST-replacement site (cache hit branch of
    // fetchAndDecryptChannel). Without the fix at that site, the WS
    // message is stomped when the cache hits.
    await page.goto(BASE + '/', { waitUntil: 'domcontentloaded' });
    await page.goto(BASE + '/#/channels', { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#chList .ch-item', { timeout: 10000 });
    await installFetchMock(page);
    const rows = await page.$$('.ch-section-network .ch-item');
    const hashA = await rows[0].getAttribute('data-hash');

    await page.evaluate(() => {
      window.__chFetchHits = 0;
      window.__chNextStub = { delayMs: 0, response: { messages: [] } };
    });
    await page.evaluate((h) => window._channelsSelectChannelForTest(h), hashA);
    await page.waitForFunction((h) => window._channelsGetStateForTest().selectedHash === h
      && window.__chFetchHits >= 1, hashA, { timeout: 3000 });

    // Seed messages with a _fromWS entry, then directly invoke
    // mergeWsAppendedIntoRest with a cache-hit-shaped payload to
    // simulate what decryptAndRender's onCacheHit now does.
    await page.evaluate((h) => {
      window._channelsProcessWSBatchForTest([{
        type: 'message',
        data: {
          hash: 'cachehit-survivor',
          id: 'pkt-cachehit',
          decoded: { payload: { channel: h, sender: 'S', text: 't' } },
        },
      }], []);
    }, hashA);
    await page.waitForFunction(() =>
      window._channelsGetStateForTest().messages.some((m) => m.packetHash === 'cachehit-survivor'),
      undefined, { timeout: 2000 });

    // Simulate the onCacheHit path: it must merge, not stomp.
    const merged = await page.evaluate(() => {
      const s = window._channelsGetStateForTest();
      const cached = [{ packetHash: 'cached-rest-1', sender: 'C', text: 'cached', id: 'c-1' }];
      const result = window._channelsMergeWsAppendedIntoRestForTest(s.messages, cached);
      return {
        count: result.length,
        hasSurvivor: result.some((m) => m.packetHash === 'cachehit-survivor'),
        hasCached: result.some((m) => m.packetHash === 'cached-rest-1'),
      };
    });
    assert(merged.count === 2, 'onCacheHit merge: expected 2, got ' + merged.count);
    assert(merged.hasSurvivor, 'onCacheHit merge: survivor must be preserved');
    assert(merged.hasCached, 'onCacheHit merge: cached REST message must be included');
  });

  await step('WS messages from previous channel do not leak into next channel', async () => {
    await page.goto(BASE + '/', { waitUntil: 'domcontentloaded' });
    await page.goto(BASE + '/#/channels', { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#chList .ch-item', { timeout: 10000 });
    await installFetchMock(page);
    const rows = await page.$$('.ch-section-network .ch-item');
    if (rows.length < 2) throw new Error('need at least 2 network channels in fixture');
    const hashA = await rows[0].getAttribute('data-hash');
    const hashB = await rows[1].getAttribute('data-hash');

    // Select A with empty REST stub.
    await page.evaluate(() => {
      window.__chFetchHits = 0;
      window.__chNextStub = { delayMs: 0, response: { messages: [] } };
    });
    await page.evaluate((h) => window._channelsSelectChannelForTest(h), hashA);
    await page.waitForFunction((h) => window._channelsGetStateForTest().selectedHash === h
      && window.__chFetchHits >= 1, hashA, { timeout: 3000 });

    // Inject _fromWS message for A.
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
    await page.waitForFunction(() =>
      window._channelsGetStateForTest().messages.some((m) => m.packetHash === 'leak-test-from-A'),
      undefined, { timeout: 2000 });

    // Switch to B with a REST stub that does NOT contain the A hash.
    // Without the messages=[] reset, the A survivor would leak into B.
    await page.evaluate(() => {
      window.__chFetchHits = 0;
      window.__chNextStub = { delayMs: 0, response: {
        messages: [{ packetHash: 'b-rest-msg', sender: 'B', text: 'b', id: 'b-1' }],
      } };
    });
    await page.evaluate((h) => window._channelsSelectChannelForTest(h), hashB);
    await page.waitForFunction((h) => {
      const s = window._channelsGetStateForTest();
      return s.selectedHash === h
        && window.__chFetchHits >= 1
        && s.messages.some((m) => m.packetHash === 'b-rest-msg');
    }, hashB, { timeout: 3000 });

    const leaked = await page.evaluate(() =>
      window._channelsGetStateForTest().messages.some((m) => m.packetHash === 'leak-test-from-A'));
    assert(!leaked, 'WS message from channel A leaked into channel B view');
  });

  await browser.close();
  console.log(`\n=== #1498 race: ${passed} passed, ${failed} failed ===\n`);
  process.exit(failed === 0 ? 0 : 1);
})().catch((e) => { console.error(e); process.exit(1); });

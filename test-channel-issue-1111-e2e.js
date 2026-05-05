/**
 * #1111 — Hide "My Channels" section entirely when empty.
 *
 * Acceptance:
 *   - localStorage cleared → no `.ch-section-mychannels` (no header, no
 *     placeholder text, no "My Channels" string in #chList)
 *   - PSK key stored in localStorage → `.ch-section-mychannels` exists
 *     with the header
 *
 * Usage: BASE_URL=http://localhost:13581 node test-channel-issue-1111-e2e.js
 */
'use strict';

const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';
const STORAGE_KEY = 'corescope_channel_keys';

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
  const ctx = await browser.newContext();
  const page = await ctx.newPage();
  page.setDefaultTimeout(8000);
  page.on('pageerror', (e) => console.error('[pageerror]', e.message));

  console.log(`\n=== #1111 E2E against ${BASE} ===`);

  // ─── Bootstrap: ensure localStorage is empty ───
  await page.goto(BASE + '/', { waitUntil: 'domcontentloaded' });
  await page.evaluate(() => { try { localStorage.clear(); } catch (e) {} });

  // ─── Case 1: empty → My Channels section MUST NOT render ───
  await step('empty localStorage: .ch-section-mychannels MUST NOT exist', async () => {
    await page.goto(BASE + '/#/channels', { waitUntil: 'domcontentloaded' });
    // Wait for the channel list to populate (Network section is always present
    // when the API returns any public channels; otherwise wait for the list
    // container itself).
    await page.waitForSelector('#chList', { timeout: 8000 });
    // Give the renderer a tick to draw sections after the API resolves.
    await page.waitForFunction(() => {
      const el = document.getElementById('chList');
      // List is "ready" when at least one .ch-section is rendered, OR after
      // render has settled with no sections (truly empty).
      return el && (el.querySelector('.ch-section') || el.dataset.rendered === 'true' || el.children.length > 0);
    }, { timeout: 8000 }).catch(() => {});

    const mineCount = await page.$$eval('.ch-section-mychannels', els => els.length);
    assert(mineCount === 0,
      'Expected 0 .ch-section-mychannels with empty storage, got: ' + mineCount);

    // Also assert no "My Channels" header text leaked into #chList.
    const listText = await page.evaluate(() => {
      const el = document.getElementById('chList');
      return el ? el.textContent : '';
    });
    assert(!/My Channels/.test(listText),
      'Expected no "My Channels" text in #chList when empty, got: ' +
      listText.slice(0, 200));
  });

  // ─── Case 2: stored PSK key → My Channels section MUST render ───
  await step('stored PSK key: .ch-section-mychannels MUST exist with header', async () => {
    // Seed a stored key, then reload.
    await page.evaluate((sk) => {
      try {
        localStorage.setItem(sk, JSON.stringify({
          'TestChan1111': '00112233445566778899aabbccddeeff'
        }));
      } catch (e) {}
    }, STORAGE_KEY);

    await page.goto(BASE + '/#/channels', { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#chList', { timeout: 8000 });

    // Wait for the My Channels section to appear after merge.
    await page.waitForFunction(() => {
      return !!document.querySelector('.ch-section-mychannels');
    }, { timeout: 8000 });

    const mineCount = await page.$$eval('.ch-section-mychannels', els => els.length);
    assert(mineCount === 1,
      'Expected exactly 1 .ch-section-mychannels with stored key, got: ' + mineCount);

    const headerText = await page.evaluate(() => {
      const sec = document.querySelector('.ch-section-mychannels');
      const hdr = sec && sec.querySelector('.ch-section-header');
      return hdr ? hdr.textContent : '';
    });
    assert(/My Channels/.test(headerText),
      'Expected "My Channels" header in .ch-section-mychannels, got: ' + headerText);
  });

  // Cleanup so the test leaves storage in a known state.
  await page.evaluate(() => { try { localStorage.clear(); } catch (e) {} });

  console.log('\n=== Results: ' + passed + ' passed, ' + failed + ' failed ===');
  await browser.close();
  process.exit(failed > 0 ? 1 : 0);
})().catch((e) => { console.error('FATAL:', e); process.exit(1); });

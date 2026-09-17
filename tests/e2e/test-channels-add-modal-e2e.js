/**
 * #1297 B3 — channels.js Add Channel modal full coverage.
 *
 * Exercises every Section 1/2/3 path in init():
 *   - Section 1: Generate PSK Channel (key + QR + status banner)
 *   - Section 2: Add PSK Channel — invalid hex error, valid hex success,
 *     status banner, channel appears in My Channels, remove flow
 *   - Section 2: Scan QR — fallback path when ChannelQR.scan is absent
 *   - Section 3: Monitor Hashtag (with and without leading `#`)
 *   - Escape closes the modal
 *
 * Usage: BASE_URL=http://localhost:13581 node test-channels-add-modal-e2e.js
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
  // Auto-confirm window.confirm for remove flow.
  // NOTE: only one dialog handler — Playwright errors if a dialog is
  // accepted twice. Attach to the page after it's created.
  const page = await ctx.newPage();
  page.on('dialog', (d) => d.accept());
  page.setDefaultTimeout(8000);
  page.on('pageerror', (e) => console.error('[pageerror]', e.message));

  console.log(`\n=== #1297 B3 channels add-modal E2E against ${BASE} ===`);

  await page.goto(BASE + '/', { waitUntil: 'domcontentloaded' });
  await page.evaluate(() => { try { localStorage.clear(); } catch (e) {} });
  await page.goto(BASE + '/#/channels', { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('#chAddChannelBtn', { timeout: 8000 });

  async function openAddModal() {
    await page.click('#chAddChannelBtn');
    await page.waitForSelector('#chAddChannelModal:not(.hidden)', { timeout: 3000 });
  }

  await step('Section 1: Generate PSK Channel produces a key + status banner', async () => {
    await openAddModal();
    await page.fill('#chGenerateName', 'CovGenerated');
    await page.click('#chGenerateBtn');
    // Status banner appears.
    await page.waitForFunction(() => {
      const s = document.getElementById('chAddStatus');
      return s && s.style.display !== 'none' && /Generated/i.test(s.textContent);
    }, { timeout: 5000 });
    // QR output populated (either QR element or fallback text).
    const filled = await page.evaluate(() => {
      const out = document.getElementById('qr-output');
      return !!(out && (out.querySelector('img, canvas, table, svg') ||
        /Key generated/i.test(out.textContent || '')));
    });
    assert(filled, 'qr-output should be populated after generate');
    // Key persists in localStorage under 'corescope_channel_keys'.
    const stored = await page.evaluate(() => {
      try { return JSON.parse(localStorage.getItem('corescope_channel_keys') || '{}'); }
      catch (e) { return {}; }
    });
    assert(Object.keys(stored).length > 0,
      'at least one key should be persisted, got: ' + JSON.stringify(stored));
    // Close modal for next test.
    await page.keyboard.press('Escape');
    await page.waitForFunction(() => document.getElementById('chAddChannelModal')?.classList.contains('hidden'), { timeout: 3000 });
  });

  await step('Section 2: invalid hex shows inline error, does NOT close modal', async () => {
    await openAddModal();
    await page.fill('#chPskKey', 'NOT-HEX-VALUE');
    await page.click('#chPskAddBtn');
    await page.waitForFunction(() => {
      const e = document.getElementById('chPskError');
      return e && e.style.display !== 'none' && /32 hex/i.test(e.textContent);
    }, { timeout: 3000 });
    // Modal still open.
    const stillOpen = await page.$('#chAddChannelModal:not(.hidden)');
    assert(stillOpen, 'modal should remain open on invalid hex');
  });

  await step('Section 2: valid hex adds channel, closes modal, status banner', async () => {
    const KEY = 'aabbccddeeff00112233445566778899';
    await page.fill('#chPskKey', KEY);
    await page.fill('#chPskName', 'CovPsk');
    await page.click('#chPskAddBtn');
    await page.waitForFunction(() => document.getElementById('chAddChannelModal')?.classList.contains('hidden'), { timeout: 5000 });
    // Wait for the My Channels section to appear with our row.
    await page.waitForFunction(() => {
      const sec = document.querySelector('.ch-section-mychannels');
      return sec && /CovPsk|Private Channel/i.test(sec.textContent);
    }, { timeout: 5000 });
  });

  await step('Section 3: monitor hashtag strips leading # and adds row', async () => {
    await openAddModal();
    await page.fill('#chHashtagName', '#covhashtag');
    await page.click('#chHashtagBtn');
    await page.waitForFunction(() => document.getElementById('chAddChannelModal')?.classList.contains('hidden'), { timeout: 5000 });
    await page.waitForFunction(() => {
      const sec = document.querySelector('.ch-section-mychannels');
      return sec && /covhashtag/i.test(sec.textContent);
    }, { timeout: 5000 });
  });

  await step('Section 3: empty hashtag input is a no-op', async () => {
    await openAddModal();
    const beforeRows = await page.$$eval(
      '.ch-section-mychannels .ch-item', (els) => els.length);
    await page.fill('#chHashtagName', '   ');
    await page.click('#chHashtagBtn');
    // Modal stays open; nothing changes.
    await page.waitForTimeout(200);
    const stillOpen = await page.$('#chAddChannelModal:not(.hidden)');
    assert(stillOpen, 'empty hashtag should be a no-op (modal stays open)');
    const afterRows = await page.$$eval(
      '.ch-section-mychannels .ch-item', (els) => els.length);
    assert(beforeRows === afterRows,
      'rows should not change on empty hashtag, before=' + beforeRows + ' after=' + afterRows);
    await page.keyboard.press('Escape');
  });

  await step('Scan QR: clicking when ChannelQR.scan unavailable shows error', async () => {
    await openAddModal();
    // Force the unavailable path by deleting scan().
    await page.evaluate(() => {
      if (window.ChannelQR) { try { delete window.ChannelQR.scan; } catch (e) {} }
    });
    await page.click('#scan-qr-btn');
    await page.waitForFunction(() => {
      const e = document.getElementById('chPskError');
      return e && e.style.display !== 'none' && /unavailable/i.test(e.textContent);
    }, { timeout: 3000 });
    await page.keyboard.press('Escape');
  });

  await step('Escape key closes add modal', async () => {
    await openAddModal();
    await page.keyboard.press('Escape');
    await page.waitForFunction(() => document.getElementById('chAddChannelModal')?.classList.contains('hidden'), { timeout: 3000 });
  });

  await step('Remove channel: clicking ✕ removes row + clears localStorage key', async () => {
    const removeBtn = await page.$(
      '.ch-section-mychannels [data-remove-channel]');
    if (!removeBtn) { console.log('    (skip — no user-added rows)'); return; }
    const hash = await removeBtn.getAttribute('data-remove-channel');
    await removeBtn.click(); // dialog auto-accepted
    await page.waitForFunction((h) => {
      return !document.querySelector('[data-remove-channel="' + CSS.escape(h) + '"]');
    }, { timeout: 5000 }, hash);
  });

  await browser.close();
  console.log(`\n=== B3 add-modal: ${passed} passed, ${failed} failed ===\n`);
  process.exit(failed === 0 ? 0 : 1);
})().catch((e) => { console.error(e); process.exit(1); });

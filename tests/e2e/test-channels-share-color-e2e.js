/**
 * #1297 B3 — channels.js share modal + color picker coverage.
 *
 * Exercises:
 *   - Share modal normal mode (QR + Hex key + Copy button)
 *   - Share modal error mode (no key found → openShareModalError)
 *   - Escape closes share modal + focus restore
 *   - Channel color dot click triggers ChannelColorPicker.show (stubbed)
 *
 * Usage: BASE_URL=http://localhost:13581 node test-channels-share-color-e2e.js
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

  console.log(`\n=== #1297 B3 channels share-color E2E against ${BASE} ===`);

  await page.goto(BASE + '/', { waitUntil: 'domcontentloaded' });
  await page.evaluate(() => { try { localStorage.clear(); } catch (e) {} });
  await page.goto(BASE + '/#/channels', { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('#chAddChannelBtn', { timeout: 8000 });

  // Add a PSK channel so we have a Share button to click.
  await page.click('#chAddChannelBtn');
  await page.waitForSelector('#chAddChannelModal:not(.hidden)');
  await page.fill('#chPskKey', '00112233445566778899aabbccddeeff');
  await page.fill('#chPskName', 'CovShare');
  await page.click('#chPskAddBtn');
  await page.waitForFunction(() => document.getElementById('chAddChannelModal')?.classList.contains('hidden'), { timeout: 5000 });
  await page.waitForSelector('.ch-section-mychannels [data-share-channel]',
    { timeout: 5000 });

  await step('clicking Share opens dedicated #chShareModal with QR + Hex key', async () => {
    await page.click('.ch-section-mychannels [data-share-channel]');
    await page.waitForSelector('#chShareModal:not(.hidden)', { timeout: 5000 });
    const title = await page.textContent('#chShareModalTitle');
    assert(/Share/i.test(title), 'title should start with Share: ' + title);
    const keyField = await page.$eval('#chShareKey', (el) => el.value);
    assert(/^[0-9a-f]{32}$/i.test(keyField),
      'hex key field should be populated with 32-char hex, got: ' + keyField);
    const qrEl = await page.$('#chShareQr img, #chShareQr canvas, #chShareQr table, #chShareQr svg');
    assert(qrEl, 'QR element should be rendered in #chShareQr');
  });

  await step('Share modal Copy button labels success', async () => {
    const copyBtn = await page.$('#chShareModal [data-share-copy]');
    assert(copyBtn, 'copy button missing');
    await copyBtn.click();
    await page.waitForFunction(() => {
      const b = document.querySelector('#chShareModal [data-share-copy]');
      return b && /Copied/i.test(b.textContent);
    }, { timeout: 3000 });
  });

  await step('Escape closes share modal', async () => {
    await page.keyboard.press('Escape');
    await page.waitForFunction(() => document.getElementById('chShareModal')?.classList.contains('hidden'), { timeout: 3000 });
  });

  await step('Share with no stored key → error modal (openShareModalError)', async () => {
    // Wipe the stored key for our channel, then click Share again.
    await page.evaluate(() => {
      try {
        var KEY = 'corescope_channel_keys';
        var keys = JSON.parse(localStorage.getItem(KEY) || '{}');
        // Remove every key so getStoredKeys()[name] is undefined.
        localStorage.setItem(KEY, '{}');
        return keys;
      } catch (e) { return null; }
    });
    await page.click('.ch-section-mychannels [data-share-channel]');
    await page.waitForSelector('#chShareModal:not(.hidden)', { timeout: 5000 });
    const errTxt = await page.textContent('#chShareQr');
    assert(/No stored key|cannot share/i.test(errTxt),
      'error mode should show "No stored key" message, got: ' + errTxt);
    // Field-groups should be hidden in error mode.
    const fieldsHidden = await page.$$eval(
      '#chShareModal .ch-share-field-group',
      (els) => els.every((e) => e.hidden));
    assert(fieldsHidden, 'field groups should be hidden in error mode');
    // Close via the X button.
    await page.click('#chShareModalClose');
    await page.waitForFunction(() => document.getElementById('chShareModal')?.classList.contains('hidden'), { timeout: 3000 });
  });

  await step('color dot click invokes ChannelColorPicker.show', async () => {
    // Stub the color picker so we don't depend on its DOM.
    await page.evaluate(() => {
      window.__pickerCalls = [];
      window.ChannelColorPicker = {
        show: function (ch, x, y) { window.__pickerCalls.push({ ch: ch, x: x, y: y }); },
      };
    });
    const dot = await page.$('.ch-section-network .ch-color-dot');
    assert(dot, 'no .ch-color-dot found in network section');
    await dot.click();
    const calls = await page.evaluate(() => window.__pickerCalls);
    assert(calls.length >= 1, 'ChannelColorPicker.show should fire on dot click');
  });

  await step('share via keyboard Enter on the Share span (#chList keydown handler)', async () => {
    // Re-add a key so the share button exists with stored key again.
    await page.click('#chAddChannelBtn');
    await page.waitForSelector('#chAddChannelModal:not(.hidden)');
    await page.fill('#chPskKey', '11223344556677889900aabbccddeeff');
    await page.fill('#chPskName', 'CovKbd');
    await page.click('#chPskAddBtn');
    await page.waitForFunction(() => document.getElementById('chAddChannelModal')?.classList.contains('hidden'), { timeout: 5000 });
    await page.waitForSelector('.ch-section-mychannels [data-share-channel]',
      { timeout: 5000 });
    const shareSpan = await page.$('.ch-section-mychannels [data-share-channel]');
    await shareSpan.focus();
    await page.keyboard.press('Enter');
    await page.waitForSelector('#chShareModal:not(.hidden)', { timeout: 5000 });
    await page.keyboard.press('Escape');
  });

  await browser.close();
  console.log(`\n=== B3 share-color: ${passed} passed, ${failed} failed ===\n`);
  process.exit(failed === 0 ? 0 : 1);
})().catch((e) => { console.error(e); process.exit(1); });

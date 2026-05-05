/**
 * #1087 — Channel modal QR/share E2E.
 *
 * Boots Chromium against a CoreScope server (BASE_URL) and exercises
 * the four bugs filed in #1087:
 *
 *   1. Generate & Show QR produces a real QR (no "library not loaded")
 *   2. The QR-encoded `name=` parameter uses the user's display label
 *      (not `psk:<hex8>`)
 *   3. Adding a PSK channel persists across page refresh
 *   4. Clicking Share opens a DEDICATED share modal — distinct DOM id
 *      and title from the Add Channel modal
 *
 * Usage: BASE_URL=http://localhost:13581 node test-channel-issue-1087-e2e.js
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
  const ctx = await browser.newContext();
  const page = await ctx.newPage();
  page.setDefaultTimeout(8000);
  page.on('pageerror', (e) => console.error('[pageerror]', e.message));

  console.log(`\n=== #1087 E2E against ${BASE} ===`);

  // Always start clean: clear localStorage so prior test runs don't
  // leak channel keys into this session.
  await page.goto(BASE + '/', { waitUntil: 'domcontentloaded' });
  await page.evaluate(() => { try { localStorage.clear(); } catch (e) {} });

  await page.goto(BASE + '/#/channels', { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('#chAddChannelBtn', { timeout: 8000 });

  // ─── Bug 1 + Bug 2: Generate & Show QR works and uses display label ───
  await step('Bug 1+2: Generate & Show QR renders a QR using the display label', async () => {
    await page.click('#chAddChannelBtn');
    await page.waitForSelector('#chAddChannelModal:not(.hidden)');
    await page.fill('#chGenerateName', 'My Cool Channel');
    await page.click('#chGenerateBtn');

    // Wait for the QR render. The Kazuhiko Arase generator emits an
    // <img> (data URL) or table inside #qr-output.
    await page.waitForFunction(() => {
      const out = document.getElementById('qr-output');
      if (!out) return false;
      // Fail clearly if the old "[QR library not loaded]" text shows up.
      if (/QR library not loaded/i.test(out.textContent)) return true;
      return !!(out.querySelector('img, canvas, table, svg'));
    }, { timeout: 5000 });

    const out = await page.textContent('#qr-output');
    assert(!/QR library not loaded/i.test(out),
      'Bug 1: "[QR library not loaded]" must not appear');

    const hasQr = await page.evaluate(() => {
      const out = document.getElementById('qr-output');
      return !!(out && out.querySelector('img, canvas, table, svg'));
    });
    assert(hasQr, 'Bug 1: QR element (img/canvas/table/svg) must be rendered');

    // Bug 2: the QR URL printed under the QR must use the display label.
    const urlText = await page.evaluate(() => {
      const u = document.querySelector('#qr-output .channel-qr-url');
      return u ? u.textContent : '';
    });
    assert(urlText && /name=My(\+|%20|\s)?Cool(\+|%20|\s)?Channel/i.test(urlText),
      'Bug 2: QR URL must encode the user display name, got: ' + urlText);
    assert(!/name=psk(%3A|:)/i.test(urlText),
      'Bug 2: QR URL must NOT encode the internal `psk:<hex8>` key, got: ' + urlText);
    // Close the add modal.
    await page.click('[data-action="ch-modal-close"]').catch(() => {});
  });

  // ─── Bug 3: PSK channel persists across page refresh ───
  await step('Bug 3: PSK channel persists across refresh', async () => {
    await page.evaluate(() => { try { localStorage.clear(); } catch (e) {} });
    await page.reload({ waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#chAddChannelBtn');
    await page.click('#chAddChannelBtn');
    await page.waitForSelector('#chAddChannelModal:not(.hidden)');

    // Use the PSK Add path (synchronous, no key derivation needed).
    const KEY = '00112233445566778899aabbccddeeff';
    await page.fill('#chPskKey', KEY);
    await page.fill('#chPskName', 'PersistMe');
    await page.click('#chPskAddBtn');

    // Storage must contain the key SYNCHRONOUSLY after submit — not as
    // a side effect of subsequent UI events.
    const stored = await page.evaluate(() => {
      try { return localStorage.getItem('corescope_channel_keys'); }
      catch (e) { return null; }
    });
    assert(stored && stored.indexOf(KEY) !== -1,
      'Bug 3: corescope_channel_keys must contain the new key after submit, got: ' + stored);

    // Reload — the channel must still be in the sidebar.
    await page.reload({ waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#chList');
    const stillStored = await page.evaluate(() => {
      try { return localStorage.getItem('corescope_channel_keys'); }
      catch (e) { return null; }
    });
    assert(stillStored && stillStored.indexOf(KEY) !== -1,
      'Bug 3: key must survive refresh in localStorage, got: ' + stillStored);

    // Sidebar must show the user-added channel (look for the label).
    await page.waitForFunction(() => {
      const list = document.getElementById('chList');
      return !!(list && /PersistMe/.test(list.textContent));
    }, { timeout: 5000 });
  });

  // ─── Bug 4: Share opens a DEDICATED modal ───
  await step('Bug 4: Share button opens a dedicated share modal (not Add)', async () => {
    // Channel from previous step is in the sidebar.
    await page.waitForSelector('[data-share-channel]');
    // Make sure the Add modal is closed before we click Share.
    const addOpen = await page.evaluate(() => {
      const m = document.getElementById('chAddChannelModal');
      return !!(m && !m.classList.contains('hidden') && !m.hasAttribute('hidden'));
    });
    assert(!addOpen, 'precondition: Add modal must be closed before Share click');

    await page.click('[data-share-channel]');

    // The Share modal must exist and be visible.
    await page.waitForSelector('#chShareModal:not(.hidden)', { timeout: 5000 });

    // The Add modal must NOT be the one that opened.
    const addStillClosed = await page.evaluate(() => {
      const m = document.getElementById('chAddChannelModal');
      return !!(m && (m.classList.contains('hidden') || m.hasAttribute('hidden')));
    });
    assert(addStillClosed, 'Bug 4: Add modal must NOT open when Share is clicked');

    // Title must be share-specific.
    const shareTitle = await page.evaluate(() => {
      const m = document.getElementById('chShareModal');
      if (!m) return '';
      const t = m.querySelector('#chShareModalTitle, .ch-share-modal-title, h2, h3, h4');
      return t ? t.textContent : '';
    });
    assert(/share/i.test(shareTitle),
      'Bug 4: share modal title must contain "Share", got: ' + shareTitle);

    // Hex key field must be present and copyable. (#1101: URL field
    // removed — QR already encodes the URL, a separate Copy URL button
    // was redundant.)
    const hasFields = await page.evaluate(() => {
      const m = document.getElementById('chShareModal');
      if (!m) return false;
      const k = m.querySelector('#chShareKey, [data-share-field="key"]');
      const u = m.querySelector('#chShareUrl, [data-share-field="url"]');
      return !!k && !u;
    });
    assert(hasFields, 'Bug 4 / #1101: share modal exposes ONLY the hex key field (no URL field)');

    // #1101: the QR box must contain ONLY the QR <img> — no URL text
    // line, no inline Copy Key button overlapping the image.
    const qrBoxOnlyHasQr = await page.evaluate(() => {
      const qr = document.getElementById('chShareQr');
      if (!qr) return { ok: false, reason: 'no #chShareQr' };
      const imgs = qr.querySelectorAll('img');
      const urlLine = qr.querySelector('.channel-qr-url');
      const copyBtn = qr.querySelector('.channel-qr-copy, button');
      return {
        ok: imgs.length === 1 && !urlLine && !copyBtn,
        imgCount: imgs.length,
        hasUrlLine: !!urlLine,
        hasCopyBtn: !!copyBtn,
      };
    });
    assert(qrBoxOnlyHasQr.ok,
      '#1101: #chShareQr contains ONLY the QR image (got ' +
      JSON.stringify(qrBoxOnlyHasQr) + ')');
  });

  console.log('\n=== Results: ' + passed + ' passed, ' + failed + ' failed ===');
  await browser.close();
  process.exit(failed > 0 ? 1 : 0);
})().catch((e) => { console.error('FATAL:', e); process.exit(1); });

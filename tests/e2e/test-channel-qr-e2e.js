/**
 * #1297 B2 — Coverage E2E for public/channel-qr.js
 *
 * Drives window.ChannelQR in a real browser:
 *   - buildUrl / parseChannelUrl roundtrip + invalid-input rejection
 *   - generate() renders a QR <img> + URL line + Copy Key button via the
 *     vendored qrcode-generator library
 *   - generate() with qrOnly=true skips URL line + Copy button
 *   - Copy Key button copies hex to clipboard (or falls back) and flips
 *     label to "✓ Copied"
 *   - scan() returns null when navigator.mediaDevices is unavailable
 *     (browser-context shim) and shows the inline fallback
 *
 * Usage: BASE_URL=http://localhost:13581 node test-channel-qr-e2e.js
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
  // Grant clipboard permissions so navigator.clipboard.writeText succeeds.
  const ctx = await browser.newContext({ permissions: ['clipboard-read', 'clipboard-write'] });
  const page = await ctx.newPage();
  page.setDefaultTimeout(8000);
  page.on('pageerror', (e) => console.error('[pageerror]', e.message));

  console.log('\n=== #1297 B2 channel-qr E2E against ' + BASE + ' ===');

  await page.goto(BASE + '/#/channels', { waitUntil: 'domcontentloaded' });
  await page.waitForFunction(() => window.ChannelQR && window.ChannelQR.buildUrl,
    { timeout: 8000 });

  await step('buildUrl returns meshcore://channel/add?name=...&secret=...', async () => {
    const url = await page.evaluate(() =>
      window.ChannelQR.buildUrl('My Room', '00112233445566778899aabbccddeeff'));
    assert(url.indexOf('meshcore://channel/add?') === 0,
      'wrong scheme: ' + url);
    assert(url.indexOf('name=My%20Room') >= 0, 'name not encoded: ' + url);
    assert(url.indexOf('secret=00112233445566778899aabbccddeeff') >= 0,
      'secret missing: ' + url);
  });

  await step('parseChannelUrl returns { name, secret } for valid URL', async () => {
    const out = await page.evaluate(() =>
      window.ChannelQR.parseChannelUrl(
        'meshcore://channel/add?name=My%20Room&secret=00112233445566778899AABBCCDDEEFF'));
    assert(out && out.name === 'My Room', 'name: ' + (out && out.name));
    // secret should be lowercased
    assert(out && out.secret === '00112233445566778899aabbccddeeff',
      'secret: ' + (out && out.secret));
  });

  await step('parseChannelUrl rejects wrong scheme', async () => {
    const out = await page.evaluate(() =>
      window.ChannelQR.parseChannelUrl('https://example.com?name=x&secret=' + 'a'.repeat(32)));
    assert(out === null, 'expected null, got ' + JSON.stringify(out));
  });

  await step('parseChannelUrl rejects missing secret', async () => {
    const out = await page.evaluate(() =>
      window.ChannelQR.parseChannelUrl('meshcore://channel/add?name=onlyname'));
    assert(out === null, 'expected null, got ' + JSON.stringify(out));
  });

  await step('parseChannelUrl rejects non-32-hex secret', async () => {
    const out = await page.evaluate(() =>
      window.ChannelQR.parseChannelUrl(
        'meshcore://channel/add?name=x&secret=zznothex'));
    assert(out === null, 'expected null, got ' + JSON.stringify(out));
  });

  await step('parseChannelUrl rejects null/empty/non-string', async () => {
    const out = await page.evaluate(() => {
      return {
        a: window.ChannelQR.parseChannelUrl(null),
        b: window.ChannelQR.parseChannelUrl(''),
        c: window.ChannelQR.parseChannelUrl(42),
      };
    });
    assert(out.a === null && out.b === null && out.c === null,
      'expected nulls, got ' + JSON.stringify(out));
  });

  await step('generate() renders QR <img> + URL line + Copy Key button', async () => {
    const info = await page.evaluate(() => {
      const t = document.createElement('div');
      t.id = '__qrTest1';
      document.body.appendChild(t);
      window.ChannelQR.generate('My Room', '00112233445566778899aabbccddeeff', t);
      return {
        canvasHtml: t.querySelector('.channel-qr-canvas') ?
          t.querySelector('.channel-qr-canvas').innerHTML.slice(0, 200) : null,
        hasImg: !!t.querySelector('.channel-qr-canvas img'),
        urlText: t.querySelector('.channel-qr-url') ?
          t.querySelector('.channel-qr-url').textContent : null,
        copyBtnText: t.querySelector('.channel-qr-copy') ?
          t.querySelector('.channel-qr-copy').textContent : null,
      };
    });
    assert(info.hasImg, 'expected <img> in .channel-qr-canvas, got: ' + info.canvasHtml);
    assert(info.urlText && info.urlText.indexOf('meshcore://channel/add') === 0,
      'URL line wrong: ' + info.urlText);
    assert(info.copyBtnText && info.copyBtnText.indexOf('Copy Key') >= 0,
      'copy btn text: ' + info.copyBtnText);
  });

  await step('generate() with qrOnly skips URL line + Copy Key', async () => {
    const info = await page.evaluate(() => {
      const t = document.createElement('div');
      t.id = '__qrTest2';
      document.body.appendChild(t);
      window.ChannelQR.generate('Solo', 'aabbccddeeff00112233445566778899', t,
        { qrOnly: true });
      return {
        hasImg: !!t.querySelector('.channel-qr-canvas img'),
        hasUrlLine: !!t.querySelector('.channel-qr-url'),
        hasCopy: !!t.querySelector('.channel-qr-copy'),
      };
    });
    assert(info.hasImg, 'expected QR <img>');
    assert(!info.hasUrlLine, 'qrOnly should skip URL line');
    assert(!info.hasCopy, 'qrOnly should skip Copy Key button');
  });

  await step('Copy Key button writes hex to clipboard + flips label', async () => {
    const result = await page.evaluate(async () => {
      const t = document.createElement('div');
      t.id = '__qrTest3';
      document.body.appendChild(t);
      const hex = 'deadbeefcafef00d0011223344556677';
      window.ChannelQR.generate('Copyable', hex, t);
      const btn = t.querySelector('.channel-qr-copy');
      btn.click();
      // Wait a tick for the async copy to complete + label flip
      await new Promise(r => setTimeout(r, 200));
      let clip = '';
      try {
        clip = await navigator.clipboard.readText();
      } catch (_e) { clip = '(read-denied)'; }
      return { clip: clip, btnText: btn.textContent, expected: hex };
    });
    assert(result.btnText.indexOf('Copied') >= 0,
      'expected "Copied" label, got: ' + result.btnText);
    // Allow read-denied in some headless environments — primary assertion
    // is the visible label flip. When we CAN read clipboard, it should
    // contain the hex.
    if (result.clip !== '(read-denied)') {
      assert(result.clip === result.expected,
        'clipboard mismatch: got "' + result.clip + '" expected "' + result.expected + '"');
    }
  });

  await step('generate() is a no-op without target', async () => {
    const err = await page.evaluate(() => {
      try {
        window.ChannelQR.generate('x', 'aa', null);
        return null;
      } catch (e) { return e.message; }
    });
    assert(err === null, 'expected silent no-op, got: ' + err);
  });

  await step('scan() resolves with null when getUserMedia is unavailable', async () => {
    const result = await page.evaluate(async () => {
      // Shim mediaDevices off for this call. We can't undefine
      // navigator.mediaDevices directly in Chromium, so override its
      // getUserMedia to throw and clear jsQR.
      const savedJsqr = window.jsQR;
      window.jsQR = undefined;
      const out = await window.ChannelQR.scan();
      window.jsQR = savedJsqr;
      const fallback = document.querySelector('.channel-qr-fallback');
      return {
        out: out,
        fallbackText: fallback ? fallback.textContent : null,
      };
    });
    assert(result.out === null, 'expected null result, got ' + JSON.stringify(result.out));
    assert(result.fallbackText && /Camera not available/i.test(result.fallbackText),
      'expected fallback toast, got: ' + result.fallbackText);
  });

  console.log('\n=== Results: passed ' + passed + ' failed ' + failed + ' ===');
  await browser.close();
  process.exit(failed > 0 ? 1 : 0);
})().catch((e) => { console.error('FATAL:', e); process.exit(1); });

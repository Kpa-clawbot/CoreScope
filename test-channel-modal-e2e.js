/**
 * E2E (#1034 PR1): Channel Add modal + sectioned sidebar.
 *
 * Boots a headless Chromium against a locally running corescope-server and
 * exercises:
 *   - sidebar [+ Add Channel] opens modal
 *   - modal renders three labeled sections + privacy footer + QR placeholders
 *   - close (✕) hides modal
 *   - sectioned sidebar renders My Channels / Network / Encrypted sections
 *   - PSK add flow: invalid hex → error; valid hex → modal closes
 *
 * Usage: BASE_URL=http://localhost:38201 node test-channel-modal-e2e.js
 */
'use strict';
const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:38201';

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

  console.log(`\n=== #1034 PR1 E2E against ${BASE} ===`);

  await step('navigate to /channels', async () => {
    await page.goto(BASE + '/#/channels', { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#chAddChannelBtn', { timeout: 8000 });
  });

  await step('Add Channel button is visible', async () => {
    const text = await page.textContent('#chAddChannelBtn');
    assert(/Add Channel/.test(text), 'button text: ' + text);
  });

  await step('modal hidden on load', async () => {
    const isHidden = await page.evaluate(() => {
      const m = document.getElementById('chAddChannelModal');
      return !!m && (m.classList.contains('hidden') || m.hasAttribute('hidden'));
    });
    assert(isHidden, 'modal should start hidden');
  });

  await step('clicking [+ Add Channel] opens modal', async () => {
    await page.click('#chAddChannelBtn');
    await page.waitForSelector('#chAddChannelModal:not(.hidden)', { timeout: 3000 });
    const visible = await page.isVisible('#chAddChannelModal');
    assert(visible, 'modal should be visible after click');
  });

  await step('modal renders all three section titles', async () => {
    const html = await page.innerHTML('#chAddChannelModal');
    assert(html.includes('Generate PSK Channel'), 'section 1 missing');
    assert(html.includes('Add Private Channel (PSK)'), 'section 2 missing');
    assert(html.includes('Monitor Hashtag Channel'), 'section 3 missing');
  });

  await step('modal renders QR placeholders', async () => {
    assert(await page.isVisible('#qr-output'), '#qr-output missing');
    const scanBtn = await page.$('#scan-qr-btn');
    assert(scanBtn, '#scan-qr-btn missing');
    const disabled = await scanBtn.getAttribute('disabled');
    assert(disabled === null, '#scan-qr-btn must be enabled (wired in #1034 PR3)');
  });

  await step('modal renders privacy footer', async () => {
    const footer = await page.textContent('#chAddChannelModal .ch-modal-footer');
    assert(/Keys stay in your browser/.test(footer), 'footer text missing: ' + footer);
    assert(/passive observer/.test(footer), 'passive observer text missing');
  });

  await step('modal renders case-sensitivity warning', async () => {
    const warn = await page.textContent('#chAddChannelModal .ch-modal-warn');
    assert(/[Cc]ase-sensitive/.test(warn), 'warning missing: ' + warn);
  });

  await step('PSK add: invalid hex shows inline error', async () => {
    await page.fill('#chPskKey', 'not-hex');
    await page.click('#chPskAddBtn');
    await page.waitForFunction(() => {
      const e = document.getElementById('chPskError');
      return e && e.style.display !== 'none' && /hex/i.test(e.textContent);
    }, { timeout: 3000 });
  });

  await step('close button (✕) hides modal', async () => {
    await page.click('#chModalClose');
    await page.waitForFunction(() => {
      const m = document.getElementById('chAddChannelModal');
      return m && m.classList.contains('hidden');
    }, { timeout: 3000 });
  });

  await step('sidebar renders three sections (My Channels / Network / Encrypted)', async () => {
    // Wait for channel list to populate from API (or render empty-state).
    await page.waitForFunction(() => {
      const el = document.getElementById('chList');
      if (!el) return false;
      return el.querySelector('.ch-section-mychannels') &&
             el.querySelector('.ch-section-network') &&
             el.querySelector('.ch-section-encrypted');
    }, { timeout: 8000 });
    const headers = await page.$$eval('.ch-section-header', els => els.map(e => e.textContent.trim()));
    const joined = headers.join(' | ');
    assert(/My Channels/.test(joined), 'My Channels header missing: ' + joined);
    assert(/Network/.test(joined), 'Network header missing');
    assert(/Encrypted/.test(joined), 'Encrypted header missing');
  });

  await step('Encrypted section is collapsed by default', async () => {
    const collapsed = await page.getAttribute('.ch-section-encrypted', 'data-encrypted-collapsed');
    assert(collapsed === 'true', 'expected data-encrypted-collapsed=true, got ' + collapsed);
    const bodyHidden = await page.evaluate(() => {
      const b = document.getElementById('chEncryptedBody');
      return b ? b.hasAttribute('hidden') : null;
    });
    assert(bodyHidden === true, 'encrypted body should be hidden initially');
  });

  await step('clicking Encrypted toggle expands it', async () => {
    await page.click('#chEncryptedToggle');
    const bodyHidden = await page.evaluate(() => {
      const b = document.getElementById('chEncryptedBody');
      return b ? b.hasAttribute('hidden') : null;
    });
    assert(bodyHidden === false, 'encrypted body should be visible after toggle');
  });

  await step('PSK add: valid hex closes modal and persists key', async () => {
    await page.click('#chAddChannelBtn');
    await page.waitForSelector('#chAddChannelModal:not(.hidden)');
    const validHex = 'cafebabe' + '00112233' + '44556677' + '8899aabb';
    await page.fill('#chPskKey', validHex);
    await page.fill('#chPskName', 'E2E Test Channel');
    await page.click('#chPskAddBtn');
    await page.waitForFunction(() => {
      const m = document.getElementById('chAddChannelModal');
      return m && m.classList.contains('hidden');
    }, { timeout: 5000 });
    const stored = await page.evaluate(() => localStorage.getItem('corescope_channel_keys') || '');
    assert(/cafebabe/i.test(stored), 'expected stored key in localStorage corescope_channel_keys, got: ' + stored);
  });

  await browser.close();

  console.log(`\n=== Results: passed ${passed} failed ${failed} ===`);
  process.exit(failed > 0 ? 1 : 0);
})().catch(e => { console.error(e); process.exit(1); });

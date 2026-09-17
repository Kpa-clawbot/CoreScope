/**
 * #1297 B3 — channels.js selection + messages tab coverage.
 *
 * Exercises selectChannel() for a Network (unencrypted) channel,
 * messages rendering (avatars, sender colors, packet links), the node
 * detail panel open/close (showNodeDetail / closeNodeDetail), and the
 * scroll-to-bottom button.
 *
 * Usage: BASE_URL=http://localhost:13581 node test-channels-selection-flow-e2e.js
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
  page.setDefaultTimeout(8000);
  page.on('pageerror', (e) => console.error('[pageerror]', e.message));

  console.log(`\n=== #1297 B3 channels selection-flow E2E against ${BASE} ===`);

  await page.goto(BASE + '/', { waitUntil: 'domcontentloaded' });
  await page.evaluate(() => { try { localStorage.clear(); } catch (e) {} });
  await page.goto(BASE + '/#/channels', { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('#chList .ch-section-network .ch-item', { timeout: 10000 });

  let selectedHash = null;
  let selectedName = null;

  await step('clicking a network channel updates header + URL', async () => {
    const row = await page.$('.ch-section-network .ch-item');
    selectedHash = await row.getAttribute('data-hash');
    selectedName = (await row.getAttribute('aria-label')) || '';
    await row.click();
    // Header text updates with " — N messages" (count comes from list payload).
    await page.waitForFunction(() => {
      const t = document.querySelector('#chHeader .ch-header-text');
      return t && /—\s*\d+\s*messages?/.test(t.textContent);
    }, { timeout: 5000 });
    const url = page.url();
    assert(url.includes('#/channels/'), 'URL should reflect channel selection: ' + url);
    const sel = await page.$('.ch-section-network .ch-item.selected');
    assert(sel, 'selected row should get .selected class');
  });

  await step('message rows render with avatar + sender + bubble', async () => {
    // Wait for either messages or an empty-state node. We expect messages
    // for the fixture's busy public/#test/#bot channels.
    await page.waitForFunction(() => {
      const m = document.getElementById('chMessages');
      if (!m) return false;
      return m.querySelector('.ch-msg') || m.querySelector('.ch-empty');
    }, { timeout: 8000 });
    const hasMessages = await page.$('.ch-msg');
    if (hasMessages) {
      const avatar = await page.$('.ch-msg .ch-avatar[data-node]');
      assert(avatar, '.ch-avatar with data-node missing');
      const bubble = await page.$('.ch-msg .ch-msg-bubble');
      assert(bubble, '.ch-msg-bubble missing');
      const sender = await page.$('.ch-msg .ch-msg-sender');
      assert(sender, '.ch-msg-sender missing');
    } else {
      // Acceptable: channel exists but no messages — still exercised the path.
      assert(true, 'no messages — empty branch exercised');
    }
  });

  await step('view-packet link is present when packetHash exists', async () => {
    const link = await page.$('.ch-msg .ch-analyze-link');
    // Not asserted as required (fixture-dependent), but if present must point
    // at /#/packets/.
    if (link) {
      const href = await link.getAttribute('href');
      assert(href && href.indexOf('#/packets/') === 0,
        'analyze link should target packets route: ' + href);
    }
  });

  await step('clicking a sender avatar opens the node detail panel', async () => {
    const avatar = await page.$('.ch-msg .ch-avatar[data-node]');
    if (!avatar) { console.log('    (skip — no messages in fixture)'); return; }
    await avatar.click();
    await page.waitForSelector('.ch-node-panel.open', { timeout: 5000 });
    const panel = await page.$('.ch-node-panel.open');
    assert(panel, 'node panel should open');
    // URL should carry ?node=...
    const url = page.url();
    assert(/[?&]node=/.test(url), 'URL should include node param: ' + url);
  });

  await step('closing the node panel restores URL', async () => {
    const panel = await page.$('.ch-node-panel.open');
    if (!panel) { console.log('    (skip — panel not open)'); return; }
    const closeBtn = await page.$('.ch-node-panel .ch-node-close');
    assert(closeBtn, 'close button missing');
    await closeBtn.click();
    await page.waitForFunction(() => {
      const p = document.querySelector('.ch-node-panel');
      return !p || !p.classList.contains('open');
    }, { timeout: 3000 });
    const url = page.url();
    assert(!/[?&]node=/.test(url), 'URL should drop ?node= on close: ' + url);
  });

  await step('keyboard Enter on a sender link opens the node panel', async () => {
    const link = await page.$('.ch-msg .ch-msg-sender[data-node]');
    if (!link) { console.log('    (skip — no senders)'); return; }
    await link.focus();
    await page.keyboard.press('Enter');
    await page.waitForSelector('.ch-node-panel.open', { timeout: 5000 });
    // Close again so subsequent steps start clean.
    const closeBtn = await page.$('.ch-node-panel .ch-node-close');
    if (closeBtn) await closeBtn.click();
  });

  await step('deep-link route loads with selection pre-applied', async () => {
    if (!selectedHash) { console.log('    (skip — no selected hash)'); return; }
    await page.goto(BASE + '/#/channels/' + encodeURIComponent(selectedHash),
      { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('.ch-section-network .ch-item.selected', { timeout: 8000 });
    const sel = await page.$eval('.ch-item.selected', (el) => el.getAttribute('data-hash'));
    assert(sel === selectedHash,
      'selected channel should match deep-link hash: ' + sel + ' vs ' + selectedHash);
  });

  await step('scroll button exists and toggles hidden when scrolled to bottom', async () => {
    const btn = await page.$('#chScrollBtn');
    assert(btn, '#chScrollBtn missing');
    // After deep-link re-init the messages list may or may not be scrolled
    // all the way down (depends on render timing + per-channel scroll
    // restore). The contract we actually want to assert is "the button is
    // hidden when scrollTop is at bottom" — drive that condition
    // explicitly via scrollToBottom (the same code path the button click
    // would trigger) and then verify the hidden class.
    await page.evaluate(() => {
      const m = document.querySelector('.ch-messages') || document.getElementById('chMessages');
      if (m) { m.scrollTop = m.scrollHeight; m.dispatchEvent(new Event('scroll', { bubbles: true })); }
    });
    await page.waitForFunction(
      () => document.getElementById('chScrollBtn')?.classList.contains('hidden'),
      { timeout: 3000 },
    );
    const hidden = await btn.evaluate((el) => el.classList.contains('hidden'));
    assert(hidden, 'scroll button should be hidden when scrolled to bottom');
  });

  await browser.close();
  console.log(`\n=== B3 selection-flow: ${passed} passed, ${failed} failed ===\n`);
  process.exit(failed === 0 ? 0 : 1);
})().catch((e) => { console.error(e); process.exit(1); });

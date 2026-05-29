/**
 * E2E (#1486): Packets-page collapse chevron must not reopen the detail
 * panel that the operator just closed.
 *
 * Repro:
 *   1. Open /#/packets on desktop.
 *   2. Click a group-header row's expand chevron → tree expands, detail
 *      panel opens on the right.
 *   3. Close the detail panel via the ✕ button in its top-right.
 *   4. Click the SAME chevron again, intending to collapse the tree.
 *
 * Bug: the second click reopens the detail panel (because the
 * `toggle-select` action handler unconditionally calls both
 * `pktToggleGroup(value)` AND `pktSelectHash(value)` — the latter
 * re-fires selectPacket even when the row was being collapsed).
 *
 * Fix expectation: after the second chevron click the tree row must be
 * collapsed AND the #pktRight panel must remain in its "empty" /
 * collapsed state.
 *
 * Usage: BASE_URL=http://localhost:13581 node test-issue-1486-collapse-reopens-detail-e2e.js
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
  const ctx = await browser.newContext({ viewport: { width: 1400, height: 900 } });
  const page = await ctx.newPage();
  page.setDefaultTimeout(10000);
  page.on('pageerror', (e) => console.error('[pageerror]', e.message));

  console.log(`\n=== #1486 packets collapse-reopens-detail E2E against ${BASE} ===`);

  await step('navigate to /packets, enable group-by-hash, wait for a group-header row', async () => {
    await page.goto(BASE + '/#/packets', { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#pktBody', { timeout: 8000 });
    // Widen window so fixture rows render
    await page.evaluate(() => {
      const sel = document.getElementById('fTimeWindow');
      if (sel) { sel.value = '0'; sel.dispatchEvent(new Event('change', { bubbles: true })); }
    });
    // Widen window so the seeded grouped fixture row renders
    await page.evaluate(() => {
      const sel = document.getElementById('fTimeWindow');
      if (sel) { sel.value = '0'; sel.dispatchEvent(new Event('change', { bubbles: true })); }
    });
    // Group by Hash is ON by default; do not toggle.
    // Wait for at least one toggle-select (grouped) row
    await page.waitForFunction(
      () => !!document.querySelector('#pktBody tr[data-action="toggle-select"]'),
      { timeout: 12000 }
    );
    // Wait for at least one toggle-select row
    await page.waitForFunction(
      () => !!document.querySelector('#pktBody tr[data-action="toggle-select"]'),
      { timeout: 10000 }
    );
  });

  await step('1st chevron click expands row AND opens detail panel', async () => {
    const beforeHash = await page.evaluate(() => {
      const row = document.querySelector('#pktBody tr[data-action="toggle-select"]');
      return row?.dataset.hash || null;
    });
    assert(beforeHash, 'no group-header row found');

    await page.evaluate((h) => {
      const row = document.querySelector(`#pktBody tr[data-action="toggle-select"][data-hash="${h}"]`);
      row.click();
    }, beforeHash);

    // Wait for detail panel to populate
    await page.waitForFunction(
      () => {
        const p = document.getElementById('pktRight');
        return p && !p.classList.contains('empty');
      },
      { timeout: 8000 }
    );

    const state = await page.evaluate((h) => {
      const row = document.querySelector(`#pktBody tr[data-action="toggle-select"][data-hash="${h}"]`);
      const panel = document.getElementById('pktRight');
      return {
        expanded: !!row && row.classList.contains('expanded'),
        panelEmpty: !!panel && panel.classList.contains('empty'),
      };
    }, beforeHash);
    assert(state.expanded, 'row should be expanded after 1st click');
    assert(!state.panelEmpty, 'detail panel should be open after 1st click');
  });

  await step('Close detail panel via ✕ button — panel goes back to empty', async () => {
    await page.evaluate(() => {
      const btn = document.querySelector('#pktRight .panel-close-btn');
      if (btn) btn.click();
    });
    await page.waitForFunction(
      () => {
        const p = document.getElementById('pktRight');
        return p && p.classList.contains('empty');
      },
      { timeout: 5000 }
    );
  });

  await step('2nd chevron click COLLAPSES the row WITHOUT reopening the detail panel', async () => {
    const hash = await page.evaluate(() => {
      const row = document.querySelector('#pktBody tr[data-action="toggle-select"].expanded');
      return row?.dataset.hash || null;
    });
    assert(hash, 'no expanded group-header row found before 2nd click');

    await page.evaluate((h) => {
      const row = document.querySelector(`#pktBody tr[data-action="toggle-select"][data-hash="${h}"]`);
      row.click();
    }, hash);

    // Give any async pktSelectHash time to (incorrectly) repopulate panel.
    await page.waitForTimeout(800);

    const state = await page.evaluate((h) => {
      const row = document.querySelector(`#pktBody tr[data-action="toggle-select"][data-hash="${h}"]`);
      const panel = document.getElementById('pktRight');
      return {
        expanded: !!row && row.classList.contains('expanded'),
        panelEmpty: !!panel && panel.classList.contains('empty'),
      };
    }, hash);
    assert(!state.expanded, 'row must be collapsed after 2nd chevron click');
    assert(state.panelEmpty, 'detail panel must NOT reopen after collapse (was: ' + (state.panelEmpty ? 'empty' : 'populated') + ')');
  });

  await browser.close();
  console.log(`\n${passed} passed, ${failed} failed`);
  process.exit(failed === 0 ? 0 : 1);
})().catch(e => { console.error(e); process.exit(1); });

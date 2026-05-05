/**
 * E2E (#966): Wireshark-style filter UX.
 *
 * Boots Chromium against a local corescope-server (defaults to fixture instance
 * on :39966) and exercises:
 *   - Help button opens popover with field/operator reference
 *   - Autocomplete dropdown appears as user types and accepts on Enter
 *   - Right-click on a packet table cell opens "Filter by this value" menu
 *     and clicking populates the filter input
 *   - Saved-filter dropdown lists default starter filters
 *
 * Usage: BASE_URL=http://localhost:39966 node test-filter-ux-e2e.js
 */
'use strict';
const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:39966';

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
  page.setDefaultTimeout(8000);
  page.on('pageerror', (e) => console.error('[pageerror]', e.message));

  console.log(`\n=== #966 filter UX E2E against ${BASE} ===`);

  await step('navigate to /packets', async () => {
    await page.goto(BASE + '/#/packets', { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#packetFilterInput', { timeout: 8000 });
    await page.waitForFunction(() => !!document.querySelector('#filterUxBar'), { timeout: 8000 });
  });

  await step('PacketFilter metadata is exposed in window', async () => {
    const meta = await page.evaluate(() => ({
      fields: window.PacketFilter && Array.isArray(window.PacketFilter.FIELDS) && window.PacketFilter.FIELDS.length,
      ops: window.PacketFilter && Array.isArray(window.PacketFilter.OPERATORS) && window.PacketFilter.OPERATORS.length,
      types: window.PacketFilter && Array.isArray(window.PacketFilter.TYPE_VALUES) && window.PacketFilter.TYPE_VALUES.length,
      hasSuggest: typeof window.PacketFilter.suggest === 'function',
    }));
    assert(meta.fields >= 10, 'FIELDS not populated: ' + JSON.stringify(meta));
    assert(meta.ops >= 8, 'OPERATORS not populated');
    assert(meta.types >= 5, 'TYPE_VALUES not populated');
    assert(meta.hasSuggest, 'suggest() missing');
  });

  await step('Help button opens popover with field reference', async () => {
    await page.click('#filterHelpBtn');
    await page.waitForSelector('#filterHelpPopover', { timeout: 3000 });
    const txt = await page.textContent('#filterHelpPopover');
    assert(/Filter syntax/i.test(txt), 'header missing');
    assert(/payload\.name/.test(txt), 'fields table missing payload.name');
    assert(/contains/.test(txt), 'operators missing');
    assert(/ADVERT/.test(txt), 'examples missing');
    // Close it
    await page.click('#filterHelpPopover .fux-popover-close');
    await page.waitForFunction(() => !document.getElementById('filterHelpPopover'), { timeout: 3000 });
  });

  await step('Autocomplete dropdown appears on focus and filters by prefix', async () => {
    await page.click('#packetFilterInput');
    await page.fill('#packetFilterInput', '');
    await page.keyboard.type('pay');
    await page.waitForSelector('#filterAcDropdown .fux-ac-item', { timeout: 3000 });
    const items = await page.$$eval('#filterAcDropdown .fux-ac-item .fux-ac-val', els => els.map(e => e.textContent));
    assert(items.some(v => v.startsWith('payload')), 'no payload* in dropdown: ' + items.join(','));
  });

  await step('Autocomplete accepts on Enter and updates input', async () => {
    await page.fill('#packetFilterInput', '');
    await page.click('#packetFilterInput');
    await page.keyboard.type('typ');
    await page.waitForSelector('#filterAcDropdown .fux-ac-item.active', { timeout: 3000 });
    await page.keyboard.press('Enter');
    const val = await page.inputValue('#packetFilterInput');
    assert(/^type/.test(val), 'expected `type` after accept, got: ' + val);
  });

  await step('Saved-filter dropdown lists default starters', async () => {
    // Reset LS so defaults are unmodified
    await page.evaluate(() => { try { localStorage.removeItem('corescope_saved_filters_v1'); } catch (e) {} });
    await page.click('#filterSavedTrigger');
    await page.waitForSelector('#filterSavedMenu:not(.hidden)', { timeout: 3000 });
    const names = await page.$$eval('#filterSavedMenu .fux-saved-name', els => els.map(e => e.textContent));
    assert(names.length >= 5, 'expected ≥ 5 default filters, got: ' + names.length);
    assert(names.some(n => /Adverts only/i.test(n)), 'Adverts only missing: ' + names.join('|'));
    assert(names.some(n => /Strong signal/i.test(n)), 'Strong signal missing: ' + names.join('|'));
  });

  await step('Clicking a saved filter populates the input and applies it', async () => {
    // Click the "Adverts only" entry
    await page.evaluate(() => {
      const items = document.querySelectorAll('#filterSavedMenu .fux-saved-item');
      for (const it of items) { if (/Adverts only/i.test(it.textContent)) { it.click(); break; } }
    });
    await page.waitForFunction(() => /type\s*==\s*ADVERT/i.test(document.getElementById('packetFilterInput').value), { timeout: 3000 });
    const val = await page.inputValue('#packetFilterInput');
    assert(/type\s*==\s*ADVERT/i.test(val), 'expected Adverts expr, got: ' + val);
  });

  await step('Right-click on a type cell opens context menu and appends a clause', async () => {
    // Reset filter
    await page.fill('#packetFilterInput', '');
    await page.evaluate(() => document.getElementById('packetFilterInput').dispatchEvent(new Event('input', { bubbles: true })));
    // Wait for table to populate
    await page.waitForSelector('#pktBody tr td[data-filter-field="type"]', { timeout: 8000 });
    // Locate first type cell with a real value (not "—")
    const cell = await page.$('#pktBody tr td[data-filter-field="type"]');
    assert(cell, 'no type cell found');
    const cellValue = await cell.getAttribute('data-filter-value');
    // Skip if no value
    if (!cellValue || cellValue === '—' || cellValue === '') {
      console.log('    (skipped — no type cell with value)');
      return;
    }
    const box = await cell.boundingBox();
    await page.mouse.click(box.x + box.width / 2, box.y + box.height / 2, { button: 'right' });
    await page.waitForSelector('#filterContextMenu', { timeout: 3000 });
    // Click the first item (== filter)
    await page.click('#filterContextMenu .fux-ctx-item');
    await page.waitForFunction(() => {
      const v = document.getElementById('packetFilterInput').value;
      return /type\s*(==|!=|contains)\s*/.test(v);
    }, { timeout: 3000 });
    const v = await page.inputValue('#packetFilterInput');
    assert(/type\s*==\s*/.test(v), 'expected type clause appended, got: ' + v);
  });

  await step('Save current expression persists to localStorage', async () => {
    await page.fill('#packetFilterInput', 'snr > 7');
    await page.evaluate(() => document.getElementById('packetFilterInput').dispatchEvent(new Event('input', { bubbles: true })));
    await page.click('#filterSavedTrigger');
    await page.waitForSelector('#filterSavedMenu:not(.hidden)');
    // Stub prompt
    await page.evaluate(() => { window.prompt = () => 'E2E test filter'; });
    await page.click('#filterSaveCurrent');
    await page.waitForFunction(() => {
      const raw = localStorage.getItem('corescope_saved_filters_v1') || '';
      return /E2E test filter/.test(raw) && /snr > 7/.test(raw);
    }, { timeout: 3000 });
    // Cleanup
    await page.evaluate(() => localStorage.removeItem('corescope_saved_filters_v1'));
  });

  await browser.close();

  console.log(`\n=== Results: passed ${passed} failed ${failed} ===`);
  process.exit(failed > 0 ? 1 : 0);
})().catch(e => { console.error(e); process.exit(1); });

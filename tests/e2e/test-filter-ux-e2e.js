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
    // Widen time window so fixture rows render
    await page.evaluate(() => {
      const sel = document.getElementById('fTimeWindow');
      if (sel) {
        sel.value = '0';
        sel.dispatchEvent(new Event('change', { bubbles: true }));
      }
    });
    // Wait for the table to populate with cells that have a real value
    await page.waitForFunction(() => {
      const cells = document.querySelectorAll('#pktBody td[data-filter-field="type"]');
      for (const c of cells) {
        const v = c.getAttribute('data-filter-value');
        if (v && v !== '—' && v !== '') return true;
      }
      return false;
    }, { timeout: 8000 });
    // Dispatch contextmenu event programmatically (Playwright headless mouse
    // right-click does not reliably trigger 'contextmenu' DOM events).
    const result = await page.evaluate(() => {
      const cell = Array.from(document.querySelectorAll('#pktBody td[data-filter-field="type"]'))
        .find(c => {
          const v = c.getAttribute('data-filter-value');
          return v && v !== '—' && v !== '';
        });
      if (!cell) return { error: 'no type cell with value' };
      const rect = cell.getBoundingClientRect();
      const ev = new MouseEvent('contextmenu', {
        bubbles: true, cancelable: true, button: 2,
        clientX: rect.left + 5, clientY: rect.top + 5,
      });
      cell.dispatchEvent(ev);
      const menu = document.getElementById('filterContextMenu');
      if (!menu) return { error: 'context menu not opened' };
      const items = Array.from(menu.querySelectorAll('.fux-ctx-item')).map(i => i.textContent);
      // Click the first item (== filter)
      menu.querySelector('.fux-ctx-item').click();
      return { items, inputAfter: document.getElementById('packetFilterInput').value };
    });
    assert(!result.error, 'menu open failed: ' + (result.error || ''));
    assert(result.items.length === 3, 'expected 3 menu items, got: ' + result.items.length);
    assert(/type\s*==\s*/.test(result.inputAfter), 'expected type clause appended, got: ' + result.inputAfter);
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

  // Reuse the real three-observation transmission seeded by deploy.yml for
  // #1486. Missing fixture data must fail, never silently skip this regression.
  for (const routeKind of ['hash', 'id']) {
    await step('#2091: ' + routeKind + ' observation deep link survives filters and refresh', async () => {
      const response = await ctx.request.get(BASE + '/api/packets/fae0c9e6d357a814');
      assert(response.ok(), '#1486 grouped packet fixture is required');
      const detail = await response.json();
      assert(detail.packet && detail.observations && detail.observations.length > 1,
        'fixture must expose a packet with at least two observations');
      const observation = detail.observations[1];
      assert(String(observation.id) !== String(detail.observations[0].id), 'observation IDs must differ');

      const detailContext = await browser.newContext({ viewport: { width: 1600, height: 1000 } });
      try {
        const detailPage = await detailContext.newPage();
        const route = routeKind === 'hash' ? detail.packet.hash : 'id/' + detail.packet.id;
        await detailPage.goto(BASE + '/#/packets/' + route + '?obs=' + observation.id,
          { waitUntil: 'domcontentloaded' });

        async function assertSelection(stage) {
          await detailPage.waitForSelector('#pktRight .detail-obs-row.observation-current', { state: 'attached' });
          const selected = await detailPage.locator('#pktRight .observation-current').getAttribute('data-obs-id');
          assert(selected === String(observation.id), stage + ': selected observation changed to ' + selected);
          const hash = new URL(detailPage.url()).hash;
          const params = new URLSearchParams(hash.split('?')[1] || '');
          assert(params.get('obs') === String(observation.id), stage + ': obs missing from ' + hash);
          assert(params.getAll('obs').length === 1, stage + ': duplicated observation parameter');
          if (routeKind === 'hash') assert(!params.has('hash'), stage + ': hash duplicated in query');
        }

        await assertSelection('initial load');
        await detailPage.click('#typeTrigger');
        await detailPage.locator('#typeMenu input[data-type-id="' + detail.packet.payload_type + '"]').check();
        assert(await detailPage.evaluate(() => localStorage.getItem('meshcore-type-filter')) === String(detail.packet.payload_type),
          'type filter must apply the requested selection');
        await assertSelection('type filter');
        await detailPage.click('#observerTrigger');
        await detailPage.locator('#observerMenu input[data-obs-id=' + JSON.stringify(observation.observer_id) + ']').check();
        assert(new URLSearchParams(new URL(detailPage.url()).hash.split('?')[1]).get('observer') === String(observation.observer_id),
          'observer filter must update the URL to the requested observer');
        await assertSelection('observer filter');
        await detailPage.click('#observerTrigger');
        await detailPage.selectOption('#fTimeWindow', '60');
        assert(new URLSearchParams(new URL(detailPage.url()).hash.split('?')[1]).get('timeWindow') === '60',
          'time-window filter must update the URL to 60 minutes');
        await assertSelection('time-window filter');
        await detailPage.reload({ waitUntil: 'load' });
        await assertSelection('refresh');
        await detailPage.click('#clearFiltersBtn');
        await assertSelection('Clear filters');
        const params = new URLSearchParams(new URL(detailPage.url()).hash.split('?')[1]);
        assert(!params.has('observer') && !params.has('timeWindow'), 'Clear must still remove filters');
        await detailPage.reload({ waitUntil: 'load' });
        await assertSelection('refresh after Clear');
      } finally {
        await detailContext.close();
      }
    });
  }

  await browser.close();

  console.log(`\n=== Results: passed ${passed} failed ${failed} ===`);
  process.exit(failed > 0 ? 1 : 0);
})().catch(e => { console.error(e); process.exit(1); });

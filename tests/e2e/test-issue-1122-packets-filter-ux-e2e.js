/**
 * E2E (#1122 / #1124 / #667): Packets page filter UX and observer context.
 *
 * Asserts:
 *  1. Filter help opens as a centered modal with a visible backdrop AND modal
 *     content fully inside the viewport AND packet rows BELOW remain rendered
 *     (count > 0). The previous "no overlap" assertion was gameable — it
 *     passed when rows were `display:none`'d. We now assert the honest
 *     property: backdrop separation + bounded modal + table still populated.
 *  2. Help panel contains exactly ONE "Filter syntax" heading (not two).
 *  3. Path column row height stays bounded (< 60px) regardless of how many
 *     hops are in any rendered packet's path.
 *  4. Focus management: opening the help moves focus to the close button;
 *     closing returns focus to the trigger.
 *  5. Observer-source context stays visible and describes the packet table
 *     across filtering, grouping, refresh, and mobile layout.
 *
 * Usage: BASE_URL=http://localhost:13581 node test-issue-1122-packets-filter-ux-e2e.js
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

async function assertObserverContext(page) {
  const note = page.locator('#packetObserverContext');
  assert(await note.isVisible(), 'Packet counts must visibly explain their observer-report source');
  const text = await note.innerText();
  assert(/observer reports/i.test(text) && /received by this instance/i.test(text),
    'Context must identify the reports received by this instance');
  assert(/partial view/i.test(text) && /not (total network traffic|network-wide totals)/i.test(text)
    && /firmware counters/i.test(text), 'Context must distinguish partial coverage from network and firmware totals');
  const describedBy = await page.locator('#pktTable').getAttribute('aria-describedby');
  assert((describedBy || '').split(/\s+/).includes('packetObserverContext'),
    'The packet table must expose its source context to assistive technology');
  const box = await note.boundingBox();
  const viewport = page.viewportSize();
  assert(box && box.x >= 0 && box.x + box.width <= viewport.width + 1
    && box.y >= 0 && box.y + box.height <= viewport.height,
    'Observer context must be readable within the viewport');
}

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

  console.log(`\n=== #1122/#1124 packets filter UX E2E against ${BASE} ===`);

  await step('navigate to /packets and wait for table', async () => {
    await page.goto(BASE + '/#/packets', { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#packetFilterInput', { timeout: 8000 });
    await page.waitForFunction(() => !!document.querySelector('#filterUxBar'), { timeout: 8000 });
    // Widen the time window so fixture rows render
    await page.evaluate(() => {
      const sel = document.getElementById('fTimeWindow');
      if (sel) { sel.value = '0'; sel.dispatchEvent(new Event('change', { bubbles: true })); }
    });
    await page.waitForFunction(() => document.querySelectorAll('#pktBody tr').length > 0, { timeout: 8000 });
  });

  await step('Filter help: backdrop + modal in viewport + rows still rendered', async () => {
    await page.click('#filterHelpBtn');
    await page.waitForSelector('#filterHelpPopover', { timeout: 3000 });
    const result = await page.evaluate(() => {
      const help = document.getElementById('filterHelpPopover');
      const helpRect = help.getBoundingClientRect();
      const overlay = document.querySelector('.modal-overlay.fux-help-overlay');
      const overlayStyle = overlay ? getComputedStyle(overlay) : null;
      // Visible backdrop: overlay exists, dims (rgba alpha > 0), covers viewport.
      const backdropVisible = !!(overlay
        && overlayStyle
        && overlayStyle.display !== 'none'
        && overlayStyle.visibility !== 'hidden'
        && /rgba?\([^)]+,\s*0?\.\d+|rgba?\([^)]+,\s*\d+\)/.test(overlayStyle.backgroundColor || ''));
      const vw = window.innerWidth, vh = window.innerHeight;
      const modalFullyInViewport = (
        helpRect.top >= 0 && helpRect.left >= 0 &&
        helpRect.right <= vw + 0.5 && helpRect.bottom <= vh + 0.5 &&
        helpRect.width > 0 && helpRect.height > 0
      );
      // Count REAL packet rows (skip vscroll sentinel rows).
      const allRows = document.querySelectorAll('#pktBody tr');
      let renderedRows = 0;
      let renderedRowsWithLayout = 0;
      for (const row of allRows) {
        if (row.id === 'vscroll-top' || row.id === 'vscroll-bottom') continue;
        if (row.getAttribute('aria-hidden') === 'true') continue;
        renderedRows++;
        const r = row.getBoundingClientRect();
        // The `body.fux-help-open #pktBody tr { display: none }` hack would
        // collapse these to 0×0. Fail loudly if that ever returns.
        if (r.width > 0 && r.height > 0) renderedRowsWithLayout++;
      }
      return { backdropVisible, modalFullyInViewport, helpRect, renderedRows, renderedRowsWithLayout };
    });
    assert(result.backdropVisible, 'modal backdrop missing or transparent');
    assert(result.modalFullyInViewport, 'modal not fully in viewport: ' + JSON.stringify(result.helpRect));
    assert(result.renderedRows > 0, 'no packet rows rendered at all (fixture issue?)');
    assert(result.renderedRowsWithLayout > 0,
      'rows are display:none while modal is open — the hack is back. rendered=' +
      result.renderedRows + ' withLayout=' + result.renderedRowsWithLayout);
  });

  await step('Filter help contains exactly ONE "Filter syntax" heading', async () => {
    const count = await page.evaluate(() => {
      const help = document.getElementById('filterHelpPopover');
      if (!help) return -1;
      const text = help.textContent || '';
      const matches = text.match(/Filter syntax/g) || [];
      return matches.length;
    });
    assert(count === 1, 'Expected exactly 1 "Filter syntax" occurrence, got ' + count);
  });

  await step('Focus moves to close button on open', async () => {
    const ok = await page.evaluate(() => {
      const close = document.querySelector('#filterHelpPopover .fux-popover-close');
      return !!close && document.activeElement === close;
    });
    assert(ok, 'close button should be focused after modal opens');
  });

  await step('close filter help via close button restores focus to trigger', async () => {
    await page.click('#filterHelpPopover .fux-popover-close');
    await page.waitForFunction(() => !document.getElementById('filterHelpPopover'), { timeout: 3000 });
    const restored = await page.evaluate(() => {
      return document.activeElement && document.activeElement.id === 'filterHelpBtn';
    });
    assert(restored, 'focus should return to #filterHelpBtn after close');
  });

  await step('Path column row height stays bounded < 60px regardless of hop count', async () => {
    const result = await page.evaluate(() => {
      const cells = document.querySelectorAll('#pktBody td.col-path');
      let maxH = 0, maxHops = 0, offenders = [];
      for (const c of cells) {
        const r = c.getBoundingClientRect();
        const hops = c.querySelectorAll('.hop, .hop-named').length;
        if (r.height > maxH) maxH = r.height;
        if (hops > maxHops) maxHops = hops;
        if (r.height >= 60) offenders.push({ height: r.height, hops });
      }
      return { maxH, maxHops, offenders: offenders.slice(0, 5), totalCells: cells.length };
    });
    assert(result.totalCells > 0, 'No path cells rendered to inspect');
    assert(result.offenders.length === 0,
      'Path cells exceed 60px row height: ' + JSON.stringify(result.offenders) +
      ' (maxHops seen=' + result.maxHops + ', maxHeight=' + result.maxH + ')');
  });

  await step('#667: populated packet table explains its observer-report source', async () => {
    assert(await page.locator('#pktBody td.col-time').count() > 0, 'Expected real fixture packet rows');
    await assertObserverContext(page);
  });

  await step('#667: observer context survives an empty filter and clearing it', async () => {
    await page.fill('#packetFilterInput', 'snr > 1000000');
    await page.press('#packetFilterInput', 'Escape');
    await page.waitForFunction(() => document.getElementById('pktBody').textContent.includes('No packets found'));
    await assertObserverContext(page);
    await page.fill('#packetFilterInput', '');
    await page.press('#packetFilterInput', 'Escape');
    await page.waitForSelector('#pktBody td.col-time');
    await assertObserverContext(page);
  });

  await step('#667: observer context survives grouping and refresh with live updates enabled', async () => {
    await page.fill('#packetFilterInput', '');
    await page.press('#packetFilterInput', 'Escape');
    await page.waitForSelector('#pktBody td.col-time');
    const wasGrouped = await page.locator('#fGroup').evaluate(el => el.classList.contains('active'));
    await page.click('#fGroup');
    await page.waitForFunction(grouped => document.getElementById('fGroup').classList.contains('active') !== grouped, wasGrouped);
    await page.waitForSelector('#pktBody td.col-time');
    await assertObserverContext(page);
    assert(await page.locator('#pktPauseBtn').getAttribute('aria-label') === 'Pause live updates',
      'Expected live updates to remain enabled');
    const refreshed = page.waitForResponse(response => new URL(response.url()).pathname === '/api/packets' && response.ok());
    await page.click('[data-action="pkt-refresh"]');
    await (await refreshed).finished();
    await page.waitForSelector('#pktBody td.col-time');
    await assertObserverContext(page);
  });

  await step('#667: observer context remains visible in the mobile layout', async () => {
    const mobile = await browser.newPage({ viewport: { width: 390, height: 844 } });
    try {
      await mobile.goto(BASE + '/#/packets', { waitUntil: 'domcontentloaded' });
      await mobile.waitForSelector('#pktTable');
      await assertObserverContext(mobile);
    } finally {
      await mobile.close();
    }
  });

  await browser.close();

  console.log(`\n=== Results: passed ${passed} failed ${failed} ===`);
  process.exit(failed > 0 ? 1 : 0);
})().catch(e => { console.error(e); process.exit(1); });

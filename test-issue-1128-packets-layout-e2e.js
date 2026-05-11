/**
 * E2E (#1128): Packets page layout chaos.
 *
 * Asserts the user-visible properties broken by the 5 sub-bugs documented in
 * specs/packets-layout-audit.md:
 *
 *  1. Bug 4 (--surface undefined): Saved-filter dropdown background must be
 *     OPAQUE — we read its computed `background-color`, parse the alpha and
 *     fail if the alpha channel is below 0.99. Same check applies to the
 *     `+N` path-overflow popover (`.path-popover`).
 *  2. Bug 1 (path chip spill / no `+N`): every `.path-hops` host whose
 *     scrollWidth > clientWidth must have a `.path-overflow-pill` rendered
 *     after the hop-resolver mutation pass settles.
 *  3. Bug 2 (`+N` popover position + z-index): when opened, the popover's
 *     z-index must be ≤ 9000 (under modal stack) and its top edge must be
 *     within 8px of the pill's top OR bottom edge — i.e. anchored to the
 *     pill, not floating arbitrarily across the table.
 *  4. Bug 3 (filter-bar gap + multi-select trigger truncation): the
 *     `.filter-bar` row-gap must be ≥ 10px (controls are 34px tall, 6px gap
 *     allows visual overlap on wrap), and every `.multi-select-trigger` must
 *     have a CSS `max-width` ≤ 280px (clamp viewport-aware cap) so a long
 *     "TRACE,MULTIPART,..." label doesn't balloon the row.
 *
 * Usage: BASE_URL=http://localhost:13581 node test-issue-1128-packets-layout-e2e.js
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

// Parse "rgba(r,g,b,a)" / "rgb(r,g,b)" → alpha (1 if rgb).
function parseAlpha(s) {
  if (!s) return 0;
  if (s === 'transparent') return 0;
  var m = /^rgba?\(\s*(\d+)\s*,\s*(\d+)\s*,\s*(\d+)\s*(?:,\s*([\d.]+)\s*)?\)$/i.exec(s);
  if (!m) return 1; // assume opaque named color
  return m[4] === undefined ? 1 : parseFloat(m[4]);
}

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  const page = await ctx.newPage();
  page.setDefaultTimeout(8000);
  page.on('pageerror', (e) => console.error('[pageerror]', e.message));

  console.log(`\n=== #1128 packets layout E2E against ${BASE} ===`);

  await step('navigate to /packets and wait for table + rows', async () => {
    await page.goto(BASE + '/#/packets', { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#packetFilterInput', { timeout: 8000 });
    await page.waitForFunction(() => !!document.querySelector('#filterUxBar'), { timeout: 8000 });
    await page.evaluate(() => {
      const sel = document.getElementById('fTimeWindow');
      if (sel) { sel.value = '0'; sel.dispatchEvent(new Event('change', { bubbles: true })); }
    });
    await page.waitForFunction(
      () => Array.from(document.querySelectorAll('#pktBody tr'))
              .filter(r => r.id !== 'vscroll-top' && r.id !== 'vscroll-bottom').length > 0,
      { timeout: 8000 });
    // Allow hop-resolver async pass to settle so chips reflect resolved names.
    await page.waitForTimeout(400);
  });

  await step('Bug 4: Saved-filter dropdown background is OPAQUE (alpha ≥ 0.99)', async () => {
    // Open the saved menu
    await page.evaluate(() => {
      var btn = document.getElementById('filterSavedTrigger');
      if (btn) btn.click();
    });
    const result = await page.evaluate(() => {
      var menu = document.getElementById('filterSavedMenu');
      if (!menu) return { error: 'no #filterSavedMenu' };
      // un-hide if needed (some impls toggle .hidden)
      menu.classList.remove('hidden');
      var cs = getComputedStyle(menu);
      return { bg: cs.backgroundColor, display: cs.display };
    });
    assert(!result.error, result.error);
    var alpha = parseAlpha(result.bg);
    assert(alpha >= 0.99,
      'Saved menu background not opaque: alpha=' + alpha + ' bg=' + result.bg +
      ' (likely --surface undefined / Bug 4)');
    // close
    await page.keyboard.press('Escape').catch(() => {});
  });

  await step('Bug 1: every overflowing .path-hops has a .path-overflow-pill', async () => {
    const result = await page.evaluate(() => {
      var hosts = Array.from(document.querySelectorAll('#pktBody .path-hops'));
      var offenders = [];
      for (var i = 0; i < hosts.length; i++) {
        var h = hosts[i];
        // Treat "overflowing" as scrollWidth strictly greater than clientWidth
        // by more than 1 px to avoid sub-pixel rounding noise.
        if (h.scrollWidth - h.clientWidth > 1) {
          if (!h.querySelector('.path-overflow-pill')) {
            offenders.push({
              sw: h.scrollWidth, cw: h.clientWidth,
              chips: h.querySelectorAll('.hop, .hop-named').length,
            });
          }
        }
      }
      return { totalHosts: hosts.length, offenders: offenders.slice(0, 5) };
    });
    assert(result.totalHosts > 0, 'no .path-hops in fixture rows');
    assert(result.offenders.length === 0,
      'overflowing .path-hops without +N pill: ' + JSON.stringify(result.offenders));
  });

  await step('Bug 2: +N popover anchored to pill + z-index ≤ 9000', async () => {
    const found = await page.evaluate(() => {
      var pill = document.querySelector('#pktBody .path-overflow-pill');
      if (!pill) return { skip: true };
      pill.scrollIntoView({ block: 'center' });
      return { skip: false };
    });
    if (found.skip) {
      console.log('    (no +N pill present in fixture — skipping anchor check)');
      return;
    }
    // After scrollIntoView the virtual scroll may rebuild rows; wait then
    // capture the pill's rect from the *current* DOM, then click it.
    await page.waitForTimeout(250);
    const result = await page.evaluate(() => {
      var pill = document.querySelector('#pktBody .path-overflow-pill');
      if (!pill) return { error: 'pill vanished after scroll' };
      var br = pill.getBoundingClientRect();
      pill.click();
      var pop = document.querySelector('.path-popover');
      if (!pop) return { error: 'popover did not appear after pill click' };
      var pr = pop.getBoundingClientRect();
      var z = parseInt(getComputedStyle(pop).zIndex, 10) || 0;
      var anchoredBelow = Math.abs(pr.top - br.bottom) <= 8;
      var anchoredAbove = Math.abs(pr.bottom - br.top) <= 8;
      return { z, anchoredBelow, anchoredAbove,
               pr: { top: pr.top, bottom: pr.bottom },
               br: { top: br.top, bottom: br.bottom } };
    });
    assert(!result.error, result.error);
    assert(result.z <= 9000, '+N popover z-index too high (over modal stack): ' + result.z);
    assert(result.anchoredBelow || result.anchoredAbove,
      '+N popover not anchored to pill: pop=' + JSON.stringify(result.pr) +
      ' pill=' + JSON.stringify(result.br));
  });

  await step('Bug 3: .filter-bar row-gap ≥ 10px AND .multi-select-trigger has bounded max-width', async () => {
    const result = await page.evaluate(() => {
      var bar = document.querySelector('.filter-bar');
      if (!bar) return { error: 'no .filter-bar' };
      var cs = getComputedStyle(bar);
      var rg = parseFloat(cs.rowGap || cs.gap || '0');
      var triggers = Array.from(document.querySelectorAll('.multi-select-trigger'));
      var unboundedTrigger = null;
      for (var i = 0; i < triggers.length; i++) {
        var mw = getComputedStyle(triggers[i]).maxWidth;
        // "none" or empty == unbounded; numeric px > 280 == too loose
        if (mw === 'none' || mw === '' ) { unboundedTrigger = { idx: i, mw }; break; }
        var px = parseFloat(mw);
        if (!isFinite(px) || px > 280) { unboundedTrigger = { idx: i, mw }; break; }
      }
      return { rowGap: rg, triggerCount: triggers.length, unboundedTrigger };
    });
    assert(!result.error, result.error);
    assert(result.rowGap >= 10,
      '.filter-bar row-gap too small (causes wrap overlap with 34px controls): ' + result.rowGap);
    assert(result.triggerCount > 0, 'no .multi-select-trigger present (filter UX missing?)');
    assert(!result.unboundedTrigger,
      '.multi-select-trigger lacks bounded max-width: ' + JSON.stringify(result.unboundedTrigger));
  });

  await browser.close();

  console.log(`\n=== Results: passed ${passed} failed ${failed} ===`);
  process.exit(failed > 0 ? 1 : 0);
})().catch(e => { console.error(e); process.exit(1); });

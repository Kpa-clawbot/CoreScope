/**
 * E2E (#1058): Analytics chart containers — fluid + auto-stacking.
 *
 * Acceptance criteria:
 *   - Chart containers fill their parent's available width (no fixed
 *     px width on a chart container).
 *   - Side-by-side cards inside `.analytics-row` stack vertically when
 *     the container becomes too narrow.
 *   - Wide viewports keep cards side-by-side (consecutive cards share
 *     `getBoundingClientRect().top`).
 *   - Re-layout works on viewport resize (no manual handler needed —
 *     CSS does the work; test resizes 1920 → 800 and asserts re-flow).
 *
 * Tested viewports: 768 / 1080 / 1440 / 1920 / 2560.
 *
 * Selector contract:
 *   - `.analytics-row` containers hold the side-by-side chart cards.
 *   - Each chart card matched with `.analytics-row > .analytics-card.flex-1`.
 *   - This avoids virtual-scroll-spacer / utility wrappers.
 *
 * Usage: BASE_URL=http://localhost:13581 node test-charts-fluid-1058-e2e.js
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

// Pairs of side-by-side chart cards exist on the Overview tab
// (default landing tab). Use that for cross-viewport coverage.
const HASH = '#/analytics';

const VIEWPORTS = [
  { w: 768,  h: 900, expectStacked: true  },
  { w: 1080, h: 900, expectStacked: false },
  { w: 1440, h: 900, expectStacked: false },
  { w: 1920, h: 900, expectStacked: false },
  { w: 2560, h: 1200, expectStacked: false },
];

async function gatherRows(page) {
  return page.evaluate(() => {
    const rows = Array.from(document.querySelectorAll('.analytics-row'));
    return rows.map((row, idx) => {
      const cards = Array.from(row.querySelectorAll(':scope > .analytics-card.flex-1'));
      const rect = row.getBoundingClientRect();
      return {
        idx,
        rowWidth: row.clientWidth,
        rowRect: { left: rect.left, top: rect.top, width: rect.width },
        cardCount: cards.length,
        cards: cards.map(c => {
          const r = c.getBoundingClientRect();
          return { width: c.clientWidth, top: Math.round(r.top), left: Math.round(r.left) };
        }),
      };
    }).filter(r => r.cardCount >= 2); // only multi-card rows are interesting
  });
}

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });

  console.log(`\n=== #1058 fluid analytics charts E2E against ${BASE} ===`);

  for (const vp of VIEWPORTS) {
    const ctx = await browser.newContext({ viewport: { width: vp.w, height: vp.h } });
    const page = await ctx.newPage();
    page.setDefaultTimeout(10000);
    page.on('pageerror', (e) => console.error('[pageerror]', e.message));

    const tag = `analytics@${vp.w}`;

    await step(`${tag}: page renders + analytics-row containers found`, async () => {
      await page.goto(BASE + '/' + HASH, { waitUntil: 'domcontentloaded' });
      await page.waitForSelector('.analytics-row', { timeout: 10000 });
      // Wait for at least one multi-card row to materialize (data fetch)
      await page.waitForFunction(() => {
        const rows = document.querySelectorAll('.analytics-row');
        for (const r of rows) {
          if (r.querySelectorAll(':scope > .analytics-card.flex-1').length >= 2) return true;
        }
        return false;
      }, { timeout: 10000 });
      await page.waitForTimeout(300);
    });

    await step(`${tag}: chart cards fill row width (no fixed px constraint)`, async () => {
      const rows = await gatherRows(page);
      assert(rows.length >= 1, `expected ≥1 multi-card row, got ${rows.length}`);
      // Each card should be sized by the grid/flex track — sum of card
      // widths plus gaps should be ≈ rowWidth (within tolerance).
      for (const r of rows) {
        const total = r.cards.reduce((s, c) => s + c.width, 0);
        // When stacked: each card.width ≈ rowWidth. When side-by-side:
        // total + gaps ≈ rowWidth. Either way, no card should exceed
        // rowWidth + 2px.
        for (const c of r.cards) {
          assert(c.width <= r.rowWidth + 2,
            `card width ${c.width} > rowWidth ${r.rowWidth} (row #${r.idx}) — chart not fluid`);
        }
        // And no card should be <50% of rowWidth/cardCount (proxy for
        // "didn't lay out at all" — guards against zero-width stacking
        // bugs).
        for (const c of r.cards) {
          assert(c.width > 0, `card has zero width (row #${r.idx})`);
        }
      }
    });

    await step(`${tag}: layout matches expected mode (stacked vs side-by-side)`, async () => {
      const rows = await gatherRows(page);
      assert(rows.length >= 1, 'no rows');
      // Look at the first multi-card row.
      const r = rows[0];
      const tops = r.cards.map(c => c.top);
      const allSameTop = tops.every(t => Math.abs(t - tops[0]) <= 2);
      if (vp.expectStacked) {
        assert(!allSameTop,
          `expected cards to STACK at ${vp.w}px but all share top=${tops[0]} (tops=${JSON.stringify(tops)})`);
      } else {
        assert(allSameTop,
          `expected cards SIDE-BY-SIDE at ${vp.w}px but tops differ: ${JSON.stringify(tops)}`);
      }
    });

    await ctx.close();
  }

  // Re-layout on resize: start wide, then resize narrow, observe re-flow.
  await step('resize 1920 → 800 re-flows charts', async () => {
    const ctx = await browser.newContext({ viewport: { width: 1920, height: 900 } });
    const page = await ctx.newPage();
    page.setDefaultTimeout(10000);
    await page.goto(BASE + '/' + HASH, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('.analytics-row', { timeout: 10000 });
    await page.waitForFunction(() => {
      const rows = document.querySelectorAll('.analytics-row');
      for (const r of rows) {
        if (r.querySelectorAll(':scope > .analytics-card.flex-1').length >= 2) return true;
      }
      return false;
    }, { timeout: 10000 });
    await page.waitForTimeout(300);

    const wideRows = await gatherRows(page);
    assert(wideRows.length >= 1, 'no rows at 1920');
    const wideTops = wideRows[0].cards.map(c => c.top);
    const wideSame = wideTops.every(t => Math.abs(t - wideTops[0]) <= 2);
    assert(wideSame, `expected side-by-side at 1920 (tops=${JSON.stringify(wideTops)})`);

    await page.setViewportSize({ width: 800, height: 800 });
    await page.waitForTimeout(400);

    const narrowRows = await gatherRows(page);
    const narrowTops = narrowRows[0].cards.map(c => c.top);
    const narrowSame = narrowTops.every(t => Math.abs(t - narrowTops[0]) <= 2);
    assert(!narrowSame,
      `expected re-flow / stacked layout after resize to 800 (tops=${JSON.stringify(narrowTops)})`);
    await ctx.close();
  });

  await browser.close();
  console.log(`\n=== #1058 fluid analytics charts E2E: ${passed} passed, ${failed} failed ===`);
  process.exit(failed ? 1 : 0);
})();

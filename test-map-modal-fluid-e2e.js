/**
 * E2E (#1059): Map controls + modal fluid/safe-max-height behavior.
 *
 * Asserts:
 *   - Modal at 768px tall viewport: contents scrollable, close button reachable
 *     (modal box <= 90vh, close button stays in viewport when scrolled inside).
 *   - Map controls visible at 768px wide (right edge inside viewport).
 *   - No horizontal scroll on map page at any tested viewport
 *     (1024x768, 1440x900, 1920x1080, 2560x1440).
 *
 * Usage: BASE_URL=http://localhost:13581 node test-map-modal-fluid-e2e.js
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

  console.log(`\n=== #1059 map+modal fluid E2E against ${BASE} ===`);

  // --- Map page: no horizontal scroll across viewports ---
  const viewports = [
    { w: 1024, h: 768 },
    { w: 1440, h: 900 },
    { w: 1920, h: 1080 },
    { w: 2560, h: 1440 },
  ];
  for (const v of viewports) {
    await step(`no horizontal scroll on /#/map at ${v.w}x${v.h}`, async () => {
      await page.setViewportSize({ width: v.w, height: v.h });
      await page.goto(BASE + '/#/map', { waitUntil: 'domcontentloaded' });
      await page.waitForSelector('#leaflet-map', { timeout: 8000 });
      await page.waitForTimeout(300);
      const overflow = await page.evaluate(() => ({
        sw: document.documentElement.scrollWidth,
        cw: document.documentElement.clientWidth,
      }));
      assert(overflow.sw <= overflow.cw + 1,
        `horizontal scroll: scrollWidth=${overflow.sw} clientWidth=${overflow.cw}`);
    });
  }

  // --- Map controls visible (right edge inside viewport) at 768px wide ---
  await step('map controls right edge inside viewport at 768x900', async () => {
    await page.setViewportSize({ width: 768, height: 900 });
    await page.goto(BASE + '/#/map', { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('.map-controls', { timeout: 8000 });
    const box = await page.evaluate(() => {
      const el = document.querySelector('.map-controls');
      if (!el) return null;
      const r = el.getBoundingClientRect();
      return { left: r.left, right: r.right, width: r.width, vw: window.innerWidth };
    });
    assert(box, '.map-controls not found');
    assert(box.right <= box.vw + 1,
      `controls overflow viewport: right=${box.right} vw=${box.vw}`);
    assert(box.left >= 0,
      `controls clipped on left: left=${box.left}`);
    assert(box.width > 0, 'controls have zero width');
  });

  // --- Modal at 768px tall viewport: max-height <= 90vh, close button reachable ---
  await step('BYOP modal fits in 768px-tall viewport (<=90vh, sticky close)', async () => {
    await page.setViewportSize({ width: 1024, height: 768 });
    await page.goto(BASE + '/#/packets', { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('[data-action="pkt-byop"]', { timeout: 8000 });
    await page.click('[data-action="pkt-byop"]');
    await page.waitForSelector('.byop-modal', { timeout: 3000 });
    const m = await page.evaluate(() => {
      const modal = document.querySelector('.byop-modal');
      const close = document.querySelector('.byop-modal .byop-x');
      if (!modal || !close) return null;
      const mr = modal.getBoundingClientRect();
      const cr = close.getBoundingClientRect();
      const cs = getComputedStyle(modal);
      const ccs = getComputedStyle(close);
      return {
        modalH: mr.height, vh: window.innerHeight,
        modalTop: mr.top, modalBottom: mr.bottom,
        closeTop: cr.top, closeBottom: cr.bottom,
        overflowY: cs.overflowY,
        closePos: ccs.position,
      };
    });
    assert(m, 'byop modal/close not found');
    // Modal box must not exceed 90vh of the viewport.
    assert(m.modalH <= m.vh * 0.90 + 2,
      `modal height ${m.modalH} > 90vh of ${m.vh} (=${m.vh * 0.90})`);
    // Close button must be inside the viewport.
    assert(m.closeBottom <= m.vh + 1 && m.closeTop >= 0,
      `close button out of viewport: top=${m.closeTop} bottom=${m.closeBottom} vh=${m.vh}`);
    // Modal content must be scrollable internally (overflow-y auto/scroll).
    assert(m.overflowY === 'auto' || m.overflowY === 'scroll',
      `modal overflow-y must be auto/scroll, got ${m.overflowY}`);
    // Close button must be sticky so it stays reachable when content scrolls.
    assert(m.closePos === 'sticky',
      `close button must be position:sticky for reachability, got ${m.closePos}`);
  });

  await browser.close();
  console.log(`\n=== Results: ${passed} passed, ${failed} failed ===`);
  process.exit(failed === 0 ? 0 : 1);
})();

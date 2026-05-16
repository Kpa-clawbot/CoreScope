/**
 * E2E (#1236): Map page mobile layout.
 *
 * At 375x800 viewport the /#/map page must:
 *  - Collapsed map controls: leaflet map canvas width must equal the viewport
 *    width (within 1px tolerance). No silent gutter on the right.
 *  - Expanded map controls: the panel must either fit within viewport OR
 *    have a sticky element at the top of the scroll container AND
 *    `overflow-y: auto` so the scroll affordance is real.
 *
 * Desktop guard (≥768px): map controls panel layout must remain absolute
 * (position: absolute), not stretched full width.
 *
 * Run: BASE_URL=http://localhost:13581 node test-issue-1236-map-mobile-e2e.js
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

async function run() {
  const launchOpts = { args: ['--no-sandbox'] };
  if (process.env.CHROMIUM_PATH) launchOpts.executablePath = process.env.CHROMIUM_PATH;
  const browser = await chromium.launch(launchOpts);

  // === Mobile: 375x800 ===
  const ctx = await browser.newContext({ viewport: { width: 375, height: 800 } });
  const page = await ctx.newPage();

  await page.goto(BASE + '/#/map', { waitUntil: 'load', timeout: 60000 });
  await page.waitForSelector('#leaflet-map', { timeout: 10000 });
  await page.waitForSelector('#mapControls', { state: 'attached', timeout: 10000 });
  await page.waitForTimeout(500);

  await step('mobile collapsed: leaflet-map width fills viewport (no right gutter)', async () => {
    // Ensure controls panel is collapsed (default on mobile per map.js)
    const data = await page.evaluate(() => {
      const panel = document.getElementById('mapControls');
      const btn = document.getElementById('mapControlsToggle');
      if (panel && !panel.classList.contains('collapsed')) btn && btn.click();
      const lm = document.getElementById('leaflet-map');
      return {
        mapW: lm ? Math.round(lm.getBoundingClientRect().width) : null,
        mapLeft: lm ? Math.round(lm.getBoundingClientRect().left) : null,
        vw: window.innerWidth,
      };
    });
    assert(data.mapW !== null, 'leaflet-map not found');
    // Map must start at left edge and span to right edge (allow 1px rounding).
    assert(data.mapLeft <= 1,
      'leaflet-map must start at viewport left, got left=' + data.mapLeft);
    assert(data.mapW >= data.vw - 1,
      'leaflet-map width must equal viewport (' + data.vw + 'px), got ' + data.mapW + 'px');
  });

  await step('mobile expanded: panel has sticky header AND overflow-y auto', async () => {
    const data = await page.evaluate(() => {
      const panel = document.getElementById('mapControls');
      const btn = document.getElementById('mapControlsToggle');
      if (panel.classList.contains('collapsed')) btn && btn.click();
      const cs = getComputedStyle(panel);
      const h3 = panel.querySelector('h3');
      const hcs = h3 ? getComputedStyle(h3) : null;
      return {
        overflowY: cs.overflowY,
        h3Position: hcs ? hcs.position : null,
        scrollGutter: cs.scrollbarGutter || '',
        scrollH: panel.scrollHeight,
        clientH: panel.clientHeight,
        isScrollable: panel.scrollHeight > panel.clientHeight + 1,
      };
    });
    // Require explicit sticky header + scrollable overflow regardless of
    // whether content currently overflows (so future content additions are
    // covered too).
    assert(data.h3Position === 'sticky',
      'panel h3 must be position:sticky (scroll affordance), got ' + data.h3Position);
    assert(data.overflowY === 'auto' || data.overflowY === 'scroll',
      'panel overflow-y must be auto/scroll, got ' + data.overflowY);
  });

  await ctx.close();

  // === Desktop: 1280x800 — guard against regression ===
  const ctx2 = await browser.newContext({ viewport: { width: 1280, height: 800 } });
  const p2 = await ctx2.newPage();
  await p2.goto(BASE + '/#/map', { waitUntil: 'load', timeout: 60000 });
  await p2.waitForSelector('#mapControls', { state: 'attached', timeout: 10000 });
  await p2.waitForTimeout(300);

  await step('desktop (1280px): map controls panel position is absolute', async () => {
    const data = await p2.evaluate(() => {
      const panel = document.getElementById('mapControls');
      const cs = getComputedStyle(panel);
      const rect = panel.getBoundingClientRect();
      return {
        position: cs.position,
        width: Math.round(rect.width),
        vw: window.innerWidth,
      };
    });
    assert(data.position === 'absolute',
      'desktop panel must be position:absolute, got ' + data.position);
    // Should be modest width (not full viewport)
    assert(data.width < data.vw * 0.5,
      'desktop panel must be <50% viewport width, got ' + data.width + '/' + data.vw);
  });

  await browser.close();

  console.log('\n' + passed + '/' + (passed + failed) + ' tests passed' +
              (failed ? ', ' + failed + ' failed' : ''));
  process.exit(failed > 0 ? 1 : 0);
}

run().catch(err => { console.error('Fatal:', err); process.exit(1); });

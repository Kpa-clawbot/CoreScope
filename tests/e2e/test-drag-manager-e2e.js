/**
 * E2E (#1297 B4): drag-manager.js — Real Playwright pointer drag on a live panel
 *
 * Exercises public/drag-manager.js via the /live route which registers
 * #liveFeed, #liveLegend, #liveNodeDetail panels. We use Playwright's pointer
 * API (mouse.move/down/up) to simulate a drag, and assert:
 *  - data-position attribute is removed (free-form mode engaged)
 *  - data-dragged='true' is set
 *  - panel-drag-{id} localStorage entry contains xPct/yPct
 *  - Restoring on reload re-applies the position
 *
 * Usage: BASE_URL=http://localhost:13581 node test-drag-manager-e2e.js
 */
'use strict';
const { chromium } = require('playwright');
const BASE = process.env.BASE_URL || 'http://localhost:3000';

let passed = 0, failed = 0;
async function step(name, fn) {
  try { await fn(); passed++; console.log('  \u2713 ' + name); }
  catch (e) { failed++; console.error('  \u2717 ' + name + ': ' + e.message); }
}
function assert(c, m) { if (!c) throw new Error(m || 'assertion failed'); }

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });
  // Desktop viewport: drag is disabled below 768px or on coarse pointer
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  const page = await ctx.newPage();
  page.setDefaultTimeout(10000);
  page.on('pageerror', (e) => console.error('[pageerror]', e.message));

  console.log(`\n=== #1297 B4 drag-manager E2E against ${BASE} ===`);

  await step('navigate to /live', async () => {
    await page.goto(BASE + '/#/live', { waitUntil: 'domcontentloaded' });
    // Clear any prior drag positions
    await page.evaluate(() => {
      ['liveFeed', 'liveLegend', 'liveNodeDetail'].forEach(id => {
        try { localStorage.removeItem('panel-drag-' + id); } catch (_) {}
      });
    });
    await page.reload({ waitUntil: 'load' });
    await page.waitForSelector('#liveFeed .panel-header', { timeout: 8000 });
  });

  await step('DragManager is initialized', async () => {
    const hasDM = await page.evaluate(() => typeof window.DragManager === 'function');
    assert(hasDM, 'window.DragManager should be defined');
  });

  await step('pointer drag on #liveFeed transitions panel into free-form mode', async () => {
    // Locate the panel-header rect
    const headerBox = await page.$eval('#liveFeed .panel-header', el => {
      const r = el.getBoundingClientRect();
      return { x: r.left + r.width / 2, y: r.top + r.height / 2 };
    });

    // Real mouse drag — outside the dead zone (5px)
    await page.mouse.move(headerBox.x, headerBox.y);
    await page.mouse.down();
    await page.mouse.move(headerBox.x + 120, headerBox.y + 80, { steps: 8 });
    await page.mouse.up();
    await page.waitForTimeout(200);

    const state = await page.$eval('#liveFeed', el => ({
      dragged: el.dataset.dragged,
      position: el.getAttribute('data-position'),
      left: el.style.left,
      top: el.style.top,
    }));
    assert(state.dragged === 'true', 'data-dragged should be "true", got: ' + JSON.stringify(state));
    assert(state.position === null, 'data-position should be removed, got: ' + state.position);
    assert(state.left && state.left.endsWith('px'), 'inline left should be px value, got: ' + state.left);
  });

  await step('panel-drag-liveFeed localStorage entry written with xPct/yPct', async () => {
    const raw = await page.evaluate(() => localStorage.getItem('panel-drag-liveFeed'));
    assert(raw, 'panel-drag-liveFeed should be written');
    const parsed = JSON.parse(raw);
    assert(typeof parsed.xPct === 'number' && parsed.xPct >= 0 && parsed.xPct <= 1,
      'xPct should be in [0,1], got: ' + raw);
    assert(typeof parsed.yPct === 'number' && parsed.yPct >= 0 && parsed.yPct <= 1,
      'yPct should be in [0,1], got: ' + raw);
  });

  await step('reload restores panel to dragged position via restorePositions()', async () => {
    const savedRaw = await page.evaluate(() => localStorage.getItem('panel-drag-liveFeed'));
    const saved = JSON.parse(savedRaw);

    await page.reload({ waitUntil: 'load' });
    await page.waitForSelector('#liveFeed .panel-header', { timeout: 8000 });
    await page.waitForTimeout(500); // restorePositions runs after init

    const restored = await page.$eval('#liveFeed', el => ({
      dragged: el.dataset.dragged,
      left: el.style.left,
      top: el.style.top,
    }));
    assert(restored.dragged === 'true', 'data-dragged should persist as "true" after reload, got: ' + JSON.stringify(restored));

    // Expected px from saved xPct/yPct × viewport
    const expectedLeft = Math.round(saved.xPct * 1280);
    const actualLeft = parseInt(restored.left, 10);
    assert(Math.abs(actualLeft - expectedLeft) <= 5,
      'restored left ~' + expectedLeft + 'px, got ' + restored.left);
  });

  await step('click without movement (within dead zone) does NOT engage drag', async () => {
    // Use a fresh panel to avoid prior state
    await page.evaluate(() => localStorage.removeItem('panel-drag-liveLegend'));
    await page.reload({ waitUntil: 'load' });
    await page.waitForSelector('#liveLegend .panel-header', { timeout: 8000 });

    const headerBox = await page.$eval('#liveLegend .panel-header', el => {
      const r = el.getBoundingClientRect();
      return { x: r.left + r.width / 2, y: r.top + r.height / 2 };
    });
    await page.mouse.move(headerBox.x, headerBox.y);
    await page.mouse.down();
    await page.mouse.move(headerBox.x + 2, headerBox.y + 2); // inside DEAD_ZONE (5px)
    await page.mouse.up();
    await page.waitForTimeout(200);

    const raw = await page.evaluate(() => localStorage.getItem('panel-drag-liveLegend'));
    assert(raw === null, 'dead-zone click should NOT persist a position, got: ' + raw);
    const dragged = await page.$eval('#liveLegend', el => el.dataset.dragged);
    assert(dragged !== 'true', 'data-dragged should not be set for dead-zone click, got: ' + dragged);
  });

  await step('cleanup', async () => {
    await page.evaluate(() => {
      ['liveFeed', 'liveLegend', 'liveNodeDetail'].forEach(id => {
        try { localStorage.removeItem('panel-drag-' + id); } catch (_) {}
      });
    });
  });

  await browser.close();
  console.log('\n' + passed + '/' + (passed + failed) + ' tests passed');
  process.exit(failed > 0 ? 1 : 0);
})();

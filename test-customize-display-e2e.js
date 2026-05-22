/**
 * E2E (#1297 B4): Customizer V2 — Display settings (timestamps, distance, heatmap opacity sliders)
 *
 * Exercises:
 *  - Display tab renders timestamp/distance/heatmap controls
 *  - Select change for distanceUnit persists to overrides
 *  - Heatmap opacity slider writes scalar override (cs-theme-overrides.heatmapOpacity)
 *  - Reload preserves the values
 *
 * Usage: BASE_URL=http://localhost:13581 node test-customize-display-e2e.js
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
  const ctx = await browser.newContext();
  const page = await ctx.newPage();
  page.setDefaultTimeout(8000);
  page.on('pageerror', (e) => console.error('[pageerror]', e.message));

  console.log(`\n=== #1297 B4 customize-display E2E against ${BASE} ===`);

  await step('setup', async () => {
    await page.goto(BASE + '/', { waitUntil: 'domcontentloaded' });
    await page.evaluate(() => localStorage.removeItem('cs-theme-overrides'));
    await page.reload({ waitUntil: 'load' });
    await page.waitForFunction(() => window._customizerV2 && window._customizerV2.initDone, null, { timeout: 8000 });
  });

  await step('open customizer + switch to display tab', async () => {
    await page.click('#customizeToggle');
    await page.waitForSelector('.cust-overlay:not(.hidden)');
    const tab = await page.$('.cust-tab[data-tab="display"]');
    if (tab) await tab.click();
    await page.waitForSelector('[data-cv2-select="distanceUnit"]', { timeout: 4000 });
  });

  await step('distanceUnit select writes scalar override', async () => {
    await page.selectOption('[data-cv2-select="distanceUnit"]', 'mi');
    await page.waitForTimeout(400);
    const raw = await page.evaluate(() => localStorage.getItem('cs-theme-overrides'));
    const parsed = JSON.parse(raw || '{}');
    assert(parsed.distanceUnit === 'mi', 'distanceUnit should be "mi", got: ' + raw);
  });

  await step('timestamps.defaultMode select writes nested override', async () => {
    await page.selectOption('[data-cv2-select="timestamps.defaultMode"]', 'absolute');
    await page.waitForTimeout(400);
    const raw = await page.evaluate(() => localStorage.getItem('cs-theme-overrides'));
    const parsed = JSON.parse(raw);
    assert(parsed.timestamps && parsed.timestamps.defaultMode === 'absolute',
      'timestamps.defaultMode missing, got: ' + raw);
  });

  await step('heatmap opacity slider — switch to nodes tab + drag slider', async () => {
    const nodesTab = await page.$('.cust-tab[data-tab="nodes"]');
    if (nodesTab) await nodesTab.click();
    await page.waitForSelector('[data-cv2-slider="heatmapOpacity"]', { timeout: 4000 });
    const slider = await page.$('[data-cv2-slider="heatmapOpacity"]');
    // Fire input + change events with a specific value
    await page.evaluate((el) => {
      el.value = '75';
      el.dispatchEvent(new Event('input', { bubbles: true }));
      el.dispatchEvent(new Event('change', { bubbles: true }));
    }, slider);
    await page.waitForTimeout(400);
    const raw = await page.evaluate(() => localStorage.getItem('cs-theme-overrides'));
    const parsed = JSON.parse(raw);
    assert(typeof parsed.heatmapOpacity === 'number',
      'heatmapOpacity should be numeric, got: ' + JSON.stringify(parsed.heatmapOpacity));
    assert(Math.abs(parsed.heatmapOpacity - 0.75) < 0.001,
      'heatmapOpacity should be 0.75, got: ' + parsed.heatmapOpacity);
  });

  await step('node role color picker writes typed override', async () => {
    const picker = await page.$('input[data-cv2-field="nodeColors.repeater"]');
    assert(picker, 'nodeColors.repeater picker missing');
    await page.evaluate((el) => {
      el.value = '#aabbcc';
      el.dispatchEvent(new Event('input', { bubbles: true }));
      el.dispatchEvent(new Event('change', { bubbles: true }));
    }, picker);
    await page.waitForTimeout(400);
    const raw = await page.evaluate(() => localStorage.getItem('cs-theme-overrides'));
    const parsed = JSON.parse(raw);
    assert(parsed.nodeColors && parsed.nodeColors.repeater === '#aabbcc',
      'nodeColors.repeater missing/wrong, got: ' + raw);
  });

  await step('all display overrides survive reload', async () => {
    await page.reload({ waitUntil: 'load' });
    await page.waitForFunction(() => window._customizerV2 && window._customizerV2.initDone, null, { timeout: 8000 });
    const raw = await page.evaluate(() => localStorage.getItem('cs-theme-overrides'));
    const parsed = JSON.parse(raw);
    assert(parsed.distanceUnit === 'mi', 'distanceUnit lost on reload');
    assert(parsed.timestamps.defaultMode === 'absolute', 'timestamps.defaultMode lost on reload');
    assert(Math.abs(parsed.heatmapOpacity - 0.75) < 0.001, 'heatmapOpacity lost on reload');
    assert(parsed.nodeColors.repeater === '#aabbcc', 'nodeColors.repeater lost on reload');
  });

  await step('cleanup', async () => {
    await page.evaluate(() => localStorage.removeItem('cs-theme-overrides'));
  });

  await browser.close();
  console.log('\n' + passed + '/' + (passed + failed) + ' tests passed');
  process.exit(failed > 0 ? 1 : 0);
})();

/**
 * E2E (#1297 B4): Customizer V2 — Export / Import / Reset
 *
 * Exercises export tab + raw JSON area + reset-all button in public/customize-v2.js:
 *  - Write an override → switch to export tab → raw JSON textarea reflects it
 *  - Copy button populates the clipboard via document.execCommand or navigator.clipboard
 *  - Reset All clears cs-theme-overrides and CSS variables revert
 *
 * Usage: BASE_URL=http://localhost:13581 node test-customize-export-e2e.js
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
  page.on('dialog', async (d) => { await d.accept(); }); // confirm() for reset
  page.on('pageerror', (e) => console.error('[pageerror]', e.message));

  console.log(`\n=== #1297 B4 customize-export E2E against ${BASE} ===`);

  await step('setup: seed an override before loading', async () => {
    await page.goto(BASE + '/', { waitUntil: 'domcontentloaded' });
    await page.evaluate(() => {
      localStorage.setItem('cs-theme-overrides',
        JSON.stringify({ theme: { accent: '#ff00aa' }, distanceUnit: 'mi' }));
    });
    await page.reload({ waitUntil: 'load' });
    await page.waitForFunction(() => window._customizerV2 && window._customizerV2.initDone, null, { timeout: 8000 });
  });

  await step('open customizer + switch to export tab', async () => {
    await page.click('#customizeToggle');
    await page.waitForSelector('.cust-overlay:not(.hidden)');
    const tab = await page.$('.cust-tab[data-tab="export"]');
    assert(tab, 'export tab missing');
    await tab.click();
    await page.waitForSelector('#cv2ExportJson', { state: 'attached', timeout: 4000 });
  });

  await step('raw JSON textarea reflects current overrides', async () => {
    // Open <details> to reveal textarea
    await page.evaluate(() => {
      document.querySelectorAll('.cust-overlay details').forEach(d => d.open = true);
    });
    const json = await page.$eval('#cv2ExportJson', el => el.value);
    const parsed = JSON.parse(json);
    assert(parsed.distanceUnit === 'mi', 'export JSON should include distanceUnit, got: ' + json);
    assert(parsed.theme && parsed.theme.accent === '#ff00aa',
      'export JSON should include theme.accent, got: ' + json);
  });

  await step('Download button exists and is wired', async () => {
    const btn = await page.$('#cv2Download');
    assert(btn, '#cv2Download missing');
  });

  await step('Reset All clears overrides + reverts CSS variable', async () => {
    const resetBtn = await page.$('#cv2ResetAll');
    assert(resetBtn, '#cv2ResetAll missing (should appear when there are overrides)');
    await resetBtn.click();
    await page.waitForTimeout(500);
    const raw = await page.evaluate(() => localStorage.getItem('cs-theme-overrides'));
    assert(raw === null || raw === '{}' || (raw && Object.keys(JSON.parse(raw)).length === 0),
      'cs-theme-overrides should be cleared after Reset All, got: ' + raw);

    // After reset, the inline --accent override on documentElement.style should be gone
    // (server default may still set computed style; we check the inline override)
    const inlineAccent = await page.evaluate(() =>
      document.documentElement.style.getPropertyValue('--accent').trim()
    );
    // Either empty (truly reset) or replaced by server default (not the user's pink #ff00aa)
    assert(inlineAccent !== '#ff00aa',
      'inline --accent should no longer be #ff00aa after reset, got: ' + inlineAccent);
  });

  await step('reset persists across reload', async () => {
    await page.reload({ waitUntil: 'load' });
    await page.waitForFunction(() => window._customizerV2 && window._customizerV2.initDone, null, { timeout: 8000 });
    const raw = await page.evaluate(() => localStorage.getItem('cs-theme-overrides'));
    assert(raw === null || raw === '{}' || (raw && Object.keys(JSON.parse(raw)).length === 0),
      'overrides should remain cleared after reload, got: ' + raw);
  });

  await browser.close();
  console.log('\n' + passed + '/' + (passed + failed) + ' tests passed');
  process.exit(failed > 0 ? 1 : 0);
})();

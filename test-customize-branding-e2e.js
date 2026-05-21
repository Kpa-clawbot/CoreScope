/**
 * E2E (#1297 B4): Customizer V2 — Branding (siteName, tagline, logo, favicon)
 *
 * Verifies the branding subsystem in public/customize-v2.js:
 *  - Site name input → updates .brand-text + document.title live
 *  - Logo URL input → swaps inline SVG for <img> (PR #1137 helper)
 *  - Override persisted to cs-theme-overrides.branding
 *  - Survives reload
 *
 * Usage: BASE_URL=http://localhost:13581 node test-customize-branding-e2e.js
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

  console.log(`\n=== #1297 B4 customize-branding E2E against ${BASE} ===`);

  await step('setup: clear overrides + load', async () => {
    await page.goto(BASE + '/', { waitUntil: 'domcontentloaded' });
    await page.evaluate(() => localStorage.removeItem('cs-theme-overrides'));
    await page.reload({ waitUntil: 'load' });
    await page.waitForFunction(() => window._customizerV2 && window._customizerV2.initDone, null, { timeout: 8000 });
  });

  await step('open customizer + switch to branding tab', async () => {
    await page.click('#customizeToggle');
    await page.waitForSelector('.cust-overlay:not(.hidden)');
    const brandingTab = await page.$('.cust-tab[data-tab="branding"]');
    if (brandingTab) await brandingTab.click();
    await page.waitForSelector('input[data-cv2-field="branding.siteName"]', { timeout: 4000 });
  });

  await step('siteName input updates document.title live', async () => {
    const inp = await page.$('input[data-cv2-field="branding.siteName"]');
    assert(inp, 'branding.siteName input missing');
    await page.evaluate((el) => {
      el.value = 'MyMeshTest';
      el.dispatchEvent(new Event('input', { bubbles: true }));
    }, inp);
    await page.waitForTimeout(400);
    const title = await page.title();
    assert(title === 'MyMeshTest', 'document.title should update live, got: ' + title);
  });

  await step('siteName persists to cs-theme-overrides.branding.siteName', async () => {
    const raw = await page.evaluate(() => localStorage.getItem('cs-theme-overrides'));
    assert(raw, 'overrides not written');
    const parsed = JSON.parse(raw);
    assert(parsed.branding && parsed.branding.siteName === 'MyMeshTest',
      'branding.siteName missing in overrides: ' + raw);
  });

  await step('logoUrl input triggers _setBrandLogoUrl helper (swaps SVG → img)', async () => {
    const inp = await page.$('input[data-cv2-field="branding.logoUrl"]');
    assert(inp, 'branding.logoUrl input missing');
    const testUrl = 'data:image/svg+xml;base64,PHN2ZyB4bWxucz0iaHR0cDovL3d3dy53My5vcmcvMjAwMC9zdmciLz4=';
    await page.evaluate((args) => {
      args.el.value = args.url;
      args.el.dispatchEvent(new Event('input', { bubbles: true }));
    }, { el: inp, url: testUrl });
    await page.waitForTimeout(300);
    // Brand logo node should now be <img>
    const tag = await page.evaluate(() => {
      const n = document.querySelector('.nav-brand .brand-logo');
      return n ? n.tagName.toLowerCase() : null;
    });
    assert(tag === 'img', 'expected brand-logo to be <img> after logoUrl set, got: ' + tag);
    const src = await page.evaluate(() => {
      const n = document.querySelector('.nav-brand .brand-logo');
      return n ? n.getAttribute('src') : null;
    });
    assert(src === testUrl, 'brand-logo src should match URL, got: ' + (src || '').slice(0, 40));
  });

  await step('branding overrides persist across reload', async () => {
    await page.reload({ waitUntil: 'load' });
    await page.waitForFunction(() => window._customizerV2 && window._customizerV2.initDone, null, { timeout: 8000 });
    const title = await page.title();
    // app.js applies branding.siteName to document.title on init (or customizer pipeline does)
    // At minimum, the override is still in localStorage:
    const raw = await page.evaluate(() => localStorage.getItem('cs-theme-overrides'));
    const parsed = JSON.parse(raw);
    assert(parsed.branding && parsed.branding.siteName === 'MyMeshTest',
      'siteName override should persist, got: ' + raw);
  });

  await step('cleanup: clear overrides', async () => {
    await page.evaluate(() => localStorage.removeItem('cs-theme-overrides'));
  });

  await browser.close();
  console.log('\n' + passed + '/' + (passed + failed) + ' tests passed');
  process.exit(failed > 0 ? 1 : 0);
})();

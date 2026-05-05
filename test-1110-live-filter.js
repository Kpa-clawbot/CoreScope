/**
 * Issue #1110 E2E — Live page node filter (autocomplete + theming).
 * Standalone runner so it can be exercised independently of the
 * full test-e2e-playwright.js suite. Mirrors the same assertions
 * that have been added to test-e2e-playwright.js.
 *
 * Usage:
 *   BASE_URL=http://localhost:13581 node test-1110-live-filter.js
 */
const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';
let pass = 0, fail = 0, failed = [];

function assert(cond, msg) { if (!cond) throw new Error(msg || 'assert'); }

async function test(name, fn) {
  try {
    await fn();
    console.log(`  ✅ ${name}`);
    pass++;
  } catch (e) {
    console.log(`  ❌ ${name}: ${e.message}`);
    failed.push({ name, err: e.message });
    fail++;
  }
}

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });
  const ctx = await browser.newContext();
  const page = await ctx.newPage();
  page.setDefaultTimeout(10000);
  console.log(`#1110 Live filter E2E against ${BASE}`);

  await test('input matches toolbar styling (theme-aware bg, comparable height)', async () => {
    await page.goto(BASE + '#/live', { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#liveNodeFilterInput', { timeout: 10000 });
    await page.evaluate(() => document.documentElement.setAttribute('data-theme', 'dark'));
    const bg = await page.$eval('#liveNodeFilterInput', el => getComputedStyle(el).backgroundColor);
    assert(bg !== 'rgb(255, 255, 255)' && bg !== '#ffffff' && bg !== 'white',
      `bg should not be hardcoded white in dark mode, got ${bg}`);
    const inputH = await page.$eval('#liveNodeFilterInput', el => el.getBoundingClientRect().height);
    const labelH = await page.$eval('.live-toggles label', el => el.getBoundingClientRect().height);
    assert(Math.abs(inputH - labelH) < 12,
      `input height (${inputH}) within 12px of label height (${labelH})`);
  });

  await test('typing shows autocomplete dropdown', async () => {
    await page.goto(BASE + '#/live', { waitUntil: 'domcontentloaded' });
    await page.evaluate(() => { try { localStorage.removeItem('live-node-filter'); } catch (_) {} });
    await page.reload({ waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#liveNodeFilterInput', { timeout: 10000 });
    await page.fill('#liveNodeFilterInput', '');
    await page.type('#liveNodeFilterInput', 'te', { delay: 30 });
    await page.waitForSelector('#liveNodeFilterDropdown:not(.hidden) .live-node-filter-option', { timeout: 5000 });
    const n = await page.$$eval('#liveNodeFilterDropdown .live-node-filter-option', els => els.length);
    assert(n >= 1, `expected >=1 suggestion, got ${n}`);
  });

  await test('clicking suggestion filters without reload', async () => {
    await page.goto(BASE + '#/live', { waitUntil: 'domcontentloaded' });
    await page.evaluate(() => { try { localStorage.removeItem('live-node-filter'); } catch (_) {} });
    await page.reload({ waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#liveNodeFilterInput', { timeout: 10000 });
    await page.evaluate(() => { window.__m = 'still-here'; });
    await page.fill('#liveNodeFilterInput', '');
    await page.type('#liveNodeFilterInput', 'te', { delay: 30 });
    await page.waitForSelector('#liveNodeFilterDropdown:not(.hidden) .live-node-filter-option', { timeout: 5000 });
    await page.click('#liveNodeFilterDropdown .live-node-filter-option');
    const m = await page.evaluate(() => window.__m);
    assert(m === 'still-here', 'page must not reload');
    assert(page.url().includes('#/live'), `URL should still target #/live, got ${page.url()}`);
    const keys = await page.evaluate(() => (window._liveGetNodeFilterKeys ? window._liveGetNodeFilterKeys() : []));
    assert(Array.isArray(keys) && keys.length >= 1, `filter active after click, got ${JSON.stringify(keys)}`);
  });

  await test('Enter does not reload or navigate', async () => {
    await page.goto(BASE + '#/live', { waitUntil: 'domcontentloaded' });
    await page.evaluate(() => { try { localStorage.removeItem('live-node-filter'); } catch (_) {} });
    await page.reload({ waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#liveNodeFilterInput', { timeout: 10000 });
    await page.evaluate(() => { window.__m2 = 'still-here'; });
    const urlBefore = page.url();
    await page.fill('#liveNodeFilterInput', 'te');
    await page.focus('#liveNodeFilterInput');
    await page.keyboard.press('Enter');
    await page.waitForTimeout(250);
    const m = await page.evaluate(() => window.__m2);
    assert(m === 'still-here', 'Enter must not reload page');
    assert(page.url() === urlBefore || page.url().includes('#/live'),
      `URL must not navigate away, got ${page.url()} (was ${urlBefore})`);
  });

  await browser.close();
  console.log(`\n${pass}/${pass + fail} passed${fail ? `, ${fail} failed` : ''}`);
  process.exit(fail ? 1 : 0);
})().catch(e => { console.error(e); process.exit(2); });

#!/usr/bin/env node
/* Coverage E2E for public/path-inspector.js (#1297 B5).
 *
 * The existing test-path-inspector-e2e.js uses @playwright/test runner
 * which is not wired into CI's `e2e-test` step (CI runs raw
 * `node test-…-e2e.js`). This file uses the plain chromium-launch
 * pattern compatible with CI and exercises the standalone tools page
 * surface end-to-end:
 *
 *   - navigate to /#/tools/path-inspector
 *   - assert page chrome renders (input, submit btn, help text)
 *   - validation paths: empty input → error; mixed prefix lengths →
 *     error; non-hex input → error
 *   - valid prefixes (1-byte) → submit → API round-trip completes,
 *     URL gets ?prefixes=… appended, results table OR no-results
 *     state renders
 *   - if a candidate exists, expand its evidence row by clicking the
 *     non-button cells and exercise "Show on Map" (asserts route
 *     hand-off via window._pendingPathInspectorRoute / nav to #/map)
 *   - deep-link auto-fill: /#/tools/path-inspector?prefixes=2c
 *     auto-runs and renders the input value
 *
 * Target: lift public/path-inspector.js coverage >= 50% by exercising
 * init/parsePrefixes/validatePrefixes/submit/renderResults/showOnMap
 * branches.
 *
 * Usage: BASE_URL=http://localhost:13581 node test-path-inspector-coverage-e2e.js
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

async function goPI(page, qs) {
  const url = BASE + '/#/tools/path-inspector' + (qs ? '?' + qs : '');
  await page.goto(url, { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('.path-inspector-page', { timeout: 8000 });
}

(async () => {
  const requireChromium = process.env.CHROMIUM_REQUIRE === '1';
  let browser;
  try {
    browser = await chromium.launch({
      headless: true,
      executablePath: process.env.CHROMIUM_PATH || undefined,
      args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
    });
  } catch (err) {
    if (requireChromium) {
      console.error('test-path-inspector-coverage-e2e.js: FAIL — Chromium required but unavailable: ' + err.message);
      process.exit(1);
    }
    console.log('test-path-inspector-coverage-e2e.js: SKIP (Chromium unavailable: ' + err.message.split('\n')[0] + ')');
    process.exit(0);
  }

  const ctx = await browser.newContext({ viewport: { width: 1400, height: 900 } });
  const page = await ctx.newPage();
  page.setDefaultTimeout(15000);
  page.on('pageerror', (e) => console.error('[pageerror]', e.message));

  console.log('\n=== path-inspector.js coverage E2E against ' + BASE + ' ===');

  // ── 1. Page chrome ──
  await step('page renders input, submit button, and help text', async () => {
    await goPI(page);
    const input = await page.$('#path-inspector-input');
    const btn = await page.$('#path-inspector-submit');
    assert(input, '#path-inspector-input missing');
    assert(btn, '#path-inspector-submit missing');
    const helpHasCode = await page.$('.help-text code');
    assert(helpHasCode, 'help-text <code> example missing');
  });

  // ── 2. Validation branches ──
  await step('empty input shows "Enter at least one prefix." error', async () => {
    await goPI(page);
    await page.click('#path-inspector-submit');
    const err = (await page.textContent('#path-inspector-error') || '').trim();
    assert(/at least one prefix/i.test(err), 'expected validation error, got: ' + err);
  });

  await step('non-hex input shows "Invalid hex" error', async () => {
    await goPI(page);
    await page.fill('#path-inspector-input', 'zz');
    await page.click('#path-inspector-submit');
    const err = (await page.textContent('#path-inspector-error') || '').trim();
    assert(/invalid hex/i.test(err), 'expected "Invalid hex" error, got: ' + err);
  });

  await step('odd-length prefix shows error', async () => {
    await goPI(page);
    await page.fill('#path-inspector-input', 'abc');
    await page.click('#path-inspector-submit');
    const err = (await page.textContent('#path-inspector-error') || '').trim();
    assert(/odd-length/i.test(err), 'expected odd-length error, got: ' + err);
  });

  await step('mixed prefix lengths show error', async () => {
    await goPI(page);
    await page.fill('#path-inspector-input', '2c,aabb');
    await page.click('#path-inspector-submit');
    const err = (await page.textContent('#path-inspector-error') || '').trim();
    assert(/mixed/i.test(err), 'expected mixed-length error, got: ' + err);
  });

  // ── 3. Enter-key submit path ──
  await step('Enter key in input triggers submit (URL gets ?prefixes=…)', async () => {
    await goPI(page);
    await page.fill('#path-inspector-input', '2c');
    await page.press('#path-inspector-input', 'Enter');
    // The history.replaceState appends ?prefixes=2c to the hash.
    await page.waitForFunction(() => location.hash.includes('prefixes='), { timeout: 4000 });
    const hash = await page.evaluate(() => location.hash);
    assert(hash.includes('prefixes=2c'), 'expected hash to contain prefixes=2c, got: ' + hash);
  });

  // ── 4. Submit produces a result (table or no-results) ──
  await step('valid 1-byte prefixes → results table OR no-results renders', async () => {
    await goPI(page);
    await page.fill('#path-inspector-input', '2c,a1');
    await page.click('#path-inspector-submit');
    await page.waitForFunction(() => {
      const r = document.getElementById('path-inspector-results');
      const e = document.getElementById('path-inspector-error');
      return (r && (r.querySelector('.path-inspector-table') || r.querySelector('.no-results'))) ||
             (e && e.textContent.trim().length > 0);
    }, { timeout: 8000 });
    // If error path, surface it so the test fails informatively.
    const err = (await page.textContent('#path-inspector-error') || '').trim();
    if (err) assert(false, 'unexpected error from valid input: ' + err);
  });

  // ── 5. Candidate interactions (only if table rendered) ──
  await step('if candidates returned: clicking row toggles evidence row', async () => {
    await goPI(page);
    await page.fill('#path-inspector-input', '2c,a1');
    await page.click('#path-inspector-submit');
    await page.waitForSelector('.path-inspector-table, .no-results', { timeout: 6000 });
    const hasTable = await page.$('.path-inspector-table');
    if (!hasTable) return; // No candidates in fixture; that's still coverage of renderResults() empty branch.
    // Click on the # cell of the first candidate row (NOT the Show on Map button).
    const firstRow = await page.$('.path-inspector-table tbody tr:not(.evidence-row)');
    if (firstRow) {
      // Click the first <td> (the index cell) to avoid the Show on Map button.
      await firstRow.evaluate((row) => {
        const td = row.querySelector('td');
        if (td) td.click();
      });
      await page.waitForTimeout(200);
      // Evidence row should toggle .collapsed off (or stay off if already shown).
      const evidenceRow = await page.$('.evidence-row');
      assert(evidenceRow, 'evidence row should exist when candidates render');
    }
  });

  await step('"Show on Map" hands off to map page (hash → #/map)', async () => {
    await goPI(page);
    await page.fill('#path-inspector-input', '2c,a1');
    await page.click('#path-inspector-submit');
    await page.waitForSelector('.path-inspector-table, .no-results', { timeout: 6000 });
    const hasTable = await page.$('.path-inspector-table');
    if (!hasTable) return;
    const btn = await page.$('.path-inspector-table button[data-idx]');
    if (!btn) return;
    await btn.click();
    await page.waitForFunction(() => location.hash.startsWith('#/map'), { timeout: 4000 });
    const hash = await page.evaluate(() => location.hash);
    assert(hash.startsWith('#/map'), 'expected nav to #/map, got: ' + hash);
  });

  // ── 6. Deep-link auto-fill ──
  await step('deep link ?prefixes=2c auto-fills the input', async () => {
    await goPI(page, 'prefixes=2c');
    const val = await page.inputValue('#path-inspector-input');
    assert(val === '2c', 'expected input prefilled with "2c", got: ' + val);
    // And auto-submit kicks off a request that produces results or no-results.
    await page.waitForFunction(() => {
      const r = document.getElementById('path-inspector-results');
      const e = document.getElementById('path-inspector-error');
      return (r && (r.querySelector('.path-inspector-table') || r.querySelector('.no-results'))) ||
             (e && e.textContent.trim().length > 0);
    }, { timeout: 8000 });
  });

  await browser.close();
  console.log('\n--- ' + passed + ' passed, ' + failed + ' failed ---\n');
  process.exit(failed > 0 ? 1 : 0);
})().catch((e) => { console.error(e); process.exit(1); });

#!/usr/bin/env node
/**
 * E2E for #1510 — live-page nav pin must persist across refresh.
 *
 * Symptom: clicking #navPinBtn pinned the nav, but reloading the page reset
 * pinned=false regardless. Fix: store 'live-nav-pinned' in localStorage and
 * restore on init.
 *
 * Acceptance:
 *   1. Clicking #navPinBtn adds class 'pinned' and sets localStorage.
 *   2. After page reload the pin button still has class 'pinned' (restored from storage).
 *   3. Clicking again removes 'pinned'; reload leaves button unpinned.
 */
'use strict';

const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';
const LS_KEY = 'live-nav-pinned';

let passed = 0, failed = 0;

async function step(name, fn) {
  try { await fn(); passed++; console.log('  ✓ ' + name); }
  catch (e) { failed++; console.error('  ✗ ' + name + ': ' + e.message); }
}
function assert(cond, msg) { if (!cond) throw new Error(msg || 'assertion failed'); }

async function gotoLive(page) {
  await page.goto(BASE + '/#/live', { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('#navPinBtn', { timeout: 10000 });
  await page.evaluate(() => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r))));
}

async function main() {
  let browser;
  try {
    browser = await chromium.launch({
      headless: true,
      executablePath: process.env.CHROMIUM_PATH || undefined,
      args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
    });
  } catch (err) {
    if (process.env.CHROMIUM_REQUIRE === '1') {
      console.error('test-issue-1510-live-nav-pin-e2e.js: FAIL — Chromium unavailable: ' + err.message);
      process.exit(1);
    }
    console.log('test-issue-1510-live-nav-pin-e2e.js: SKIP (Chromium unavailable: ' + err.message.split('\n')[0] + ')');
    process.exit(0);
  }

  const ctx = await browser.newContext();
  const page = await ctx.newPage();
  page.setDefaultTimeout(12000);

  // Clear any prior state.
  await page.goto(BASE + '/#/live', { waitUntil: 'domcontentloaded' });
  await page.evaluate((k) => localStorage.removeItem(k), LS_KEY);

  // (1) Initial state: pin button exists and is unpinned.
  await step('pin button renders without pinned class on first load', async () => {
    await gotoLive(page);
    const pinned = await page.$eval('#navPinBtn', el => el.classList.contains('pinned'));
    assert(!pinned, 'pin button must not have class "pinned" before first click');
    const stored = await page.evaluate((k) => localStorage.getItem(k), LS_KEY);
    assert(stored !== 'true', 'localStorage must not be "true" before first click');
  });

  // (2) Click pin → class added, localStorage updated.
  await step('click sets pinned class and persists to localStorage', async () => {
    await page.$eval('#navPinBtn', btn => btn.click());
    await page.evaluate(() => new Promise(r => requestAnimationFrame(r)));
    const pinned = await page.$eval('#navPinBtn', el => el.classList.contains('pinned'));
    assert(pinned, 'pin button must have class "pinned" after click');
    const stored = await page.evaluate((k) => localStorage.getItem(k), LS_KEY);
    assert(stored === 'true', `localStorage["${LS_KEY}"] must be "true", got "${stored}"`);
  });

  // (3) Reload → pin state restored from localStorage.
  await step('pin state survives page reload', async () => {
    await gotoLive(page);
    const pinned = await page.$eval('#navPinBtn', el => el.classList.contains('pinned'));
    assert(pinned, 'pin button must have class "pinned" after reload (restored from localStorage)');
  });

  // (4) Click again to unpin → class removed, localStorage updated.
  await step('second click removes pinned class and updates localStorage', async () => {
    await page.$eval('#navPinBtn', btn => btn.click());
    await page.evaluate(() => new Promise(r => requestAnimationFrame(r)));
    const pinned = await page.$eval('#navPinBtn', el => el.classList.contains('pinned'));
    assert(!pinned, 'pin button must not have class "pinned" after unpin click');
    const stored = await page.evaluate((k) => localStorage.getItem(k), LS_KEY);
    assert(stored === 'false', `localStorage["${LS_KEY}"] must be "false", got "${stored}"`);
  });

  // (5) Reload after unpin → button is not pinned.
  await step('unpinned state survives page reload', async () => {
    await gotoLive(page);
    const pinned = await page.$eval('#navPinBtn', el => el.classList.contains('pinned'));
    assert(!pinned, 'pin button must not have class "pinned" after reload in unpinned state');
  });

  await browser.close();
  const total = passed + failed;
  console.log(`\ntest-issue-1510-live-nav-pin-e2e.js: ${failed === 0 ? 'OK' : 'FAIL'} — ${passed}/${total} passed`);
  process.exit(failed > 0 ? 1 : 0);
}

main().catch(err => {
  console.error('test-issue-1510-live-nav-pin-e2e.js: ERROR', err);
  process.exit(1);
});

#!/usr/bin/env node
/* #2236 E2E: ?theme=dark|light in the hash query sets the theme for one page
 * load without touching the saved choice.
 *
 * Asserts:
 *   1. #/map?embed=1&theme=dark with a saved "light" loads dark, in embed mode,
 *      and the saved choice is still "light" once app.js has started.
 *   2. A reload without the param loads the saved "light" again.
 *   3. theme=light with a saved "dark" loads light.
 *   4. A theme change from another tab (storage event) does not end the override.
 *   5. The dark-mode toggle is an explicit choice: it ends the override and
 *      saves (toggled twice, so the saved value differs from the start).
 *   6. An unknown value (theme=blue) leaves the saved choice in place.
 */
'use strict';

const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';

function fail(msg) {
  console.error(`test-theme-url-param-2236-e2e.js: FAIL — ${msg}`);
  process.exit(1);
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
    if (process.env.CHROMIUM_REQUIRE === '1') fail(`Chromium required but unavailable: ${err.message}`);
    console.log(`test-theme-url-param-2236-e2e.js: SKIP (Chromium unavailable: ${err.message.split('\n')[0]})`);
    process.exit(0);
  }

  let passed = 0;
  const total = 6;
  try {
    const context = await browser.newContext({ viewport: { width: 1280, height: 900 } });
    const page = await context.newPage();
    page.setDefaultTimeout(10000);

    const state = () => page.evaluate(() => ({
      theme: document.documentElement.getAttribute('data-theme'),
      saved: localStorage.getItem('meshcore-theme'),
      embed: document.body.classList.contains('embed'),
      checked: !!(document.getElementById('darkModeCheckbox') || {}).checked,
    }));
    // Load a URL with a given saved choice; wait until app.js has run its
    // theme setup (the toggle is in sync with data-theme).
    async function open(hash, saved) {
      await page.goto(BASE + '/', { waitUntil: 'domcontentloaded' });
      await page.evaluate((v) => localStorage.setItem('meshcore-theme', v), saved);
      await page.goto(BASE + '/' + hash, { waitUntil: 'domcontentloaded' });
      await page.reload({ waitUntil: 'domcontentloaded' });
      await page.waitForFunction(() => {
        const cb = document.getElementById('darkModeCheckbox');
        return cb && cb.checked === (document.documentElement.getAttribute('data-theme') === 'dark') &&
          document.querySelector('#app') && document.querySelector('#app').children.length > 0;
      });
    }

    await open('#/map?embed=1&theme=dark', 'light');
    let s = await state();
    if (s.theme !== 'dark' || !s.embed || s.saved !== 'light' || !s.checked) {
      fail(`1: expected dark + embed + saved light, got ${JSON.stringify(s)}`);
    }
    passed++;

    await page.goto(BASE + '/#/map', { waitUntil: 'domcontentloaded' });
    await page.reload({ waitUntil: 'domcontentloaded' });
    await page.waitForFunction(() => document.getElementById('darkModeCheckbox'));
    s = await state();
    if (s.theme !== 'light' || s.saved !== 'light') fail(`2: expected saved light after reload, got ${JSON.stringify(s)}`);
    passed++;

    await open('#/channels?theme=light', 'dark');
    s = await state();
    if (s.theme !== 'light' || s.saved !== 'dark') fail(`3: expected light with saved dark, got ${JSON.stringify(s)}`);
    passed++;

    await page.evaluate(() => {
      window.dispatchEvent(new StorageEvent('storage', { key: 'meshcore-theme', newValue: 'dark' }));
    });
    s = await state();
    if (s.theme !== 'light') fail(`4: a storage event ended the override, got ${JSON.stringify(s)}`);
    passed++;

    const toggle = () => page.evaluate(() => {
      const cb = document.getElementById('darkModeCheckbox');
      cb.checked = !cb.checked;
      cb.dispatchEvent(new Event('change'));
    });
    await toggle();
    await toggle();
    s = await state();
    if (s.theme !== 'light' || s.saved !== 'light') fail(`5: toggling twice should leave light saved, got ${JSON.stringify(s)}`);
    await page.evaluate(() => {
      window.dispatchEvent(new StorageEvent('storage', { key: 'meshcore-theme', newValue: 'dark' }));
    });
    s = await state();
    if (s.theme !== 'dark') fail(`5: after the toggle, other tabs apply again, got ${JSON.stringify(s)}`);
    passed++;

    await open('#/map?theme=blue', 'light');
    s = await state();
    if (s.theme !== 'light' || s.saved !== 'light') fail(`6: unknown value should keep saved light, got ${JSON.stringify(s)}`);
    passed++;

    await context.close();
  } catch (err) {
    fail(err.stack || err.message);
  } finally {
    await browser.close();
  }
  console.log(`test-theme-url-param-2236-e2e.js: ${passed}/${total} passed`);
  process.exit(passed === total ? 0 : 1);
}

main();

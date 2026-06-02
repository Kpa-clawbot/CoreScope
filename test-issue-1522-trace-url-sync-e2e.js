// E2E test for issue #1522 — trace hash written into URL after doTrace().
// Run: npx playwright test test-issue-1522-trace-url-sync-e2e.js
// Requires: running server on BASE_URL (default http://localhost:3000).
'use strict';

const { test, expect } = require('@playwright/test');
const BASE_URL = process.env.BASE_URL || 'http://localhost:3000';

test.describe('Trace URL sync — issue #1522', () => {
  test('hash written into URL after clicking Trace', async ({ page }) => {
    await page.goto(`${BASE_URL}/#/tools/trace/`);
    await page.waitForSelector('#traceHashInput', { timeout: 8000 });
    await page.fill('#traceHashInput', 'deadbeef');
    await page.click('#traceBtn');
    await expect(page).toHaveURL(/#\/tools\/trace\/deadbeef$/);
  });

  test('deep-link pre-fills input and fires trace on load', async ({ page }) => {
    await page.goto(`${BASE_URL}/#/tools/trace/cafebabe`);
    await page.waitForSelector('#traceHashInput', { timeout: 8000 });
    const value = await page.inputValue('#traceHashInput');
    expect(value).toBe('cafebabe');
    // URL must remain unchanged (no extra replaceState pollution)
    await expect(page).toHaveURL(/#\/tools\/trace\/cafebabe$/);
  });
});

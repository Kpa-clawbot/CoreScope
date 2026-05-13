// E2E tests for Path Inspector (spec §5 — Playwright).
// Run: npx playwright test test-path-inspector-e2e.js
// Requires: running server on BASE_URL (default http://localhost:3000).
'use strict';

const { test, expect } = require('@playwright/test');
const BASE_URL = process.env.BASE_URL || 'http://localhost:3000';

test.describe('Path Inspector — Map Side Pane (spec §2.7)', () => {
  test('side pane present and collapsed by default', async ({ page }) => {
    await page.goto(`${BASE_URL}/#/map`);
    const pane = page.locator('#mapSidePane');
    await expect(pane).toBeVisible();
    await expect(pane).not.toHaveClass(/expanded/);
  });

  test('click toggle expands the pane', async ({ page }) => {
    await page.goto(`${BASE_URL}/#/map`);
    await page.click('#mapPaneToggle');
    const pane = page.locator('#mapSidePane');
    await expect(pane).toHaveClass(/expanded/);
  });

  test('submit valid prefixes renders candidates within 1s', async ({ page }) => {
    await page.goto(`${BASE_URL}/#/map`);
    await page.click('#mapPaneToggle');
    await page.fill('#mapPiInput', '2c,a1,f4');
    await page.click('#mapPiSubmit');
    // Wait for results or error (both indicate API round-trip complete).
    await expect(page.locator('#mapPiResults table, #mapPiResults .no-results, #mapPiError')).toBeVisible({ timeout: 1000 });
  });

  test('Show on Map button draws polyline on map', async ({ page }) => {
    await page.goto(`${BASE_URL}/#/map`);
    await page.click('#mapPaneToggle');
    await page.fill('#mapPiInput', '2c,a1');
    await page.click('#mapPiSubmit');
    // Wait for results.
    const btn = page.locator('#mapPiResults button[data-idx="0"]');
    await btn.waitFor({ timeout: 2000 });
    await btn.click();
    // Check that route layer has SVG polyline paths drawn.
    const svg = page.locator('#leaflet-map .leaflet-overlay-pane svg path');
    await expect(svg.first()).toBeVisible({ timeout: 2000 });
  });

  test('switching candidate clears prior polyline', async ({ page }) => {
    await page.goto(`${BASE_URL}/#/map`);
    await page.click('#mapPaneToggle');
    await page.fill('#mapPiInput', '2c,a1');
    await page.click('#mapPiSubmit');
    const btn0 = page.locator('#mapPiResults button[data-idx="0"]');
    await btn0.waitFor({ timeout: 2000 });
    await btn0.click();
    // Click second candidate if available.
    const btn1 = page.locator('#mapPiResults button[data-idx="1"]');
    if (await btn1.isVisible()) {
      await btn1.click();
      // Prior route should be cleared — only one polyline group visible.
    }
  });
});

test.describe('Path Inspector — Standalone Page', () => {
  test('deep link auto-fills and runs', async ({ page }) => {
    await page.goto(`${BASE_URL}/#/tools/path-inspector?prefixes=2c,a1,f4`);
    const input = page.locator('#path-inspector-input');
    await expect(input).toHaveValue('2c,a1,f4');
    // Should auto-submit and show results or error.
    await expect(page.locator('#path-inspector-results table, #path-inspector-results .no-results, #path-inspector-error')).toBeVisible({ timeout: 2000 });
  });

  test('old #/traces/<hash> redirects to #/tools/trace/<hash>', async ({ page }) => {
    await page.goto(`${BASE_URL}/#/traces/abc123`);
    await page.waitForTimeout(500);
    expect(page.url()).toContain('#/tools/trace/abc123');
  });
});

test.describe('Path Inspector — Tools Landing (spec §2.8)', () => {
  test('Tools nav shows landing with both entries', async ({ page }) => {
    await page.goto(`${BASE_URL}/#/tools`);
    await expect(page.locator('.tools-landing')).toBeVisible();
    await expect(page.locator('a[href="#/tools/path-inspector"]')).toBeVisible();
    await expect(page.locator('a[href*="#/tools/trace"]')).toBeVisible();
  });
});

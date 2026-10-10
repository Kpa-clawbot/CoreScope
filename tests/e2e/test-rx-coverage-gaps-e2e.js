#!/usr/bin/env node
'use strict';
// "Nothing received" cells on the Mobile RX coverage page: the toggle exists
// only with clientRfSamples, is on by default and asks /api/rx-coverage for
// gaps=1, draws the gap cells hatched underneath the reception cells, and the
// off state is deep-linkable (gaps=0) and stops asking for gaps.
const assert = require('assert');
const { chromium } = require('playwright');
const BASE = process.env.BASE_URL || 'http://localhost:3000';

const poly = (lon, lat) => ({ type: 'Polygon', coordinates: [[[lon, lat], [lon + 0.01, lat], [lon + 0.01, lat + 0.01], [lon, lat + 0.01], [lon, lat]]] });
const recv = (lon, lat) => ({ type: 'Feature', geometry: poly(lon, lat), properties: { cell: 'r' + lon, count: 2, best_snr: -4, has_sig: true, nodes: [] } });
const gap = (lon, lat, samples) => ({ type: 'Feature', geometry: poly(lon, lat), properties: { cell: 'g' + lon, samples } });

let passed = 0, failed = 0;
async function step(name, fn) {
  try { await fn(); passed++; console.log('  ✓ ' + name); }
  catch (e) { failed++; console.log('  ✗ ' + name + ': ' + e.message); }
}

async function openPage(browser, { rfSamples, hash }) {
  const page = await browser.newPage();
  const errors = [];
  page.on('pageerror', e => errors.push(e.message));
  const covRequests = [];
  await page.route('**/api/config/client', async route => {
    const response = await route.fetch();
    await route.fulfill({ json: { ...await response.json(), clientRxCoverage: true, clientRfSamples: rfSamples } });
  });
  await page.route('**/api/config/map', route => route.fulfill({ json: { center: [51, 4], zoom: 9 } }));
  await page.route('**/api/rx-leaderboard?*', route => route.fulfill({ json: { observers: [] } }));
  await page.route('**/api/rx-coverage?*', route => {
    const url = route.request().url();
    covRequests.push(url);
    const withGaps = new URL(url).searchParams.get('gaps') === '1';
    return route.fulfill({ json: { type: 'FeatureCollection', features: [recv(4.40, 51.20)], ...(withGaps ? { gaps: [gap(4.50, 51.20, 1), gap(4.60, 51.20, 5)] } : {}) } });
  });
  await page.goto(BASE + '/#/rx-coverage' + (hash || ''));
  await page.waitForSelector('#rxMap.leaflet-container', { timeout: 15000 });
  return { page, errors, covRequests };
}

const gapCells = page => page.locator('#rxMap path.rx-gap-cell');

(async () => {
  const browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM_PATH || undefined });
  console.log('\n=== RX coverage "nothing received" E2E against ' + BASE + ' ===');
  try {
    await step('no toggle and no gaps request when clientRfSamples is off', async () => {
      const { page, errors, covRequests } = await openPage(browser, { rfSamples: false });
      await page.waitForFunction(() => document.querySelectorAll('#rxMap path.leaflet-interactive').length === 1, null, { timeout: 10000 });
      assert.strictEqual(await page.locator('#rxGapsBtn').count(), 0, 'toggle must not render when the feature is off');
      assert.ok(covRequests.every(u => !/[?&]gaps=1/.test(u)), 'no gaps=1 request without RF samples');
      assert.deepStrictEqual(errors, []);
      await page.close();
    });

    await step('on by default: gaps=1 requested, hatched cells drawn under the reception cells, legend entry', async () => {
      const { page, errors, covRequests } = await openPage(browser, { rfSamples: true });
      await page.waitForFunction(() => document.querySelectorAll('#rxMap path.rx-gap-cell').length === 2, null, { timeout: 10000 });
      assert.ok(covRequests.some(u => /[?&]gaps=1/.test(u)), 'the signal layer asks for gaps');
      assert.strictEqual(await page.locator('#rxGapsBtn').getAttribute('aria-pressed'), 'true');
      assert.strictEqual(await gapCells(page).first().getAttribute('fill'), 'url(#rxGapHatch)', 'gap cells use the hatch pattern');
      assert.strictEqual(await page.locator('#rxMap pattern#rxGapHatch').count(), 1, 'the hatch pattern exists once');
      const below = await page.evaluate(() => {
        const g = document.querySelector('#rxMap path.rx-gap-cell'), r = document.querySelector('#rxMap path.leaflet-interactive:not(.rx-gap-cell)');
        return !!(g && r && (g.compareDocumentPosition(r) & Node.DOCUMENT_POSITION_FOLLOWING));
      });
      assert.ok(below, 'gap cells are drawn before (under) the reception cells');
      assert.ok(/nothing received/i.test(await page.locator('#rxLegend').innerText()), 'legend names the gap cells');
      assert.deepStrictEqual(errors, []);
      await page.close();
    });

    await step('toggle off: cells gone, gaps=0 in the hash, no more gaps requests; the link restores it', async () => {
      const { page, errors, covRequests } = await openPage(browser, { rfSamples: true });
      await page.waitForFunction(() => document.querySelectorAll('#rxMap path.rx-gap-cell').length === 2, null, { timeout: 10000 });
      await page.click('#rxGapsBtn');
      await page.waitForFunction(() => document.querySelectorAll('#rxMap path.rx-gap-cell').length === 0, null, { timeout: 10000 });
      assert.ok(/[?&]gaps=0/.test(await page.evaluate(() => location.hash)), 'off state is in the URL hash');
      assert.ok(!/[?&]gaps=1/.test(covRequests[covRequests.length - 1]), 'the request after switching off does not ask for gaps');
      assert.ok(!/nothing received/i.test(await page.locator('#rxLegend').innerText()), 'legend entry removed');
      await page.close();
      const again = await openPage(browser, { rfSamples: true, hash: '?gaps=0' });
      await again.page.waitForFunction(() => document.querySelectorAll('#rxMap path.leaflet-interactive').length === 1, null, { timeout: 10000 });
      assert.strictEqual(await again.page.locator('#rxGapsBtn').getAttribute('aria-pressed'), 'false', 'deep link opens with the toggle off');
      assert.strictEqual(await gapCells(again.page).count(), 0);
      assert.deepStrictEqual(errors.concat(again.errors), []);
      await again.page.close();
    });

    await step('the noise layer hides the toggle and draws no gap cells', async () => {
      const { page, errors } = await openPage(browser, { rfSamples: true });
      await page.route('**/api/rf-noise?*', route => route.fulfill({ json: { type: 'FeatureCollection', features: [] } }));
      await page.waitForFunction(() => document.querySelectorAll('#rxMap path.rx-gap-cell').length === 2, null, { timeout: 10000 });
      assert.ok(await page.locator('#rxGapsBar').isVisible(), 'toggle visible on the signal layer');
      await page.click('#rxLayerBar button[data-layer="noise"]');
      assert.ok(!(await page.locator('#rxGapsBar').isVisible()), 'toggle hidden on the noise layer');
      await page.waitForTimeout(500);
      assert.strictEqual(await gapCells(page).count(), 0);
      assert.deepStrictEqual(errors, []);
      await page.close();
    });
  } finally {
    await browser.close();
  }
  console.log(`\ntest-rx-coverage-gaps-e2e: ${passed} passed, ${failed} failed`);
  process.exit(failed ? 1 : 0);
})();

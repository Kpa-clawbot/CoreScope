#!/usr/bin/env node
'use strict';
// RF noise-floor layer on the Mobile RX coverage page: the Signal/Noise toggle
// exists only when clientRfSamples is on, switching draws /api/rf-noise cells
// with the noise legend, the layer is deep-linkable, and an empty answer says
// so instead of leaving a blank map.
const assert = require('assert');
const { chromium } = require('playwright');
const BASE = process.env.BASE_URL || 'http://localhost:3000';

const cell = (lon, lat, median) => ({
  type: 'Feature',
  geometry: { type: 'Polygon', coordinates: [[[lon, lat], [lon + 0.01, lat], [lon + 0.01, lat + 0.01], [lon, lat + 0.01], [lon, lat]]] },
  properties: { cell: 'c' + median, count: 3, median_noise_floor: median, quietest_noise_floor: median - 2, noisiest_noise_floor: median + 2 },
});

let passed = 0, failed = 0;
async function step(name, fn) {
  try { await fn(); passed++; console.log('  ✓ ' + name); }
  catch (e) { failed++; console.log('  ✗ ' + name + ': ' + e.message); }
}

async function openPage(browser, { rfSamples, noiseFeatures, hash }) {
  const page = await browser.newPage();
  const errors = [];
  page.on('pageerror', e => errors.push(e.message));
  const noiseRequests = [];
  await page.route('**/api/config/client', async route => {
    const response = await route.fetch();
    await route.fulfill({ json: { ...await response.json(), clientRxCoverage: true, clientRfSamples: rfSamples } });
  });
  await page.route('**/api/config/map', route => route.fulfill({ json: { center: [51, 4], zoom: 9 } }));
  await page.route('**/api/rx-leaderboard?*', route => route.fulfill({ json: { observers: [] } }));
  await page.route('**/api/rx-coverage?*', route => route.fulfill({ json: { type: 'FeatureCollection', features: [] } }));
  await page.route('**/api/rf-noise?*', route => {
    noiseRequests.push(route.request().url());
    return route.fulfill({ json: { type: 'FeatureCollection', features: noiseFeatures, truncated: false } });
  });
  await page.goto(BASE + '/#/rx-coverage' + (hash || ''));
  await page.waitForSelector('#rxMap.leaflet-container', { timeout: 15000 });
  return { page, errors, noiseRequests };
}

(async () => {
  const browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM_PATH || undefined });
  console.log('\n=== RF noise layer E2E against ' + BASE + ' ===');
  try {
    await step('no Signal/Noise toggle when clientRfSamples is off', async () => {
      const { page, errors } = await openPage(browser, { rfSamples: false, noiseFeatures: [] });
      assert.strictEqual(await page.locator('#rxLayerBar').count(), 0, 'toggle must not render when the feature is off');
      assert.deepStrictEqual(errors, []);
      await page.close();
    });

    await step('Noise draws /api/rf-noise cells with the noise legend and deep link', async () => {
      const features = [cell(4.40, 51.20, -118), cell(4.50, 51.20, -111), cell(4.60, 51.20, -100)];
      const { page, errors, noiseRequests } = await openPage(browser, { rfSamples: true, noiseFeatures: features });
      await page.waitForSelector('#rxLayerBar button[data-layer="noise"]');
      await page.click('#rxLayerBar button[data-layer="noise"]');
      await page.waitForFunction(() => document.querySelectorAll('#rxMap path.leaflet-interactive').length === 3, null, { timeout: 10000 });
      assert.ok(noiseRequests.length >= 1, 'the noise layer must fetch /api/rf-noise');
      const q = new URL(noiseRequests[noiseRequests.length - 1]).searchParams;
      assert.ok(q.get('bbox') && q.get('z') && q.get('days'), 'bbox, z and days are sent: ' + q.toString());
      const legend = await page.locator('#rxLegend').innerText();
      assert.ok(/quiet/.test(legend) && /busy/.test(legend), 'legend switches to the noise tiers: ' + legend);
      assert.ok(/noise floor/i.test(await page.locator('#rxSubtitle').innerText()), 'subtitle names the noise floor');
      assert.ok(/[?&]layer=noise/.test(await page.evaluate(() => location.hash)), 'layer is in the URL hash');
      const pressed = await page.locator('#rxLayerBar button[data-layer="noise"]').getAttribute('class');
      assert.ok(/active/.test(pressed || ''), 'Noise button is marked active');
      assert.ok(!(await page.locator('#rxNoiseEmpty').isVisible()), 'no empty-state message when cells exist');
      assert.deepStrictEqual(errors, []);
      await page.close();
    });

    await step('a deep link opens on the noise layer, and an empty answer is labelled', async () => {
      const { page, errors, noiseRequests } = await openPage(browser, { rfSamples: true, noiseFeatures: [], hash: '?layer=noise' });
      await page.waitForFunction(() => {
        const el = document.getElementById('rxNoiseEmpty');
        return el && getComputedStyle(el).display !== 'none';
      }, null, { timeout: 10000 });
      assert.ok(noiseRequests.length >= 1, 'a ?layer=noise link must fetch the noise layer without a click');
      assert.ok(/No RF samples/.test(await page.locator('#rxNoiseEmpty').innerText()));
      assert.deepStrictEqual(errors, []);
      await page.close();
    });
  } finally {
    await browser.close();
  }
  console.log(`\ntest-rx-coverage-noise-e2e: ${passed} passed, ${failed} failed`);
  process.exit(failed ? 1 : 0);
})();

#!/usr/bin/env node
'use strict';
// #2032: real Leaflet viewport initialization, persistence and observer links.
const assert = require('assert');
const { chromium } = require('playwright');
const BASE = process.env.BASE_URL || 'http://localhost:3000';
(async () => {
  const browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM_PATH || undefined });
  try {
    const page = await browser.newPage();
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    await page.addInitScript(() => {
      let leaflet;
      Object.defineProperty(window, 'L', {
        configurable: true,
        get: () => leaflet,
        set(value) {
          leaflet = value;
          value.Map.addInitHook(function () { window.__rxTestMap = this; });
        }
      });
    });
    await page.route('**/api/config/client', async route => {
      const response = await route.fetch();
      await route.fulfill({ json: { ...await response.json(), clientRxCoverage: true } });
    });
    await page.route('**/api/config/map', route => route.fulfill({ json: { center: [12, 34], zoom: 6 } }));
    await page.route('**/api/rx-leaderboard?*', route => route.fulfill({ json: { observers: [] } }));
    let worldRequests = 0;
    const observerCell = { type: 'Feature', geometry: { type: 'Polygon', coordinates: [[[40, 10], [41, 10], [41, 11], [40, 11], [40, 10]]] }, properties: { count: 1, has_sig: false, nodes: [] } };
    await page.route('**/api/rx-coverage?*', route => {
      if (new URL(route.request().url()).searchParams.get('bbox') === '-90,-180,90,180') worldRequests++;
      return route.fulfill({ json: { type: 'FeatureCollection', features: [observerCell] } });
    });
    async function waitMap() {
      await page.waitForFunction(() => window.__rxTestMap && window.__rxTestMap._loaded && document.querySelector('#rxMap.leaflet-container'));
    }
    async function viewport() {
      return page.evaluate(() => { const m = window.__rxTestMap, c = m.getCenter(); return { lat: c.lat, lng: c.lng, zoom: m.getZoom() }; });
    }
    // Leaflet keeps the centre as a pixel coordinate, so a centre read back is
    // only ever accurate to the pixel it landed on. One pixel spans
    // 360/(256*2^zoom) degrees of longitude: 0.022° at zoom 6, 0.0014° at
    // zoom 10. The budget below used to be a flat 0.001°, which at zoom 6
    // demanded 1/20 of a pixel, so any change to the map container's size
    // moved the pixel centre enough to fail an assertion about the *view*.
    // For latitude the Mercator scale is this figure times cos(lat), so using
    // the longitude figure is the looser bound there, deliberately.
    const degPerPixel = (zoom) => 360 / (256 * Math.pow(2, zoom));

    function assertViewport(actual, expected) {
      const budget = degPerPixel(expected.zoom);
      const off = (axis) => axis + ' ' + actual[axis] + ' is more than one pixel ('
        + budget.toFixed(5) + '°) from ' + expected[axis] + ': ' + JSON.stringify(actual);
      assert(Math.abs(actual.lat - expected.lat) < budget, off('lat'));
      assert(Math.abs(actual.lng - expected.lng) < budget, off('lng'));
      assert.equal(actual.zoom, expected.zoom);
    }
    await page.goto(BASE + '/#/rx-coverage'); await waitMap();
    assertViewport(await viewport(), { lat: 12, lng: 34, zoom: 6 });
    await page.evaluate(() => window.__rxTestMap.setView([22, 44], 10, { animate: false }));
    await page.waitForFunction((budget) => Math.abs(Number(new URLSearchParams(location.hash.split('?')[1]).get('lat')) - 22) < budget, degPerPixel(10));
    assertViewport(await page.evaluate(() => JSON.parse(localStorage.getItem('rx-coverage-view'))), { lat: 22, lng: 44, zoom: 10 });
    assert.equal(await page.evaluate(() => localStorage.getItem('map-view')), null, 'coverage must not write the main map\'s saved viewport');
    await page.reload(); await waitMap(); assertViewport(await viewport(), { lat: 22, lng: 44, zoom: 10 });
    // Remove the shareable URL to independently verify this page's saved state.
    await page.goto(BASE + '/#/rx-coverage'); await page.reload(); await waitMap();
    assertViewport(await viewport(), { lat: 22, lng: 44, zoom: 10 });
    await page.goto(BASE + '/#/rx-coverage?days=14&rx=abcd&lat=0&lon=0&zoom=5'); await page.reload(); await waitMap();
    await page.waitForTimeout(500);
    assertViewport(await viewport(), { lat: 0, lng: 0, zoom: 5 });
    assert.equal(worldRequests, 0, 'explicit observer viewport must not auto-fit');
    await page.click('#rxDays button[data-days="30"]');
    const params = new URLSearchParams(new URL(page.url()).hash.split('?')[1]);
    assert.equal(params.get('days'), '30'); assert.equal(params.get('rx'), 'abcd'); assert.equal(params.get('lat'), '0.00000');
    // Observer-only links still fit the observer's complete extent.
    await page.goto(BASE + '/#/rx-coverage?rx=abcd'); await page.reload(); await waitMap();
    await page.waitForFunction(() => {
      const c = window.__rxTestMap.getCenter();
      return c.lat > 10 && c.lat < 11 && c.lng > 40 && c.lng < 41;
    });
    assert(worldRequests > 0, 'observer-only links must request full extent');
    if (process.env.SCREENSHOT_PATH) await page.screenshot({ path: process.env.SCREENSHOT_PATH, fullPage: true });
    assert.deepStrictEqual(errors, []);
    console.log('RX coverage viewport browser regressions OK');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exit(1); });

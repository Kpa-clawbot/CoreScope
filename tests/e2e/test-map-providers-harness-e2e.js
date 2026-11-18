/**
 * E2E self-test of the map harness (tests/e2e/lib/map-harness.js, #2428):
 * in stub mode every tile on #/map is answered locally in light and dark,
 * nothing leaks to an external host, waitForTiles sees no failures, the
 * screenshot is a real PNG, and a runtime provider switch is drawn by the
 * picked provider's stub.
 *
 * Usage: BASE_URL=http://localhost:13581 node tests/e2e/test-map-providers-harness-e2e.js
 */
'use strict';
const fs = require('fs');
const os = require('os');
const path = require('path');
const { chromium } = require('playwright');
const MH = require('./lib/map-harness');

const BASE = process.env.BASE_URL || 'http://localhost:13581';
const OUT = fs.mkdtempSync(path.join(os.tmpdir(), 'map-harness-e2e-'));

let passed = 0, failed = 0;
async function test(name, fn) {
  try { await fn(); passed++; console.log(`  ✅ ${name}`); }
  catch (e) { failed++; console.log(`  ❌ ${name}: ${e.message}`); }
}
function assert(cond, msg) { if (!cond) throw new Error(msg || 'Assertion failed'); }

const EXPECTED_KEY = { light: 'carto-light', dark: 'carto-dark' };

async function openMap(browser, theme) {
  const context = await browser.newContext({ viewport: { width: 1280, height: 800 }, serviceWorkers: 'block' });
  const page = await context.newPage();
  page.setDefaultTimeout(15000);
  await MH.installCollectors(page);
  await MH.installTileStub(page, { mode: 'stub', baseUrl: BASE });
  await MH.presetStorage(page, { theme: theme, provider: 'auto' });
  await page.goto(`${BASE}/#/map`, { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('#leaflet-map .leaflet-tile', { timeout: 15000 });
  const tiles = await MH.waitForTiles(page, { timeout: 15000 });
  return { context, page, tiles };
}

async function tileSrcs(page) {
  return page.$$eval('#leaflet-map img.leaflet-tile', els => els.map(e => e.src));
}

async function run() {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });
  console.log(`\nMap harness self-test against ${BASE}\n`);
  try {
    for (const theme of ['light', 'dark']) {
      console.log(`--- ${theme} ---`);
      const { context, page, tiles } = await openMap(browser, theme);
      try {
        await test(`${theme}: data-theme applied from preset`, async () => {
          const t = await page.evaluate(() => document.documentElement.getAttribute('data-theme'));
          assert(t === theme, `data-theme=${t}`);
        });
        await test(`${theme}: waitForTiles reports visible tiles loaded, 0 failed`, async () => {
          assert(!tiles.timedOut, `timed out: ${JSON.stringify(tiles)}`);
          assert(tiles.loaded > 0, `loaded=${tiles.loaded}`);
          assert(tiles.failed === 0, `failed=${tiles.failed}`);
          assert(tiles.pending === 0, `pending=${tiles.pending}`);
        });
        await test(`${theme}: every tile on #/map was intercepted by the stub`, async () => {
          const log = MH.getRequestLog(page);
          const seen = new Set(log.intercepted.map(r => r.url));
          const srcs = await tileSrcs(page);
          assert(srcs.length > 0, 'no tile <img> elements');
          const missing = srcs.filter(s => !seen.has(s));
          assert(missing.length === 0, `not intercepted: ${missing.slice(0, 3).join(', ')}`);
          const keys = Object.keys(log.counts);
          assert(keys.length === 1 && keys[0] === EXPECTED_KEY[theme], `provider keys ${JSON.stringify(log.counts)}`);
        });
        await test(`${theme}: stub tiles decode as 256px images`, async () => {
          const dims = await page.$$eval('#leaflet-map img.leaflet-tile-loaded', els => els.map(e => e.naturalWidth));
          assert(dims.length > 0 && dims.every(w => w === 256), `naturalWidth ${dims.slice(0, 5)}`);
        });
        await test(`${theme}: zero external requests leaked, none failed, no MapLibre`, async () => {
          const log = MH.getRequestLog(page);
          assert(log.external.length === 0, `external: ${log.external.map(r => r.url).slice(0, 5).join(', ')}`);
          assert(log.failed.length === 0, `failed: ${JSON.stringify(log.failed.slice(0, 3))}`);
          assert(log.maplibre.length === 0, `maplibre requested: ${log.maplibre[0]}`);
        });
        await test(`${theme}: screenshot is a non-empty PNG`, async () => {
          const p = await MH.screenshot(page, OUT, `harness-map-${theme}`);
          const buf = fs.readFileSync(p);
          assert(buf.length > 5000, `size ${buf.length}`);
          assert(buf.subarray(1, 4).toString('ascii') === 'PNG', 'not a PNG');
        });
        await test(`${theme}: collectors report first tile and long-task stats`, async () => {
          const m = await MH.collectMetrics(page);
          assert(typeof m.firstTileMs === 'number' && m.firstTileMs > 0, `firstTileMs ${m.firstTileMs}`);
          assert(typeof m.allTilesMs === 'number' && m.allTilesMs >= m.firstTileMs, `allTilesMs ${m.allTilesMs}`);
          assert(typeof m.longTaskCount === 'number', 'longTaskCount');
          assert(m.layers && m.layers.maps >= 1 && m.layers.layers > 0, `layers ${JSON.stringify(m.layers)}`);
        });
        if (theme === 'dark') {
          await test('dark: setTileProvider switch is drawn by the picked provider stub', async () => {
            const provs = await MH.listProviders(page);
            const target = provs.find(p => p.type === 'dark' && p.id === 'esri-darkgray-labels') || provs.find(p => p.type === 'dark' && p.id !== 'carto-dark');
            assert(target, `no alternative dark provider in ${JSON.stringify(provs)}`);
            const ok = await MH.setTileProvider(page, { theme: 'dark', id: target.id });
            assert(ok, 'MC_setDarkTileProvider returned false');
            await page.waitForFunction((id) => localStorage.getItem('mc-dark-tile-provider') === id, target.id);
            await page.waitForFunction(() => Array.from(document.querySelectorAll('#leaflet-map img.leaflet-tile')).some(i => !/cartocdn/.test(i.src)));
            const t = await MH.waitForTiles(page, { timeout: 15000 });
            assert(t.failed === 0 && !t.timedOut, JSON.stringify(t));
            const log = MH.getRequestLog(page);
            assert(Object.keys(log.counts).some(k => k !== 'carto-dark'), `keys ${JSON.stringify(log.counts)}`);
            assert(log.external.length === 0, 'external leak after switch');
          });
          await test('dark: setTheme flips the app to light via its own toggle', async () => {
            await MH.setTheme(page, 'light');
            const stored = await page.evaluate(() => localStorage.getItem('meshcore-theme'));
            assert(stored === 'light', `meshcore-theme=${stored}`);
            const t = await MH.waitForTiles(page, { timeout: 15000 });
            assert(t.failed === 0 && !t.timedOut, JSON.stringify(t));
            assert((MH.getRequestLog(page).counts['carto-light'] || 0) > 0, 'light tiles not requested after theme flip');
          });
        }
      } finally {
        await context.close();
      }
    }
  } finally {
    await browser.close();
    fs.rmSync(OUT, { recursive: true, force: true });
  }
  console.log(`\n${passed} passed, ${failed} failed`);
  if (failed) process.exit(1);
}

run().catch((e) => { console.error(e); process.exit(1); });

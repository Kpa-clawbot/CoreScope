/**
 * E2E for the #2428 map tile providers: picker structure, gray / satellite
 * filters, operator custom entries (valid + hostile), overlays (Hillshade)
 * incl. persistence across reload and the planner, vector (MapLibre or raster
 * fallback), and the Auto-group invariant on #/live. Every tile / style /
 * elevation request is answered by tests/e2e/lib/map-harness.js; nothing
 * reaches a real tile host.
 *
 * Usage: BASE_URL=http://localhost:13581 node tests/e2e/test-map-providers-e2e.js
 */
'use strict';
const { chromium } = require('playwright');
const MH = require('./lib/map-harness');

const BASE = process.env.BASE_URL || 'http://localhost:13581';
const CUSTOM_HOST = 'tiles.e2e-custom.test';
const T0 = Date.now();

let passed = 0, failed = 0;
async function test(name, fn) {
  try { await fn(); passed++; console.log(`  ✅ ${name}`); }
  catch (e) { failed++; console.log(`  ❌ ${name}: ${e.message}`); }
}
function assert(cond, msg) { if (!cond) throw new Error(msg || 'Assertion failed'); }

const CUSTOM_ENTRIES = [
  { id: 'e2e-sat', label: 'E2E Custom Sat', type: 'any', category: 'satellite', format: 'raster',
    url: `https://${CUSTOM_HOST}/{z}/{x}/{y}.png`, attribution: 'E2E attribution', maxZoom: 18 },
  { id: 'evil', label: 'Evil', type: 'light', category: 'street', format: 'raster',
    url: 'javascript:alert(1)', attribution: '<img src=x onerror=window.__x=1>' },
];

async function newPage(browser, { theme, provider, overlays, custom, planner } = {}) {
  const context = await browser.newContext({ viewport: { width: 1280, height: 800 }, serviceWorkers: 'block' });
  const page = await context.newPage();
  page.setDefaultTimeout(15000);
  const errors = [], warnings = [];
  page.on('pageerror', e => errors.push(String(e && e.message || e)));
  page.on('console', m => { if (m.type() === 'warning') warnings.push(m.text()); });
  await MH.installCollectors(page);
  await MH.installTileStub(page, {
    mode: 'stub', baseUrl: BASE,
    extraHosts: [{ key: 'e2e-custom', kind: 'raster', test: new RegExp(CUSTOM_HOST.replace(/\./g, '\\.')) }],
  });
  await MH.presetStorage(page, { theme: theme || 'light', provider: provider || 'auto', overlays });
  // Rewrite map.tiles.custom / planner in the client config.
  await page.route('**/api/config/client', async (route) => {
    const res = await route.fetch();
    const json = await res.json();
    json.map = json.map || {};
    json.map.tiles = Object.assign({}, json.map.tiles || {});
    if (custom) json.map.tiles.custom = custom;
    if (planner) json.planner = { enabled: true, defaultRangeKm: 30, newAntennaM: 10, existingAntennaM: 20, maxAreaKm: 100, nodeMarginKm: 20, maxLegKm: 100 };
    await route.fulfill({ response: res, json });
  });
  const requests = [];
  page.on('request', r => requests.push(r.url()));
  return { context, page, errors, warnings, requests };
}

async function gotoMap(page, hash) {
  await page.goto(`${BASE}/#/${hash || 'map'}`, { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('.leaflet-control-layers', { state: 'attached' });
  await page.waitForSelector('.leaflet-tile-pane .leaflet-tile-loaded', { timeout: 15000 });
}

async function pick(page, id) {
  const ctl = page.locator('.leaflet-control-layers').first();
  await ctl.hover();
  await page.locator(`label[data-tile-id="${id}"] input`).click();
}
async function pickOverlay(page, id) {
  await page.locator('.leaflet-control-layers').first().hover();
  await page.locator(`label[data-tile-overlay-id="${id}"] input`).click();
}
const paneFilter = (page) => page.evaluate(() => {
  const p = document.querySelector('.leaflet-tile-pane');
  return p ? (p.style.filter || getComputedStyle(p).filter || '') : null;
});
const norm = (f) => (f === 'none' ? '' : f);
const tileKeys = (page) => page.$$eval('.leaflet-tile-pane img.leaflet-tile', els => els.map(e => e.src));

async function waitFilter(page, pred, what) {
  await page.waitForFunction((src) => {
    const p = document.querySelector('.leaflet-tile-pane');
    const f = p ? (p.style.filter || getComputedStyle(p).filter || '') : '';
    return new Function('f', 'return ' + src)(f === 'none' ? '' : f);
  }, pred, { timeout: 8000 }).catch(() => { throw new Error('timeout waiting for ' + what); });
}

async function run() {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-dev-shm-usage'],
  });
  console.log(`\nMap providers E2E against ${BASE}\n`);
  let vectorBranch = 'n/a';
  try {
    // ── 1. Picker structure (light + dark) ──────────────────────────────
    for (const theme of ['light', 'dark']) {
      const c = await newPage(browser, { theme });
      try {
        await gotoMap(c.page);
        await MH.waitForTiles(c.page, { timeout: 15000 });
        await test(`1 ${theme}: Auto first, headings in order, ids registered`, async () => {
          await c.page.locator('.leaflet-control-layers').first().hover();
          const first = await c.page.$eval('.leaflet-control-layers-base label', l => l.getAttribute('data-tile-id') + '|' + l.textContent.trim());
          assert(first.startsWith('__auto__') && first.endsWith('Auto'), 'first entry: ' + first);
          const heads = await c.page.$$eval('.leaflet-control-layers .mc-tile-group-heading', els => els.map(e => e.textContent));
          assert(JSON.stringify(heads) === JSON.stringify(['Street', 'Topographic', 'Satellite', 'Overlays']), 'headings: ' + heads);
          const ids = await c.page.$$eval('.leaflet-control-layers-base label[data-tile-id]', els => els.map(e => e.getAttribute('data-tile-id')));
          const reg = new Set((await MH.listProviders(c.page)).map(p => p.id));
          const bad = ids.filter(i => i !== '__auto__' && !reg.has(i));
          assert(ids.length > 5 && bad.length === 0, 'unregistered ids: ' + bad);
        });
        await test(`1 ${theme}: no external request leaked, no page errors`, async () => {
          const log = MH.getRequestLog(c.page);
          assert(log.external.length === 0, 'external: ' + JSON.stringify(log.external.slice(0, 3)));
          assert(log.failed.length === 0, 'failed: ' + JSON.stringify(log.failed.slice(0, 3)));
          assert(c.errors.length === 0, 'page errors: ' + c.errors);
        });
      } finally { await c.context.close(); }
    }

    // ── 2. Gray raster and satellite filters ────────────────────────────
    for (const [theme, id] of [['light', 'osmfr-gray'], ['dark', 'osmfr-gray-dark']]) {
      const c = await newPage(browser, { theme });
      try {
        await gotoMap(c.page);
        await test(`2 ${theme}: ${id} applies a grayscale filter`, async () => {
          await pick(c.page, id);
          await waitFilter(c.page, "f.indexOf('grayscale(') >= 0", 'grayscale filter');
          await c.page.waitForFunction(() => Array.from(document.querySelectorAll('.leaflet-tile-pane img.leaflet-tile')).some(i => /osmfr/.test(i.src)));
          assert(/grayscale\(/.test(await paneFilter(c.page)), 'filter: ' + await paneFilter(c.page));
        });
        if (theme === 'dark') {
          await test('2 dark: satellite eox-s2-2025 is never inverted', async () => {
            await pick(c.page, 'eox-s2-2025');
            await waitFilter(c.page, "f === ''", 'empty filter');
            await c.page.waitForFunction(() => Array.from(document.querySelectorAll('.leaflet-tile-pane img.leaflet-tile')).some(i => /eox/.test(i.src)));
            assert(norm(await paneFilter(c.page)) === '', 'filter: ' + await paneFilter(c.page));
          });
        }
        assert(c.errors.length === 0, 'page errors: ' + c.errors);
      } finally { await c.context.close(); }
    }

    // ── 3. Custom entries (valid + hostile) ─────────────────────────────
    {
      const c = await newPage(browser, { theme: 'light', custom: CUSTOM_ENTRIES });
      try {
        await gotoMap(c.page);
        await test('3 valid custom entry lists under Satellite and renders from its host', async () => {
          await c.page.locator('.leaflet-control-layers').first().hover();
          const cat = await c.page.$eval('label[data-tile-id="custom-e2e-sat"]', l => {
            const fs = l.closest('fieldset');
            return fs ? fs.querySelector('legend').textContent : null;
          });
          assert(cat === 'Satellite', 'category: ' + cat);
          const text = await c.page.$eval('label[data-tile-id="custom-e2e-sat"]', l => l.textContent.trim());
          assert(text === 'E2E Custom Sat', 'label: ' + text);
          await pick(c.page, 'custom-e2e-sat');
          await c.page.waitForFunction((h) => Array.from(document.querySelectorAll('.leaflet-tile-pane img.leaflet-tile-loaded')).some(i => i.src.indexOf(h) >= 0), CUSTOM_HOST);
          assert(MH.getRequestLog(c.page).counts['e2e-custom'] > 0, 'no request reached the custom host');
        });
        await test('3 hostile entry is absent, inert, and warned about once without its URL', async () => {
          const n = await c.page.locator('label[data-tile-id="custom-evil"]').count();
          assert(n === 0, 'hostile entry listed');
          const x = await c.page.evaluate(() => window.__x);
          assert(x === undefined, 'window.__x = ' + x);
          assert(await c.page.locator('.leaflet-control-layers img[src="x"], .leaflet-control-attribution img').count() === 0, 'injected <img> present');
          const w = c.warnings.filter(t => /evil/.test(t));
          assert(w.length === 1, 'warnings naming evil: ' + JSON.stringify(c.warnings));
          assert(!/javascript:|alert\(1\)|onerror/.test(w[0]), 'warning leaks URL/attribution: ' + w[0]);
          assert(c.errors.length === 0, 'page errors: ' + c.errors);
        });
      } finally { await c.context.close(); }
    }

    // ── 4. Overlays: Hillshade ──────────────────────────────────────────
    {
      const c = await newPage(browser, { theme: 'light', planner: true });
      try {
        await gotoMap(c.page);
        const overlayState = () => c.page.evaluate(() => {
          const out = [];
          document.querySelectorAll('.leaflet-container').forEach(m => {
            const p = m.querySelector('.leaflet-mcOverlay-pane');
            out.push({ canvases: p ? p.querySelectorAll('canvas.leaflet-tile').length : -1, box: m.querySelector('.leaflet-control-layers-overlays input:checked') !== null });
          });
          return out;
        });
        await test('4 ticking Hillshade mounts canvas tiles in mcOverlayPane and fetches terrarium', async () => {
          await pickOverlay(c.page, 'hillshade');
          await c.page.waitForFunction(() => { const p = document.querySelector('.leaflet-mcOverlay-pane'); return p && p.querySelectorAll('canvas.leaflet-tile').length > 0; });
          assert((MH.getRequestLog(c.page).counts.terrarium || 0) > 0, 'no terrarium requests');
          const ls = await c.page.evaluate(() => JSON.parse(localStorage.getItem('mc-tile-overlays') || '[]'));
          assert(ls.indexOf('hillshade') >= 0, 'not persisted: ' + JSON.stringify(ls));
        });
        await test('4 overlay survives reload', async () => {
          await c.page.reload({ waitUntil: 'domcontentloaded' });
          await c.page.waitForSelector('.leaflet-control-layers', { state: 'attached' });
          await c.page.waitForFunction(() => { const p = document.querySelector('.leaflet-mcOverlay-pane'); return p && p.querySelectorAll('canvas.leaflet-tile').length > 0; });
          await c.page.locator('.leaflet-control-layers').first().hover();
          assert(await c.page.locator('label[data-tile-overlay-id="hillshade"] input').isChecked(), 'checkbox not ticked');
        });
        await test('4 overlay is restored on the planner map', async () => {
          await c.page.evaluate(() => { location.hash = '#/tools/planner'; });
          await c.page.waitForFunction(() => document.querySelector('.leaflet-container') && /planner/.test(location.hash));
          await c.page.waitForFunction(() => { const p = document.querySelector('.leaflet-mcOverlay-pane'); return p && p.querySelectorAll('canvas.leaflet-tile').length > 0; });
          await c.page.locator('.leaflet-control-layers').first().hover();
          assert(await c.page.locator('label[data-tile-overlay-id="hillshade"] input').isChecked(), 'planner checkbox not ticked');
        });
        await test('4 unticking removes it on the planner and on #/map', async () => {
          await pickOverlay(c.page, 'hillshade');
          await c.page.waitForFunction(() => { const p = document.querySelector('.leaflet-mcOverlay-pane'); return !p || p.querySelectorAll('canvas.leaflet-tile').length === 0; });
          await c.page.evaluate(() => { location.hash = '#/map'; });
          await c.page.waitForSelector('#leaflet-map .leaflet-control-layers', { state: 'attached' });
          await c.page.waitForSelector('.leaflet-tile-pane .leaflet-tile-loaded');
          const st = await overlayState();
          assert(st.every(s => s.canvases <= 0 && !s.box), 'still present: ' + JSON.stringify(st));
          const ls = await c.page.evaluate(() => JSON.parse(localStorage.getItem('mc-tile-overlays') || '[]'));
          assert(ls.indexOf('hillshade') < 0, 'still persisted: ' + JSON.stringify(ls));
          assert(c.errors.length === 0, 'page errors: ' + c.errors);
        });
      } finally { await c.context.close(); }
    }

    // ── 5. Vector: lazy load, GL or raster fallback ─────────────────────
    {
      const c = await newPage(browser, { theme: 'light' });
      try {
        await gotoMap(c.page);
        await test('5 vendor/maplibre-gl is not requested on page load', async () => {
          const hit = c.requests.filter(u => /vendor\/maplibre-gl/.test(u));
          assert(hit.length === 0, 'requested: ' + hit.slice(0, 3));
        });
        await test('5 openfreemap-liberty mounts a GL canvas or falls back to raster tiles', async () => {
          await pick(c.page, 'openfreemap-liberty');
          await c.page.waitForFunction(() => {
            const pane = document.querySelector('.leaflet-tile-pane');
            if (!pane) return false;
            if (pane.querySelector('canvas.maplibregl-canvas')) return true;
            return Array.from(pane.querySelectorAll('img.leaflet-tile-loaded')).some(i => /cartocdn/.test(i.src));
          }, null, { timeout: 20000 });
          const gl = await c.page.locator('.leaflet-tile-pane canvas.maplibregl-canvas').count();
          vectorBranch = gl > 0 ? 'webgl-canvas' : 'raster-fallback';
          assert(c.requests.some(u => /vendor\/maplibre-gl/.test(u)), 'maplibre never loaded');
          assert(c.errors.length === 0, 'page errors: ' + c.errors);
        });
        await test('5 back to Auto removes the GL canvas', async () => {
          await pick(c.page, '__auto__');
          await c.page.waitForFunction(() => !document.querySelector('.leaflet-tile-pane canvas.maplibregl-canvas'));
          await c.page.waitForFunction(() => Array.from(document.querySelectorAll('.leaflet-tile-pane img.leaflet-tile-loaded')).some(i => /cartocdn/.test(i.src)));
          assert(c.errors.length === 0, 'page errors: ' + c.errors);
        });
      } finally { await c.context.close(); }
    }

    // ── 6. #/live: pick + theme flip + Auto keeps one picker / one group ─
    {
      const c = await newPage(browser, { theme: 'light' });
      try {
        await gotoMap(c.page, 'live');
        await test('6 live: provider pick, theme flip and Auto leave one picker and one auto layer', async () => {
          await pick(c.page, 'osmfr-hot');
          await c.page.waitForFunction(() => Array.from(document.querySelectorAll('.leaflet-tile-pane img.leaflet-tile')).some(i => /openstreetmap\.fr/.test(i.src)));
          await MH.setTheme(c.page, 'dark');
          await MH.setTheme(c.page, 'light');
          await pick(c.page, '__auto__');
          await c.page.waitForFunction(() => Array.from(document.querySelectorAll('.leaflet-tile-pane img.leaflet-tile-loaded')).some(i => /cartocdn/.test(i.src)));
          await MH.waitForTiles(c.page, { timeout: 15000 });
          const r = await c.page.evaluate(() => {
            const maps = window.__mcHarness.maps;
            const m = maps[maps.length - 1];
            let grids = 0;
            m.eachLayer(l => { if (l instanceof L.GridLayer && l._url) grids++; });
            return { pickers: document.querySelectorAll('.leaflet-control-layers').length, grids, containers: document.querySelectorAll('.leaflet-tile-pane > .leaflet-layer').length };
          });
          assert(r.pickers === 1, 'pickers: ' + r.pickers);
          assert(r.grids === 1, 'tile layers on map: ' + r.grids);
          assert(r.containers === 1, 'tile pane layers: ' + r.containers);
          assert(c.errors.length === 0, 'page errors: ' + c.errors);
        });
      } finally { await c.context.close(); }
    }
  } finally {
    await browser.close();
  }
  const secs = ((Date.now() - T0) / 1000).toFixed(1);
  console.log(`\nVector branch: ${vectorBranch}`);
  console.log(`${passed} passed, ${failed} failed (${secs}s)`);
  process.exit(failed > 0 ? 1 : 0);
}

run().catch(e => { console.error(e); process.exit(1); });

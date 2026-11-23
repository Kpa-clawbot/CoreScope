/**
 * E2E for the #2428 map tile providers: picker structure, gray / satellite
 * filters, operator custom entries (valid + hostile), overlays (Hillshade)
 * incl. persistence across reload and the planner, vector (MapLibre or raster
 * fallback), and the Auto-group invariant on #/live. Every tile / style /
 * elevation request is answered by tests/e2e/lib/map-harness.js; nothing
 * reaches a real tile host.
 *
 * The #2428 groups (eox, osmfr, openfreemap, versatiles, hillshade) are opt-in,
 * so newPage rewrites the client config to enable them (providers: null leaves
 * the config untouched). Section 7 covers the saved pick (mc-tile-pinned).
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

// map.tiles.providers that turns every opt-in #2428 group on.
const OPT_IN_ON = { eox: { enabled: true }, osmfr: { enabled: true }, openfreemap: { enabled: true },
  versatiles: { enabled: true }, hillshade: { enabled: true } };
const OPT_IN_IDS = ['eox-s2-2025', 'eox-s2-2016', 'eox-terrain-light', 'osmfr-hot', 'osmfr-cyclosm', 'osmfr-gray', 'osmfr-gray-dark',
  'openfreemap-liberty', 'openfreemap-bright', 'openfreemap-positron', 'openfreemap-dark', 'openfreemap-fiord',
  'versatiles-colorful', 'versatiles-muted', 'versatiles-gray', 'versatiles-colorful-dark', 'versatiles-gray-dark'];

async function newPage(browser, { theme, provider, overlays, custom, planner, providers } = {}) {
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
    if (providers !== null) json.map.tiles.providers = Object.assign({}, json.map.tiles.providers || {}, providers || OPT_IN_ON);
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

    // ── 4b. Another tab's pick that lands mid-click is kept ─────────────
    // Tab A's write is already in localStorage when tab B's picker click
    // runs, but its 'storage' event has not arrived. The click must write
    // once, keep A's overlay (stored, on the map, box ticked), and the next
    // click must not remove it. Leaflet applies a click by walking every
    // checkbox; a sync applied mid-click left A's box stale (or unticked it
    // in the same walk) and the removal was written back to every tab.
    {
      const c = await newPage(browser, { theme: 'light' });
      try {
        await gotoMap(c.page);
        const ids = await c.page.evaluate(() => Array.from(document.querySelectorAll('label[data-tile-overlay-id]')).map(l => l.getAttribute('data-tile-overlay-id')));
        const other = ids.find(id => id !== 'hillshade');
        await test('4b a pick from another tab that lands mid-click stays (one write per click)', async () => {
          assert(ids.indexOf('hillshade') >= 0 && other, 'need hillshade and one more overlay: ' + ids.join(','));
          await c.page.evaluate((o) => {
            window.__ovWrites = 0;
            const set = Storage.prototype.setItem;
            Storage.prototype.setItem = function (k, v) { if (k === 'mc-tile-overlays') window.__ovWrites++; return set.call(this, k, v); };
            // Tab A's write: in storage, no 'storage' event in this tab.
            set.call(localStorage, 'mc-tile-overlays', JSON.stringify([o]));
          }, other);
          const state = () => c.page.evaluate((o) => {
            const box = document.querySelector(`label[data-tile-overlay-id="${o}"] input`);
            return { writes: window.__ovWrites, stored: JSON.parse(localStorage.getItem('mc-tile-overlays') || '[]'), box: !!(box && box.checked) };
          }, other);
          await pickOverlay(c.page, 'hillshade');
          await c.page.waitForTimeout(300);
          let st = await state();
          assert(st.writes === 1, `first click wrote ${st.writes}x: ${JSON.stringify(st.stored)}`);
          assert(st.stored.indexOf(other) >= 0 && st.stored.indexOf('hillshade') >= 0, 'after first click stored ' + JSON.stringify(st.stored));
          assert(st.box, `${other} box not ticked after the first click`);
          await pickOverlay(c.page, 'hillshade');
          await c.page.waitForTimeout(300);
          st = await state();
          assert(st.writes === 2, `second click: ${st.writes} writes in total, want 2: ${JSON.stringify(st.stored)}`);
          assert(st.stored.indexOf(other) >= 0 && st.stored.indexOf('hillshade') < 0, 'after second click stored ' + JSON.stringify(st.stored));
          assert(st.box, `${other} box not ticked after the second click`);
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

    // ── 7. Saved pick (mc-tile-pinned) ──────────────────────────────────
    const pinned = (page) => page.evaluate(() => localStorage.getItem('mc-tile-pinned'));
    const radioOn = async (page, id) => {
      await page.locator('.leaflet-control-layers').first().hover();
      return page.locator(`label[data-tile-id="${id}"] input`).isChecked();
    };
    const reloadMap = async (page) => {
      await page.reload({ waitUntil: 'domcontentloaded' });
      await page.waitForSelector('.leaflet-control-layers', { state: 'attached' });
      await page.waitForSelector('.leaflet-tile-pane .leaflet-tile-loaded', { timeout: 15000 });
    };
    const drawn = (page, re, scope) => page.waitForFunction(([src, sel]) =>
      Array.from(document.querySelectorAll(sel + ' .leaflet-tile-pane img.leaflet-tile-loaded')).some(i => new RegExp(src).test(i.src)),
      [re, scope || ''], { timeout: 15000 });
    {
      const c = await newPage(browser, { theme: 'light' });
      try {
        await gotoMap(c.page);
        await test('7a picking a base map saves it, and a reload keeps it selected and drawn', async () => {
          assert(await pinned(c.page) === null, 'pinned before any pick: ' + await pinned(c.page));
          await pick(c.page, 'osmfr-hot');
          await drawn(c.page, 'openstreetmap\\.fr/hot');
          assert(await pinned(c.page) === 'osmfr-hot', 'mc-tile-pinned: ' + await pinned(c.page));
          await reloadMap(c.page);
          await drawn(c.page, 'openstreetmap\\.fr/hot');
          assert(await radioOn(c.page, 'osmfr-hot'), 'osmfr-hot radio not selected after reload');
          assert(!(await radioOn(c.page, '__auto__')), 'Auto still selected after reload');
          assert(await c.page.locator('label[data-tile-id="osmfr-hot"]').count() === 1, 'label[data-tile-id] missing');
          assert(!(await tileKeys(c.page)).some(u => /cartocdn/.test(u)), 'Auto (carto) tiles still on the map');
        });
        await test('7a the saved pick also shows on a page without a picker (node detail map)', async () => {
          const pk = await c.page.evaluate(async () => {
            const r = await fetch('/api/nodes?limit=200'); const j = await r.json();
            const n = (j.nodes || []).find(x => x.lat && x.lon);
            return n ? n.public_key : null;
          });
          assert(pk, 'the fixture has no node with a location');
          await c.page.evaluate((k) => { location.hash = '#/nodes/' + k; }, pk);
          await c.page.waitForSelector('#nodeFullMap .leaflet-tile-loaded', { timeout: 15000 });
          assert(await c.page.locator('#nodeFullMap .leaflet-control-layers').count() === 0, 'the node map has a picker');
          await drawn(c.page, 'openstreetmap\\.fr/hot', '#nodeFullMap');
        });
        await test('7a a pin that does not fit the theme is kept but not drawn on a picker-less page', async () => {
          // osmfr-hot is a light map: in dark the node map shows the dark choice, the pick stays saved.
          await MH.setTheme(c.page, 'dark');
          await c.page.waitForFunction(() => Array.from(document.querySelectorAll('#nodeFullMap .leaflet-tile-pane img.leaflet-tile-loaded')).some(i => /cartocdn/.test(i.src)), null, { timeout: 15000 });
          assert(await c.page.locator('#nodeFullMap .leaflet-tile-pane img[src*="openstreetmap.fr"]').count() === 0, 'light pin drawn in dark');
          assert(await pinned(c.page) === 'osmfr-hot', 'pin was dropped: ' + await pinned(c.page));
          assert(c.errors.length === 0, 'page errors: ' + c.errors);
        });
      } finally { await c.context.close(); }
    }
    {
      const c = await newPage(browser, { theme: 'light' });
      try {
        await gotoMap(c.page);
        await test('7b picking Auto clears the saved pick; a reload shows Auto selected', async () => {
          await pick(c.page, 'osmfr-hot');
          await drawn(c.page, 'openstreetmap\\.fr/hot');
          assert(await pinned(c.page) === 'osmfr-hot', 'not saved');
          await pick(c.page, '__auto__');
          await drawn(c.page, 'cartocdn');
          assert(await pinned(c.page) === null, 'key still there: ' + await pinned(c.page));
          await reloadMap(c.page);
          assert(await radioOn(c.page, '__auto__'), 'Auto not selected after reload');
          assert(!(await radioOn(c.page, 'osmfr-hot')), 'osmfr-hot selected after reload');
          assert(await pinned(c.page) === null, 'key came back after reload');
          assert(c.errors.length === 0, 'page errors: ' + c.errors);
        });
        await test('7b a saved id that is not registered (junk) is ignored: Auto, no error', async () => {
          await c.page.evaluate(() => localStorage.setItem('mc-tile-pinned', '<img src=x onerror=window.__x=1>'));
          await reloadMap(c.page);
          assert(await radioOn(c.page, '__auto__'), 'Auto not selected');
          assert(await c.page.evaluate(() => window.__x) === undefined, 'junk was executed');
          assert(c.errors.length === 0, 'page errors: ' + c.errors);
        });
      } finally { await c.context.close(); }
    }

    // ── 7c. Untouched config: the opt-in groups are not offered ─────────
    {
      const c = await newPage(browser, { theme: 'light', providers: null });
      try {
        await gotoMap(c.page);
        await test('7c with no map.tiles.providers the picker lists none of the opt-in groups', async () => {
          await c.page.locator('.leaflet-control-layers').first().hover();
          const ids = await c.page.$$eval('.leaflet-control-layers label[data-tile-id], .leaflet-control-layers label[data-tile-overlay-id]',
            els => els.map(e => e.getAttribute('data-tile-id') || e.getAttribute('data-tile-overlay-id')));
          assert(ids.indexOf('carto-light') >= 0 && ids.indexOf('__auto__') >= 0, 'picker looks empty: ' + ids);
          const leaked = ids.filter(i => OPT_IN_IDS.indexOf(i) >= 0 || /^(hillshade|eox-labels)/.test(i));
          assert(leaked.length === 0, 'opt-in entries offered: ' + leaked);
          const reg = (await MH.listProviders(c.page)).map(p => p.id);
          assert(OPT_IN_IDS.every(i => reg.indexOf(i) < 0), 'registered: ' + reg.filter(i => OPT_IN_IDS.indexOf(i) >= 0));
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

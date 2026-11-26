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

async function newPage(browser, opts = {}) {
  const context = await browser.newContext({ viewport: { width: 1280, height: 800 }, serviceWorkers: 'block' });
  return newTab(context, opts, true);
}

// Another page (tab) in `context`, set up like the first one. The tile stub
// is per context: only the first page installs it.
async function newTab(context, { theme, provider, overlays, custom, planner, providers } = {}, first) {
  const page = await context.newPage();
  page.setDefaultTimeout(15000);
  const errors = [], warnings = [];
  page.on('pageerror', e => errors.push(String(e && e.message || e)));
  page.on('console', m => { if (m.type() === 'warning') warnings.push(m.text()); });
  await MH.installCollectors(page);
  if (first) {
    await MH.installTileStub(page, {
      mode: 'stub', baseUrl: BASE,
      extraHosts: [{ key: 'e2e-custom', kind: 'raster', test: new RegExp(CUSTOM_HOST.replace(/\./g, '\\.')) }],
    });
  }
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

// Hold the server's actual reply. Let the normal customizer apply it and
// dispatch its debounced refresh only after the detail is open (#2464).
async function holdTheme(page) {
  let receive;
  const held = new Promise(resolve => { receive = resolve; });
  await page.route('**/api/config/theme', async route => {
    receive({ route, response: await route.fetch() });
  });
  return async () => {
    const { route, response } = await held;
    assert(response.ok(), 'server theme request failed');
    await page.evaluate(() => {
      localStorage.setItem('cs-theme-overrides', JSON.stringify({
        distanceUnit: 'km', home: { footerLinks: [{ label: 'Route guide', url: '#/map' }] },
      }));
      window.addEventListener('theme-refresh', () => {
        if (window.SITE_CONFIG?.distanceUnit === 'km') window.__detailThemeReady = true;
      });
    });
    await route.fulfill({ response });
    await page.waitForFunction(() => window.__detailThemeReady);
  };
}

async function openRoutePatterns(page) {
  // A rendered bulk tab proves the initial analytics load has finished.
  await page.goto(BASE + '/#/analytics?tab=hashsizes', { waitUntil: 'domcontentloaded', timeout: 60000 });
  await page.locator('#analyticsContent .hash-bar-row, #analyticsContent .analytics-table').first().waitFor();
  await page.locator('#analyticsTabs [data-tab="subpaths"]').click();
}

const rowHops = rows => rows.evaluateAll(rows => rows.map(row => row.dataset.hops));
const hasShortHop = hops => hops.split(',').some(hop => hop.length === 2);

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
          // Waits for the state the asserts below expect after click n (n
          // writes, hillshade stored iff `hill`, the other overlay stored and
          // its box ticked), then one more frame and tick, so a late extra
          // write or a box unticked by the deferred sync is still seen. A
          // state that never arrives is reported by the asserts.
          const settled = async (n, hill) => {
            await c.page.waitForFunction(([o, want, h]) => {
              const box = document.querySelector(`label[data-tile-overlay-id="${o}"] input`);
              const stored = JSON.parse(localStorage.getItem('mc-tile-overlays') || '[]');
              return window.__ovWrites >= want && stored.indexOf(o) >= 0 && (stored.indexOf('hillshade') >= 0) === h && !!(box && box.checked);
            }, [other, n, hill], { timeout: 5000 }).catch(() => {});
            await c.page.evaluate(() => new Promise(r => requestAnimationFrame(() => setTimeout(r, 0))));
            return state();
          };
          await pickOverlay(c.page, 'hillshade');
          let st = await settled(1, true);
          assert(st.writes === 1, `first click wrote ${st.writes}x: ${JSON.stringify(st.stored)}`);
          assert(st.stored.indexOf(other) >= 0 && st.stored.indexOf('hillshade') >= 0, 'after first click stored ' + JSON.stringify(st.stored));
          assert(st.box, `${other} box not ticked after the first click`);
          await pickOverlay(c.page, 'hillshade');
          st = await settled(2, false);
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
    // Home and the Analytics tabs rebuild their content on 'theme-refresh',
    // which app.js fires 300 ms after each 'theme-changed'; a page load ends
    // with one, once /api/config/theme is applied. A card or route detail
    // opened before it is dropped with its map, and the network going idle
    // does not order after it: wait for the config and for app.js's timer.
    const themeSettled = (page) => page.waitForFunction(() =>
      !!(window._customizerV2 && window._customizerV2.initDone) && _themeRefreshTimer === null,
      null, { timeout: 15000 });
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

    // ── 7d. A load with a saved pick never starts the Auto tiles ────────
    // The picker shows the pick; the theme's map (the Auto layer) must not
    // be started first only to be replaced and cancelled. Chromium drops a
    // tile image aborted in the same task before it reaches the network, so
    // the check counts the tile images Leaflet starts (every http(s) src set
    // on an <img>) as well as the network requests.
    {
      const c = await newPage(browser, { theme: 'light' });
      try {
        await gotoMap(c.page);
        await test('7d a reload with a saved pick starts the pinned tiles only, none of the Auto (theme) map', async () => {
          await pick(c.page, 'osmfr-hot');
          await drawn(c.page, 'openstreetmap\\.fr/hot');
          await c.page.addInitScript(() => {
            window.__tileSrcs = [];
            const d = Object.getOwnPropertyDescriptor(HTMLImageElement.prototype, 'src');
            Object.defineProperty(HTMLImageElement.prototype, 'src', {
              configurable: true, get: d.get,
              set(v) { if (/^https?:/.test(v)) window.__tileSrcs.push(String(v)); return d.set.call(this, v); },
            });
          });
          c.requests.length = 0;
          await reloadMap(c.page);
          await drawn(c.page, 'openstreetmap\\.fr/hot');
          await MH.waitForTiles(c.page, { timeout: 15000 });
          const srcs = await c.page.evaluate(() => window.__tileSrcs);
          const count = (list, re) => list.filter(u => re.test(u)).length;
          const AUTO = /cartocdn\.com\//, PIN = /openstreetmap\.fr\/hot\//;
          const n = { autoStarted: count(srcs, AUTO), pinStarted: count(srcs, PIN), autoRequested: count(c.requests, AUTO), pinRequested: count(c.requests, PIN) };
          console.log('     pinned reload: ' + JSON.stringify(n));
          assert(n.pinStarted > 0 && n.pinRequested > 0, 'no pinned tile requested: ' + JSON.stringify(n));
          assert(n.autoStarted === 0 && n.autoRequested === 0, 'Auto (carto) tiles started on a load with a saved pick: ' + JSON.stringify(n));
          await pick(c.page, '__auto__');
          await drawn(c.page, 'cartocdn');
          assert(await pinned(c.page) === null, 'Auto did not clear the pin');
          assert(c.errors.length === 0, 'page errors: ' + c.errors);
        });
      } finally { await c.context.close(); }
    }

    // ── 7e. A pick made in another tab reaches open maps ────────────────
    // Pages in one context (real 'storage' events): A picks on the map
    // page; B shows a node detail map (no picker) and follows without a
    // reload; C is another map page with a picker, which keeps its own
    // layer and radio. Nothing is written back. The Home mini-map and the
    // Analytics route map (two more tabs) follow like B.
    {
      const a = await newPage(browser, { theme: 'light' });
      try {
        const b = await newTab(a.context, { theme: 'light' });
        const c = await newTab(a.context, { theme: 'light' });
        await gotoMap(a.page);
        await gotoMap(c.page);
        const pk = await a.page.evaluate(async () => {
          const r = await fetch('/api/nodes?limit=200'); const j = await r.json();
          const n = (j.nodes || []).find(x => x.lat && x.lon);
          return n ? n.public_key : null;
        });
        assert(pk, 'the fixture has no node with a location');
        await b.page.goto(`${BASE}/#/nodes/${pk}`, { waitUntil: 'domcontentloaded' });
        await b.page.waitForSelector('#nodeFullMap .leaflet-tile-loaded', { timeout: 15000 });
        // The client config, and the node page once its data lands, re-apply
        // the tiles (and so pick up a stored pin by themselves): let every
        // tab settle first.
        for (const t of [a, b, c]) await t.page.waitForFunction(() => window.MC_MAP_CFG, null, { timeout: 15000 });
        await b.page.waitForLoadState('networkidle');
        const countWrites = (p) => p.evaluate(() => {
          window.__pinWrites = 0;
          const set = Storage.prototype.setItem, rm = Storage.prototype.removeItem;
          Storage.prototype.setItem = function (k, v) { if (k === 'mc-tile-pinned') window.__pinWrites++; return set.call(this, k, v); };
          Storage.prototype.removeItem = function (k) { if (k === 'mc-tile-pinned') window.__pinWrites++; return rm.call(this, k); };
        });
        await countWrites(b.page);
        await countWrites(c.page);
        const noOsmfr = (p, scope) => p.locator(`${scope || ''} .leaflet-tile-pane img[src*="openstreetmap.fr"]`).count();
        await test('7e a pick in tab A redraws a picker-less map in tab B without a reload', async () => {
          await pick(a.page, 'osmfr-hot');
          await drawn(a.page, 'openstreetmap\\.fr/hot');
          await drawn(b.page, 'openstreetmap\\.fr/hot', '#nodeFullMap');
        });
        await test('7e Auto in tab A returns tab B to the theme map', async () => {
          await pick(a.page, '__auto__');
          await drawn(a.page, 'cartocdn');
          await b.page.waitForFunction(() => {
            const imgs = Array.from(document.querySelectorAll('#nodeFullMap .leaflet-tile-pane img.leaflet-tile'));
            return imgs.length > 0 && imgs.every(i => !/openstreetmap\.fr/.test(i.src)) && imgs.some(i => i.classList.contains('leaflet-tile-loaded') && /cartocdn/.test(i.src));
          }, null, { timeout: 15000 });
        });
        await test('7e a map page with a picker in tab C keeps its layer and radio; no tab writes the pick back', async () => {
          // Tab C's own 'storage' event for the pick, counted by a listener
          // added after the page's, so the page has handled every one counted.
          await c.page.evaluate(() => {
            window.__pinEvents = 0;
            window.addEventListener('storage', (e) => { if (e.key === 'mc-tile-pinned') window.__pinEvents++; });
          });
          await pick(a.page, 'osmfr-hot');
          await drawn(b.page, 'openstreetmap\\.fr/hot', '#nodeFullMap');
          await c.page.waitForFunction(() => window.__pinEvents >= 1, null, { timeout: 15000 });
          await c.page.evaluate(() => new Promise(r => requestAnimationFrame(() => setTimeout(r, 0)))); // and drew what it would
          assert(await radioOn(c.page, '__auto__'), 'tab C: Auto no longer selected');
          assert(!(await radioOn(c.page, 'osmfr-hot')), 'tab C: osmfr-hot selected');
          assert(await noOsmfr(c.page) === 0, 'tab C draws the other tab\'s pick');
          assert((await tileKeys(c.page)).some(u => /cartocdn/.test(u)), 'tab C lost its Auto tiles');
          const w = [await b.page.evaluate(() => window.__pinWrites), await c.page.evaluate(() => window.__pinWrites)];
          assert(w[0] === 0 && w[1] === 0, 'pin writes in tabs B, C: ' + w);
          assert(await pinned(c.page) === 'osmfr-hot', 'stored pin: ' + await pinned(c.page));
          for (const t of [a, b, c]) assert(t.errors.length === 0, 'page errors: ' + t.errors);
        });
        await test('7e the Home mini-map and the Analytics route map follow a pick in tab A, and its Auto', async () => {
          await pick(a.page, '__auto__');
          await drawn(a.page, 'cartocdn');
          const d = await newTab(a.context, { theme: 'light' });
          const e = await newTab(a.context, { theme: 'light' });
          // Home: the located node as a claimed node, its card opened.
          await d.page.addInitScript((k) => {
            localStorage.setItem('meshcore-my-nodes', JSON.stringify([{ pubkey: k, name: 'e2e', addedAt: 0 }]));
            localStorage.setItem('meshcore-user-level', 'experienced');
          }, pk);
          await d.page.goto(`${BASE}/#/home`, { waitUntil: 'domcontentloaded' });
          await themeSettled(d.page);
          await d.page.waitForSelector('.my-node-card');
          await d.page.locator('.my-node-card .mnc-btn[data-action="health"]').first().click();
          await drawn(d.page, 'cartocdn', '#healthMap');
          // Analytics: the first route whose detail has a map (two located nodes).
          await e.page.goto(`${BASE}/#/analytics?tab=subpaths`, { waitUntil: 'domcontentloaded' });
          await themeSettled(e.page);
          await e.page.waitForSelector('tr[data-hops]');
          const rows = await e.page.locator('tr[data-hops]').count();
          let found = false;
          for (let i = 0; i < Math.min(rows, 25) && !found; i++) {
            await e.page.locator('tr[data-hops]').nth(i).click();
            await e.page.waitForSelector('#subpathDetail .subpath-detail-inner');
            found = await e.page.locator('#subpathMap').count() > 0;
          }
          assert(found, 'no route with a map among the first 25 of ' + rows);
          await drawn(e.page, 'cartocdn', '#subpathMap');
          for (const t of [d, e]) {
            await t.page.waitForFunction(() => window.MC_MAP_CFG, null, { timeout: 15000 });
            await t.page.waitForLoadState('networkidle');
          }
          await pick(a.page, 'osmfr-hot');
          await drawn(d.page, 'openstreetmap\\.fr/hot', '#healthMap');
          await drawn(e.page, 'openstreetmap\\.fr/hot', '#subpathMap');
          await pick(a.page, '__auto__');
          for (const [t, sel] of [[d, '#healthMap'], [e, '#subpathMap']]) {
            await t.page.waitForFunction((scope) => {
              const imgs = Array.from(document.querySelectorAll(scope + ' .leaflet-tile-pane img.leaflet-tile'));
              return imgs.length > 0 && imgs.every(i => !/openstreetmap\.fr/.test(i.src)) && imgs.some(i => i.classList.contains('leaflet-tile-loaded') && /cartocdn/.test(i.src));
            }, sel, { timeout: 15000 });
            assert(t.errors.length === 0, 'page errors: ' + t.errors);
          }
        });
      } finally { await a.context.close(); }
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

    await test('Home keeps its open health card and map when the server theme arrives (#2464)', async () => {
      const c = await newPage(browser);
      try {
        const { page } = c;
        const releaseTheme = await holdTheme(page);
        const nodes = await (await page.request.get(BASE + '/api/nodes?limit=200')).json();
        const node = nodes.nodes.find(n => n.name && n.lat && n.lon);
        assert(node, 'fixture needs a named node with coordinates');
        await MH.presetStorage(page, { extra: {
          'meshcore-user-level': 'experienced',
          'meshcore-my-nodes': JSON.stringify([{ pubkey: node.public_key, name: node.name }]),
        } });
        await page.goto(BASE + '/#/home', { waitUntil: 'domcontentloaded', timeout: 60000 });
        await page.locator('.mnc-btn[data-action="health"]').click();
        const card = page.locator('#homeHealth.visible');
        await card.locator('#healthMap .leaflet-tile-loaded').first().waitFor();
        const packet = card.locator('.timeline-item').first();
        const journey = page.locator('#homeJourney.visible');
        await packet.press('Enter');
        const original = await journey.innerText();
        assert(original.includes(node.name), 'packet journey must show the selected sender');
        await releaseTheme();
        await page.locator('.home-footer-link', { hasText: 'Route guide' }).waitFor();
        assert(await card.getByText(node.name, { exact: true }).first().isVisible(),
          'the selected node health card disappeared after the theme refresh');
        await card.locator('#healthMap .leaflet-tile-loaded').first().waitFor();
        assert(await page.evaluate(() => window.__mcHarness.maps.length) === 1, 'expected one live health map');
        assert(await journey.isVisible() && await journey.innerText() === original, 'the open packet journey changed');
        await page.locator('.mnc-btn[data-action="health"]').click();
        assert(!await journey.isVisible(), 'opening health again must close the old journey');
        await packet.click();
        assert(await journey.innerText() === original, 'the refreshed timeline must still open the packet');
        assert(c.errors.length === 0, 'page errors: ' + c.errors);
        await page.evaluate(() => { location.hash = '/packets'; });
        await card.waitFor({ state: 'detached' });
        assert(await page.evaluate(() => window.__mcNodeTileSync.maps.length) === 0, 'closed health map is still registered for tile changes');
      } finally { await c.context.close(); }
    });

    await test('Route Patterns keeps the latest route and map and applies the new distance unit (#2464)', async () => {
      const c = await newPage(browser);
      try {
        const { page } = c;
        const releaseTheme = await holdTheme(page);
        await MH.presetStorage(page, { extra: { 'meshcore-distance-unit': 'mi' } });
        await openRoutePatterns(page);
        const rows = page.locator('#sp-quads tr[data-hops]');
        await rows.first().click();
        const latest = rows.nth(1);
        const hops = await latest.getAttribute('data-hops');
        await latest.click();
        const detail = page.locator('#subpathDetail:not(.collapsed)');
        const prefix = detail.getByText(hops.split(',').join(' → '), { exact: true });
        await prefix.waitFor();
        await detail.locator('#subpathMap .leaflet-tile-loaded').first().waitFor();
        assert(/\bmi\b/.test(await detail.textContent()), 'fixture route must have distances in miles before refresh');
        await releaseTheme();
        assert(await prefix.isVisible(),
          'the selected route detail disappeared after the theme refresh');
        assert(await page.locator('tr.subpath-selected').getAttribute('data-hops') === hops, 'selection changed');
        await detail.locator('#subpathMap .leaflet-tile-loaded').first().waitFor();
        assert(/\bkm\b/.test(await detail.textContent()), 'open route distances did not change to kilometres');
        assert(await page.evaluate(() => window.__mcHarness.maps.length) === 1, 'expected one live route map');
        assert(c.errors.length === 0, 'page errors: ' + c.errors);
        await page.locator('#analyticsTabs [data-tab="hashsizes"]').click();
        assert(await page.evaluate(() => window.__mcNodeTileSync.maps.length) === 0, 'closed route map is still registered for tile changes');
      } finally { await c.context.close(); }
    });

    await test('a route that the user left before the theme arrived stays closed (#2464)', async () => {
      const c = await newPage(browser);
      try {
        const { page } = c;
        const releaseTheme = await holdTheme(page);
        await openRoutePatterns(page);
        await page.locator('#sp-quads tr[data-hops]').first().click();
        await page.locator('#subpathDetail .subpath-meta').waitFor();
        await page.locator('#analyticsTabs [data-tab="hashsizes"]').click();
        await releaseTheme();
        await page.locator('#analyticsTabs [data-tab="subpaths"]').click();
        await page.locator('#subpathList tr[data-hops]').first().waitFor();
        assert(await page.locator('#subpathDetail.collapsed').count() === 1, 'a previous route reopened');
      } finally { await c.context.close(); }
    });

    for (const excluded of [false, true]) {
      await test('the hop filter ' + (excluded ? 'closes an excluded' : 'keeps an eligible') + ' selected route (#2464)', async () => {
        const { page, context, errors, requests } = await newPage(browser);
        try {
          const releaseTheme = await holdTheme(page);
          await openRoutePatterns(page);
          const rows = page.locator('#subpathList tr[data-hops]');
          await rows.first().waitFor();
          const group = page.locator((excluded ? '#sp-quads' : '#sp-pairs') + ' tr[data-hops]');
          const hops = (await rowHops(group)).find(hops => hasShortHop(hops) === excluded);
          assert(hops, 'fixture needs routes with and without one-byte hops');
          await page.locator('tr[data-hops="' + hops + '"]').first().click();
          const tiles = page.locator('#subpathMap .leaflet-tile-loaded').first();
          await tiles.waitFor();
          await page.evaluate(() => {
            invalidateApiCache('/analytics/subpath-detail');
            window.MC_setHide1ByteHops(true);
          });
          await releaseTheme();
          await rows.first().waitFor();
          assert(!(await rowHops(rows)).some(hasShortHop), 'filtered list still contains a one-byte hop');
          if (excluded) {
            assert(await page.locator('tr.subpath-selected').count() === 0 && await page.locator('#subpathDetail.collapsed').count() === 1,
              'excluded selection stayed open');
          } else {
            const selected = page.locator('tr.subpath-selected');
            assert(await selected.count() === 1 && await selected.getAttribute('data-hops') === hops, 'eligible selection lost');
            assert(await page.locator('#subpathDetail .hop-prefix').first().textContent() === hops.split(',').join(' → '), 'wrong route detail');
            await tiles.waitFor();
          }
          assert(await page.evaluate(() => window.__mcHarness.maps.length) === (excluded ? 0 : 1), 'unexpected live route map count');
          assert(requests.filter(url => url.includes('/subpath-detail?')).length === 1, 'expected a single route detail request');
          assert(errors.length === 0, 'page errors: ' + errors);
        } finally { await context.close(); }
      });
    }

    await test('the late server theme retries a failed Route Patterns list using the current filters (#2464)', async () => {
      const { page, context, errors } = await newPage(browser);
      try {
        const releaseTheme = await holdTheme(page);
        let attempts = 0;
        await page.route('**/api/analytics/subpaths-bulk?**', route => ++attempts === 1
          ? route.fulfill({ status: 500, body: 'Temporary list failure' }) : route.continue());
        await openRoutePatterns(page);
        const error = page.getByText(/Error loading subpath data/);
        await error.waitFor();
        await page.evaluate(() => window.MC_setHide1ByteHops(true));
        await releaseTheme();
        assert(!await error.isVisible(), 'route list still shows its error after ' + attempts + ' request(s)');
        const rows = page.locator('#subpathList tr[data-hops]');
        await rows.first().waitFor();
        assert(attempts === 2 && !(await rowHops(rows)).some(hasShortHop), 'expected one retry showing only eligible rows');
        assert(await page.locator('#analyticsTabs .active').getAttribute('data-tab') === 'subpaths', 'retry changed tabs');
        assert(errors.length === 0, 'page errors: ' + errors);
      } finally { await context.close(); }
    });
  } finally {
    await browser.close();
  }
  const secs = ((Date.now() - T0) / 1000).toFixed(1);
  console.log(`\nVector branch: ${vectorBranch}`);
  console.log(`${passed} passed, ${failed} failed (${secs}s)`);
  process.exit(failed > 0 ? 1 : 0);
}

run().catch(e => { console.error(e); process.exit(1); });

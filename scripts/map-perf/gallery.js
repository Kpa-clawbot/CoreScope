#!/usr/bin/env node
/* gallery.js — screenshot gallery of every map type on the synthetic dataset
 * with REAL tiles (#2428).
 *
 *   node scripts/map-perf/gallery.js --base-url http://localhost:13591 \
 *        --out test-results/map-perf/gallery [--only id,id]
 *
 * Live mode only (tile requests pass through, at most two in flight, see
 * map-harness.js). Gentle by design: one view per map type, one page load
 * each, no pan/zoom sweeps. State is set through localStorage before load
 * (theme, provider pick, overlays, 'map-view'). Each entry waits for the
 * tiles (and the first GL idle for vector maps), then screenshots at
 * 1280x800 and writes a 320px-wide thumbnail. An entry FAILS (the run goes
 * on, the exit code is 1) when tiles failed to load or a vector map fell back
 * to raster. Writes <out>/index.json, <out>/*.png, <out>/thumbs/*.png.
 *
 * The 'custom' entries inject one raster provider through /api/config/client
 * and answer its tiles from a labelled fake tile server routed in Playwright
 * (no network).
 *
 * The 'ducting' entries (tropospheric ducting overlay) need a server with a
 * `ducting` config block and a finished /api/ducting; they are skipped, with
 * the reason in index.json, otherwise. Their views come from the cells of
 * /api/ducting, not from VIEWS (so a fixture server works), and a view whose
 * nodes are missing skips its entries instead of failing the run. Start such a
 * server with the fake Open-Meteo described in tests/e2e/test-map-ducting-e2e.js.
 * Run only them: --only ducting-map-light,ducting-map-dark,ducting-zoom-light,
 * ducting-popup-light,ducting-live-light
 */
'use strict';
const fs = require('fs');
const path = require('path');
const { chromium } = require('playwright');
const MH = require('../../tests/e2e/lib/map-harness');
const { VECTOR_PROBE } = require('./run');

const VIEWPORT = { width: 1280, height: 800 };
const THUMB_W = 320;
const FAKE_TILE_PATH = '/__fake-tiles/';
const CUSTOM = { id: 'gallery', label: 'Gallery fake tiles', type: 'light', category: 'street', format: 'raster', url: FAKE_TILE_PATH + '{z}/{x}/{y}.png', attribution: 'Fake labelled test tiles', maxZoom: 19 };

// Where to look, from the dataset: a bounding box per view; the centre is the
// median node position inside it. Mountains for topo/hillshade/satellite
// (Denver: Front Range west of the plains), a dense city for the rest (NYC).
const VIEWS = {
  mountains: { label: 'Denver / Front Range', bbox: { lat: [39, 41], lon: [-106.5, -104] }, zoom: 10 },
  city: { label: 'New York', bbox: { lat: [40.3, 41.2], lon: [-74.5, -73.5] }, zoom: 11 },
};

// Views for the ducting entries, resolved from /api/ducting (resolveDuctingViews).
// 'ducting' = the densest cluster of coloured cells, 'ducting-surface' = a
// surface-duct cell (one that can carry LoRa when there is one).
const DUCTING_VIEWS = {
  ducting: { label: 'Ducting cells', zoom: 7 },
  'ducting-surface': { label: 'A surface-duct cell', zoom: 8 },
};

// ── Data table ──────────────────────────────────────────────────────────────
// { id, title, category, route, theme, provider ('auto' = shipped default),
//   overlays, view, picker?: true (open the layers control), custom?: true,
//   vector?: true (wait for GL idle; fail on raster fallback) }
const E = [];
function add(category, id, title, o) {
  E.push(Object.assign({ id, title, category, route: '#/map', overlays: [], view: 'city' }, o));
}
const both = (category, base, title, provider, o) => {
  for (const theme of ['light', 'dark']) {
    const p = typeof provider === 'function' ? provider(theme) : provider;
    add(category, `${base}-${theme}`, `${title} (${theme})`, Object.assign({ theme, provider: p }, o));
  }
};
both('street', 'street-carto', 'Street: Carto (default)', (t) => 'carto-' + t);
// Light-only providers (type 'light') are not offered in the dark picker.
const light = (category, id, title, provider, o) => add(category, `${id}-light`, `${title} (light)`, Object.assign({ theme: 'light', provider }, o));
const dark = (category, id, title, provider, o) => add(category, `${id}-dark`, `${title} (dark)`, Object.assign({ theme: 'dark', provider }, o));
light('street', 'street-osmfr-hot', 'Street: OSM France Humanitarian', 'osmfr-hot');
both('gray', 'gray-osmfr', 'Gray: OSM France', (t) => t === 'dark' ? 'osmfr-gray-dark' : 'osmfr-gray');
light('gray', 'gray-versatiles', 'Gray: VersaTiles', 'versatiles-gray', { vector: true });
dark('gray', 'gray-versatiles', 'Gray: VersaTiles', 'versatiles-gray-dark', { vector: true });
light('gray', 'gray-openfreemap', 'Gray: OpenFreeMap Positron', 'openfreemap-positron', { vector: true });
dark('gray', 'gray-openfreemap', 'Gray: OpenFreeMap Dark', 'openfreemap-dark', { vector: true });
light('topo', 'topo-cyclosm', 'Topo: CyclOSM', 'osmfr-cyclosm', { view: 'mountains' });
light('topo', 'topo-eox-terrain', 'Topo: EOX Terrain Light', 'eox-terrain-light', { view: 'mountains' });
light('topo', 'topo-opentopomap', 'Topo: OpenTopoMap (opt-in)', 'opentopomap', { view: 'mountains', optional: true, enable: ['opentopomap'] });
// Satellite is never colour-inverted; the second theme only shows the page chrome.
for (const theme of ['light', 'dark']) {
  add('satellite', `sat-s2-2025-${theme}`, `Satellite: Sentinel-2 2025 (${theme})`, { theme, provider: 'eox-s2-2025', view: 'mountains' });
  add('satellite', `sat-s2-2025-labels-${theme}`, `Satellite: Sentinel-2 2025 with labels (${theme})`, { theme, provider: 'eox-s2-2025', overlays: ['eox-labels'], view: 'mountains' });
}
add('satellite', 'sat-s2-2016-light', 'Satellite: Sentinel-2 2016 (light)', { theme: 'light', provider: 'eox-s2-2016', view: 'mountains' });
add('satellite', 'sat-s2-2016-labels-light', 'Satellite: Sentinel-2 2016 with labels (light)', { theme: 'light', provider: 'eox-s2-2016', overlays: ['eox-labels'], view: 'mountains' });
light('vector', 'vector-liberty', 'Vector: OpenFreeMap Liberty', 'openfreemap-liberty', { vector: true });
dark('vector', 'vector-dark', 'Vector: OpenFreeMap Dark', 'openfreemap-dark', { vector: true });
light('vector', 'vector-versatiles-colorful', 'Vector: VersaTiles Colorful', 'versatiles-colorful', { vector: true });
dark('vector', 'vector-versatiles-colorful', 'Vector: VersaTiles Colorful Dark', 'versatiles-colorful-dark', { vector: true });
for (const theme of ['light', 'dark']) {
  add('overlay', `overlay-hillshade-s2-${theme}`, `Overlay: hillshade over Sentinel-2 (${theme})`, { theme, provider: 'eox-s2-2025', overlays: ['hillshade'], view: 'mountains' });
  add('overlay', `overlay-hillshade-carto-${theme}`, `Overlay: hillshade over Carto (${theme})`, { theme, provider: 'carto-' + theme, overlays: ['hillshade'], view: 'mountains' });
}
for (const theme of ['light', 'dark']) {
  add('planner', `planner-s2-hillshade-${theme}`, `Planner: Sentinel-2 + hillshade (${theme})`, { theme, route: '#/tools/planner', provider: 'eox-s2-2025', overlays: ['hillshade'], view: 'mountains' });
}
for (const theme of ['light', 'dark']) {
  add('picker', `picker-${theme}`, `Layer picker open (${theme})`, { theme, provider: 'auto', picker: true });
}
// Tropospheric ducting overlay (opt-in, server needs `ducting`).
for (const theme of ['light', 'dark']) {
  add('ducting', `ducting-map-${theme}`, `Ducting overlay with legend (${theme})`, { theme, provider: 'auto', overlays: ['ducting'], view: 'ducting', ducting: true });
}
add('ducting', 'ducting-zoom-light', 'Ducting overlay, zoomed on coloured cells (light)', { theme: 'light', provider: 'auto', overlays: ['ducting'], view: 'ducting-surface', ducting: true });
add('ducting', 'ducting-popup-light', 'Ducting overlay, popup on a surface duct (light)', { theme: 'light', provider: 'auto', overlays: ['ducting'], view: 'ducting-surface', ducting: true, ductPopup: true });
add('ducting', 'ducting-live-light', 'Live page with the ducting overlay (light)', { theme: 'light', route: '#/live', provider: 'auto', overlays: ['ducting'], view: 'ducting', ducting: true });
add('custom', 'custom-map', 'Custom raster provider on the map', { theme: 'light', provider: 'custom-' + CUSTOM.id, custom: true });
add('custom', 'custom-picker', 'Custom raster provider in the picker', { theme: 'light', provider: 'custom-' + CUSTOM.id, custom: true, picker: true });

// ── Helpers ─────────────────────────────────────────────────────────────────
async function fetchJson(url) {
  const r = await fetch(url, { signal: AbortSignal.timeout(60000) });
  if (!r.ok) throw new Error(`${url} -> HTTP ${r.status}`);
  return r.json();
}

function median(a) { const s = a.slice().sort((x, y) => x - y); return s[Math.floor(s.length / 2)]; }

/** Resolve each VIEWS entry to { lat, lon, zoom, nodes } from the node list. */
async function resolveViews(baseUrl) {
  const d = await fetchJson(baseUrl + '/api/nodes?limit=2000');
  const nodes = (d.nodes || []).filter((n) => typeof n.lat === 'number' && typeof n.lon === 'number' && (n.lat || n.lon));
  const out = {};
  for (const k of Object.keys(VIEWS)) {
    const v = VIEWS[k];
    const inside = nodes.filter((n) => n.lat >= v.bbox.lat[0] && n.lat <= v.bbox.lat[1] && n.lon >= v.bbox.lon[0] && n.lon <= v.bbox.lon[1]);
    if (!inside.length) { out[k] = null; continue; }   // entries using it are skipped
    out[k] = { label: v.label, lat: +median(inside.map((n) => n.lat)).toFixed(4), lon: +median(inside.map((n) => n.lon)).toFixed(4), zoom: v.zoom, nodes: inside.length };
  }
  return out;
}

/**
 * The ducting views, from the cells of /api/ducting: { views, reason }.
 * `reason` is why the ducting entries cannot be captured ('' = they can).
 */
async function resolveDuctingViews(baseUrl) {
  const out = { views: {}, reason: '' };
  let cfg;
  try { cfg = await fetchJson(baseUrl + '/api/config/client'); } catch (err) { out.reason = 'cannot read /api/config/client: ' + err.message; return out; }
  if (!cfg || !cfg.ducting) { out.reason = 'the server has no ducting block in /api/config/client (ducting.enabled is off)'; return out; }
  let d = null;
  for (let i = 0; i < 40; i++) {
    try { d = await fetchJson(baseUrl + '/api/ducting'); } catch (_) { d = null; }
    if (d && d.status === 'ok') break;
    await new Promise((r) => setTimeout(r, 500));
  }
  if (!d || d.status !== 'ok' || !d.cells || !d.cells.length) { out.reason = `/api/ducting has no data (status ${d && d.status}${d && d.error ? ': ' + d.error : ''})`; return out; }
  const idx = (() => { const now = Date.now() / 1000; let b = 0; d.times.forEach((t, i) => { if (t <= now) b = i; }); return b; })();
  // Centre on the densest cluster of coloured cells (class >= 2) at the
  // current hour: with nodes worldwide, the median of all cells is often in
  // an empty area. Falls back to that median when nothing is coloured.
  const step = Number(d.stepDeg) || 0.5;
  const coloured = d.cells.filter((c) => c.c[idx] >= 2);
  let lat = median(d.cells.map((c) => c.lat)), lon = median(d.cells.map((c) => c.lon));
  let best = null;
  coloured.forEach((c) => {
    const near = coloured.filter((o) => Math.abs(o.lat - c.lat) <= 4 * step && Math.abs(o.lon - c.lon) <= 4 * step);
    if (!best || near.length > best.length) best = near;
  });
  if (best) {
    lat = best.reduce((a, c) => a + c.lat, 0) / best.length;
    lon = best.reduce((a, c) => a + c.lon, 0) / best.length;
  }
  out.views.ducting = { label: DUCTING_VIEWS.ducting.label, lat: +lat.toFixed(4), lon: +lon.toFixed(4), zoom: DUCTING_VIEWS.ducting.zoom, nodes: best ? best.length : d.cells.length };
  // A surface duct that can carry LoRa (fmin <= 1000 MHz) when there is one.
  const surf = d.cells.find((c) => c.c[idx] === 4 && c.f[idx] > 0 && c.f[idx] <= 1000) ||
    d.cells.find((c) => c.c[idx] === 4) || d.cells.find((c) => c.c.indexOf(4) >= 0) || d.cells[0];
  out.views['ducting-surface'] = { label: DUCTING_VIEWS['ducting-surface'].label, lat: surf.lat, lon: surf.lon, zoom: DUCTING_VIEWS['ducting-surface'].zoom, nodes: 1 };
  return out;
}

/** Fake labelled raster tile: a grey square naming z/x/y. */
function fakeTile(z, x, y) {
  const hue = (x * 37 + y * 91) % 360;
  return `<svg xmlns="http://www.w3.org/2000/svg" width="256" height="256"><rect width="256" height="256" fill="hsl(${hue},35%,82%)" stroke="#556" stroke-width="2"/>` +
    `<text x="128" y="120" text-anchor="middle" font-family="sans-serif" font-size="22" fill="#223">FAKE TILE</text>` +
    `<text x="128" y="150" text-anchor="middle" font-family="monospace" font-size="18" fill="#223">${z}/${x}/${y}</text></svg>`;
}

async function installConfig(page, e) {
  // Registered after installTileStub, so Playwright tries these routes first.
  // e.custom adds the fake custom provider; e.enable switches on opt-in groups
  // (e.g. opentopomap) for this screenshot only — the server config is unchanged.
  await page.context().route('**/api/config/client', async (route) => {
    const res = await route.fetch();
    const body = await res.json();
    body.map = body.map || {};
    const tiles = Object.assign({}, body.map.tiles);
    if (e.custom) tiles.custom = [CUSTOM];
    if (e.enable) {
      tiles.providers = Object.assign({}, tiles.providers);
      for (const g of e.enable) tiles.providers[g] = Object.assign({}, tiles.providers[g], { enabled: true });
    }
    body.map.tiles = tiles;
    await route.fulfill({ response: res, json: body });
  });
  if (!e.custom) return;
  await page.context().route((u) => u.pathname.startsWith(FAKE_TILE_PATH), async (route) => {
    const m = new URL(route.request().url()).pathname.slice(FAKE_TILE_PATH.length).match(/^(\d+)\/(\d+)\/(\d+)\.png$/);
    if (!m) return route.fulfill({ status: 404, body: '' });
    return route.fulfill({ status: 200, contentType: 'image/svg+xml', headers: { 'cache-control': 'no-store' }, body: fakeTile(+m[1], +m[2], +m[3]) });
  });
}

// The map fills ~1230x750 px: at least 5x3 tiles. waitForTiles alone can
// declare victory while Leaflet has only created the first few (and while the
// page is still moving to the saved view), so require a full screen of tiles,
// none pending or failed, unchanged for 3 polls of 500 ms.
const MIN_TILES = 12;
async function settle(page, minTiles) {
  const t0 = Date.now();
  let last = '', same = 0, s = null;
  while (Date.now() - t0 < 60000) {
    s = await page.evaluate(() => window.__mcHarness.tileState());
    const key = [s.visible, s.loaded, s.pending, s.failed].join('/');
    same = (key === last && s.visible >= minTiles && s.pending === 0) ? same + 1 : 0;
    last = key;
    if (same >= 3) return Object.assign({}, s, { ms: Date.now() - t0, timedOut: false });
    await page.waitForTimeout(500);
  }
  return Object.assign({}, s, { ms: Date.now() - t0, timedOut: true });
}

async function openPicker(page) {
  await page.evaluate(() => {
    const c = document.querySelector('.leaflet-control-layers');
    if (c) c.classList.add('leaflet-control-layers-expanded');
  });
  await page.waitForTimeout(300);
}

async function capture(browser, e, views, outDir, base) {
  const rec = { id: e.id, file: null, thumb: null, title: e.title, category: e.category, theme: e.theme, provider: e.provider, overlays: e.overlays, route: e.route, view: null, status: 'ok', failures: [], tiles: null, vectorPath: null };
  const v = views[e.view];
  if (!v) {
    rec.status = 'skipped';
    rec.failures.push(e.ducting ? `ducting entries skipped: ${views.__ductingReason || 'no view'}` : `no nodes in the '${e.view}' view box`);
    return rec;
  }
  rec.view = { name: v.label, lat: v.lat, lon: v.lon, zoom: v.zoom };
  // The open picker is taller than 800 px (its Overlays group is below the fold).
  const context = await browser.newContext({ viewport: e.picker ? { width: VIEWPORT.width, height: 1100 } : VIEWPORT, deviceScaleFactor: 1, serviceWorkers: 'block' });
  const page = await context.newPage();
  page.setDefaultTimeout(30000);
  const pageErrors = [];
  page.on('pageerror', (err) => { if (pageErrors.length < 10) pageErrors.push(String(err && err.message || err)); });
  try {
    await MH.installCollectors(page);
    if (e.vector) await page.addInitScript(VECTOR_PROBE);
    await MH.installTileStub(page, { mode: 'live', baseUrl: base });
    if (e.custom || e.enable) await installConfig(page, e);
    await MH.presetStorage(page, {
      theme: e.theme, provider: e.provider, overlays: e.overlays,
      extra: { 'map-view': JSON.stringify({ lat: v.lat, lng: v.lon, zoom: v.zoom }), 'live-map-view': JSON.stringify({ lat: v.lat, lng: v.lon, zoom: v.zoom }), 'live-controls-expanded': 'true' },
    });
    await page.goto(base + '/' + e.route, { waitUntil: 'domcontentloaded', timeout: 60000 });
    await page.waitForSelector('.leaflet-container', { timeout: 60000 });
    // The pick must be registered (opentopomap is opt-in; a build may lack ids).
    const picked = e.provider === 'auto' ? true : await page.waitForFunction((id) => !!(window.MC_TILE_PROVIDERS && window.MC_TILE_PROVIDERS[id]), e.provider, { timeout: 10000 }).then(() => true, () => false);
    if (!picked) { rec.status = 'skipped'; rec.failures.push(`provider '${e.provider}' is not registered`); return rec; }
    for (const o of e.overlays) {
      const ok = await page.waitForFunction((id) => !!(window.MC_TILE_OVERLAYS && window.MC_TILE_OVERLAYS[id]), o, { timeout: 10000 }).then(() => true, () => false);
      if (!ok) { rec.status = 'skipped'; rec.failures.push(`overlay '${o}' is not registered`); return rec; }
    }
    await MH.waitForMarkers(page, { timeout: 30000 });
    if (e.ducting) {
      const drawn = await page.waitForFunction(() => {
        const maps = ((window.__mcHarness && window.__mcHarness.maps) || []).filter((m) => m && m._container && m._container.isConnected && m._loaded);
        let n = 0; maps.forEach((m) => m.eachLayer((l) => { if (l._mcCell) n++; }));
        return n > 0 && !!document.querySelector('.mc-duct-legend');
      }, null, { timeout: 20000 }).then(() => true, () => false);
      if (!drawn) rec.failures.push('ducting cells or legend not drawn');
    }
    if (e.vector) {
      await page.waitForFunction(() => { const p = window.__mcVector; return p && (p.glIdleMs != null || p.fallback); }, null, { timeout: 45000, polling: 100 }).catch(() => {});
    }
    const t = await settle(page, e.vector ? 0 : MIN_TILES);
    if (e.ductPopup) {
      const opened = await page.evaluate(() => {
        const m = window.__mcHarness.maps.filter((x) => x._container && x._container.isConnected && x._loaded)[0];
        const s = document.querySelector('.mc-duct-time input[type=range]');
        const idx = s ? parseInt(s.value, 10) : 0;
        // The surface-duct cell nearest the view centre (the restored view
        // need not be exactly on the cell centre).
        const c = m.getCenter();
        let hit = null, best = Infinity;
        m.eachLayer((l) => {
          if (!l._mcCell || l._mcCell.c[idx] !== 4) return;
          const d = Math.pow(l._mcCell.lat - c.lat, 2) + Math.pow(l._mcCell.lon - c.lng, 2);
          if (d < best) { best = d; hit = l; }
        });
        if (!hit) return false;
        hit.openPopup([hit._mcCell.lat, hit._mcCell.lon]);
        return !!document.querySelector('.leaflet-popup .mc-duct-popup');
      });
      if (!opened) rec.failures.push('no surface-duct cell popup could be opened');
      await page.waitForTimeout(300);
    }
    if (e.picker) await openPicker(page);
    const probe = e.vector ? await page.evaluate(() => window.__mcVector) : null;
    const log = MH.getRequestLog(page);
    const bad = log.intercepted.filter((r) => r.status >= 400);
    const resolved = await page.evaluate((th) => { try { return th === 'dark' ? window.MC_getDarkTileProvider() : window.MC_getLightTileProvider(); } catch (_) { return null; } }, e.theme).catch(() => null);
    rec.resolvedProvider = resolved;
    rec.tiles = { visible: t.visible, loaded: t.loaded, failed: t.failed, pending: t.pending, requests: log.intercepted.length, httpErrors: bad.length, requestFailures: log.failed.length, byHost: log.counts, waitMs: t.ms };
    if (e.vector) rec.vectorPath = probe && probe.fallback ? 'raster-fallback' : (probe && probe.glIdleMs != null ? 'maplibre-gl' : 'maplibre-gl-no-idle');
    if (t.timedOut) rec.failures.push('tiles did not settle within 45 s');
    if (t.failed) rec.failures.push(`${t.failed} tile(s) failed to load`);
    if (bad.length) rec.failures.push(`${bad.length} tile request(s) with HTTP >= 400 (first: ${bad[0].status} ${MH.redactUrl(bad[0].url)})`);
    // The live page builds its map at a default view and then jumps to the saved
    // one, so Chromium cancels the first view's tile requests: not a failure.
    const failedReq = log.failed.filter((r) => !(e.route === '#/live' && /ERR_ABORTED/.test(r.error)));
    if (failedReq.length) rec.failures.push(`${failedReq.length} request(s) failed (first: ${failedReq[0].error})`);
    if (e.vector && rec.vectorPath !== 'maplibre-gl') rec.failures.push(`vector path: ${rec.vectorPath}${probe && probe.fallbackReason ? ' (' + probe.fallbackReason + ')' : ''}`);
    if (!e.vector && log.maplibre.length) rec.failures.push('raster entry requested MapLibre');
    if (resolved && e.provider !== 'auto' && resolved !== e.provider) rec.failures.push(`resolved provider is '${resolved}', expected '${e.provider}'`);
    if (pageErrors.length) rec.pageErrors = pageErrors;
    const p = await MH.screenshot(page, outDir, e.id);
    rec.file = path.basename(p);
    if (rec.failures.length) rec.status = 'failed';
  } catch (err) {
    rec.status = 'failed';
    rec.failures.push('error: ' + String(err && err.message || err).split('\n')[0]);
    try { rec.file = path.basename(await MH.screenshot(page, outDir, e.id)); } catch (_) { /* page gone */ }
  } finally {
    await context.close().catch(() => {});
  }
  return rec;
}

/** Downscale a PNG to THUMB_W wide in the browser (canvas); no extra deps. */
async function makeThumb(browser, file, outFile) {
  const ctx = await browser.newContext({ viewport: { width: 400, height: 300 } });
  try {
    const page = await ctx.newPage();
    const b64 = fs.readFileSync(file).toString('base64');
    const data = await page.evaluate(async (src) => {
      const img = new Image();
      await new Promise((res, rej) => { img.onload = res; img.onerror = rej; img.src = src; });
      const w = 320, h = Math.round(img.naturalHeight * w / img.naturalWidth);
      const c = document.createElement('canvas');
      c.width = w; c.height = h;
      const g = c.getContext('2d');
      g.imageSmoothingQuality = 'high';
      g.drawImage(img, 0, 0, w, h);
      return c.toDataURL('image/png').split(',')[1];
    }, 'data:image/png;base64,' + b64);
    fs.writeFileSync(outFile, Buffer.from(data, 'base64'));
  } finally { await ctx.close().catch(() => {}); }
}

function parseArgs(argv) {
  const a = {};
  for (let i = 2; i < argv.length; i++) {
    if (argv[i] === '--base-url') a.baseUrl = argv[++i];
    else if (argv[i] === '--out') a.out = argv[++i];
    else if (argv[i] === '--only') a.only = argv[++i];
    else throw new Error('unknown argument: ' + argv[i]);
  }
  return a;
}

// --only reruns keep the other entries of an earlier index.json (table order).
function writeIndex(outDir, base, views, entries) {
  const file = path.join(outDir, 'index.json');
  let old = [];
  try { old = JSON.parse(fs.readFileSync(file, 'utf8')).entries || []; } catch (_) { /* first run */ }
  const fresh = new Map(entries.map((x) => [x.id, x]));
  const prev = new Map(old.map((x) => [x.id, x]));
  const merged = E.map((e) => fresh.get(e.id) || prev.get(e.id)).filter(Boolean);
  fs.writeFileSync(file, JSON.stringify({ meta: { baseUrl: base, generatedAt: new Date().toISOString(), viewport: VIEWPORT, mode: 'live', views: Object.fromEntries(Object.entries(views).filter(([k]) => !k.startsWith('__'))) }, entries: merged }, null, 2));
}

async function main() {
  const a = parseArgs(process.argv);
  if (!a.baseUrl || !a.out) throw new Error('usage: node scripts/map-perf/gallery.js --base-url <url> --out <dir> [--only id,id]');
  const base = a.baseUrl.replace(/\/+$/, '');
  const outDir = path.resolve(a.out);
  fs.mkdirSync(path.join(outDir, 'thumbs'), { recursive: true });
  const views = await resolveViews(base);
  const dv = await resolveDuctingViews(base);
  Object.assign(views, dv.views);
  if (dv.reason) views.__ductingReason = dv.reason;
  let list = E;
  if (a.only) { const want = new Set(a.only.split(',')); list = E.filter((e) => want.has(e.id)); if (!list.length) throw new Error('--only matches nothing'); }
  const browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM_PATH || undefined, args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'] });
  const entries = [];
  try {
    for (const e of list) {
      let rec = await capture(browser, e, views, outDir, base);
      // Community tile servers are sometimes slow: retry a tile failure once.
      if (rec.status === 'failed' && rec.failures.some((f) => /tile|request/.test(f))) {
        rec = await capture(browser, e, views, outDir, base);
        rec.retried = true;
      }
      if (rec.file) { rec.thumb = 'thumbs/' + rec.file; await makeThumb(browser, path.join(outDir, rec.file), path.join(outDir, rec.thumb)).catch((err) => rec.failures.push('thumbnail: ' + err.message)); }
      entries.push(rec);
      console.log(`${rec.status.padEnd(7)} ${e.id.padEnd(34)}${rec.tiles ? ` tiles ${rec.tiles.loaded}/${rec.tiles.visible} failed ${rec.tiles.failed}` : ''}${rec.vectorPath ? ' ' + rec.vectorPath : ''}${rec.failures.length ? '  <- ' + rec.failures.join('; ') : ''}`);
      writeIndex(outDir, base, views, entries);
      await new Promise((r) => setTimeout(r, 1000)); // gentle on the tile hosts
    }
  } finally { await browser.close().catch(() => {}); }
  const failed = entries.filter((x) => x.status === 'failed');
  console.log(`[gallery] ${entries.length} entries: ${entries.filter((x) => x.status === 'ok').length} ok, ${failed.length} failed, ${entries.filter((x) => x.status === 'skipped').length} skipped -> ${path.join(outDir, 'index.json')}`);
  if (failed.length) process.exitCode = 1;
}

if (require.main === module) main().catch((e) => { console.error(e && e.stack || e); process.exit(1); });
module.exports = { ENTRIES: E, VIEWS };

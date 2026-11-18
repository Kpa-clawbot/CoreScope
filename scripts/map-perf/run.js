#!/usr/bin/env node
/* run.js — map performance runner (#2428).
 *
 *   node scripts/map-perf/run.js --base-url <url> --label <name> --out <dir>
 *        [--runs 5] [--warmup 1] [--scenarios all|a,b] [--mode stub|live]
 *
 * Every scenario in scenarios.js runs --runs times (after --warmup discarded
 * runs), each in a fresh browser context, with all tile/style requests
 * stubbed (default). Writes <out>/<label>/results.json (raw runs + median/p95
 * summary) and screenshots to <out>/<label>/shots/. A scenario whose route or
 * prerequisite is absent is recorded as skipped with a reason, not a crash.
 * results.json is rewritten after every scenario, so an interrupted run keeps
 * what it finished.
 */
'use strict';
const fs = require('fs');
const os = require('os');
const path = require('path');
const { chromium } = require('playwright');
const MH = require('../../tests/e2e/lib/map-harness');
const { SURFACES, SCENARIOS } = require('./scenarios');

function parseArgs(argv) {
  const a = { runs: 5, warmup: 1, scenarios: 'all', mode: 'stub' };
  for (let i = 2; i < argv.length; i++) {
    const k = argv[i];
    const v = argv[i + 1];
    switch (k) {
      case '--base-url': a.baseUrl = v; i++; break;
      case '--label': a.label = v; i++; break;
      case '--out': a.out = v; i++; break;
      case '--runs': a.runs = parseInt(v, 10); i++; break;
      case '--warmup': a.warmup = parseInt(v, 10); i++; break;
      case '--scenarios': a.scenarios = v; i++; break;
      case '--mode': a.mode = v; i++; break;
      case '-h': case '--help': a.help = true; break;
      default: throw new Error('unknown argument: ' + k);
    }
  }
  return a;
}

const USAGE = 'usage: node scripts/map-perf/run.js --base-url <url> --label <name> --out <dir> [--runs 5] [--warmup 1] [--scenarios all|a,b] [--mode stub|live]';

class SkipError extends Error {}
const skip = (reason) => { throw new SkipError(reason); };

// ── Stats ───────────────────────────────────────────────────────────────────
function quantile(sorted, p) {
  if (!sorted.length) return null;
  const idx = (sorted.length - 1) * p;
  const lo = Math.floor(idx), hi = Math.ceil(idx);
  return sorted[lo] + (sorted[hi] - sorted[lo]) * (idx - lo);
}
function summarize(runs) {
  const keys = new Set();
  runs.forEach(r => Object.keys(r.metrics || {}).forEach(k => { if (typeof r.metrics[k] === 'number') keys.add(k); }));
  const out = {};
  for (const k of keys) {
    const vals = runs.map(r => r.metrics[k]).filter(v => typeof v === 'number' && isFinite(v)).sort((x, y) => x - y);
    if (!vals.length) continue;
    const mean = vals.reduce((s, v) => s + v, 0) / vals.length;
    out[k] = { n: vals.length, median: quantile(vals, 0.5), p95: quantile(vals, 0.95), min: vals[0], max: vals[vals.length - 1], mean: mean };
  }
  return out;
}

// ── Prerequisites ───────────────────────────────────────────────────────────
async function fetchJson(url) {
  const r = await fetch(url, { signal: AbortSignal.timeout(60000) });
  if (!r.ok) throw new Error(`${url} -> HTTP ${r.status}`);
  return r.json();
}
async function loadContext(baseUrl) {
  const ctx = { node: null, repeater: null, notes: [] };
  try {
    const d = await fetchJson(baseUrl + '/api/nodes?limit=2000');
    const located = (d.nodes || []).filter(n => typeof n.lat === 'number' && typeof n.lon === 'number' && (n.lat !== 0 || n.lon !== 0));
    // Deterministic pick: most adverts, ties by pubkey.
    const rank = (a, b) => (b.advert_count || 0) - (a.advert_count || 0) || String(a.public_key).localeCompare(String(b.public_key));
    located.sort(rank);
    ctx.node = located[0] || null;
    // Node reach only draws a map when the repeater has links in the default
    // 7-day window: probe a few, most adverts first.
    const reps = located.filter(n => n.role === 'repeater');
    for (const n of reps.slice(0, 10)) {
      try {
        const r = await fetchJson(baseUrl + '/api/nodes/' + encodeURIComponent(n.public_key) + '/reach?days=7');
        if (r && Array.isArray(r.links) && r.links.length && r.node && r.node.lat != null) { ctx.repeater = n; break; }
      } catch (_) { /* busy or no reach for this one; try the next */ }
    }
    if (!ctx.repeater) ctx.notes.push('no located repeater among the top 10 has reach links in the last 7 days');
    if (!ctx.node) ctx.notes.push('no node with a location in /api/nodes?limit=2000');
  } catch (e) {
    ctx.notes.push('nodes prerequisite failed: ' + e.message);
  }
  try { ctx.server = await fetchJson(baseUrl + '/api/stats'); } catch (_) { ctx.server = null; }
  return ctx;
}

// ── Browser plumbing ────────────────────────────────────────────────────────
const VIEWPORT = { width: 1280, height: 800 };

async function newPage(browser, opts, sc, ctx) {
  const context = await browser.newContext({ viewport: VIEWPORT, deviceScaleFactor: 1, serviceWorkers: 'block' });
  const page = await context.newPage();
  page.setDefaultTimeout(30000);
  await MH.installCollectors(page);
  await MH.installTileStub(page, { mode: opts.mode, baseUrl: opts.baseUrl });
  if (sc.format === 'vector') await page.addInitScript(VECTOR_PROBE);
  const surface = SURFACES[sc.surface];
  // Prerequisites are checked (and the scenario skipped) in the load action.
  const ready = (surface.requires || []).every(r => ctx[r]);
  const extra = Object.assign({}, surface.storage && ready ? surface.storage(ctx) : {});
  if (sc.vcrSpeed) extra['live-vcr-speed'] = String(sc.vcrSpeed);
  extra['live-controls-expanded'] = 'true';
  await MH.presetStorage(page, { theme: sc.theme, provider: sc.provider, overlays: sc.overlays, extra: extra });
  return { context, page };
}

async function listProvidersProbe(browser, opts) {
  const sc = { surface: 'map', theme: 'light', provider: 'auto' };
  const { context, page } = await newPage(browser, opts, sc, {});
  try {
    await page.goto(opts.baseUrl + '/#/map', { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('.leaflet-container .leaflet-tile', { timeout: 60000 });
    await MH.waitForTiles(page, { timeout: 30000 });
    return await MH.listProviders(page);
  } finally { await context.close(); }
}

// In-page probe for vector scenarios (installed before any app script): polls
// every 20 ms for MapLibre readiness, the first MapLibre map, and its first GL
// idle (style + tiles loaded, not moving), and notes the layer's raster
// fallback warning (map-vector.js warnOnce).
function VECTOR_PROBE() {
  var P = window.__mcVector = { libReadyMs: null, glMapMs: null, glIdleMs: null, fallback: false, fallbackReason: null };
  var warn = console.warn;
  console.warn = function () {
    var m = String(arguments[0] || '');
    if (m.indexOf('[map-vector]') === 0) { P.fallback = true; P.fallbackReason = m.slice(0, 160); }
    return warn.apply(console, arguments);
  };
  setInterval(function () {
    var t = performance.now();
    if (P.libReadyMs == null && window.maplibregl) P.libReadyMs = t;
    var H = window.__mcHarness;
    if (!H || P.glIdleMs != null) return;
    H.maps.forEach(function (m) {
      m.eachLayer(function (ly) {
        if (typeof ly.getMaplibreMap !== 'function') return;
        var g = null;
        try { g = ly.getMaplibreMap(); } catch (_) {}
        if (!g) return;
        if (P.glMapMs == null) P.glMapMs = t;
        try { if (g.loaded() && g.areTilesLoaded() && !g.isMoving()) P.glIdleMs = t; } catch (_) {}
      });
    });
  }, 20);
}

// ── Prepare hooks (named in SURFACES[].prepare) ─────────────────────────────
const PREPARE = {
  async openSubpathMap(page, first, ctx) {
    try { await page.waitForSelector('tr[data-hops]', { timeout: 60000 }); }
    catch (_) { if (first) skip('analytics route patterns: no rows (tr[data-hops]) within 60 s'); throw new Error('no subpath rows'); }
    await MH.waitForThemeRefresh(page, { timeout: 10000 }); // analytics re-renders on it too
    // Later runs go straight to the subpath the first run found, so every run
    // measures the same map.
    if (ctx.subpathHops) {
      await page.locator(`tr[data-hops="${ctx.subpathHops.replace(/"/g, '\\"')}"]`).first().click({ timeout: 30000 });
      await page.waitForSelector('#subpathMap.leaflet-container', { timeout: 30000 });
      return;
    }
    // The table re-renders as data arrives: locate each row afresh per click.
    const count = await page.locator('tr[data-hops]').count();
    for (let i = 0; i < Math.min(count, 15); i++) {
      let hops;
      try {
        const row = page.locator('tr[data-hops]').nth(i);
        hops = await row.getAttribute('data-hops', { timeout: 10000 });
        await row.click({ timeout: 10000 });
      } catch (_) { continue; }
      try {
        await page.waitForSelector('#subpathMap.leaflet-container', { timeout: 8000 });
        ctx.subpathHops = hops;
        return;
      } catch (_) { /* this subpath had <2 located hops; try the next */ }
    }
    if (first) skip('analytics route patterns: none of the first 15 subpaths has 2+ located hops (no #subpathMap)');
    throw new Error('no subpath map');
  },
  async openHomeHealth(page, first) {
    try { await page.waitForSelector('.my-node-card', { timeout: 60000 }); }
    catch (_) { if (first) skip('home: My Nodes card did not render'); throw new Error('no my-node card'); }
    // home.js rebuilds its cards on the load-time 'theme-refresh', wiping a
    // health card opened before it (this, not the server, made earlier runs
    // take 15-31 s). Open the card after it; retry once if a later re-render
    // still wipes it.
    await MH.waitForThemeRefresh(page, { timeout: 10000 });
    for (let attempt = 0; attempt < 2; attempt++) {
      await page.waitForSelector('.my-node-card', { timeout: 30000 });
      await page.click('.my-node-card');
      try { await page.waitForSelector('#healthMap.leaflet-container', { timeout: 15000 }); }
      catch (_) { continue; }
      return;
    }
    if (first) skip('home: health card has no mini map after 2 attempts (node without location?)');
    throw new Error('no health map');
  },
};

// ── Actions ─────────────────────────────────────────────────────────────────
const ACTIONS = {
  async load(st) {
    const { page, sc, ctx, opts, first } = st;
    const surface = SURFACES[sc.surface];
    for (const r of surface.requires || []) if (!ctx[r]) skip(`prerequisite '${r}' missing: ${ctx.notes.join('; ') || 'not found'}`);
    const route = typeof surface.route === 'function' ? surface.route(ctx) : surface.route;
    st.route = route;
    const t0 = Date.now();
    await page.goto(opts.baseUrl + '/' + route, { waitUntil: 'domcontentloaded', timeout: 60000 });
    if (surface.prepare) await PREPARE[surface.prepare](page, first, ctx);
    const sel = '.leaflet-container' + (surface.unavailable ? ', ' + surface.unavailable : '');
    let el = null;
    try { el = await page.waitForSelector(sel, { timeout: 60000 }); } catch (_) { el = null; }
    const isMap = el ? await el.evaluate((e) => e.classList.contains('leaflet-container')) : false;
    if (!isMap) {
      const why = el ? `${route} shows "${((await el.innerText()) || '').trim().replace(/\s+/g, ' ').slice(0, 160)}"` : `no Leaflet map rendered on ${route} within 60 s`;
      if (first) skip(why);
      throw new Error(why);
    }
    // Candidate-only scenarios: skip when the build does not register the
    // provider/overlay ids (the upstream baseline has none of them).
    const need = sc.needs || {};
    for (const [kind, global] of [['providers', 'MC_TILE_PROVIDERS'], ['overlays', 'MC_TILE_OVERLAYS']]) {
      for (const id of need[kind] || []) {
        const ok = await page.waitForFunction(([g, i]) => !!(window[g] && window[g][i]), [global, id], { timeout: 5000 }).then(() => true, () => false);
        if (!ok) { if (first) skip(`${kind === 'providers' ? 'provider' : 'overlay'} '${id}' is not registered in this build (or is disabled)`); throw new Error(id + ' not registered'); }
      }
    }
    st.metrics.loadWallMs = Date.now() - t0;
  },
  async hillshadeKernel(st) {
    // createTile -> tileload (hillshadeTileMsMedian) includes the elevation
    // fetch and image decode. This times the shading kernel alone, on a
    // synthetic rolling-terrain tile, median of 50 runs after 5 warm-ups.
    const r = await st.page.evaluate(() => {
      if (typeof window.MC_hillshadeShade !== 'function') return null;
      const time = (n) => {
        const elev = new Float32Array(n * n), out = new Uint8ClampedArray(n * n * 4);
        for (let y = 0; y < n; y++) for (let x = 0; x < n; x++) elev[y * n + x] = 1500 + 400 * Math.sin(x / 23) * Math.cos(y / 31) + 120 * Math.sin((x + y) / 7);
        const ms = [];
        for (let i = 0; i < 55; i++) {
          const t0 = performance.now();
          window.MC_hillshadeShade(elev, n, n, 10, 40, out);
          if (i >= 5) ms.push(performance.now() - t0);
        }
        ms.sort((a, b) => a - b);
        return ms[Math.floor(ms.length / 2)];
      };
      return { k256: time(256), k512: time(512) };
    });
    if (!r) { if (st.first) skip('MC_hillshadeShade is not defined in this build'); throw new Error('no hillshade kernel'); }
    st.metrics.hillshadeKernel256MsMedian = r.k256;
    st.metrics.hillshadeKernel512MsMedian = r.k512;
  },
  async vector(st) {
    // Waits for the first GL idle (or the raster fallback), then reads the probe.
    const { page } = st;
    await page.waitForFunction(() => { const p = window.__mcVector; return p && (p.glIdleMs != null || p.fallback); }, null, { timeout: 30000, polling: 100 }).catch(() => {});
    const p = await page.evaluate(() => window.__mcVector);
    const res = await page.evaluate(() => performance.getEntriesByType('resource').filter(e => /maplibre/i.test(e.name)).map(e => e.startTime));
    st.metrics.vectorFallbackUsed = p.fallback ? 1 : 0;
    if (res.length && p.libReadyMs != null) st.metrics.maplibreLoadMs = p.libReadyMs - Math.min.apply(null, res);
    if (p.glMapMs != null) st.metrics.glMapCreatedMs = p.glMapMs;
    if (p.glIdleMs != null) { st.metrics.glFirstIdleMs = p.glIdleMs; if (p.glMapMs != null) st.metrics.glIdleSinceMapMs = p.glIdleMs - p.glMapMs; }
    if (p.fallback) st.extra.vectorFallbackReason = p.fallbackReason;
  },
  async markers(st) {
    if (!SURFACES[st.sc.surface].markers) return;
    st.metrics.markersFound = (await MH.waitForMarkers(st.page, { timeout: 60000 })) ? 1 : 0;
  },
  async tiles(st) {
    // A vector basemap draws no .leaflet-tile elements: do not require one.
    const t = await MH.waitForTiles(st.page, { timeout: 60000, minTiles: st.sc.format === 'vector' ? 0 : 1 });
    st.metrics.tilesWaitMs = t.ms;
    st.metrics.tilesTimedOut = t.timedOut ? 1 : 0;
  },
  async metrics(st) {
    const m = await MH.collectMetrics(st.page);
    for (const k of Object.keys(m)) if (typeof m[k] === 'number') st.metrics[k] = m[k];
    // Same timings measured from the first L.Map construction: isolates the
    // map from page work before it (analytics tab load, home card click).
    if (m.mapCreatedMs != null) {
      if (m.allTilesMs != null) st.metrics.allTilesSinceMapMs = m.allTilesMs - m.mapCreatedMs;
      // A marker drawn on an earlier, discarded map predates this one: omit.
      if (m.firstMarkerMs != null && m.firstMarkerMs >= m.mapCreatedMs) st.metrics.firstMarkerSinceMapMs = m.firstMarkerMs - m.mapCreatedMs;
    }
    if (m.layers) { st.metrics.leafletMaps = m.layers.maps; st.metrics.leafletLayers = m.layers.layers; st.metrics.tileEls = m.layers.tileEls; }
  },
  async heap(st) {
    const b = await MH.measureHeap(st.page);
    if (b != null) st.metrics.heapUsedMB = b / 1048576;
  },
  async screenshot(st) {
    if (!st.first) return;
    const name = st.sc.name;
    const p = await MH.screenshot(st.page, path.join(st.outDir, 'shots'), name);
    st.screenshot = path.relative(st.outDir, p).split(path.sep).join('/');
  },
  async panzoom(st) {
    const r = await MH.scriptedPanZoom(st.page, MH.DEFAULT_PANZOOM_PATH, { tileTimeout: 30000 });
    Object.assign(st.metrics, {
      panzoomMs: r.durationMs,
      frameP50Ms: r.frames.p50, frameP95Ms: r.frames.p95, frameMaxMs: r.frames.max,
      framesDropped: r.frames.dropped, frames: r.frames.frames,
      panzoomLongTaskCount: r.longTasks.count, panzoomLongTaskTotalMs: r.longTasks.totalMs, panzoomLongTaskMaxMs: r.longTasks.maxMs,
    });
  },
  async switch(st) {
    const { page, sc, first } = st;
    const all = await MH.listProviders(page);
    const formats = sc.switchFormats || ['raster'];
    const ids = all.filter(p => (p.type === sc.theme || p.type === 'any') && formats.indexOf(p.format || 'raster') >= 0).map(p => p.id);
    if (ids.length < 2) { if (first) skip(`only ${ids.length} ${sc.theme} provider(s) registered`); throw new Error('too few providers'); }
    st.extra.providers = ids;
    const start = await page.evaluate((t) => (t === 'dark' ? window.MC_getDarkTileProvider() : window.MC_getLightTileProvider()), sc.theme);
    // Warm-up: one pass through every provider so first-use costs (image
    // decode paths, JIT) are not counted as growth.
    for (const id of ids.concat([start])) {
      await MH.setTileProvider(page, { theme: sc.theme, id });
      await MH.waitForTiles(page, { timeout: 30000 });
    }
    const heapBefore = await MH.measureHeap(page);
    const lcBefore = await MH.layerCount(page);
    const order = ids.filter(id => id !== start).concat([start]);
    const switchMs = [];
    let failedTiles = 0;
    for (let i = 0; i < (sc.switches || 50); i++) {
      const id = order[i % order.length];
      const t0 = Date.now();
      const ok = await MH.setTileProvider(page, { theme: sc.theme, id });
      if (!ok) throw new Error('setTileProvider refused ' + id);
      const t = await MH.waitForTiles(page, { timeout: 30000 });
      failedTiles += t.failed;
      switchMs.push(Date.now() - t0);
    }
    await MH.setTileProvider(page, { theme: sc.theme, id: start });
    await MH.waitForTiles(page, { timeout: 30000 });
    const heapAfter = await MH.measureHeap(page);
    const lcAfter = await MH.layerCount(page);
    const sorted = switchMs.slice().sort((a, b) => a - b);
    Object.assign(st.metrics, {
      switches: switchMs.length,
      switchMsMedian: quantile(sorted, 0.5), switchMsP95: quantile(sorted, 0.95),
      switchFailedTiles: failedTiles,
      heapBeforeMB: heapBefore / 1048576, heapAfterMB: heapAfter / 1048576,
      heapGrowthPct: heapBefore ? (heapAfter - heapBefore) / heapBefore * 100 : null,
      layersBefore: lcBefore.layers, layersAfter: lcAfter.layers, layerDelta: lcAfter.layers - lcBefore.layers,
      tileElsBefore: lcBefore.tileEls, tileElsAfter: lcAfter.tileEls,
    });
  },
  async vcr(st) {
    const { page, sc, first } = st;
    const btn = await page.$('#vcrRewindBtn');
    if (!btn) { if (first) skip('live: #vcrRewindBtn not present'); throw new Error('no VCR'); }
    // Rewind by the default 1 h scope; if that window is empty, widen to the
    // largest scope (24 h). An empty rewind flashes REPLAY then drops back to
    // LIVE, so require REPLAY to hold for a second.
    const playing = () => page.evaluate(() => /PLAY/.test((document.getElementById('vcrMode') || {}).textContent || ''));
    let started = false;
    for (const scope of [null, '86400000']) {
      if (scope) {
        await page.evaluate((s) => {
          const live = document.getElementById('vcrLiveBtn'); if (live) live.click();
          const b = document.querySelector('.vcr-scope-btn[data-scope="' + s + '"]'); if (b) b.click();
        }, scope);
      }
      await page.evaluate(() => document.getElementById('vcrRewindBtn').click());
      try {
        await page.waitForFunction(() => /PLAY/.test((document.getElementById('vcrMode') || {}).textContent || ''), null, { timeout: 60000 });
        await page.waitForTimeout(1000);
        if (await playing()) { started = true; st.metrics.vcrScopeH = (scope ? +scope : 3600000) / 3600000; break; }
      } catch (_) { /* nothing to replay in this scope */ }
    }
    if (!started) {
      let newest = 'unknown';
      try { const d = await fetchJson(st.opts.baseUrl + '/api/packets?limit=1'); newest = (d.packets && d.packets[0] && d.packets[0].first_seen) || 'none'; } catch (_) {}
      const why = `live: VCR replay found no packets in the last 1 h or 24 h (newest packet ${newest}); the VCR scopes are relative to now`;
      if (first) skip(why);
      throw new Error(why);
    }
    await page.evaluate(() => window.__mcHarness.startFrames());
    // Fixed observation window: this measures steady-state animation, so it
    // is a wall-clock window by design.
    await page.waitForTimeout(sc.vcrWindowMs || 10000);
    const f = await page.evaluate(() => window.__mcHarness.stopFrames());
    const lt = await page.evaluate((fr) => window.__mcHarness.longTaskStats(fr.from, fr.to), f);
    const stillPlaying = await page.evaluate(() => /PLAY/.test((document.getElementById('vcrMode') || {}).textContent || ''));
    Object.assign(st.metrics, {
      vcrFrameP50Ms: f.p50, vcrFrameP95Ms: f.p95, vcrFrameMaxMs: f.max, vcrFramesDropped: f.dropped, vcrFrames: f.frames,
      vcrLongTaskCount: lt.count, vcrLongTaskTotalMs: lt.totalMs, vcrLongTaskMaxMs: lt.maxMs,
      vcrStillPlaying: stillPlaying ? 1 : 0,
    });
  },
};

async function runOnce(browser, opts, sc, ctx, outDir, first) {
  const { context, page } = await newPage(browser, opts, sc, ctx);
  const st = { page, sc, ctx, opts, outDir, first, metrics: {}, extra: {}, screenshot: null, route: null };
  const errors = [];
  page.on('pageerror', (e) => { if (errors.length < 20) errors.push(String(e && e.message || e)); });
  try {
    for (const a of sc.actions) {
      if (!ACTIONS[a]) throw new Error('unknown action ' + a);
      await ACTIONS[a](st);
    }
    const log = MH.getRequestLog(page);
    st.metrics.requestsIntercepted = log.intercepted.length;
    st.metrics.requestsExternal = log.external.length;
    st.metrics.requestsFailed = log.failed.length;
    st.metrics.maplibreRequests = log.maplibre.length;
    const api = log.api.filter(r => r.ms != null).sort((x, y) => y.ms - x.ms);
    if (api.length) st.metrics.apiSlowestMs = api[0].ms;
    st.resolvedProvider = await page.evaluate((t) => {
      try { return t === 'dark' ? window.MC_getDarkTileProvider() : window.MC_getLightTileProvider(); } catch (_) { return null; }
    }, sc.theme).catch(() => null);
    return {
      metrics: st.metrics, extra: st.extra, screenshot: st.screenshot, route: st.route, resolvedProvider: st.resolvedProvider,
      requests: { counts: log.counts, external: log.external.slice(0, 20).map(r => r.url), failed: log.failed.slice(0, 20), maplibre: log.maplibre.slice(0, 5), slowestApi: api.slice(0, 3).map(r => ({ url: r.url, ms: Math.round(r.ms) })) },
      pageErrors: errors,
    };
  } finally {
    await context.close().catch(() => {});
  }
}

function expandScenarios(list, providers) {
  const out = [];
  for (const sc of list) {
    if (sc.provider !== 'each') { out.push(sc); continue; }
    const ids = providers.filter(p => p.type === sc.theme || p.type === 'any');
    for (const p of ids) {
      out.push(Object.assign({}, sc, { name: `${sc.name}--${p.id}`, provider: p.id, format: p.format, expandedFrom: sc.name }));
    }
  }
  return out;
}

function writeResults(file, results) {
  fs.writeFileSync(file + '.tmp', JSON.stringify(results, null, 2));
  fs.renameSync(file + '.tmp', file);
}

async function main() {
  const opts = parseArgs(process.argv);
  if (opts.help) { console.log(USAGE); return; }
  if (!opts.baseUrl || !opts.label || !opts.out) throw new Error(USAGE);
  if (!/^[A-Za-z0-9._-]+$/.test(opts.label)) throw new Error('--label: letters, digits, . _ - only');
  if (opts.mode !== 'stub' && opts.mode !== 'live') throw new Error('--mode stub|live');
  if (!(opts.runs >= 1)) throw new Error('--runs must be >= 1');
  opts.baseUrl = opts.baseUrl.replace(/\/+$/, '');
  const outDir = path.resolve(opts.out, opts.label);
  fs.mkdirSync(path.join(outDir, 'shots'), { recursive: true });
  const resultsFile = path.join(outDir, 'results.json');

  const ctx = await loadContext(opts.baseUrl);
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });
  const results = {
    meta: {
      label: opts.label, baseUrl: opts.baseUrl, mode: opts.mode, runs: opts.runs, warmup: opts.warmup,
      startedAt: new Date().toISOString(), finishedAt: null, viewport: VIEWPORT,
      host: { platform: os.platform(), release: os.release(), cpus: os.cpus().length, cpuModel: (os.cpus()[0] || {}).model, memGB: Math.round(os.totalmem() / 1073741824) },
      node: process.version, playwright: require('playwright/package.json').version, browser: browser.version(),
      server: ctx.server ? { version: ctx.server.version, commit: ctx.server.commit, totalNodes: ctx.server.totalNodes, totalTransmissions: ctx.server.totalTransmissions, totalObservations: ctx.server.totalObservations } : null,
      prerequisites: { node: ctx.node && ctx.node.public_key, repeater: ctx.repeater && ctx.repeater.public_key, notes: ctx.notes },
      providers: [],
    },
    scenarios: {},
  };
  try {
    const providers = await listProvidersProbe(browser, opts);
    results.meta.providers = providers;
    let list = expandScenarios(SCENARIOS, providers);
    if (opts.scenarios !== 'all') {
      const want = new Set(opts.scenarios.split(',').map(s => s.trim()).filter(Boolean));
      list = list.filter(sc => want.has(sc.name) || (sc.expandedFrom && want.has(sc.expandedFrom)));
      if (!list.length) throw new Error('no scenario matches --scenarios ' + opts.scenarios);
    }
    console.log(`[map-perf] ${opts.label}: ${list.length} scenarios × (${opts.warmup} warmup + ${opts.runs} runs) against ${opts.baseUrl} (${opts.mode})`);
    for (const sc of list) {
      const rec = {
        scenario: { name: sc.name, surface: sc.surface, theme: sc.theme, provider: sc.provider, overlays: sc.overlays || [], format: sc.format || 'raster', actions: sc.actions, expandedFrom: sc.expandedFrom || null },
        status: 'ok', reason: null, runs: [], warmupRuns: [], errors: [], summary: {}, screenshot: null,
      };
      results.scenarios[sc.name] = rec;
      const total = (sc.runs || opts.runs) + opts.warmup;
      const t0 = Date.now();
      for (let i = 0; i < total; i++) {
        const warm = i < opts.warmup;
        try {
          const r = await runOnce(browser, opts, sc, ctx, outDir, i === 0);
          r.warmup = warm;
          (warm ? rec.warmupRuns : rec.runs).push(r);
          if (r.screenshot) rec.screenshot = r.screenshot;
          if (r.route) rec.route = r.route;
          if (r.resolvedProvider) rec.resolvedProvider = r.resolvedProvider;
        } catch (e) {
          if (e instanceof SkipError) { rec.status = 'skipped'; rec.reason = e.message; break; }
          rec.errors.push({ run: i, warmup: warm, error: String(e && e.stack || e).split('\n').slice(0, 3).join(' | ') });
        }
      }
      if (rec.status !== 'skipped') {
        if (!rec.runs.length) rec.status = 'error';
        rec.summary = summarize(rec.runs);
      }
      const med = (k) => rec.summary[k] ? rec.summary[k].median.toFixed(0) : '-';
      console.log(`  ${rec.status === 'ok' ? 'ok  ' : rec.status.padEnd(4)} ${sc.name.padEnd(44)} ${((Date.now() - t0) / 1000).toFixed(1)}s` +
        (rec.status === 'ok' ? `  allTiles=${med('allTilesMs')} markers=${med('firstMarkerMs')} longTask=${med('longTaskTotalMs')}` : '') +
        (rec.reason ? `  (${rec.reason})` : '') + (rec.errors.length ? `  errors=${rec.errors.length}` : ''));
      writeResults(resultsFile, results);
    }
  } finally {
    results.meta.finishedAt = new Date().toISOString();
    writeResults(resultsFile, results);
    await browser.close().catch(() => {});
  }
  console.log(`[map-perf] wrote ${resultsFile}`);
}

if (require.main === module) {
  main().catch((e) => { console.error(e && e.stack || e); process.exit(1); });
}

module.exports = { VECTOR_PROBE, parseArgs, summarize, quantile, expandScenarios };

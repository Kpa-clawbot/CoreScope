/**
 * E2E for the tropospheric-ducting map overlay, against a real CoreScope
 * server whose ducting source is a fake Open-Meteo (tests/e2e/lib/
 * fake-open-meteo.js, scenario 'mixed'). Nothing here reaches the real
 * Open-Meteo or a real tile host.
 *
 * CI: runs in the GitHub Actions E2E job (.github/workflows/deploy.yml, e2e
 * shard 1): the fake Open-Meteo on 13597, then a ducting + planner server on
 * 13596, then `suite 1 test-map-ducting-e2e.js` with CHROMIUM_REQUIRE=1
 * BASE_URL=http://localhost:13596.
 *
 * Locally, start the pieces in this order (the server's first refresh runs at startup;
 * if the fake is not listening yet it fails and the server retries only
 * after 5 minutes):
 *
 *   mkdir -p /tmp/ducting-e2e && cp test-fixtures/e2e-fixture.db /tmp/ducting-e2e/
 *   cat > /tmp/ducting-e2e/config.json <<'JSON'
 *   { "port": 13596,
 *     "planner": { "enabled": true, "defaultRangeKm": 30, "newAntennaM": 10,
 *                  "existingAntennaM": 20, "maxAreaKm": 100, "nodeMarginKm": 20, "maxLegKm": 100 },
 *     "ducting": { "enabled": true, "baseUrl": "http://127.0.0.1:13597",
 *                  "gridStepDeg": 0.5, "forecastHours": 48 } }
 *   JSON
 *   node tests/e2e/lib/fake-open-meteo.js --port 13597 --scenario mixed &
 *   (cd cmd/server && go build -o /tmp/ducting-e2e/corescope-server .)   # or: scripts/map-perf/serve-dataset.sh build worktree <dir>
 *   /tmp/ducting-e2e/corescope-server -config-dir /tmp/ducting-e2e \
 *       -db /tmp/ducting-e2e/e2e-fixture.db -port 13596 -public public &
 *   BASE_URL=http://localhost:13596 node tests/e2e/test-map-ducting-e2e.js
 *
 * (the sqlite driver needs cgo: build with the Makefile or the Docker build in
 * scripts/map-perf/serve-dataset.sh, never CGO_ENABLED=0.)
 *
 * If something already listens on FAKE_PORT the test uses that fake through
 * its /__state endpoint; otherwise it starts its own, which only helps when
 * the server is started after it (the server then retries within 5 minutes
 * only if it failed; restart it).
 *
 * Env:
 *   BASE_URL       CoreScope server (default http://localhost:13596)
 *   FAKE_PORT      fake Open-Meteo port (default 13597)
 *   CHROMIUM_PATH  optional browser binary
 */
'use strict';
const { chromium } = require('playwright');
const MH = require('./lib/map-harness');
const FAKE = require('./lib/fake-open-meteo');

const BASE = (process.env.BASE_URL || 'http://localhost:13596').replace(/\/+$/, '');
const FAKE_PORT = parseInt(process.env.FAKE_PORT || '13597', 10);
const SCENARIO = 'mixed';
// The LoRa frequencies the overlay judges (map-ducting.js loraFreqs): the
// site's los.frequencyMhz from /api/config/client when it is 100..6000 MHz
// and not the shipped default 910.525 (not the operator's choice), else 868
// and 915 MHz. Set once the client config is read.
let LORA_FREQS = [868, 915];
const loraFreqsOf = (cfg) => { const f = Number(cfg && cfg.los && cfg.los.frequencyMhz); return (f >= 100 && f <= 6000 && Math.abs(f - 910.525) >= 0.0005) ? [f] : [868, 915]; };
// The /api/ducting answer `d` with its times moved so the first one is
// `startHoursAgo` whole hours before the current hour.
const shifted = (d, startHoursAgo) => {
  const hour = Math.floor(Date.now() / 3600000) * 3600, by = hour - startHoursAgo * 3600 - d.times[0];
  return Object.assign({}, d, { times: d.times.map(t => t + by) });
};
// The panel's status line as a user sees it: text of the visible
// role=status elements, how many there are, whether one sits in the legend.
const inPageStatus = () => {
  const p = document.querySelector('.mc-duct-panel');
  if (!p) return null;
  const vis = (el) => { const r = el.getBoundingClientRect(); const cs = getComputedStyle(el); return r.width > 0 && r.height > 0 && cs.visibility !== 'hidden' && cs.display !== 'none'; };
  const all = Array.prototype.slice.call(p.querySelectorAll('[role="status"]'));
  const r = p.getBoundingClientRect();
  return {
    count: all.length, text: all.filter(vis).map(e => e.textContent).join(' | '),
    inLegend: all.some(e => !!e.closest('.mc-duct-legend')),
    legend: vis(p.querySelector('.mc-duct-legend')),
    inView: r.left >= 0 && r.top >= 0 && r.right <= window.innerWidth && r.bottom <= window.innerHeight,
  };
};
// f_min as the popup shows it (2 significant figures); verdicts use this value.
const sig2 = (v) => Number(Number(v).toPrecision(2));
const shownFmin = (c, h) => (c.c[h] >= 3 && c.f[h] > 0) ? sig2(c.f[h]) : 0;
const fmtFreq = (f) => String(Math.round(f * 1000) / 1000);
// The field's colour bands start at these values of n, the mean dN/dh over
// the lowest 1.5 km in N/km (map-ducting.js STOP_N).
const STOP_N = [-44, -48, -54, -58, -62, -70, -80, -90];
// The field value of a cell at hour h: n, or null for no data (0 or class 0).
const fieldN = (c, h) => (c.c[h] >= 1 && Array.isArray(c.n) && c.n[h]) ? c.n[h] : null;
// A stop between a and b (within 0.5 N/km): the two may legitimately sit in
// different bands, so their colours are not compared.
const stopBetween = (a, b) => STOP_N.some(s => s >= Math.min(a, b) - 0.5 && s <= Math.max(a, b) + 0.5);

let passed = 0, failed = 0, skipped = 0;
async function test(name, fn) {
  try { await fn(); passed++; console.log(`  ✅ ${name}`); }
  catch (e) { failed++; console.log(`  ❌ ${name}: ${e.message}`); }
}
// In CI (GitHub Actions sets CI=true) the server is set up with everything
// these checks need, so a skip there means a broken setup: count it as a
// failure instead.
function skip(name, why) {
  if (process.env.CI === 'true') { failed++; console.log(`  ❌ ${name}: skipped in CI (${why})`); return; }
  skipped++; console.log(`  ⏭  ${name}: ${why}`);
}
function assert(cond, msg) { if (!cond) throw new Error(msg || 'Assertion failed'); }
const sleep = (ms) => new Promise(r => setTimeout(r, ms));

// ── Fake Open-Meteo: own instance, or the one already listening ─────────────
async function getFake() {
  let own = null;
  try {
    own = await FAKE.start({ port: FAKE_PORT, scenario: SCENARIO });
    console.log(`started fake Open-Meteo on ${own.url} (${SCENARIO})`);
    return {
      url: own.url, own,
      state: async () => ({ scenario: own.scenario, requests: own.requests, violations: own.violations }),
      close: () => own.close(),
    };
  } catch (e) {
    if (e.code !== 'EADDRINUSE') throw e;
  }
  const url = `http://127.0.0.1:${FAKE_PORT}`;
  const get = async () => (await fetch(url + '/__state', { signal: AbortSignal.timeout(5000) })).json();
  const st = await get().catch(() => null);
  if (!st) throw new Error(`port ${FAKE_PORT} is in use by something that is not the fake Open-Meteo`);
  if (st.scenario !== SCENARIO) {
    await fetch(`${url}/__scenario?name=${SCENARIO}`);
    console.log(`fake on ${url} was '${st.scenario}', switched to '${SCENARIO}' (the server only sees it at its next refresh)`);
  }
  console.log(`using the fake Open-Meteo already listening on ${url}`);
  return { url, own: null, state: get, close: async () => {} };
}

async function getJson(path) {
  const r = await fetch(BASE + path, { signal: AbortSignal.timeout(20000) });
  if (!r.ok) throw new Error(`${path} -> HTTP ${r.status}`);
  return r.json();
}

// ── Page plumbing ───────────────────────────────────────────────────────────
const IGNORED_CONSOLE = [/net::ERR_BLOCKED_BY_CLIENT/];

async function newPage(browser, opts) {
  opts = opts || {};
  const context = await browser.newContext({ viewport: { width: 1280, height: 800 }, serviceWorkers: 'block' });
  const page = await context.newPage();
  page.setDefaultTimeout(20000);
  page.__errors = [];
  page.on('pageerror', (e) => page.__errors.push('pageerror: ' + (e && (e.stack || e.message) || e)));
  page.on('console', (m) => {
    if (m.type() !== 'error') return;
    const t = m.text();
    if (IGNORED_CONSOLE.some(re => re.test(t))) return;
    const loc = m.location && m.location();
    // Status noise from unrelated /api/ probes is not the overlay's business,
    // but a failing /api/ducting is.
    if (/^Failed to load resource: the server responded/.test(t) && loc && /\/api\//.test(loc.url || '') && !/\/api\/ducting/.test(loc.url)) return;
    page.__errors.push('console.error: ' + t);
  });
  await MH.installCollectors(page);
  await MH.installTileStub(page, { mode: 'stub', baseUrl: BASE });
  await MH.presetStorage(page, { theme: opts.theme || 'light', provider: 'auto', overlays: opts.overlays || [] });
  if (opts.stripDucting || opts.noPlanner === false) {
    await page.route('**/api/config/client', async (route) => {
      const res = await route.fetch();
      const json = await res.json();
      if (opts.stripDucting) delete json.ducting;
      await route.fulfill({ response: res, json });
    });
  }
  return { context, page };
}

async function gotoRoute(page, hash, waitControl) {
  await page.goto(`${BASE}/${hash}`, { waitUntil: 'domcontentloaded' });
  if (waitControl !== false) await page.waitForSelector('.leaflet-control-layers', { state: 'attached', timeout: 25000 });
}
async function openPicker(page) { await page.locator('.leaflet-control-layers').first().hover(); }
// Ticks the picker's own checkbox (its click handler, as Leaflet wires it). A
// real mouse click is not used: the expanded picker can sit under the legend,
// which has its own test ('legend does not cover the expanded picker').
async function setOverlayUi(page, on) {
  const inp = page.locator('label[data-tile-overlay-id="ducting"] input').first();
  await inp.waitFor({ state: 'attached' });
  if ((await inp.isChecked()) !== on) await inp.evaluate((el) => el.click());
}

// What the page shows right now, for every live map: the cells the ducting
// layers hold, the LoRa outline layers (cells, style), edge lines, field
// images, any interactive layer of the overlay (there must be none), and
// the panel with its legend/time sections.
function inPageState() {
  const maps = ((window.__mcHarness && window.__mcHarness.maps) || [])
    .filter(m => m && m._container && m._container.isConnected && m._loaded);
  let cells = 0;
  const lora = [], fields = [], edges = [], interactive = [];
  maps.forEach((m, mi) => {
    m.eachLayer((l) => {
      if (l._mcDuct) cells += (l._cells || []).length;
      if (l._mcEdge) {
        const parts = l.getLatLngs().map(p => p.map(ll => [ll.lat, ll.lng]));
        edges.push({ map: mi, parts, color: l.options.color, dash: l.options.dashArray, weight: l.options.weight, interactive: l.options.interactive });
      }
      if (l._mcLora) lora.push({ map: mi, cells: l._mcCells.map(c => [c.lat, c.lon]), color: l.options.color, dash: l.options.dashArray, fill: l.options.fill, interactive: l.options.interactive, pane: l._renderer && l._renderer.options.pane });
      if ((l._mcEdge || l._mcLora || l._mcCell) && l.options.interactive !== false) interactive.push(l._mcEdge ? 'edge' : l._mcLora ? 'lora' : 'cell');
      if (l._mcField) fields.push({ map: mi, inDom: !!(l._image && l._image.isConnected), pane: l.options.pane });
    });
  });
  const vis = (el) => { const r = el.getBoundingClientRect(); const cs = getComputedStyle(el); return r.width > 0 && r.height > 0 && cs.visibility !== 'hidden' && cs.display !== 'none'; };
  const q = (sel) => Array.prototype.slice.call(document.querySelectorAll(sel));
  return {
    maps: maps.length,
    cells,
    lora,
    interactive,
    fields,
    edges,
    panels: q('.mc-duct-panel').length,
    legends: q('.mc-duct-legend').length,
    times: q('.mc-duct-time').length,
    legendVisible: q('.mc-duct-legend').some(vis),
    timeVisible: q('.mc-duct-time').some(vis),
    legendText: (q('.mc-duct-legend')[0] || {}).textContent || '',
  };
}
const pageState = (page) => page.evaluate(inPageState);
// Waits until the ducting layers of the live maps hold at least min cells.
async function waitCells(page, min) {
  await page.waitForFunction((n) => {
    const maps = ((window.__mcHarness && window.__mcHarness.maps) || []).filter(m => m && m._container && m._container.isConnected && m._loaded);
    let c = 0; maps.forEach(m => m.eachLayer(l => { if (l._mcDuct) c += (l._cells || []).length; }));
    return c >= n;
  }, min || 1, { timeout: 25000, polling: 100 });
}

// Bounding boxes of the controls that must not overlap the ducting panel.
function inPageOverlaps() {
  const boxes = (el) => { const r = el.getBoundingClientRect(); return { l: r.left, t: r.top, r: r.right, b: r.bottom, w: r.width, h: r.height }; };
  const visible = (el) => { const r = el.getBoundingClientRect(); const cs = getComputedStyle(el); return r.width > 0 && r.height > 0 && cs.visibility !== 'hidden' && cs.display !== 'none'; };
  const mine = Array.prototype.slice.call(document.querySelectorAll('.mc-duct-panel')).filter(visible);
  const others = Array.prototype.slice.call(document.querySelectorAll('.leaflet-control, #mapControls, #mapControlsToggle'))
    .filter(el => visible(el) && !el.matches('.mc-duct-panel') && !mine.some(m => m.contains(el) || el.contains(m)));
  const hits = [];
  mine.forEach((m, i) => {
    const a = boxes(m);
    const name = (e) => e.id ? '#' + e.id : '.' + String(e.className).split(/\s+/).slice(0, 3).join('.');
    others.forEach((o) => {
      const b = boxes(o);
      const ix = Math.min(a.r, b.r) - Math.max(a.l, b.l), iy = Math.min(a.b, b.b) - Math.max(a.t, b.t);
      if (ix > 1 && iy > 1) hits.push(`${name(m)} overlaps ${name(o)} by ${Math.round(ix)}x${Math.round(iy)}px`);
    });
    mine.forEach((m2, j) => {
      if (j <= i) return;
      const b = boxes(m2);
      const ix = Math.min(a.r, b.r) - Math.max(a.l, b.l), iy = Math.min(a.b, b.b) - Math.max(a.t, b.t);
      if (ix > 1 && iy > 1) hits.push(`${name(m)} overlaps ${name(m2)} by ${Math.round(ix)}x${Math.round(iy)}px`);
    });
    const vw = document.documentElement.clientWidth, vh = document.documentElement.clientHeight;
    if (a.l < -1 || a.t < -1 || a.r > vw + 1 || a.b > vh + 1) hits.push(`${name(m)} outside the viewport (${Math.round(a.l)},${Math.round(a.t)},${Math.round(a.r)},${Math.round(a.b)} in ${vw}x${vh})`);
  });
  // The panel's own sections: the legend body and the timeline inside it.
  const inside = mine.map(m => ['.mc-duct-legend', '.mc-duct-time'].filter(sel => { const e = m.querySelector(sel); return e && visible(e); }).length);
  return { hits, mine: mine.length, inside };
}

function cssVars(page) {
  return page.evaluate(() => {
    const cs = getComputedStyle(document.documentElement);
    const o = { lora: cs.getPropertyValue('--mc-duct-lora').trim(), alpha: parseFloat(cs.getPropertyValue('--mc-duct-alpha')) };
    for (let k = 1; k <= 8; k++) o['s' + k] = cs.getPropertyValue('--mc-duct-s' + k).trim();
    return o;
  });
}

/**
 * Probes the field image of the first live map at each cell centre: the
 * canvas pixel under the centre, found with Leaflet's own projection (pixel
 * points of the image corners and of the centre), so a field drawn with the
 * wrong projection does not line up. For each probe also returns what the
 * field should be at that pixel's centre (MC_ductSample at Leaflet's latlng
 * for it) and the colour of the cell's own n. Runs in the page.
 */
function inPageFieldProbe(arg) {
  const m = window.__mcHarness.maps.filter(x => x._container && x._container.isConnected && x._loaded)[0];
  let field = null, layer = null;
  m.eachLayer((l) => { if (l._mcField) field = l; if (l._drawField) layer = l; });
  if (!field || !layer) return { err: 'no field image or ducting layer' };
  const cv = field._image;
  const ctx = cv.getContext('2d');
  const b = field.getBounds();
  const z = m.getZoom();   // project/unproject: unrounded, unlike latLngToLayerPoint
  const nw = m.project(b.getNorthWest(), z), se = m.project(b.getSouthEast(), z);
  const stops = window.MC_ductFieldStops();
  const alpha = parseFloat(getComputedStyle(document.documentElement).getPropertyValue('--mc-duct-alpha'));
  const grid = layer._grid, vals = window.MC_ductGridValues(grid, layer._data.cells, arg.idx);
  return {
    w: cv.width, h: cv.height, alpha,
    sum: (() => { const d = ctx.getImageData(0, 0, cv.width, cv.height).data; let h = 0; for (let i = 0; i < d.length; i += 4) h = (h * 31 + d[i] * 7 + d[i + 1] * 3 + d[i + 2] + d[i + 3] * 11) % 1000000007; return h; })(),
    probes: arg.cells.map((c) => {
      const p = m.project([c.lat, c.lon], z);
      const x = Math.floor((p.x - nw.x) / (se.x - nw.x) * cv.width), y = Math.floor((p.y - nw.y) / (se.y - nw.y) * cv.height);
      const px = Array.from(ctx.getImageData(x, y, 1, 1).data);
      const ll = m.unproject([nw.x + (x + 0.5) / cv.width * (se.x - nw.x), nw.y + (y + 0.5) / cv.height * (se.y - nw.y)], z);
      const smp = window.MC_ductSample(grid, vals, ll.lat, ll.lng);
      const exp = window.MC_ductFieldColor(smp.g, stops);
      return { lat: c.lat, lon: c.lon, n: c.n, px, smp, exp: [exp[0], exp[1], exp[2], Math.round(exp[3] * alpha * smp.a * 255)], own: window.MC_ductFieldColor(c.n, stops) };
    }),
  };
}

// Lattice neighbours (N, S, E, W) of a cell, from the data.
function neighbours(data, c) {
  const st = data.stepDeg, key = (la, lo) => `${la.toFixed(4)},${lo.toFixed(4)}`;
  const byKey = new Map(data.cells.map(x => [key(x.lat, x.lon), x]));
  return [[st, 0], [-st, 0], [0, st], [0, -st]].map(([a, b]) => byKey.get(key(c.lat + a, c.lon + b))).filter(Boolean);
}

// 1 for a cell whose 8 neighbours are all in the forecast area (data at some
// hour), 0.5 on the area's outer ring: the field's coverage at a cell centre.
// 0 (not checked) when a neighbour in the area has no n at hour idx.
function ringFactor(data, c, idx) {
  const st = data.stepDeg, key = (la, lo) => `${la.toFixed(4)},${lo.toFixed(4)}`;
  const byKey = new Map(data.cells.filter(x => x.c.some(v => v >= 1)).map(x => [key(x.lat, x.lon), x]));
  let f = 1;
  for (const a of [-st, 0, st]) for (const b of [-st, 0, st]) {
    const x = byKey.get(key(c.lat + a, c.lon + b));
    if (!x) f = Math.min(f, 0.5);
    else if (fieldN(x, idx) == null) return 0;
  }
  return f;
}

/**
 * Checks the field at hour idx: coloured cells (n <= -44) coloured like their
 * n, a normal cell (n > -44, with only normal or missing neighbours) transparent, and every probed
 * pixel equal to the field sampled at Leaflet's position of that pixel. The
 * colours are flat bands, so a probe within 0.5 N/km of a band boundary (its
 * pixel's float rounding may land on either side) has its colour checks
 * skipped; the alpha check always applies.
 */
async function fieldProblems(page, data, idx) {
  // Coloured cells, clear of the first stop (a cell at exactly -44 samples
  // either side of it a fraction of a pixel away); spread over the bands.
  const ducts = data.cells.filter(c => fieldN(c, idx) != null && fieldN(c, idx) <= STOP_N[0] - 1)
    .sort((a, b) => a.n[idx] - b.n[idx]).filter((c, i, arr) => i % Math.max(1, Math.floor(arr.length / 12)) === 0).slice(0, 12);
  // Normal: n above -44 with a margin, and no lattice neighbour so much
  // stronger that the slope carries the value past -44 within the pixel or
  // two between the probe and the cell centre.
  const calm = data.cells.filter((c) => {
    const v = fieldN(c, idx);
    return v != null && v > STOP_N[0] + 1 && neighbours(data, c).every(nb => { const w = fieldN(nb, idx); return w == null || w > v - 20; });
  }).slice(0, 6);
  const cells = ducts.concat(calm).map(c => ({ lat: c.lat, lon: c.lon, n: c.n[idx] }));
  const r = await page.evaluate(inPageFieldProbe, { idx, cells });
  if (r.err) return { probs: [r.err], r };
  const probs = [];
  if (!ducts.length) probs.push(`no cell with n <= ${STOP_N[0]} at hour ${idx}`);
  r.probes.forEach((p, i) => {
    const where = `${p.lat},${p.lon} n=${p.n}`;
    // Colour only where opaque enough: the canvas stores premultiplied alpha.
    const edge = stopBetween(p.smp.g, p.smp.g);
    const bad = Math.abs(p.px[3] - p.exp[3]) > 3 || (!edge && p.exp[3] >= 64 && p.px.slice(0, 3).some((v, k) => Math.abs(v - p.exp[k]) > 4));
    if (bad) probs.push(`${where}: pixel ${p.px} but the field there is ${p.exp}`);
    if (i < ducts.length) {
      // Full opacity inside the forecast area, half on its outer ring (the wider edge fade);
      // a neighbour without n at this hour fades it too, so only data-rich cells are probed for alpha.
      if (p.px[3] < 0.8 * r.alpha * 255 * ringFactor(data, ducts[i], idx)) probs.push(`${where}: coloured centre alpha ${p.px[3]}`);
      // The pixel centre is a fraction of a cell from the cell centre, so it
      // has the cell's own band colour unless a band boundary lies between.
      if (!stopBetween(p.n, p.smp.g) && p.px.slice(0, 3).some((v, k) => Math.abs(v - p.own[k]) > 4)) probs.push(`${where}: pixel ${p.px} not its own band colour ${p.own} (sampled n ${p.smp.g})`);
    } else if (p.px[3] !== 0) probs.push(`${where}: normal cell centre not transparent (${p.px})`);
  });
  return { probs, r, ducts: ducts.length, calm: calm.length };
}

// Outline problems: one dashed, unfilled, non-interactive --mc-duct-lora
// outline layer exactly on the cells where the model shows a duct that may
// carry LoRa at hour idx (0 < shown f_min <= the highest judged frequency).
function outlineProblems(st, data, idx, lora) {
  const probs = [];
  const want = new Set(data.cells.filter(c => { const v = shownFmin(c, idx); return v > 0 && v <= LORA_FREQS[LORA_FREQS.length - 1]; }).map(c => `${c.lat},${c.lon}`));
  if (st.lora.length > 1) probs.push(`${st.lora.length} LoRa outline layers`);
  const l = st.lora[0];
  if (!want.size) { if (l) probs.push(`outline drawn on ${l.cells.length} cells, none wanted`); return { probs, n: 0 }; }
  if (!l) { probs.push(`no outline layer, ${want.size} LoRa cells`); return { probs, n: want.size }; }
  if (!(l.dash === '4 3' && l.fill === false && l.interactive === false && String(l.color).toLowerCase() === String(lora).toLowerCase())) probs.push('outline style ' + JSON.stringify([l.color, l.dash, l.fill, l.interactive]));
  if (l.pane !== 'mcDuctLinePane') probs.push('outline renderer pane ' + l.pane);
  const got = new Set(l.cells.map(([a, b]) => `${a},${b}`));
  want.forEach(k => { if (!got.has(k)) probs.push(`${k}: LoRa duct without the dashed outline`); });
  got.forEach((k) => {
    if (want.has(k)) return;
    const c = data.cells.find(x => `${x.lat},${x.lon}` === k);
    probs.push(`${k}: class ${c && c.c[idx]} f=${c && c.f[idx]} has an outline`);
  });
  return { probs, n: want.size };
}

// The first live map's container point of (lat, lon) in page coordinates,
// and whether a marker, interactive path, control or popup covers it.
function inPagePoint(a) {
  const m = window.__mcHarness.maps.filter(x => x._container && x._container.isConnected && x._loaded)[0];
  const p = m.latLngToContainerPoint([a.lat, a.lon]);
  const r = m.getContainer().getBoundingClientRect();
  const x = r.left + p.x, y = r.top + p.y;
  const el = document.elementFromPoint(x, y);
  const blocked = !el || !!el.closest('.leaflet-marker-icon, .leaflet-interactive, .leaflet-control, .leaflet-popup, #mapControls, .mc-duct-panel, .leaflet-marker-pane, .leaflet-tooltip')
    || x < r.left + 40 || y < r.top + 40 || x > r.right - 40 || y > r.bottom - 40;
  return { x, y, blocked };
}

// Centres the first live map on cell c and returns up to `max` clickable
// points inside the cell (a node marker or control may sit on the centre).
async function pointsIn(page, c, step, max) {
  await page.evaluate((a) => {
    const m = window.__mcHarness.maps.filter(x => x._container && x._container.isConnected && x._loaded)[0];
    m.closePopup();
    m.setView([a.lat, a.lon], 9, { animate: false });
  }, c);
  await sleep(400);
  const d = step * 0.3, out = [];
  for (const [dy, dx] of [[0, 0], [d, d], [-d, d], [d, -d], [-d, -d], [0, d], [d, 0], [-d, 0], [0, -d]]) {
    const pt = await page.evaluate(inPagePoint, { lat: c.lat + dy, lon: c.lon + dx });
    if (!pt.blocked) out.push(pt);
    if (out.length >= max) break;
  }
  return out;
}

// The timeline (a role=slider strip): the shown index, and moving it with
// the keyboard as a user does (ArrowLeft / ArrowRight from where it is).
const timeIdx = (page) => page.evaluate(() => parseInt(document.querySelector('.mc-duct-timeline').getAttribute('aria-valuenow'), 10));
async function setTime(page, idx) {
  const tl = page.locator('.mc-duct-timeline').first();
  await tl.focus();
  const cur = await timeIdx(page);
  for (let k = 0; k < Math.abs(idx - cur); k++) await page.keyboard.press(idx > cur ? 'ArrowRight' : 'ArrowLeft');
  await page.waitForFunction((i) => document.querySelector('.mc-duct-timeline').getAttribute('aria-valuenow') === String(i), idx, { timeout: 5000 });
}
// The first live map's view, the ducting layer's strip state.
function inPageView() {
  const m = window.__mcHarness.maps.filter(x => x._container && x._container.isConnected && x._loaded)[0];
  const c = m.getCenter();
  let layer = null;
  m.eachLayer((l) => { if (l._mcDuct) layer = l; });
  const tl = document.querySelector('.mc-duct-timeline');
  const o = m.getPixelOrigin(), pp = m._getMapPanePos();
  return {
    lat: c.lat, lng: c.lng, zoom: m.getZoom(), px: o.x - pp.x, py: o.y - pp.y,
    idx: tl ? parseInt(tl.getAttribute('aria-valuenow'), 10) : -1,
    scrollLeft: tl ? tl.scrollLeft : null,
    xs: layer && layer._tlLayout ? layer._tlLayout.xs : [],
    times: layer && layer._data ? layer._data.times : [],
  };
}
// Waits until the strip has settled: the shown time sits under the centre.
async function settled(page) {
  await page.waitForFunction(() => {
    const m = window.__mcHarness.maps.filter(x => x._container && x._container.isConnected && x._loaded)[0];
    let layer = null;
    m.eachLayer((l) => { if (l._mcDuct) layer = l; });
    const tl = document.querySelector('.mc-duct-timeline');
    if (!layer || !layer._tlLayout || !tl) return false;
    return Math.abs(tl.scrollLeft - layer._tlLayout.xs[layer._idx]) <= 1;
  }, null, { timeout: 4000, polling: 50 }).catch(() => {});
  return page.evaluate(inPageView);
}
// Same view: same zoom and the same pixel at the top left (getCenter() may
// round differently once the map has been touched, without any move).
const sameView = (a, b) => a.zoom === b.zoom && Math.abs(a.px - b.px) < 0.5 && Math.abs(a.py - b.py) < 0.5;
// The panel's box, where it lives, and its stored state.
function inPagePanel() {
  const p = document.querySelector('.mc-duct-panel');
  if (!p) return null;
  const r = p.getBoundingClientRect();
  const vis = (sel) => { const e = p.querySelector(sel); if (!e) return false; const b = e.getBoundingClientRect(); return b.width > 0 && b.height > 0 && getComputedStyle(e).display !== 'none'; };
  let stored = null;
  try { stored = JSON.parse(localStorage.getItem('mc-duct-panel')); } catch (_) {}
  return {
    l: r.left, t: r.top, w: r.width, h: r.height,
    floating: p.classList.contains('mc-duct-panel-floating'),
    inCorner: !!p.closest('.leaflet-bottom.leaflet-left'),
    inMap: p.parentNode && p.parentNode.classList.contains('leaflet-container'),
    legend: vis('.mc-duct-legend'), time: vis('.mc-duct-time'), reset: vis('.mc-duct-reset'),
    expanded: p.querySelector('.mc-duct-collapse').getAttribute('aria-expanded'),
    stored,
  };
}
const panelState = (page) => page.evaluate(inPagePanel);

async function run() {
  console.log(`\nDucting overlay E2E — ${BASE}\n`);
  const fake = await getFake();
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });
  const pages = [];
  const track = async (opts) => { const p = await newPage(browser, opts); pages.push(p); return p; };

  try {
    let cfg;
    await test('/api/config/client has ducting and never the baseUrl', async () => {
      cfg = await getJson('/api/config/client');
      assert(cfg.ducting && typeof cfg.ducting === 'object', 'no ducting block (is ducting.enabled set on the server?)');
      assert(cfg.ducting.model === 'gfs' && cfg.ducting.modelLabel && cfg.ducting.stepDeg > 0 && cfg.ducting.attribution, 'incomplete: ' + JSON.stringify(cfg.ducting));
      const txt = JSON.stringify(cfg);
      assert(!/baseUrl/i.test(txt) && !txt.includes('127.0.0.1:' + FAKE_PORT), 'client config leaks the ducting baseUrl');
    });
    LORA_FREQS = loraFreqsOf(cfg);
    if (!cfg || !cfg.ducting) throw new Error('server has no ducting configured; see the header for how to start it');

    let data;
    await test('/api/ducting reaches status ok', async () => {
      const t0 = Date.now();
      for (;;) {
        data = await getJson('/api/ducting');
        if (data.status === 'ok') break;
        if (Date.now() - t0 > 30000) {
          throw new Error(`status ${data.status} ${data.error || ''} after 30 s. If the server started before the fake Open-Meteo listened, restart it (it retries only after 5 min)`);
        }
        await sleep(500);
      }
      assert(data.model === 'gfs' && data.attribution && data.generatedAt > 0, 'metadata missing');
      assert(data.cells.length > 0 && data.times.length === 48, `cells=${data.cells.length} times=${data.times.length}`);
      assert(data.times.every((t, i) => i === 0 || t - data.times[i - 1] === 3600), 'times are not hourly ascending');
    });

    await test('cell classes and frequencies match the scenario', async () => {
      const probs = [];
      data.cells.forEach((c) => {
        ['c', 'f', 'b', 't', 'dm', 'g', 'n'].forEach((k) => { if (!Array.isArray(c[k]) || c[k].length !== data.times.length) probs.push(`${c.lat},${c.lon}: ${k} has ${c[k] && c[k].length} entries`); });
        data.times.forEach((_, h) => {
          const want = FAKE.intendedKind(SCENARIO, c.lat, c.lon, h).cls;
          if (c.c[h] !== want) probs.push(`${c.lat},${c.lon} h${h}: class ${c.c[h]} want ${want}`);
          const a = FAKE.analyzeCell(SCENARIO, c.lat, c.lon, h, data.times.length);
          if (a.cls !== c.c[h]) probs.push(`${c.lat},${c.lon} h${h}: fake port says ${a.cls}, server ${c.c[h]}`);
          if (c.c[h] === 4 && !(c.f[h] > 0 && c.f[h] <= 868)) probs.push(`${c.lat},${c.lon} h${h}: surface duct fmin ${c.f[h]} does not trap 868 MHz`);
          if (c.c[h] >= 3 && Math.abs(c.f[h] - Math.round(a.fmin)) > 1) probs.push(`${c.lat},${c.lon} h${h}: fmin ${c.f[h]} vs ${Math.round(a.fmin)}`);
          if (c.c[h] <= 2 && c.f[h] !== 0) probs.push(`${c.lat},${c.lon} h${h}: class ${c.c[h]} with fmin ${c.f[h]}`);
          if (Array.isArray(c.n) && !Number.isInteger(c.n[h])) probs.push(`${c.lat},${c.lon} h${h}: n ${c.n[h]} is not a whole number`);
        });
      });
      assert(!probs.length, probs.slice(0, 6).join(' | '));
      const seen = new Set();
      data.cells.forEach(c => c.c.forEach(v => seen.add(v)));
      [1, 2, 3, 4].forEach(k => assert(seen.has(k), `no class ${k} cell in the data (fixture has too few nodes? cells=${data.cells.length})`));
      assert(data.cells.some(c => new Set(c.c).size > 1), 'no cell changes over the forecast hours; the timeline could not change a colour');
    });

    await test('the fake saw valid, batched requests only', async () => {
      const st = await fake.state();
      assert(!st.violations.length, 'fake rejected requests: ' + st.violations.slice(0, 3).join('; '));
      const ok = st.requests.filter(r => r.valid && r.path === '/v1/forecast');
      assert(ok.length > 0, 'the fake saw no forecast request');
      ok.forEach((r) => { assert(r.locations >= 1 && r.locations <= FAKE.BATCH_MAX, `request with ${r.locations} locations`); assert(r.hours === 48, `forecast_hours=${r.hours}`); });
      const totalLoc = ok.reduce((a, r) => a + r.locations, 0);
      assert(totalLoc >= data.cells.length, `requested ${totalLoc} locations for ${data.cells.length} cells`);
      const refreshes = Math.max(1, Math.round(totalLoc / data.cells.length));
      const perRefresh = Math.ceil(data.cells.length / FAKE.BATCH_MAX);
      assert(ok.length <= perRefresh * refreshes, `${ok.length} requests for ${refreshes} refresh(es) of ${data.cells.length} cells, want <= ${perRefresh} each`);
    });

    // ── Map page ──
    const mp = await track({ theme: 'light' });
    const page = mp.page;
    let idx0 = 0;
    await test('map page: overlay entry sits in the Overlays group and is off by default', async () => {
      await gotoRoute(page, '#/map');
      await openPicker(page);
      const lab = page.locator('.leaflet-control-layers-overlays label[data-tile-overlay-id="ducting"]').first();
      await lab.waitFor({ state: 'attached', timeout: 20000 });
      assert(/ducting/i.test(await lab.textContent()), 'label text: ' + (await lab.textContent()));
      assert(!(await lab.locator('input').isChecked()), 'ducting is on by default');
      const st = await pageState(page);
      assert(st.legends === 0 && st.times === 0 && !st.cells && !st.lora.length && !st.fields.length, 'legend/time/cells/field present with the overlay off');
    });

    await test('enabling it draws cells, with one visible, unobstructed panel: timeline and legend', async () => {
      await setOverlayUi(page, true);
      await waitCells(page, 1);
      await page.mouse.move(5, 5);
      await page.waitForSelector('.mc-duct-legend', { state: 'visible' });
      const st = await pageState(page);
      assert(st.maps === 1 && st.cells === data.cells.length, `maps=${st.maps} cells drawn=${st.cells} cells=${data.cells.length}`);
      assert(!st.interactive.length, 'interactive overlay layers: ' + st.interactive.join(','));
      assert(st.fields.length === 1 && st.fields[0].inDom && st.fields[0].pane === 'mcDuctFieldPane', 'field images: ' + JSON.stringify(st.fields));
      assert(st.panels === 1 && st.legends === 1 && st.times === 1 && st.legendVisible && st.timeVisible, `panels=${st.panels} legends=${st.legends} times=${st.times}`);
      assert(st.legendText.includes('may carry LoRa at ' + LORA_FREQS.map(fmtFreq).join(' / ') + ' MHz') && /Above normal/.test(st.legendText) && /Very high/.test(st.legendText) && !/Enhanced|Strong/.test(st.legendText) && /lowest 1\.5 km \(N\/km\)/.test(st.legendText) && /Updated/.test(st.legendText), 'legend text: ' + st.legendText.slice(0, 200));
      const ov = await page.evaluate(inPageOverlaps);
      assert(ov.mine === 1 && ov.inside[0] === 2, 'panel, legend and timeline not all visible: ' + JSON.stringify(ov));
      assert(!ov.hits.length, ov.hits.join('; '));
    });

    await test('the forecast area has a dotted outline on its outer boundary, and the legend explains it', async () => {
      const st = await pageState(page);
      assert(st.edges.length === 1, 'edge layers: ' + st.edges.length);
      const e = st.edges[0];
      assert(e.dash === '1 5' && e.weight === 1 && e.interactive === false && e.color, 'edge style: ' + JSON.stringify([e.dash, e.weight, e.interactive, e.color]));
      assert(/Dotted line: edge of the forecast area/.test(st.legendText), 'legend text: ' + st.legendText.slice(0, 200));
      // Every unit of every run separates a cell with data from one without.
      const step = Number(data.stepDeg) || 0.5;
      const has = new Set();
      data.cells.forEach(c => { if (c.c.some(v => v >= 1)) has.add(Math.round(c.lat / step - 0.5) + ',' + Math.round(c.lon / step - 0.5)); });
      const cell = (i, j) => has.has(i + ',' + j);
      let units = 0;
      e.parts.forEach((p) => {
        assert(p.length === 2, 'a run has 2 points');
        const [a, b] = p;
        if (Math.abs(a[0] - b[0]) < 1e-9) {            // horizontal, between rows
          const r = Math.round(a[0] / step), j0 = Math.round(Math.min(a[1], b[1]) / step), j1 = Math.round(Math.max(a[1], b[1]) / step);
          for (let j = j0; j < j1; j++, units++) assert(cell(r - 1, j) !== cell(r, j), `run at lat ${a[0]} lon ${j * step}: not a boundary`);
        } else {                                       // vertical, between columns
          assert(Math.abs(a[1] - b[1]) < 1e-9, 'run is neither horizontal nor vertical');
          const c = Math.round(a[1] / step), i0 = Math.round(Math.min(a[0], b[0]) / step), i1 = Math.round(Math.max(a[0], b[0]) / step);
          for (let i = i0; i < i1; i++, units++) assert(cell(i, c - 1) !== cell(i, c), `run at lon ${a[1]} lat ${i * step}: not a boundary`);
        }
      });
      // ... and every boundary unit is covered (count them independently).
      let want = 0;
      has.forEach((k) => {
        const [i, j] = k.split(',').map(Number);
        [[i - 1, j], [i + 1, j], [i, j - 1], [i, j + 1]].forEach(([a, b]) => { if (!cell(a, b)) want++; });
      });
      assert(units === want, `boundary units drawn=${units} want ${want}`);
    });

    // On a short viewport the expanded picker list is taller than the space
    // beside the legend, so the boxes may intersect; what must hold is that the
    // picker is on top: every sampled point of it hits the picker itself.
    await test('the expanded layers picker is on top of the ducting panel', async () => {
      await openPicker(page);
      await sleep(300);
      const res = await page.evaluate(() => {
        const pk = document.querySelector('.leaflet-control-layers-expanded');
        if (!pk) return { err: 'picker did not expand' };
        const r = pk.getBoundingClientRect(), bad = [];
        for (let i = 1; i <= 5; i++) {
          for (let j = 1; j <= 9; j++) {
            const x = r.left + r.width * i / 6, y = r.top + r.height * j / 10;
            if (y >= window.innerHeight) continue;
            const el = document.elementFromPoint(x, y);
            if (!el || !pk.contains(el)) bad.push(`${Math.round(x)},${Math.round(y)} -> ${el ? (el.className || el.tagName) : 'nothing'}`);
          }
        }
        return { bad };
      });
      await page.mouse.move(5, 5);
      assert(!res.err, res.err);
      assert(!res.bad.length, 'picker covered at ' + res.bad.slice(0, 5).join('; '));
    });

    await test('the field image matches the data at the current forecast hour, aligned with the map', async () => {
      idx0 = await timeIdx(page);
      assert(idx0 >= 0 && idx0 < data.times.length, 'timeline index ' + idx0);
      const f = await fieldProblems(page, data, idx0);
      assert(!f.probs.length, f.probs.slice(0, 5).join(' | '));
      assert(f.calm > 0, 'no calm cell to check for transparency');
      const panes = await page.evaluate(() => ['mcDuctField', 'mcDuctLine'].map((n) => { const p = document.querySelector('.leaflet-' + n + '-pane'); const cs = p && getComputedStyle(p); return cs ? { z: +cs.zIndex, pe: cs.pointerEvents, canvases: p.querySelectorAll('canvas').length } : null; }));
      assert(panes[0] && panes[0].z > 200 && panes[0].z < 400 && panes[0].pe === 'none', 'field pane ' + JSON.stringify(panes[0]));
      assert(panes[1] && panes[1].z > panes[0].z && panes[1].z < 400 && panes[1].pe === 'none' && panes[1].canvases === 1, 'line pane ' + JSON.stringify(panes[1]));
      assert(!(await page.evaluate(() => document.querySelectorAll('.leaflet-overlay-pane canvas').length)), 'the overlay put a canvas in the overlay pane');
    });

    await test('LoRa cells, and only they, have the dashed outline', async () => {
      const o = outlineProblems(await pageState(page), data, idx0, (await cssVars(page)).lora);
      assert(!o.probs.length, o.probs.slice(0, 5).join(' | '));
      assert(o.n > 0, 'no LoRa duct at hour ' + idx0);
    });

    await test('the timeline keys (End, ArrowLeft) change the field, the outlines and the label', async () => {
      const before = (await page.evaluate(inPageFieldProbe, { idx: idx0, cells: [] })).sum;
      const label0 = await page.locator('.mc-duct-time-label').first().textContent();
      const max = await page.evaluate(() => parseInt(document.querySelector('.mc-duct-timeline').getAttribute('aria-valuemax'), 10));
      assert(max === data.times.length - 1, `timeline max ${max}`);
      await page.locator('.mc-duct-timeline').first().focus();
      await page.keyboard.press('End');
      assert((await timeIdx(page)) === max, 'End did not go to the last time');
      await page.keyboard.press('ArrowLeft');
      assert((await timeIdx(page)) === max - 1, 'ArrowLeft did not step back');
      await page.keyboard.press('ArrowRight');
      const after = await fieldProblems(page, data, max);
      assert(after.r.sum !== before, 'field image did not change');
      assert(!after.probs.length, after.probs.slice(0, 5).join(' | '));
      const o = outlineProblems(await pageState(page), data, max, (await cssVars(page)).lora);
      assert(!o.probs.length, o.probs.slice(0, 5).join(' | '));
      assert((await page.locator('.mc-duct-time-label').first().textContent()) !== label0, 'label did not change');
      assert((await pageState(page)).fields.length === 1, 'the timeline added a field image');
      const vt = await page.evaluate(() => document.querySelector('.mc-duct-timeline').getAttribute('aria-valuetext'));
      assert((await page.locator('.mc-duct-time-label').first().textContent()) === 'Forecast time: ' + vt, 'aria-valuetext differs from the label: ' + vt);
      await setTime(page, idx0);
      const back = await fieldProblems(page, data, idx0);
      assert(!back.probs.length && back.r.sum === before, 'after moving back: ' + back.probs.slice(0, 3).join(' | '));
    });

    // A real mouse click at a free point of a cell of class wantClass at the
    // shown hour; the popup comes from the overlay's map click handler.
    async function popupFor(wantClass) {
      const cell = data.cells.find(c => c.c[idx0] === wantClass);
      assert(cell, `no class ${wantClass} cell at hour ${idx0}`);
      for (const pt of await pointsIn(page, cell, data.stepDeg, 6)) {
        await page.mouse.click(pt.x, pt.y);
        try {
          await page.waitForSelector('.leaflet-popup .mc-duct-popup', { timeout: 2500 });
          return { text: await page.locator('.leaflet-popup .mc-duct-popup').first().innerText(), cell };
        } catch (_) { /* try the next point */ }
      }
      throw new Error(`could not open the popup of cell ${cell.lat},${cell.lon} (class ${wantClass})`);
    }
    // The popup's LoRa verdict lines for cell c at hour h: one per judged frequency.
    const verdicts = (c, h) => LORA_FREQS.map(fr => `LoRa ${fmtFreq(fr)} MHz: ` + (shownFmin(c, h) > 0 && shownFmin(c, h) <= fr ? 'possibly trapped (model estimate)' : 'not trapped by this layer'));

    await test('clicking a surface-duct cell opens its popup: trapping layer, frequency estimate, LoRa possibly trapped', async () => {
      const r = await popupFor(4);
      const f = r.cell.f[idx0];
      assert(/Surface-based duct/.test(r.text), 'popup: ' + r.text);
      assert(r.text.includes('Trapping layer in the model (its levels are ~200 m apart, so thinner layers are not resolved)'), 'popup: ' + r.text);
      assert(r.text.includes(`Lowest trapped frequency: \u2248 ${sig2(f)} MHz (model estimate)`), `popup (f=${f}): ` + r.text);
      const lines = r.text.split('\n').filter(l => /^LoRa /.test(l));
      assert(JSON.stringify(lines) === JSON.stringify(verdicts(r.cell, idx0)), `popup LoRa lines (f=${f}) ${JSON.stringify(lines)} want ${JSON.stringify(verdicts(r.cell, idx0))}`);
      assert(lines.some(l => /possibly trapped/.test(l)), 'surface duct of the fake traps LoRa: ' + r.text);
      assert(!/Can carry|: yes|: no\b/.test(r.text), 'old yes/no wording: ' + r.text);
      assert(/Base: 0 m/.test(r.text), 'popup: ' + r.text);
      const n = fieldN(r.cell, idx0);
      if (n != null) assert(r.text.includes('Gradient, lowest 1.5 km: ' + (n < 0 ? '\u2212' + -n : n) + ' N/km'), 'popup gradient (n=' + n + '): ' + r.text);
    });
    await test('clicking a super-refraction cell: no trapping layer, LoRa not trapped', async () => {
      const r = await popupFor(2);
      const lines = r.text.split('\n').filter(l => /^LoRa /.test(l));
      assert(/Super-refraction/.test(r.text) && /Lowest trapped frequency: none/.test(r.text), 'popup: ' + r.text);
      assert(lines.length === LORA_FREQS.length && lines.every(l => / MHz: not trapped by this layer$/.test(l)), 'popup LoRa lines: ' + JSON.stringify(lines));
      assert(!/Trapping layer/.test(r.text), 'popup: ' + r.text);
    });
    await test('a map click in the forecast area reaches the page\'s own map click listeners; the popup follows the timeline', async () => {
      const cell = data.cells.find(c => c.c[idx0] === 4);
      const pts = await pointsIn(page, cell, data.stepDeg, 1);
      assert(pts.length, 'no free point in the cell');
      await page.evaluate(() => {
        const m = window.__mcHarness.maps.filter(x => x._container && x._container.isConnected && x._loaded)[0];
        window.__ductTestClicks = 0;
        window.__ductTestOn = () => { window.__ductTestClicks++; };
        m.on('click', window.__ductTestOn);
      });
      await page.mouse.click(pts[0].x, pts[0].y);
      await page.waitForSelector('.leaflet-popup .mc-duct-popup', { timeout: 2500 });
      const got = await page.evaluate(() => {
        const m = window.__mcHarness.maps.filter(x => x._container && x._container.isConnected && x._loaded)[0];
        m.off('click', window.__ductTestOn);
        return window.__ductTestClicks;
      });
      assert(got === 1, 'map click listeners saw ' + got + ' clicks');
      // Another hour: the open popup shows that hour's analysis of the same cell.
      const other = cell.c.findIndex((v, h) => h !== idx0 && v >= 1 && v !== 4);
      if (other >= 0) {
        await setTime(page, other);
        const t = await page.locator('.leaflet-popup .mc-duct-popup').first().innerText().catch(() => '');
        assert(t && !/Surface-based duct/.test(t), `popup at hour ${other} (class ${cell.c[other]}): ${t}`);
        await setTime(page, idx0);
      }
      await page.evaluate(() => window.__mcHarness.maps.forEach(m => m.closePopup && m.closePopup()));
    });

    await test('theme toggle redraws the field and outlines from the CSS variables', async () => {
      await page.evaluate(() => window.__mcHarness.maps.forEach(m => m.closePopup && m.closePopup()));
      const light = await cssVars(page);
      const before = (await page.evaluate(inPageFieldProbe, { idx: idx0, cells: [] })).sum;
      const cells0 = (await pageState(page)).cells;
      await MH.setTheme(page, 'dark');
      await page.waitForFunction(() => document.documentElement.getAttribute('data-theme') === 'dark');
      const dark = await cssVars(page);
      assert(dark.s4 && dark.s4 !== light.s4 && dark.lora !== light.lora, `--mc-duct-* did not change with the theme (${light.s4}/${dark.s4}, ${light.lora}/${dark.lora})`);
      let f = null;
      for (let i = 0; i < 30; i++) {
        f = await fieldProblems(page, data, idx0);
        if (!f.probs.length && f.r.sum !== before) break;
        await sleep(200);
      }
      assert(f.r.sum !== before, 'field image unchanged after the theme change');
      assert(!f.probs.length, 'after theme change: ' + f.probs.slice(0, 4).join(' | '));
      const st = await pageState(page);
      const o = outlineProblems(st, data, idx0, dark.lora);
      assert(!o.probs.length, 'dark outlines: ' + o.probs.slice(0, 3).join(' | '));
      assert(st.cells === cells0 && st.fields.length === 1, `cells ${cells0}->${st.cells}, fields ${st.fields.length}`);
      const ov = await page.evaluate(inPageOverlaps);
      assert(!ov.hits.length, 'dark: ' + ov.hits.join('; '));
      await MH.setTheme(page, 'light');
    });

    // ── The panel and the timeline by pointer ──
    await test('collapse hides only the legend body; header and timeline stay', async () => {
      const btn = page.locator('.mc-duct-collapse').first();
      assert((await btn.getAttribute('aria-label')) === 'Collapse ducting legend', 'label: ' + await btn.getAttribute('aria-label'));
      const h0 = (await panelState(page)).h;
      await btn.click();
      let p = await panelState(page);
      assert(!p.legend && p.time && p.expanded === 'false', 'collapsed: ' + JSON.stringify(p));
      assert(p.h < h0 - 40, `panel did not shrink (${h0} -> ${p.h})`);
      assert(await page.locator('.mc-duct-title').first().isVisible(), 'title hidden');
      assert((await btn.getAttribute('aria-label')) === 'Expand ducting legend');
      assert(p.stored && p.stored.collapsed === true, 'not stored: ' + JSON.stringify(p.stored));
      const ov = await page.evaluate(inPageOverlaps);
      assert(!ov.hits.length, 'collapsed: ' + ov.hits.join('; '));
      await btn.click();
      p = await panelState(page);
      assert(p.legend && p.time && p.expanded === 'true', 'expanded again: ' + JSON.stringify(p));
    });

    await test('wheel, drag and click on the strip change the hour; the map neither pans nor zooms', async () => {
      await page.evaluate(() => window.__mcHarness.maps.forEach(m => m.closePopup && m.closePopup()));
      await setTime(page, 0);
      const v0 = await page.evaluate(inPageView);
      const box = await page.locator('.mc-duct-timeline').first().boundingBox();
      const cx = box.x + box.width / 2, cy = box.y + box.height / 2;
      await page.mouse.move(cx, cy);
      await page.mouse.wheel(0, 60);
      await page.waitForFunction(() => document.querySelector('.mc-duct-timeline').getAttribute('aria-valuenow') !== '0', null, { timeout: 3000 });
      await sleep(300);
      let v = await settled(page);
      assert(v.idx > 0, 'wheel did not move the hour');
      assert(sameView(v0, v), `wheel moved the map: ${JSON.stringify([v0.px, v0.py, v0.zoom])} -> ${JSON.stringify([v.px, v.py, v.zoom])}`);
      assert(Math.abs(v.scrollLeft - v.xs[v.idx]) <= 1, `not settled: scrollLeft ${v.scrollLeft}, time x ${v.xs[v.idx]}`);
      // Drag left by 40 px: later by ~40 px of strip.
      const i1 = v.idx, x1 = v.scrollLeft;
      await page.mouse.move(cx, cy);
      await page.mouse.down();
      await page.mouse.move(cx - 20, cy, { steps: 4 });
      await page.mouse.move(cx - 40, cy, { steps: 4 });
      await page.mouse.up();
      await sleep(300);
      v = await settled(page);
      assert(v.idx > i1, `drag did not move the hour (${i1} -> ${v.idx})`);
      assert(Math.abs(v.xs[v.idx] - (x1 + 40)) <= (v.xs[1] - v.xs[0]), `drag landed at ${v.xs[v.idx]}, want ~${x1 + 40}`);
      assert(Math.abs(v.scrollLeft - v.xs[v.idx]) <= 1, `not settled after the drag: ${v.scrollLeft} vs ${v.xs[v.idx]}`);
      assert(sameView(v0, v), 'drag on the strip moved the map');
      // A click 30 px right of the centre picks the time ~30 px later.
      const i2 = v.idx, x2 = v.scrollLeft;
      await page.mouse.click(cx + 30, cy);
      v = await settled(page);
      assert(v.idx > i2 && Math.abs(v.xs[v.idx] - (x2 + 30)) <= (v.xs[1] - v.xs[0]), `click picked ${v.idx} (x ${v.xs[v.idx]}), want ~x ${x2 + 30}`);
      assert(sameView(v0, v), 'click on the strip moved the map');
      // The field follows the picked time.
      const f = await fieldProblems(page, data, v.idx);
      assert(!f.probs.length, f.probs.slice(0, 3).join(' | '));
    });

    await test('Now returns to the current hour', async () => {
      await page.locator('.mc-duct-timeline').first().focus();
      await page.keyboard.press('End');
      await page.locator('.mc-duct-now').first().click();
      const v = await settled(page);
      const nowS = await page.evaluate(() => Date.now() / 1000);
      let want = 0;
      v.times.forEach((t, i) => { if (t <= nowS) want = i; });
      assert(v.idx === want, `Now picked ${v.idx}, want ${want}`);
      assert(Math.abs(v.scrollLeft - v.xs[want]) <= 1, `strip not on the hour: ${v.scrollLeft} vs ${v.xs[want]}`);
      idx0 = v.idx;
    });

    await test('dragging the handle moves the panel and the map stays put; Reset puts it back', async () => {
      const v0 = await page.evaluate(inPageView);
      const p0 = await panelState(page);
      assert(p0.inCorner && !p0.floating && !p0.reset, 'not docked at the start: ' + JSON.stringify(p0));
      const hb = await page.locator('.mc-duct-drag').first().boundingBox();
      await page.mouse.move(hb.x + hb.width / 2, hb.y + hb.height / 2);
      await page.mouse.down();
      await page.mouse.move(hb.x + hb.width / 2 + 200, hb.y + hb.height / 2 - 150, { steps: 8 });
      await page.mouse.move(hb.x + hb.width / 2 + 400, hb.y + hb.height / 2 - 300, { steps: 8 });
      await page.mouse.up();
      const p = await panelState(page);
      assert(p.floating && p.inMap && !p.inCorner && p.reset, 'not floating: ' + JSON.stringify(p));
      assert(Math.abs(p.l - p0.l - 400) <= 2 && Math.abs(p.t - p0.t + 300) <= 2, `moved by ${p.l - p0.l},${p.t - p0.t}, want 400,-300`);
      assert(p.stored && p.stored.pos && p.stored.pos.fx > 0 && p.stored.pos.fy >= 0, 'position not stored: ' + JSON.stringify(p.stored));
      const v = await settled(page);
      assert(sameView(v0, v), 'the map moved with the panel');
      assert(v.idx === v0.idx && Math.abs(v.scrollLeft - v.xs[v.idx]) <= 1, `strip lost its place in the move: idx ${v0.idx}->${v.idx}, scrollLeft ${v.scrollLeft} vs ${v.xs[v.idx]}`);
      // Dragging far outside: it stays 8 px inside the map, and where the
      // page's Map Controls cover the map's right side it stops beside them,
      // its handle, collapse and Now buttons still clickable.
      const hb2 = await page.locator('.mc-duct-drag').first().boundingBox();
      await page.mouse.move(hb2.x + 5, hb2.y + 5);
      await page.mouse.down();
      await page.mouse.move(5000, -500, { steps: 6 });
      await page.mouse.up();
      const clamp = await page.evaluate(() => {
        const panel = document.querySelector('.mc-duct-panel');
        const p = panel.getBoundingClientRect(), c = panel.parentNode.getBoundingClientRect();
        const covered = ['.mc-duct-drag', '.mc-duct-collapse', '.mc-duct-now'].filter((sel) => {
          const r = panel.querySelector(sel).getBoundingClientRect();
          return [r.left + r.width / 2, r.right - 2].some(x => !panel.contains(document.elementFromPoint(x, r.top + r.height / 2)));
        });
        const mc = document.getElementById('mapControls');
        const mcr = mc && mc.offsetParent ? mc.getBoundingClientRect() : null;
        return { right: c.right - p.right, top: p.top - c.top, left: p.left - c.left, covered, besideMc: mcr ? p.right <= mcr.left + 4 : null };
      });
      assert(Math.abs(clamp.top - 8) <= 1 && clamp.right >= 7 && clamp.left >= 7, 'not 8 px inside the map: ' + JSON.stringify(clamp));
      assert(!clamp.covered.length, 'covered: ' + JSON.stringify(clamp));
      assert(clamp.besideMc !== false || Math.abs(clamp.right - 8) <= 1, 'neither at the right margin nor beside Map Controls: ' + JSON.stringify(clamp));
      assert(sameView(v0, await page.evaluate(inPageView)), 'the map moved');
      // The expanded picker is still on top of the moved panel.
      const ov = await page.evaluate(inPageOverlaps);
      const onTop = await page.evaluate(() => {
        const ctl = document.querySelector('.leaflet-control-zoom');
        if (!ctl) return true;
        const r = ctl.getBoundingClientRect(), el = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
        return ctl.contains(el);
      });
      assert(onTop, 'zoom control covered by the moved panel ' + ov.hits.join('; '));
      await page.locator('.mc-duct-reset').first().click();
      const back = await panelState(page);
      assert(back.inCorner && !back.floating && !back.reset, 'not back in the corner: ' + JSON.stringify(back));
      assert(Math.abs(back.l - p0.l) <= 1 && Math.abs(back.t - p0.t) <= 1, `corner place changed: ${back.l},${back.t} vs ${p0.l},${p0.t}`);
      assert(back.stored && back.stored.pos === null, 'position still stored');
      const vb = await settled(page);
      assert(Math.abs(vb.scrollLeft - vb.xs[vb.idx]) <= 1, `strip lost its place on reset: ${vb.scrollLeft} vs ${vb.xs[vb.idx]}`);
      const ov2 = await page.evaluate(inPageOverlaps);
      assert(!ov2.hits.length, 'after reset: ' + ov2.hits.join('; '));
    });

    await test('the strip is wide enough to slide to an hour: >= 220 px at 1440x900, >= 200 px at 390x844; the time is never cut', async () => {
      let wp = null;
      const measure = () => wp.page.evaluate(() => {
        const tl = document.querySelector('.mc-duct-timeline');
        const lab = document.querySelector('.mc-duct-time-label'), val = document.querySelector('.mc-duct-time-value');
        const now = document.querySelector('.mc-duct-time-head .mc-duct-now');
        const lr = lab.getBoundingClientRect(), vr = val.getBoundingClientRect();
        // Every day label inside the strip shows its whole text (none narrower than its content).
        const cut = Array.prototype.filter.call(document.querySelectorAll('.mc-duct-day-label'), (d) => d.scrollWidth > d.clientWidth + 1).length;
        return { strip: tl.clientWidth, panel: document.querySelector('.mc-duct-panel').getBoundingClientRect().width, oneLine: lr.height < 24, timeShown: vr.right <= lr.right + 0.5 && vr.left >= lr.left - 0.5, now: !!now, cut };
      });
      // A fresh page per size: the map page lays itself out for the size it loads at.
      for (const [w, h, min] of [[1440, 900, 220], [390, 844, 200]]) {
        wp = await track({ theme: 'light', overlays: ['ducting'] });
        await wp.page.setViewportSize({ width: w, height: h });
        await gotoRoute(wp.page, '#/map');
        await waitCells(wp.page, 1);
        await wp.page.waitForSelector('.mc-duct-timeline', { state: 'visible' });
        await sleep(300);
        const m = await measure();
        assert(m.strip >= min, `${w}x${h}: strip ${m.strip}px < ${min}px (panel ${m.panel}px)`);
        assert(m.oneLine && m.timeShown && m.now, `${w}x${h}: forecast-time line ${JSON.stringify(m)}`);
        assert(!m.cut, `${w}x${h}: ${m.cut} day labels cut`);
        const ov = await wp.page.evaluate(inPageOverlaps);
        assert(!ov.hits.length, `${w}x${h}: ` + ov.hits.join('; '));
        assert(!wp.page.__errors.length, wp.page.__errors.slice(0, 3).join(' | '));
      }
    });

    await test('a moved, collapsed panel keeps its place and state across a reload', async () => {
      const rp = await track({ theme: 'light', overlays: ['ducting'] });
      await gotoRoute(rp.page, '#/map');
      await waitCells(rp.page, 1);
      await rp.page.waitForSelector('.mc-duct-panel', { state: 'visible' });
      await rp.page.mouse.move(5, 5);
      const hb = await rp.page.locator('.mc-duct-drag').first().boundingBox();
      await rp.page.mouse.move(hb.x + hb.width / 2, hb.y + hb.height / 2);
      await rp.page.mouse.down();
      await rp.page.mouse.move(hb.x + hb.width / 2 + 300, hb.y + hb.height / 2 - 250, { steps: 8 });
      await rp.page.mouse.up();
      await rp.page.locator('.mc-duct-collapse').first().click();
      const before = await panelState(rp.page);
      assert(before.floating && !before.legend && before.time, 'before reload: ' + JSON.stringify(before));
      await rp.page.reload({ waitUntil: 'domcontentloaded' });
      await waitCells(rp.page, 1);
      await rp.page.waitForSelector('.mc-duct-panel', { state: 'visible' });
      await sleep(300);
      const after = await panelState(rp.page);
      assert(after.floating && after.inMap && !after.legend && after.time && after.expanded === 'false', 'after reload: ' + JSON.stringify(after));
      assert(Math.abs(after.l - before.l) <= 2 && Math.abs(after.t - before.t) <= 2, `place changed: ${before.l},${before.t} -> ${after.l},${after.t}`);
      const va = await settled(rp.page);
      assert(Math.abs(va.scrollLeft - va.xs[va.idx]) <= 1, `restored panel's strip not on the shown time: ${va.scrollLeft} vs ${va.xs[va.idx]}`);
      await rp.page.locator('.mc-duct-reset').first().click();
      const reset = await panelState(rp.page);
      assert(reset.inCorner && !reset.legend, 'reset keeps collapsed, docks: ' + JSON.stringify(reset));
      assert(!rp.page.__errors.length, rp.page.__errors.slice(0, 3).join(' | '));
    });

    // Leaflet 1.9.4: a map move redraws the canvas at once and orphans the
    // queued frame; switching off before that frame used to throw from the
    // orphan ("reading 'clearRect'", stress gate seed 303, scenario g).
    await test('switching off right after a canvas move redraw throws nothing', async () => {
      const before = page.__errors.length;
      const queued = await page.evaluate(() => {
        let line = null;
        window.__mcHarness.maps.forEach(m => { if (m._loaded) m.eachLayer(l => { if (!line && l._mcEdge && l._renderer) line = l; }); });
        if (!line) return 'no edge line';
        const r = line._renderer;
        line.setStyle({ opacity: 0.5 });       // queues a frame redraw
        if (!r._redrawRequest) return 'no redraw queued';
        r._updatePaths();                      // what every moveend does
        line.setStyle({ opacity: 0.4 });       // marks an area to redraw
        window.MC_setTileOverlay('ducting', false);
        return 'ok';
      });
      assert(queued === 'ok', queued);
      await sleep(300);
      const errs = page.__errors.slice(before);
      assert(!errs.length, errs.join(' | '));
      await page.evaluate(() => window.MC_setTileOverlay('ducting', true));
      await waitCells(page, 1);
    });

    await test('disabling the overlay removes the panel (legend, timeline) and cells', async () => {
      await setOverlayUi(page, false);
      await page.waitForFunction(() => !document.querySelector('.mc-duct-panel') && !document.querySelector('.mc-duct-legend') && !document.querySelector('.mc-duct-time'), null, { timeout: 5000 });
      const st = await pageState(page);
      assert(!st.cells && !st.lora.length && !st.edges.length, `${st.cells} cells, ${st.lora.length} outlines, ${st.edges.length} edges still drawn`);
      assert(!(await page.evaluate(() => document.querySelectorAll('.leaflet-mcDuctLine-pane canvas').length)), 'line canvas left in the DOM');
      assert(!st.fields.length, 'field image left on the map');
      assert(!(await page.evaluate(() => document.querySelectorAll('.mc-duct-field').length)), 'field canvas left in the DOM');
      assert(st.panels === 0 && st.legends === 0 && st.times === 0, 'panel/legend/time boxes left');
      assert(await page.evaluate(() => JSON.parse(localStorage.getItem('mc-tile-overlays') || '[]').indexOf('ducting') < 0), 'preference still has ducting');
    });

    await test('re-enabling after a disable draws one panel with one legend and one timeline, no duplicates', async () => {
      await setOverlayUi(page, true);
      await waitCells(page, 1);
      await setOverlayUi(page, false);
      await setOverlayUi(page, true);
      await waitCells(page, data.cells.length);
      await sleep(300);
      const st = await pageState(page);
      assert(st.panels === 1 && st.legends === 1 && st.times === 1 && st.cells === data.cells.length && st.edges.length === 1 && st.lora.length <= 1, `legends=${st.legends} times=${st.times} cells=${st.cells} edges=${st.edges.length} outlines=${st.lora.length}`);
      assert(st.fields.length === 1 && await page.evaluate(() => document.querySelectorAll('.mc-duct-field').length) === 1, 'field images: ' + st.fields.length);
      await setOverlayUi(page, false);
    });

    await test('map page has no page errors', async () => { assert(!page.__errors.length, page.__errors.slice(0, 3).join(' | ')); });

    // ── Live ──
    let livePage = null;
    await test('live page: entry present, enabling draws cells and an unobstructed legend', async () => {
      const lp = await track({ theme: 'light', overlays: ['ducting'] });
      await gotoRoute(lp.page, '#/live');
      await lp.page.waitForSelector('label[data-tile-overlay-id="ducting"]', { state: 'attached', timeout: 25000 });
      await waitCells(lp.page, data.cells.length);
      await lp.page.waitForSelector('.mc-duct-legend', { state: 'visible' });
      await lp.page.mouse.move(5, 5);
      const st = await pageState(lp.page);
      assert(st.legends === 1 && st.times === 1, `legends=${st.legends} times=${st.times}`);
      assert(st.fields.length === 1 && st.fields[0].inDom, 'live field images: ' + st.fields.length);
      assert(!st.interactive.length, 'interactive overlay layers: ' + st.interactive.join(','));
      const ov = await lp.page.evaluate(inPageOverlaps);
      assert(!ov.hits.length, ov.hits.join('; '));
      assert(!lp.page.__errors.length, lp.page.__errors.slice(0, 3).join(' | '));
      livePage = lp.page;
    });

    // Live draws its clickable packet paths on its own canvas (preferCanvas)
    // in the overlay pane. The overlay must not put a canvas above it, and a
    // click inside the forecast area must reach Live's map and its paths.
    await test('live page: a click inside the forecast area reaches Live\'s map and canvas paths; no ducting popup over them', async () => {
      assert(livePage, 'live page did not load');
      const lpg = livePage;
      const cell = data.cells.find(c => c.c.some(v => v >= 1));
      const pts = await pointsIn(lpg, cell, data.stepDeg, 3);
      assert(pts.length, 'no free point in a forecast cell on Live');
      const pt = pts[0];
      // Nothing of the overlay is hit-tested at the click point.
      const top = await lpg.evaluate((p) => {
        const el = document.elementFromPoint(p.x, p.y);
        return { inDuct: !!(el && el.closest('.leaflet-mcDuctLine-pane, .leaflet-mcDuctField-pane')), tag: el && el.tagName };
      }, pt);
      assert(!top.inDuct, 'a ducting pane is hit-tested at the click point: ' + JSON.stringify(top));
      // A clickable path like Live's (interactive, transparent, on the map's
      // canvas renderer, a popup on click) through the click point, plus a map listener.
      const added = await lpg.evaluate((p) => {
        const m = window.__mcHarness.maps.filter(x => x._container && x._container.isConnected && x._loaded)[0];
        const r = m.getContainer().getBoundingClientRect();
        const a = m.containerPointToLatLng([p.x - r.left - 60, p.y - r.top]), b = m.containerPointToLatLng([p.x - r.left + 60, p.y - r.top]);
        window.__ductLive = { path: 0, map: 0 };
        const poly = L.polyline([a, b], { weight: 12, opacity: 0, interactive: true }).addTo(m);
        poly.on('click', (e) => { window.__ductLive.path++; L.popup({ className: 'path-info-popup' }).setLatLng(e.latlng).setContent('<div class="duct-test-path">path</div>').openOn(m); });
        window.__ductLive.poly = poly;
        window.__ductLive.onMap = () => { window.__ductLive.map++; };
        m.on('click', window.__ductLive.onMap);
        return { canvas: !!(poly._renderer && poly._renderer._container && poly._renderer._container.tagName === 'CANVAS'), pane: poly._renderer && poly._renderer.options.pane };
      }, pt);
      assert(added.canvas && added.pane === 'overlayPane', 'test path not on Live\'s overlay-pane canvas: ' + JSON.stringify(added));
      await lpg.mouse.move(pt.x, pt.y - 2);
      await lpg.mouse.move(pt.x, pt.y);
      await sleep(100);
      await lpg.mouse.click(pt.x, pt.y);
      await sleep(400);
      const res = await lpg.evaluate(() => {
        const m = window.__mcHarness.maps.filter(x => x._container && x._container.isConnected && x._loaded)[0];
        m.off('click', window.__ductLive.onMap);
        const out = { path: window.__ductLive.path, map: window.__ductLive.map, pathPopup: !!document.querySelector('.leaflet-popup .duct-test-path'), ductPopup: !!document.querySelector('.leaflet-popup .mc-duct-popup') };
        window.__ductLive.poly.remove();
        m.closePopup();
        return out;
      });
      assert(res.path === 1, 'Live\'s canvas path did not get the click: ' + JSON.stringify(res));
      assert(res.map === 1, 'the map click did not propagate past the overlay: ' + JSON.stringify(res));
      assert(res.pathPopup && !res.ductPopup, 'the path\'s popup must stay, no ducting popup over it: ' + JSON.stringify(res));
      // A click on free map (no path) opens the ducting popup there.
      await lpg.mouse.move(5, 5);
      await lpg.mouse.click(pt.x, pt.y);
      await lpg.waitForSelector('.leaflet-popup .mc-duct-popup', { timeout: 2500 });
      await lpg.evaluate(() => window.__mcHarness.maps.forEach(m => m.closePopup && m.closePopup()));
      assert(!lpg.__errors.length, lpg.__errors.slice(0, 3).join(' | '));
    });

    // ── Planner (opt-in) ──
    if (cfg.planner) {
      await test('planner page: entry present', async () => {
        const pp = await track({ theme: 'light' });
        await gotoRoute(pp.page, '#/tools/planner');
        await pp.page.waitForSelector('label[data-tile-overlay-id="ducting"]', { state: 'attached', timeout: 25000 });
        assert(!pp.page.__errors.length, pp.page.__errors.slice(0, 3).join(' | '));
      });
      // Planner places a site on a map click (map.on('click') in planner.js).
      // Interactive cells with popups used to stop that click everywhere in
      // the forecast area.
      await test('planner page: with the overlay on, a click inside the forecast area places a site (and opens no ducting popup)', async () => {
        const pp = await track({ theme: 'light', overlays: ['ducting'] });
        await gotoRoute(pp.page, '#/tools/planner?mode=coverage');
        await waitCells(pp.page, data.cells.length);
        await pp.page.waitForSelector('.mc-duct-legend', { state: 'visible' });
        await pp.page.mouse.move(5, 5);
        assert(!(await pp.page.locator('path.mc-pl-cov-site').count()), 'a site before any click');
        const idx = await timeIdx(pp.page);
        // A cell with data at the shown hour, so its popup would open if the click went to the overlay.
        const cell = data.cells.find(c => c.c[idx] >= 3) || data.cells.find(c => c.c[idx] >= 1);
        const pts = await pointsIn(pp.page, cell, data.stepDeg, 4);
        assert(pts.length, 'no free point in the cell');
        await pp.page.mouse.click(pts[0].x, pts[0].y);
        await pp.page.waitForSelector('path.mc-pl-cov-site', { timeout: 5000 }).catch(() => {});
        await sleep(300);
        const res = await pp.page.evaluate(() => ({ site: document.querySelectorAll('path.mc-pl-cov-site').length, duct: !!document.querySelector('.leaflet-popup .mc-duct-popup') }));
        assert(res.site === 1, `no site placed by a click at ${cell.lat},${cell.lon} (overlay on): ` + JSON.stringify(res));
        assert(!res.duct, 'the click placed a site and also opened the ducting popup over it');
        const st = await pageState(pp.page);
        assert(!st.interactive.length, 'interactive overlay layers: ' + st.interactive.join(','));
        assert(!pp.page.__errors.length, pp.page.__errors.slice(0, 3).join(' | '));
      });
    } else {
      skip('planner page: entry present', 'planner disabled on this server');
      skip('planner page: a click inside the forecast area places a site', 'planner disabled on this server');
    }

    // ── Config / API states ──
    await test('without ducting in the client config there is no overlay entry', async () => {
      const np = await track({ theme: 'light', overlays: ['ducting'], stripDucting: true });
      await gotoRoute(np.page, '#/map');
      await openPicker(np.page);
      await np.page.waitForSelector('.leaflet-control-layers-overlays', { state: 'attached' });
      assert((await np.page.locator('label[data-tile-overlay-id="ducting"]').count()) === 0, 'entry present');
      await sleep(400);
      const st = await pageState(np.page);
      assert(!st.cells && !st.fields.length && !st.legends && !st.times, 'overlay drawn although the server has none');
      assert(!np.page.__errors.length, np.page.__errors.slice(0, 3).join(' | '));
    });

    for (const [name, status, body, want] of [
      ['pending', 200, { status: 'pending', times: [], cells: [] }, /being prepared/],
      ['error', 200, { status: 'error', error: 'weather data unavailable', times: [], cells: [] }, /unavailable/],
      ['HTTP 500', 500, { error: 'x' }, /unavailable/],
    ]) {
      await test(`the status line explains a ${name} /api/ducting answer, with no cells, also when collapsed`, async () => {
        const sp = await track({ theme: 'light', overlays: ['ducting'] });
        await sp.page.route('**/api/ducting', (route) => route.fulfill({ status, json: body }));
        await gotoRoute(sp.page, '#/map');
        await sp.page.waitForSelector('.mc-duct-legend', { state: 'visible', timeout: 25000 });
        await sp.page.waitForFunction((src) => new RegExp(src).test((document.querySelector('.mc-duct-status') || {}).textContent || ''), want.source, { timeout: 10000 });
        let ss = await sp.page.evaluate(inPageStatus);
        assert(ss.count === 1 && !ss.inLegend && want.test(ss.text), 'status: ' + JSON.stringify(ss));
        await sp.page.locator('.mc-duct-collapse').first().click();
        ss = await sp.page.evaluate(inPageStatus);
        assert(!ss.legend && ss.count === 1 && want.test(ss.text) && ss.inView, 'collapsed: ' + JSON.stringify(ss));
        const st = await pageState(sp.page);
        assert(!st.cells && !st.lora.length && !st.fields.length, 'cells or field drawn');
        const errs = sp.page.__errors.filter(e => !/Failed to load resource/.test(e));
        assert(!errs.length, errs.slice(0, 3).join(' | '));
      });
    }

    // A forecast whose last hour has ended is out of date at once. Phones
    // start collapsed; the notice must not be hidden with the legend.
    await test('out-of-date notice stays visible in a collapsed panel: 390x844 (collapsed by default) and 1280x800 (by click)', async () => {
      const old = shifted(data, data.times.length + 1);
      for (const [w, h] of [[390, 844], [1280, 800]]) {
        const op = await track({ theme: 'light', overlays: ['ducting'] });
        await op.page.setViewportSize({ width: w, height: h });
        await op.page.route('**/api/ducting', (route) => route.fulfill({ status: 200, json: old }));
        await gotoRoute(op.page, '#/map');
        await waitCells(op.page, 1);
        await op.page.waitForSelector('.mc-duct-timeline', { state: 'visible' });
        if (w > 600) await op.page.locator('.mc-duct-collapse').first().click();
        await sleep(200);
        const ss = await op.page.evaluate(inPageStatus);
        assert(!ss.legend, `${w}x${h}: legend not collapsed`);
        assert(ss.count === 1 && !ss.inLegend && /^Forecast out of date/.test(ss.text), `${w}x${h}: status ${JSON.stringify(ss)}`);
        assert(ss.inView, `${w}x${h}: collapsed panel outside the viewport`);
        const ov = await op.page.evaluate(inPageOverlaps);
        assert(!ov.hits.length, `${w}x${h}: ` + ov.hits.join('; '));
        const st = await pageState(op.page);
        assert(st.panels === 1 && st.times === 1 && st.legends === 1, `${w}x${h}: ${JSON.stringify([st.panels, st.times, st.legends])}`);
        assert(!op.page.__errors.length, op.page.__errors.slice(0, 3).join(' | '));
      }
      // A current forecast says nothing.
      const cp = await track({ theme: 'light', overlays: ['ducting'] });
      await gotoRoute(cp.page, '#/map');
      await waitCells(cp.page, 1);
      const cur = await cp.page.evaluate(inPageStatus);
      assert(cur.count === 1 && cur.text === '', 'status on a current forecast: ' + JSON.stringify(cur));
    });

    // The strip cannot scroll while the map has no layout; it must take its
    // place when the map is shown, not stay on the first hour.
    await test('a map laid out late (hidden while the forecast arrives) shows the current hour under the centre marker', async () => {
      const hp = await track({ theme: 'light', overlays: ['ducting'] });
      await hp.page.route('**/api/ducting', (route) => route.fulfill({ status: 200, json: shifted(data, 5) }));
      await hp.page.addInitScript(() => {
        const hide = () => { const st = document.createElement('style'); st.id = 'e2e-hide'; st.textContent = '#app{display:none !important}'; document.documentElement.appendChild(st); };
        if (document.documentElement) hide(); else document.addEventListener('readystatechange', hide, { once: true });
      });
      await gotoRoute(hp.page, '#/map');
      await waitCells(hp.page, 1);
      const read = () => hp.page.evaluate(() => {
        const vp = document.querySelector('.mc-duct-timeline');
        const marks = Array.prototype.map.call(document.querySelectorAll('.mc-duct-tick'), (t) => parseFloat(t.style.left));
        let under = 0;
        marks.forEach((x, i) => { if (Math.abs(x - vp.scrollLeft) < Math.abs(marks[under] - vp.scrollLeft)) under = i; });
        return { hidden: !vp.getClientRects().length, now: Number(vp.getAttribute('aria-valuenow')), under, scrollLeft: vp.scrollLeft, want: marks[Number(vp.getAttribute('aria-valuenow'))] };
      });
      let r = await read();
      assert(r.hidden && r.now === 5 && r.scrollLeft === 0, 'precondition (hidden, hour 5 shown, strip not scrolled): ' + JSON.stringify(r));
      await hp.page.evaluate(() => document.getElementById('e2e-hide').remove());
      await hp.page.waitForSelector('.mc-duct-timeline', { state: 'visible' });
      await hp.page.waitForFunction(() => { const vp = document.querySelector('.mc-duct-timeline'); return vp.scrollLeft > 0; }, null, { timeout: 5000 }).catch(() => {});
      await sleep(400);
      r = await read();
      assert(r.now === 5 && r.under === 5 && Math.abs(r.scrollLeft - r.want) < 1, 'the shown hour is not under the centre marker: ' + JSON.stringify(r));
      assert(!hp.page.__errors.length, hp.page.__errors.slice(0, 3).join(' | '));
    });

    // ── The fake itself ──
    await test('fake: error500, ratelimit, nulls, pending and validation scenarios', async () => {
      const f = await FAKE.start({ port: 0, scenario: 'error500' });
      try {
        const q = (n) => `${f.url}/v1/forecast?latitude=${Array(n).fill('51.25').join(',')}&longitude=${Array.from({ length: n }, (_, i) => (0.25 + i * 0.5).toFixed(2)).join(',')}&hourly=${FAKE.hourlyVariables().join(',')}&models=gfs_global&forecast_hours=3&timeformat=unixtime&timezone=GMT`;
        assert((await fetch(q(1))).status === 500, 'error500');
        f.setScenario('ratelimit');
        assert((await fetch(q(1))).status === 429, 'ratelimit');
        f.setScenario('nulls');
        const j = await (await fetch(q(8))).json();
        assert(Array.isArray(j) && j.length === 8 && j[0].hourly.time.length === 3, 'nulls shape');
        assert(j.some(l => l.hourly.temperature_900hPa.every(v => v === null)), 'nulls: no location without levels');
        assert(j.some(l => l.hourly.temperature_1000hPa.every(v => v !== null) && l.hourly.temperature_950hPa.every(v => v === null)), 'nulls: no location with single missing levels');
        f.setScenario('mixed');
        const one = await (await fetch(q(1))).json();
        assert(!Array.isArray(one) && one.elevation > 0 && one.hourly.surface_pressure.length === 3, 'one location is an object');
        assert((await fetch(q(51))).status === 400, '51 locations accepted');
        assert((await fetch(q(1).replace('gfs_global', 'icon_global'))).status === 400, 'wrong model accepted');
        assert((await fetch(q(1).replace('timeformat=unixtime', 'timeformat=iso8601'))).status === 400, 'wrong timeformat accepted');
        assert((await fetch(q(1).replace('temperature_850hPa', 'x'))).status === 400, 'missing variable accepted');
        assert(f.violations.length === 4, 'violations ' + f.violations.length);
        f.setScenario('pending');
        const ctl = new AbortController();
        let answered = false;
        const pend = fetch(q(1), { signal: ctl.signal }).then(() => { answered = true; }, () => {});
        await sleep(300);
        assert(!answered, 'pending answered');
        ctl.abort();
        await pend;
      } finally { await f.close(); }
    });
  } finally {
    for (const p of pages) await p.context.close().catch(() => {});
    await browser.close().catch(() => {});
    await fake.close().catch(() => {});
  }
  console.log(`\n${passed} passed, ${failed} failed, ${skipped} skipped`);
  process.exit(failed ? 1 : 0);
}

run().catch((e) => { console.error(e); process.exit(1); });

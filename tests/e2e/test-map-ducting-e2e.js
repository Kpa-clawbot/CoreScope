/**
 * E2E for the tropospheric-ducting map overlay, against a real CoreScope
 * server whose ducting source is a fake Open-Meteo (tests/e2e/lib/
 * fake-open-meteo.js, scenario 'mixed'). Nothing here reaches the real
 * Open-Meteo or a real tile host. Local only: not part of the GitHub Actions
 * E2E job.
 *
 * Start the pieces in this order (the server's first refresh runs at startup;
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
const LORA_MAX_MHZ = 1000;
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
function skip(name, why) { skipped++; console.log(`  ⏭  ${name}: ${why}`); }
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

// What the page shows right now: rectangles (with their outline) and field
// images of every live map, legend/time boxes.
function inPageState() {
  const maps = ((window.__mcHarness && window.__mcHarness.maps) || [])
    .filter(m => m && m._container && m._container.isConnected && m._loaded);
  const rects = [], fields = [], edges = [];
  maps.forEach((m, mi) => {
    m.eachLayer((l) => {
      if (l._mcEdge) {
        const parts = l.getLatLngs().map(p => p.map(ll => [ll.lat, ll.lng]));
        edges.push({ map: mi, parts, color: l.options.color, dash: l.options.dashArray, weight: l.options.weight, interactive: l.options.interactive });
      }
      if (l._mcCell) {
        const c = l._mcCell;
        rects.push({ map: mi, lat: c.lat, lon: c.lon, stroke: !!l.options.stroke, color: l.options.stroke ? l.options.color : null, dash: l.options.stroke ? l.options.dashArray : null, fillOpacity: l.options.fillOpacity });
      }
      if (l._mcField) fields.push({ map: mi, inDom: !!(l._image && l._image.isConnected), pane: l.options.pane });
    });
  });
  const vis = (el) => { const r = el.getBoundingClientRect(); const cs = getComputedStyle(el); return r.width > 0 && r.height > 0 && cs.visibility !== 'hidden' && cs.display !== 'none'; };
  const q = (sel) => Array.prototype.slice.call(document.querySelectorAll(sel));
  return {
    maps: maps.length,
    rects,
    fields,
    edges,
    legends: q('.mc-duct-legend').length,
    times: q('.mc-duct-time').length,
    legendVisible: q('.mc-duct-legend').some(vis),
    timeVisible: q('.mc-duct-time').some(vis),
    legendText: (q('.mc-duct-legend')[0] || {}).textContent || '',
  };
}
const pageState = (page) => page.evaluate(inPageState);
async function waitRects(page, min) {
  await page.waitForFunction((n) => {
    const maps = ((window.__mcHarness && window.__mcHarness.maps) || []).filter(m => m && m._container && m._container.isConnected && m._loaded);
    let c = 0; maps.forEach(m => m.eachLayer(l => { if (l._mcCell) c++; }));
    return c >= n;
  }, min || 1, { timeout: 25000, polling: 100 });
}

// Bounding boxes of the controls that must not overlap the legend / time box.
function inPageOverlaps() {
  const boxes = (el) => { const r = el.getBoundingClientRect(); return { l: r.left, t: r.top, r: r.right, b: r.bottom, w: r.width, h: r.height }; };
  const visible = (el) => { const r = el.getBoundingClientRect(); const cs = getComputedStyle(el); return r.width > 0 && r.height > 0 && cs.visibility !== 'hidden' && cs.display !== 'none'; };
  const mine = Array.prototype.slice.call(document.querySelectorAll('.mc-duct-legend, .mc-duct-time')).filter(visible);
  const others = Array.prototype.slice.call(document.querySelectorAll('.leaflet-control, #mapControls, #mapControlsToggle'))
    .filter(el => visible(el) && !el.matches('.mc-duct-legend, .mc-duct-time') && !mine.some(m => m.contains(el) || el.contains(m)));
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
  return { hits, mine: mine.length };
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

// Outline problems: a dashed --mc-duct-lora outline exactly on cells where a
// duct can carry 868/915 MHz at hour idx.
function outlineProblems(rects, data, idx, lora) {
  const byKey = new Map(data.cells.map(c => [`${c.lat},${c.lon}`, c]));
  const probs = [];
  let n = 0;
  rects.forEach((r) => {
    const c = byKey.get(`${r.lat},${r.lon}`);
    if (!c) { probs.push(`rect at ${r.lat},${r.lon} has no cell`); return; }
    const want = c.c[idx] >= 3 && c.f[idx] > 0 && c.f[idx] <= LORA_MAX_MHZ;
    if (want) n++;
    if (r.fillOpacity !== 0) probs.push(`${r.lat},${r.lon}: filled (${r.fillOpacity})`);
    if (want && !(r.stroke && r.dash === '4 3' && String(r.color).toLowerCase() === String(lora).toLowerCase())) probs.push(`${r.lat},${r.lon}: LoRa duct without the dashed outline (${r.color}/${r.dash})`);
    if (!want && r.stroke) probs.push(`${r.lat},${r.lon}: class ${c.c[idx]} f=${c.f[idx]} has an outline`);
  });
  return { probs, n };
}

async function setSlider(page, idx) {
  await page.evaluate((i) => {
    const s = document.querySelector('.mc-duct-time input[type=range]');
    s.value = String(i);
    s.dispatchEvent(new Event('input', { bubbles: true }));
  }, idx);
}

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
      assert(data.cells.some(c => new Set(c.c).size > 1), 'no cell changes over the forecast hours; the slider could not change a colour');
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
      assert(st.legends === 0 && st.times === 0 && !st.rects.length && !st.fields.length, 'legend/time/cells/field present with the overlay off');
    });

    await test('enabling it draws cells, with a visible, unobstructed legend and time slider', async () => {
      await setOverlayUi(page, true);
      await waitRects(page, 1);
      await page.mouse.move(5, 5);
      await page.waitForSelector('.mc-duct-legend', { state: 'visible' });
      const st = await pageState(page);
      assert(st.maps === 1 && st.rects.length === data.cells.length, `maps=${st.maps} rects=${st.rects.length} cells=${data.cells.length}`);
      assert(st.fields.length === 1 && st.fields[0].inDom && st.fields[0].pane === 'mcDuctFieldPane', 'field images: ' + JSON.stringify(st.fields));
      assert(st.legends === 1 && st.times === 1 && st.legendVisible && st.timeVisible, `legends=${st.legends} times=${st.times}`);
      assert(/LoRa 868\/915/.test(st.legendText) && /Enhanced/.test(st.legendText) && /Very strong/.test(st.legendText) && /lowest 1\.5 km \(N\/km\)/.test(st.legendText) && /Updated/.test(st.legendText), 'legend text: ' + st.legendText.slice(0, 160));
      const ov = await page.evaluate(inPageOverlaps);
      assert(ov.mine === 2, 'legend and time box not both visible');
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
    await test('the expanded layers picker is on top of the legend and time slider', async () => {
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
      idx0 = await page.evaluate(() => { const s = document.querySelector('.mc-duct-time input[type=range]'); return parseInt(s.value, 10); });
      assert(idx0 >= 0 && idx0 < data.times.length, 'slider index ' + idx0);
      const f = await fieldProblems(page, data, idx0);
      assert(!f.probs.length, f.probs.slice(0, 5).join(' | '));
      assert(f.calm > 0, 'no calm cell to check for transparency');
      const pane = await page.evaluate(() => { const p = document.querySelector('.leaflet-mcDuctField-pane'); const cs = p && getComputedStyle(p); return cs ? { z: +cs.zIndex, pe: cs.pointerEvents } : null; });
      assert(pane && pane.z > 200 && pane.z < 400 && pane.pe === 'none', 'field pane ' + JSON.stringify(pane));
    });

    await test('LoRa cells, and only they, have the dashed outline', async () => {
      const o = outlineProblems((await pageState(page)).rects, data, idx0, (await cssVars(page)).lora);
      assert(!o.probs.length, o.probs.slice(0, 5).join(' | '));
      assert(o.n > 0, 'no LoRa duct at hour ' + idx0);
    });

    await test('moving the time slider changes the field, the outlines and the label', async () => {
      const before = (await page.evaluate(inPageFieldProbe, { idx: idx0, cells: [] })).sum;
      const label0 = await page.locator('.mc-duct-time-label').first().textContent();
      const max = await page.evaluate(() => parseInt(document.querySelector('.mc-duct-time input[type=range]').max, 10));
      assert(max === data.times.length - 1, `slider max ${max}`);
      await setSlider(page, max);
      const after = await fieldProblems(page, data, max);
      assert(after.r.sum !== before, 'field image did not change');
      assert(!after.probs.length, after.probs.slice(0, 5).join(' | '));
      const o = outlineProblems((await pageState(page)).rects, data, max, (await cssVars(page)).lora);
      assert(!o.probs.length, o.probs.slice(0, 5).join(' | '));
      assert((await page.locator('.mc-duct-time-label').first().textContent()) !== label0, 'label did not change');
      assert((await pageState(page)).fields.length === 1, 'slider added a field image');
      await setSlider(page, idx0);
      const back = await fieldProblems(page, data, idx0);
      assert(!back.probs.length && back.r.sum === before, 'after moving back: ' + back.probs.slice(0, 3).join(' | '));
    });

    async function popupFor(wantClass) {
      const cell = data.cells.find(c => c.c[idx0] === wantClass);
      assert(cell, `no class ${wantClass} cell at hour ${idx0}`);
      await page.evaluate((c) => {
        const m = window.__mcHarness.maps.filter(x => x._container && x._container.isConnected && x._loaded)[0];
        m.closePopup();
        m.setView([c.lat, c.lon], 9, { animate: false });
      }, cell);
      await sleep(400);
      const step = data.stepDeg * 0.3;
      // A few interior points: a node marker or control may sit on the centre.
      const offsets = [[0, 0], [step, step], [-step, step], [step, -step], [-step, -step], [0, step], [step, 0]];
      for (const [dy, dx] of offsets) {
        const pt = await page.evaluate((a) => {
          const m = window.__mcHarness.maps.filter(x => x._container && x._container.isConnected && x._loaded)[0];
          const p = m.latLngToContainerPoint([a.lat + a.dy, a.lon + a.dx]);
          const r = m.getContainer().getBoundingClientRect();
          const x = r.left + p.x, y = r.top + p.y;
          const el = document.elementFromPoint(x, y);
          const blocked = !el || el.closest('.leaflet-marker-icon, .leaflet-control, .leaflet-popup, #mapControls, .mc-duct-legend, .mc-duct-time, .leaflet-marker-pane');
          return { x, y, blocked: !!blocked };
        }, { lat: cell.lat, lon: cell.lon, dy, dx });
        if (pt.blocked) continue;
        await page.mouse.click(pt.x, pt.y);
        try {
          await page.waitForSelector('.leaflet-popup .mc-duct-popup', { timeout: 2500 });
          return { text: await page.locator('.leaflet-popup .mc-duct-popup').first().innerText(), cell };
        } catch (_) { /* try the next point */ }
      }
      throw new Error(`could not open the popup of cell ${cell.lat},${cell.lon} (class ${wantClass})`);
    }

    await test('clicking a surface-duct cell opens a popup that says 868 and 915 MHz: yes', async () => {
      const r = await popupFor(4);
      assert(/Surface-based duct/.test(r.text), 'popup: ' + r.text);
      assert(/Can carry 868 MHz: yes/.test(r.text) && /Can carry 915 MHz: yes/.test(r.text), 'popup: ' + r.text);
      assert(/Base: 0 m/.test(r.text) && /Lowest trapped frequency: \d+ MHz/.test(r.text), 'popup: ' + r.text);
      const n = fieldN(r.cell, idx0);
      if (n != null) assert(r.text.includes('Gradient, lowest 1.5 km: ' + (n < 0 ? '\u2212' + -n : n) + ' N/km'), 'popup gradient (n=' + n + '): ' + r.text);
    });
    await test('clicking a super-refraction cell says no duct, 868 MHz: no', async () => {
      const r = await popupFor(2);
      assert(/Super-refraction/.test(r.text) && /Can carry 868 MHz: no/.test(r.text) && /Lowest trapped frequency: none/.test(r.text), 'popup: ' + r.text);
    });

    await test('theme toggle redraws the field and outlines from the CSS variables', async () => {
      await page.evaluate(() => window.__mcHarness.maps.forEach(m => m.closePopup && m.closePopup()));
      const light = await cssVars(page);
      const before = (await page.evaluate(inPageFieldProbe, { idx: idx0, cells: [] })).sum;
      const rects0 = (await pageState(page)).rects.length;
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
      const o = outlineProblems(st.rects, data, idx0, dark.lora);
      assert(!o.probs.length, 'dark outlines: ' + o.probs.slice(0, 3).join(' | '));
      assert(st.rects.length === rects0 && st.fields.length === 1, `rects ${rects0}->${st.rects.length}, fields ${st.fields.length}`);
      const ov = await page.evaluate(inPageOverlaps);
      assert(!ov.hits.length, 'dark: ' + ov.hits.join('; '));
      await MH.setTheme(page, 'light');
    });

    // Leaflet 1.9.4: a map move redraws the canvas at once and orphans the
    // queued frame; switching off before that frame used to throw from the
    // orphan ("reading 'clearRect'", stress gate seed 303, scenario g).
    await test('switching off right after a canvas move redraw throws nothing', async () => {
      const before = page.__errors.length;
      const queued = await page.evaluate(() => {
        let rect = null;
        window.__mcHarness.maps.forEach(m => { if (m._loaded) m.eachLayer(l => { if (!rect && l._mcCell && l._renderer) rect = l; }); });
        if (!rect) return 'no cell';
        const r = rect._renderer;
        rect.setStyle({ fillOpacity: 0.5 });   // queues a frame redraw
        if (!r._redrawRequest) return 'no redraw queued';
        r._updatePaths();                      // what every moveend does
        rect.setStyle({ fillOpacity: 0.4 });   // marks an area to redraw
        window.MC_setTileOverlay('ducting', false);
        return 'ok';
      });
      assert(queued === 'ok', queued);
      await sleep(300);
      const errs = page.__errors.slice(before);
      assert(!errs.length, errs.join(' | '));
      await page.evaluate(() => window.MC_setTileOverlay('ducting', true));
      await waitRects(page, 1);
    });

    await test('disabling the overlay removes legend, time slider and cells', async () => {
      await setOverlayUi(page, false);
      await page.waitForFunction(() => !document.querySelector('.mc-duct-legend') && !document.querySelector('.mc-duct-time'), null, { timeout: 5000 });
      const st = await pageState(page);
      assert(!st.rects.length, `${st.rects.length} cells still drawn`);
      assert(!st.fields.length, 'field image left on the map');
      assert(!(await page.evaluate(() => document.querySelectorAll('.mc-duct-field').length)), 'field canvas left in the DOM');
      assert(st.legends === 0 && st.times === 0, 'legend/time boxes left');
      assert(await page.evaluate(() => JSON.parse(localStorage.getItem('mc-tile-overlays') || '[]').indexOf('ducting') < 0), 'preference still has ducting');
    });

    await test('re-enabling after a disable draws one legend and one slider, no duplicates', async () => {
      await setOverlayUi(page, true);
      await waitRects(page, 1);
      await setOverlayUi(page, false);
      await setOverlayUi(page, true);
      await waitRects(page, data.cells.length);
      await sleep(300);
      const st = await pageState(page);
      assert(st.legends === 1 && st.times === 1 && st.rects.length === data.cells.length, `legends=${st.legends} times=${st.times} rects=${st.rects.length}`);
      assert(st.fields.length === 1 && await page.evaluate(() => document.querySelectorAll('.mc-duct-field').length) === 1, 'field images: ' + st.fields.length);
      await setOverlayUi(page, false);
    });

    await test('map page has no page errors', async () => { assert(!page.__errors.length, page.__errors.slice(0, 3).join(' | ')); });

    // ── Live ──
    await test('live page: entry present, enabling draws cells and an unobstructed legend', async () => {
      const lp = await track({ theme: 'light', overlays: ['ducting'] });
      await gotoRoute(lp.page, '#/live');
      await lp.page.waitForSelector('label[data-tile-overlay-id="ducting"]', { state: 'attached', timeout: 25000 });
      await waitRects(lp.page, data.cells.length);
      await lp.page.waitForSelector('.mc-duct-legend', { state: 'visible' });
      await lp.page.mouse.move(5, 5);
      const st = await pageState(lp.page);
      assert(st.legends === 1 && st.times === 1, `legends=${st.legends} times=${st.times}`);
      assert(st.fields.length === 1 && st.fields[0].inDom, 'live field images: ' + st.fields.length);
      const ov = await lp.page.evaluate(inPageOverlaps);
      assert(!ov.hits.length, ov.hits.join('; '));
      assert(!lp.page.__errors.length, lp.page.__errors.slice(0, 3).join(' | '));
    });

    // ── Planner (opt-in) ──
    if (cfg.planner) {
      await test('planner page: entry present', async () => {
        const pp = await track({ theme: 'light' });
        await gotoRoute(pp.page, '#/tools/planner');
        await pp.page.waitForSelector('label[data-tile-overlay-id="ducting"]', { state: 'attached', timeout: 25000 });
        assert(!pp.page.__errors.length, pp.page.__errors.slice(0, 3).join(' | '));
      });
    } else skip('planner page: entry present', 'planner disabled on this server');

    // ── Config / API states ──
    await test('without ducting in the client config there is no overlay entry', async () => {
      const np = await track({ theme: 'light', overlays: ['ducting'], stripDucting: true });
      await gotoRoute(np.page, '#/map');
      await openPicker(np.page);
      await np.page.waitForSelector('.leaflet-control-layers-overlays', { state: 'attached' });
      assert((await np.page.locator('label[data-tile-overlay-id="ducting"]').count()) === 0, 'entry present');
      await sleep(400);
      const st = await pageState(np.page);
      assert(!st.rects.length && !st.fields.length && !st.legends && !st.times, 'overlay drawn although the server has none');
      assert(!np.page.__errors.length, np.page.__errors.slice(0, 3).join(' | '));
    });

    for (const [name, status, body, want] of [
      ['pending', 200, { status: 'pending', times: [], cells: [] }, /being prepared/],
      ['error', 200, { status: 'error', error: 'weather data unavailable', times: [], cells: [] }, /unavailable/],
      ['HTTP 500', 500, { error: 'x' }, /unavailable/],
    ]) {
      await test(`legend explains a ${name} /api/ducting answer, with no cells`, async () => {
        const sp = await track({ theme: 'light', overlays: ['ducting'] });
        await sp.page.route('**/api/ducting', (route) => route.fulfill({ status, json: body }));
        await gotoRoute(sp.page, '#/map');
        await sp.page.waitForSelector('.mc-duct-legend', { state: 'visible', timeout: 25000 });
        await sp.page.waitForFunction((src) => new RegExp(src).test((document.querySelector('.mc-duct-legend') || {}).textContent || ''), want.source, { timeout: 10000 });
        const st = await pageState(sp.page);
        assert(!st.rects.length && !st.fields.length, 'cells or field drawn');
        const errs = sp.page.__errors.filter(e => !/Failed to load resource/.test(e));
        assert(!errs.length, errs.slice(0, 3).join(' | '));
      });
    }

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

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

// What the page shows right now: rectangles of every live map, legend/time boxes.
function inPageState() {
  const maps = ((window.__mcHarness && window.__mcHarness.maps) || [])
    .filter(m => m && m._container && m._container.isConnected && m._loaded);
  const rects = [];
  maps.forEach((m, mi) => {
    m.eachLayer((l) => {
      if (l._mcCell) {
        const c = l._mcCell;
        rects.push({ map: mi, lat: c.lat, lon: c.lon, fill: l.options.fillColor || null, fillOpacity: l.options.fillOpacity, stroke: !!l.options.stroke });
      }
    });
  });
  const vis = (el) => { const r = el.getBoundingClientRect(); const cs = getComputedStyle(el); return r.width > 0 && r.height > 0 && cs.visibility !== 'hidden' && cs.display !== 'none'; };
  const q = (sel) => Array.prototype.slice.call(document.querySelectorAll(sel));
  return {
    maps: maps.length,
    rects,
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
    const o = {};
    ['lora', 'uhf', 'micro', 'super'].forEach(k => { o[k] = cs.getPropertyValue('--mc-duct-' + k).trim(); });
    return o;
  });
}

/** What each cell should be painted at time index idx, from the server data. */
async function expectedFills(page, data, idx) {
  const vars = await cssVars(page);
  const bins = await page.evaluate((arr) => arr.map(x => window.MC_ductBin(x[0], x[1])), data.cells.map(c => [c.c[idx], c.f[idx]]));
  const op = { 2: 0.35, 3: 0.3, 4: 0.55 };
  const out = new Map();
  data.cells.forEach((c, i) => {
    out.set(`${c.lat},${c.lon}`, { bin: bins[i], fill: bins[i] ? vars[bins[i]] : null, op: bins[i] ? op[c.c[idx]] : 0, cls: c.c[idx] });
  });
  return out;
}
function fillProblems(rects, exp) {
  const probs = [];
  rects.forEach((r) => {
    const e = exp.get(`${r.lat},${r.lon}`);
    if (!e) { probs.push(`rect at ${r.lat},${r.lon} has no cell`); return; }
    if (e.bin) {
      if (String(r.fill).toLowerCase() !== String(e.fill).toLowerCase() || Math.abs(r.fillOpacity - e.op) > 1e-9) probs.push(`${r.lat},${r.lon}: class ${e.cls} drawn ${r.fill}/${r.fillOpacity}, want ${e.fill}/${e.op}`);
    } else if (r.fillOpacity !== 0) probs.push(`${r.lat},${r.lon}: class ${e.cls} drawn with fill opacity ${r.fillOpacity}, want 0`);
  });
  return probs;
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
        ['c', 'f', 'b', 't', 'dm', 'g'].forEach((k) => { if (!Array.isArray(c[k]) || c[k].length !== data.times.length) probs.push(`${c.lat},${c.lon}: ${k} has ${c[k] && c[k].length} entries`); });
        data.times.forEach((_, h) => {
          const want = FAKE.intendedKind(SCENARIO, c.lat, c.lon, h).cls;
          if (c.c[h] !== want) probs.push(`${c.lat},${c.lon} h${h}: class ${c.c[h]} want ${want}`);
          const a = FAKE.analyzeCell(SCENARIO, c.lat, c.lon, h, data.times.length);
          if (a.cls !== c.c[h]) probs.push(`${c.lat},${c.lon} h${h}: fake port says ${a.cls}, server ${c.c[h]}`);
          if (c.c[h] === 4 && !(c.f[h] > 0 && c.f[h] <= 868)) probs.push(`${c.lat},${c.lon} h${h}: surface duct fmin ${c.f[h]} does not trap 868 MHz`);
          if (c.c[h] >= 3 && Math.abs(c.f[h] - Math.round(a.fmin)) > 1) probs.push(`${c.lat},${c.lon} h${h}: fmin ${c.f[h]} vs ${Math.round(a.fmin)}`);
          if (c.c[h] <= 2 && c.f[h] !== 0) probs.push(`${c.lat},${c.lon} h${h}: class ${c.c[h]} with fmin ${c.f[h]}`);
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
      assert(st.legends === 0 && st.times === 0 && !st.rects.length, 'legend/time/cells present with the overlay off');
    });

    await test('enabling it draws cells, with a visible, unobstructed legend and time slider', async () => {
      await setOverlayUi(page, true);
      await waitRects(page, 1);
      await page.mouse.move(5, 5);
      await page.waitForSelector('.mc-duct-legend', { state: 'visible' });
      const st = await pageState(page);
      assert(st.maps === 1 && st.rects.length === data.cells.length, `maps=${st.maps} rects=${st.rects.length} cells=${data.cells.length}`);
      assert(st.legends === 1 && st.times === 1 && st.legendVisible && st.timeVisible, `legends=${st.legends} times=${st.times}`);
      assert(/lora/i.test(st.legendText) && /Updated/.test(st.legendText), 'legend text: ' + st.legendText.slice(0, 120));
      const ov = await page.evaluate(inPageOverlaps);
      assert(ov.mine === 2, 'legend and time box not both visible');
      assert(!ov.hits.length, ov.hits.join('; '));
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

    await test('cell fills match the data at the current forecast hour', async () => {
      idx0 = await page.evaluate(() => { const s = document.querySelector('.mc-duct-time input[type=range]'); return parseInt(s.value, 10); });
      assert(idx0 >= 0 && idx0 < data.times.length, 'slider index ' + idx0);
      const exp = await expectedFills(page, data, idx0);
      const probs = fillProblems((await pageState(page)).rects, exp);
      assert(!probs.length, probs.slice(0, 5).join(' | '));
      assert((await pageState(page)).rects.some(r => r.fillOpacity > 0), 'no coloured cell at all');
    });

    await test('moving the time slider changes cell colours and the label', async () => {
      const before = (await pageState(page)).rects.map(r => r.fill + '/' + r.fillOpacity);
      const label0 = await page.locator('.mc-duct-time-label').first().textContent();
      const max = await page.evaluate(() => parseInt(document.querySelector('.mc-duct-time input[type=range]').max, 10));
      assert(max === data.times.length - 1, `slider max ${max}`);
      await setSlider(page, max);
      const after = (await pageState(page)).rects;
      assert(after.map(r => r.fill + '/' + r.fillOpacity).some((v, i) => v !== before[i]), 'no cell changed colour');
      const probs = fillProblems(after, await expectedFills(page, data, max));
      assert(!probs.length, probs.slice(0, 5).join(' | '));
      assert((await page.locator('.mc-duct-time-label').first().textContent()) !== label0, 'label did not change');
      await setSlider(page, idx0);
      const back = fillProblems((await pageState(page)).rects, await expectedFills(page, data, idx0));
      assert(!back.length, 'after moving back: ' + back.slice(0, 3).join(' | '));
    });

    async function popupFor(wantClass) {
      const exp = await expectedFills(page, data, idx0);
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
          return { text: await page.locator('.leaflet-popup .mc-duct-popup').first().innerText(), cell, exp: exp.get(`${cell.lat},${cell.lon}`) };
        } catch (_) { /* try the next point */ }
      }
      throw new Error(`could not open the popup of cell ${cell.lat},${cell.lon} (class ${wantClass})`);
    }

    await test('clicking a surface-duct cell opens a popup that says 868 and 915 MHz: yes', async () => {
      const r = await popupFor(4);
      assert(/Surface-based duct/.test(r.text), 'popup: ' + r.text);
      assert(/Can carry 868 MHz: yes/.test(r.text) && /Can carry 915 MHz: yes/.test(r.text), 'popup: ' + r.text);
      assert(/Base: 0 m/.test(r.text) && /Lowest trapped frequency: \d+ MHz/.test(r.text), 'popup: ' + r.text);
    });
    await test('clicking a super-refraction cell says no duct, 868 MHz: no', async () => {
      const r = await popupFor(2);
      assert(/Super-refraction/.test(r.text) && /Can carry 868 MHz: no/.test(r.text) && /Lowest trapped frequency: none/.test(r.text), 'popup: ' + r.text);
    });

    await test('theme toggle restyles the cells from the CSS variables', async () => {
      await page.evaluate(() => window.__mcHarness.maps.forEach(m => m.closePopup && m.closePopup()));
      const light = (await cssVars(page)).lora;
      const before = (await pageState(page)).rects.filter(r => r.fill).map(r => r.fill);
      assert(before.length, 'no coloured cell');
      await MH.setTheme(page, 'dark');
      const dark = (await cssVars(page)).lora;
      assert(dark && dark !== light, `--mc-duct-lora did not change with the theme (${light} / ${dark})`);
      await page.waitForFunction(() => document.documentElement.getAttribute('data-theme') === 'dark');
      const exp = await expectedFills(page, data, idx0);
      let probs = [];
      for (let i = 0; i < 30; i++) {
        probs = fillProblems((await pageState(page)).rects, exp);
        if (!probs.length) break;
        await sleep(200);
      }
      assert(!probs.length, 'after theme change: ' + probs.slice(0, 4).join(' | '));
      assert((await pageState(page)).rects.some((r, i) => r.fill && !before.includes(r.fill)), 'no fill changed with the theme');
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
      assert(!st.rects.length && !st.legends && !st.times, 'overlay drawn although the server has none');
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
        assert(!st.rects.length, 'cells drawn');
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

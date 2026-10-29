#!/usr/bin/env node
/* MediaMesh (radio2) repeaters on the Map page.
 *
 * The committed fixture has no MediaMesh evidence, so this marks two
 * repeaters in the /api/nodes response itself (one rxtx, one rx), exactly as
 * the server does (`mediamesh: {mode, radio, source, last_seen}`), and checks:
 *   (1) those two markers get the ring and the right badge class;
 *   (2) "MediaMesh only" leaves just them on the map, and is remembered;
 *   (3) unticking it brings the rest back;
 *   (4) the popup states the MediaMesh line.
 *
 * Clustering is turned off so every node is its own marker.
 * CHROMIUM_REQUIRE=1 makes Chromium-launch failure a HARD FAIL.
 */
'use strict';

const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';

let passes = 0, failures = 0;
function pass(msg) { console.log(`  ✓ ${msg}`); passes++; }
function fail(msg) { console.error(`  ✗ ${msg}`); failures++; }

async function main() {
  const requireChromium = process.env.CHROMIUM_REQUIRE === '1';
  let browser;
  try {
    browser = await chromium.launch({ headless: true });
  } catch (err) {
    if (requireChromium) {
      console.error(`HARD FAIL — Chromium unavailable: ${err.message}`);
      process.exit(1);
    }
    console.warn(`SKIP — Chromium unavailable: ${err.message}`);
    process.exit(0);
  }

  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  await ctx.addInitScript(() => {
    try {
      if (!sessionStorage.getItem('mm-e2e-init')) {
        localStorage.setItem('meshcore-map-clustering', 'false');
        localStorage.setItem('meshcore-map-hash-labels', 'false');
        localStorage.setItem('meshcore-map-mediamesh-only', 'false');
        localStorage.setItem('meshcore-map-last-heard', '30d');
        sessionStorage.setItem('mm-e2e-init', '1');
      }
    } catch (_) {}
  });

  const marked = [];
  await ctx.route(/\/api\/nodes\?/, async (route) => {
    const res = await route.fetch();
    let body;
    try { body = await res.json(); } catch { return route.fulfill({ response: res }); }
    const nodes = Array.isArray(body.nodes) ? body.nodes : [];
    let n = 0;
    for (const node of nodes) {
      if (node.role !== 'repeater' || !node.lat || !node.lon) continue;
      const mode = n === 0 ? 'rxtx' : 'rx';
      node.mediamesh = { mode, radio: '911.15,250,7,5', source: 'status', last_seen: new Date().toISOString() };
      if (!marked.includes(node.public_key)) marked.push(node.public_key);
      if (++n === 2) break;
    }
    await route.fulfill({ response: res, json: body });
  });

  const page = await ctx.newPage();
  await page.goto(`${BASE}/#/map`, { waitUntil: 'domcontentloaded' });

  try {
    await page.waitForSelector('.meshcore-marker', { timeout: 20000 });
    await page.waitForFunction(() => !!window.MediaMesh, null, { timeout: 5000 });
  } catch {
    fail('the map never drew a marker within 20s');
    await browser.close();
    console.log(`\ntest-map-mediamesh-e2e: ${passes} passed, ${failures} failed`);
    process.exit(1);
  }

  if (marked.length < 2) {
    fail(`the fixture gave ${marked.length} placed repeaters to mark, need 2`);
    await browser.close();
    process.exit(1);
  }

  const counts = () => page.evaluate(() => ({
    all: document.querySelectorAll('.leaflet-marker-icon.meshcore-marker').length,
    mm: document.querySelectorAll('.leaflet-marker-icon.mm-node').length,
    rxtx: document.querySelectorAll('.leaflet-marker-icon.mm-node--rxtx').length,
    rx: document.querySelectorAll('.leaflet-marker-icon.mm-node--rx').length,
  }));

  // Markers outside the view are not drawn; zoom out to show everything.
  await page.evaluate(() => {
    const el = document.querySelector('.leaflet-container');
    const m = el && Object.values(window).find(v => v && v._container === el && typeof v.fitWorld === 'function');
    if (m) m.setZoom(3, { animate: false });
  }).catch(() => {});

  const before = await counts();
  if (before.rxtx >= 1 && before.rx >= 1 && before.mm === before.rxtx + before.rx) {
    pass(`(1) ${before.mm} MediaMesh markers ringed (${before.rxtx} rxtx, ${before.rx} rx) among ${before.all}`);
  } else {
    fail(`(1) MediaMesh markers: ${JSON.stringify(before)}`);
  }

  await page.click('#mcMediaMeshOnly', { force: true }).catch(async () => {
    await page.evaluate(() => { const cb = document.getElementById('mcMediaMeshOnly'); cb.checked = true; cb.dispatchEvent(new Event('change')); });
  });
  await page.waitForTimeout(300);
  const only = await counts();
  const stored = await page.evaluate(() => localStorage.getItem('meshcore-map-mediamesh-only'));
  if (only.all === only.mm && only.mm === before.mm && stored === 'true') {
    pass(`(2) MediaMesh only: ${only.all} markers, all MediaMesh, and the choice is stored`);
  } else {
    fail(`(2) MediaMesh only: ${JSON.stringify(only)}, stored=${stored}`);
  }

  // (4) the popup line, from one of the remaining markers
  await page.click('.leaflet-marker-icon.mm-node', { force: true }).catch(() => {});
  const popup = await page.waitForSelector('.map-popup', { timeout: 5000 }).then(h => h.textContent()).catch(() => '');
  if (/MediaMesh/.test(popup) && /911\.15 MHz · 250 kHz SF7/.test(popup) && /reported by the node/.test(popup)) {
    pass('(4) the popup states MediaMesh, its setting and how it is known');
  } else {
    fail(`(4) popup text: ${popup.slice(0, 200)}`);
  }

  await page.evaluate(() => { const cb = document.getElementById('mcMediaMeshOnly'); cb.checked = false; cb.dispatchEvent(new Event('change')); });
  await page.waitForTimeout(300);
  const after = await counts();
  if (after.all === before.all) pass(`(3) unticked: all ${after.all} markers are back`);
  else fail(`(3) unticked: ${after.all} markers, expected ${before.all}`);

  await browser.close();
  console.log(`\ntest-map-mediamesh-e2e: ${passes} passed, ${failures} failed`);
  process.exit(failures ? 1 : 0);
}

main().catch((e) => { console.error(e); process.exit(1); });

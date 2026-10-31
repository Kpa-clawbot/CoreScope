#!/usr/bin/env node
/* MediaMesh (radio2) paths on Live: a hop of a packet carried on radio2 is
 * drawn as hollow rings, not a line.
 *
 * Checks, in a real browser against a running server:
 *   (1) a MediaMesh hop leaves fading rings as circle markers on the
 *       animations pane, on a canvas (no SVG nodes);
 *   (2) a normal hop leaves none;
 *   (3) zooming in re-spaces the rings: more of them, still within 40 a hop;
 *   (4) the legend names the style.
 *
 * CHROMIUM_REQUIRE=1 makes Chromium-launch failure a HARD FAIL.
 */
'use strict';

const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';
const RINGS_PER_HOP = 40;

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
  const page = await ctx.newPage();
  await page.goto(`${BASE}/#/live`, { waitUntil: 'domcontentloaded' });

  try {
    await page.waitForSelector('#liveMap', { timeout: 15000 });
    await page.waitForFunction(
      () => !!window._liveDrawAnimatedLine && !!window._liveTestSeams && !!window._liveTestSeams.getMediaMeshRingCount,
      null,
      { timeout: 15000 },
    );
  } catch {
    fail('the live map never exposed its animation seams within 15s');
    await browser.close();
    console.log(`\ntest-live-mediamesh-rings-e2e: ${passes} passed, ${failures} failed`);
    process.exit(1);
  }

  // Fire one hop and wait (in the page, per frame) for its fading copy; then
  // zoom in at once, while the rings are still fading, and count again.
  const result = await page.evaluate(({ mediaMesh }) => new Promise((resolve) => {
    if (window._liveVcrSetMode) window._liveVcrSetMode('LIVE');
    const seams = window._liveTestSeams;
    // A hop across the middle of the current view, a quarter of its width.
    const c = document.querySelector('#liveMap').getBoundingClientRect();
    const m = seams.getMap();
    let from = [37.40, -122.00], to = [37.40, -121.90];
    if (m) {
      const a = m.containerPointToLatLng([c.width * 0.375, c.height / 2]);
      const b = m.containerPointToLatLng([c.width * 0.625, c.height / 2]);
      from = [a.lat, a.lng]; to = [b.lat, b.lng];
    }
    window._liveDrawAnimatedLine(from, to, '#00ff00', null, null, '00AA', null,
      mediaMesh ? { cap: 40, color: 'rgb(217, 70, 239)' } : null);
    const out = { before: 0, after: 0, svgRings: 0, canvasInPane: 0 };
    const t0 = performance.now();
    (function poll() {
      const n = seams.getMediaMeshRingCount();
      if (n > 0 || performance.now() - t0 > 4000 || (!mediaMesh && seams.getAnimCount() === 0 && performance.now() - t0 > 900)) {
        out.before = n;
        const pane = document.querySelector('.leaflet-pane.leaflet-animations-pane');
        out.svgRings = pane ? pane.querySelectorAll('svg path').length : -1;
        out.canvasInPane = pane ? pane.querySelectorAll('canvas').length : 0;
        if (n > 0) {
          seams.zoomBy(1);
          out.after = seams.getMediaMeshRingCount();
        }
        resolve(out);
        return;
      }
      requestAnimationFrame(poll);
    })();
  }), { mediaMesh: true });

  if (result.before > 0 && result.svgRings === 0 && result.canvasInPane >= 2) {
    pass(`(1) a MediaMesh hop left ${result.before} rings, on a canvas in the animations pane`);
  } else {
    fail(`(1) rings ${result.before}, SVG paths in pane ${result.svgRings}, canvases in pane ${result.canvasInPane}`);
  }

  if (result.after > result.before && result.after <= RINGS_PER_HOP) {
    pass(`(3) zooming in re-spaced the rings: ${result.before} → ${result.after} (cap ${RINGS_PER_HOP})`);
  } else if (result.before === RINGS_PER_HOP && result.after === RINGS_PER_HOP) {
    pass('(3) the hop was already at the 40-ring cap and stayed there after zooming in');
  } else {
    fail(`(3) after zooming in: ${result.before} → ${result.after} rings`);
  }

  // Let everything fade, then a normal hop must leave no rings.
  await page.waitForFunction(() => window._liveTestSeams.getMediaMeshRingCount() === 0, null, { timeout: 5000, polling: 50 }).catch(() => {});
  const normal = await page.evaluate(() => new Promise((resolve) => {
    window._liveDrawAnimatedLine([37.40, -122.00], [37.40, -121.90], '#00ff00', null, null, '00AA', null);
    let max = 0;
    const t0 = performance.now();
    (function poll() {
      max = Math.max(max, window._liveTestSeams.getMediaMeshRingCount());
      if (performance.now() - t0 > 1500) { resolve(max); return; }
      requestAnimationFrame(poll);
    })();
  }));
  if (normal === 0) pass('(2) a normal hop left no rings');
  else fail(`(2) a normal hop left ${normal} rings`);

  const legend = await page.evaluate(() => (document.querySelector('#liveLegend') || {}).textContent || '');
  if (legend.includes('MediaMesh (radio2)')) pass('(4) the legend names MediaMesh rings');
  else fail('(4) the legend has no MediaMesh entry');

  await browser.close();
  console.log(`\ntest-live-mediamesh-rings-e2e: ${passes} passed, ${failures} failed`);
  process.exit(failures ? 1 : 0);
}

main().catch((e) => { console.error(e); process.exit(1); });

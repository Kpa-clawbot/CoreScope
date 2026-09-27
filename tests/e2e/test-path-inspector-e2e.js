#!/usr/bin/env node
/* Path Inspector — real fixture API, map route drawing/replacement, side pane,
 * legacy trace redirect and tools landing. Seed test-fixtures/path-inspector.sql
 * after migrating the fixture and BEFORE starting the server (see #2060).
 * Missing candidates are failures, never skips or mocked responses.
 */
'use strict';

const assert = require('node:assert/strict');
const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';
// Three-byte prefixes meet the default path-trust policy. The middle prefix
// deliberately matches two repeaters, giving two distinct routes to draw.
const PREFIXES = 'f20601,f20602,f20603';
const key = (prefix, suffix = '0') => prefix + suffix.repeat(64 - prefix.length);
const POSITIONS = {
  [key('f20601')]: [1, 1],
  [key('f20602', '1')]: [1.01, 1.01],
  [key('f20602', '2')]: [0.99, 1.01],
  [key('f20603')]: [1, 1.02],
};

let passes = 0, failures = 0;
function pass(msg) { console.log(`  ✓ ${msg}`); passes++; }
function fail(msg) { console.error(`  ✗ ${msg}`); failures++; }

// The toggle ships in the page template, but its click handler is attached by
// initMapSidePane(), which public/map.js calls at the END of loadNodes()
// (map.js:1795). So the button exists for seconds before it does anything, and
// a single click on sight is a race that silently no-ops. Measured at ~3s
// against a populated instance.
//
// Clicking for real each attempt rather than dispatching in-page on purpose: a
// click that cannot land (an overlay eating it, as in #2049) has to fail here,
// which an element.click() from evaluate would hide.
async function expandPane(page) {
  await page.waitForSelector('#mapPaneToggle', { timeout: 15000 });
  for (let i = 0; i < 20; i++) {
    const expanded = await page.evaluate(() => {
      const el = document.getElementById('mapSidePane');
      return !!el && /\bexpanded\b/.test(el.className);
    });
    if (expanded) return;
    try {
      await page.click('#mapPaneToggle', { timeout: 1500 });
    } catch { /* not clickable yet; the next round re-checks */ }
    await page.waitForTimeout(500);
  }
  throw new Error('the pane never expanded after 20 clicks over ~20s');
}

async function openPaneAndSubmit(page) {
  await page.goto(`${BASE}/#/map`, { waitUntil: 'domcontentloaded' });
  await expandPane(page);
  await page.fill('#mapPiInput', PREFIXES);
  await page.click('#mapPiSubmit');
  await page.waitForSelector('#mapPiResults button[data-idx="1"]', { timeout: 10000 });
}

async function main() {
  const requireChromium = process.env.CHROMIUM_REQUIRE === '1';
  let browser;
  try {
    browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM_PATH || undefined });
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

  // (1) The side pane exists and starts collapsed.
  await page.goto(`${BASE}/#/map`, { waitUntil: 'domcontentloaded' });
  try {
    await page.waitForSelector('#mapSidePane', { timeout: 15000 });
    const cls = await page.getAttribute('#mapSidePane', 'class');
    if (cls && /\bexpanded\b/.test(cls)) fail(`(1) the side pane starts expanded (class="${cls}")`);
    else pass('(1) the side pane is present and collapsed by default');
  } catch {
    fail('(1) #mapSidePane never appeared within 15s');
  }

  // (2) The toggle expands it.
  try {
    await expandPane(page);
    pass('(2) clicking the toggle expands the pane');
  } catch {
    const cls = await page.getAttribute('#mapSidePane', 'class').catch(() => '(missing)');
    fail(`(2) the pane did not gain .expanded (class="${cls}")`);
  }

  // (3) Real API candidates must resolve to both seeded routes. An HTTP 200
  // with an empty result used to count as coverage for this round trip.
  try {
    const response = await page.request.post(BASE + '/api/paths/inspect', {
      data: { prefixes: PREFIXES.split(',') },
    });
    assert.equal(response.status(), 200, 'fixture inspector API must be ready');
    const data = await response.json();
    assert.equal(data.candidates.length, 2, 'fixture must return two distinct candidates; apply path-inspector.sql before server startup');
    assert.deepEqual(data.candidates.map(candidate => candidate.path.join(',')).sort(), [
      [key('f20601'), key('f20602', '1'), key('f20603')].join(','),
      [key('f20601'), key('f20602', '2'), key('f20603')].join(','),
    ].sort(), 'both complete fixture routes must be returned, regardless of score order');
    for (const candidate of data.candidates) {
      assert.equal(candidate.path.length, 3);
      assert.equal(candidate.speculative, false, 'seeded graph must support both normal routes');
      candidate.path.forEach(pk => assert.ok(POSITIONS[pk], 'candidate must use a seeded GPS repeater'));
      assert.equal(candidate.evidence.perHop.length, 3);
      assert.ok(candidate.evidence.perHop.every(hop => hop.trusted), 'three-byte hops must satisfy the unchanged trust policy');
    }
    await openPaneAndSubmit(page);
    assert.equal(await page.locator('#mapPiResults button[data-idx]').count(), 2);
    pass('(3) submitting prefixes renders both real, trusted fixture candidates');

    // (4) Check actual Leaflet polylines, their coordinates and rendered SVG.
    // No forced clicks: overlays blocking either candidate must fail the test.
    for (let index = 0; index < 2; index++) {
      const path = data.candidates[index].path.map(pk => POSITIONS[pk]);
      const expected = [path.slice(0, 2), path.slice(1, 3)];
      await page.locator(`#mapPiResults button[data-idx="${index}"]`).click();
      await page.waitForFunction(expected => {
        const group = window.__mc_routeLayer;
        if (!group) return false;
        const lines = group.getLayers().filter(layer => layer instanceof L.Polyline && !(layer instanceof L.Polygon));
        return lines.length === 2 && expected.every(points => lines.some(line =>
          JSON.stringify(line.getLatLngs().map(p => [p.lat, p.lng])) === JSON.stringify(points) &&
          window.__mc_map.hasLayer(line) && line.getElement() && line.getElement().getAttribute('d')
        ));
      }, expected, { timeout: 10000 });
      if (index === 0) {
        await page.evaluate(() => { window.__previousInspectorLayers = window.__mc_routeLayer.getLayers(); });
        pass('(4) Show on Map draws both segments at the first candidate coordinates');
      } else {
        const previousRemoved = await page.evaluate(() => window.__previousInspectorLayers.every(layer =>
          !window.__mc_routeLayer.hasLayer(layer) && !window.__mc_map.hasLayer(layer)
        ));
        assert.ok(previousRemoved, 'switching candidates must remove every prior route layer from the map');
        pass('(5) switching candidates draws the other route and removes all prior route objects');
      }
    }
  } catch (err) {
    fail('(3-5) fixture route coverage: ' + err.message);
  }

  // (6) The legacy trace URL still redirects.
  await page.goto(`${BASE}/#/traces/abc123`, { waitUntil: 'domcontentloaded' });
  try {
    await page.waitForFunction(() => location.hash.indexOf('#/tools/trace/abc123') === 0, null, { timeout: 5000 });
    pass('(6) /#/traces/<hash> redirects to /#/tools/trace/<hash>');
  } catch {
    fail(`(6) no redirect; the URL is ${JSON.stringify(page.url())}`);
  }

  // (7) The tools landing lists both tools.
  await page.goto(`${BASE}/#/tools`, { waitUntil: 'domcontentloaded' });
  try {
    await page.waitForSelector('.tools-landing', { timeout: 8000 });
    const links = await page.evaluate(() => ({
      pi: !!document.querySelector('a[href="#/tools/path-inspector"]'),
      trace: !!document.querySelector('a[href*="#/tools/trace"]'),
    }));
    if (links.pi && links.trace) pass('(7) the tools landing links to both tools');
    else fail(`(7) the tools landing is missing a link (path-inspector: ${links.pi}, trace: ${links.trace})`);
  } catch {
    fail('(7) .tools-landing never rendered within 8s');
  }

  await browser.close();
  console.log(`\ntest-path-inspector-e2e: ${passes} passed, ${failures} failed`);
  process.exit(failures ? 1 : 0);
}

main().catch((e) => { console.error(e); process.exit(1); });

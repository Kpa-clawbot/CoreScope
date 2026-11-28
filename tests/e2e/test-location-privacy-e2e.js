#!/usr/bin/env node
/* Location privacy (frontend). Runs only against a server started with
 * locationPrivacy.enabled; on a server with it off it prints SKIP and exits 0.
 *
 * Asserts (privacy on):
 *   - node detail says "Approximate location (within …)" and the mini-map
 *     draws the uncertainty circle;
 *   - the main map's attribution carries "Positions approximate";
 *   - a main map pin shows its uncertainty circle on hover and while its
 *     popup is open, and a live map node while its detail panel is open;
 *   - the planner shows the approximate-positions notice (when the planner
 *     is enabled on this server);
 *   - a packet served with location_hidden shows the hidden-bytes message and
 *     the page logs no console errors;
 *   - one companion's RX coverage (?rx=) refused to an anonymous viewer shows
 *     a notice, not a blank map (when clientRxCoverage is on).
 *
 * BASE_URL defaults to http://localhost:3000. Never point this at production.
 */
'use strict';

const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:3000';
const NAME = 'test-location-privacy-e2e.js';

async function getJson(path) {
  const r = await fetch(BASE + path);
  if (!r.ok) throw new Error(path + ' -> HTTP ' + r.status);
  return r.json();
}

async function main() {
  const cfg = await getJson('/api/config/client');
  const lp = cfg && cfg.locationPrivacy;
  if (!lp || lp.enabled !== true) {
    // CI points this suite at a dedicated privacy-on server: there a skip
    // would hide a broken setup, so LOCPRIV_REQUIRE=1 turns it into a failure.
    if (process.env.LOCPRIV_REQUIRE === '1') {
      console.error('FAIL: LOCPRIV_REQUIRE=1 but /api/config/client reports location privacy off');
      process.exit(1);
    }
    console.log('SKIP (location privacy off on this server)');
    process.exit(0);
  }

  let browser;
  try {
    browser = await chromium.launch({
      headless: true,
      executablePath: process.env.CHROMIUM_PATH || undefined,
      args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
    });
  } catch (err) {
    if (process.env.CHROMIUM_REQUIRE === '1') {
      console.error(`${NAME}: FAIL — Chromium required but unavailable: ${err.message}`);
      process.exit(1);
    }
    console.log(`${NAME}: SKIP (Chromium unavailable: ${err.message.split('\n')[0]})`);
    process.exit(0);
  }

  let failures = 0;
  const fail = (m) => { failures++; console.error('  FAIL: ' + m); };
  const pass = (m) => console.log('  PASS: ' + m);
  const check = (ok, m) => (ok ? pass(m) : fail(m));

  const page = await (await browser.newContext()).newPage();
  page.setDefaultTimeout(15000);
  const consoleErrors = [];
  page.on('console', (m) => { if (m.type() === 'error') consoleErrors.push(m.text()); });
  page.on('pageerror', (e) => consoleErrors.push('pageerror: ' + e.message));

  // Node detail: a node that has a position.
  const nodes = (await getJson('/api/nodes?limit=200')).nodes || [];
  const located = nodes.find((n) => n.lat != null && n.lon != null && !(n.lat === 0 && n.lon === 0));
  if (!located) {
    console.log('  SKIP: no node with a position on this server (node detail checks)');
  } else {
    await page.goto(`${BASE}/#/nodes/${encodeURIComponent(located.public_key)}`, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('.lp-loc', { timeout: 15000 }).catch(() => {});
    const label = await page.locator('.lp-loc').first().textContent().catch(() => '');
    check(/^Approximate location \(within .+\)$/.test((label || '').trim()), 'node detail location row says approximate: ' + JSON.stringify(label));
    await page.waitForSelector('path.lp-uncertainty', { state: 'attached', timeout: 10000 }).catch(() => {});
    check(await page.locator('path.lp-uncertainty').count() === 1, 'node mini-map draws one uncertainty circle');
  }

  // Main map attribution.
  await page.goto(`${BASE}/#/map`, { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('.leaflet-control-attribution .lp-attr', { state: 'attached', timeout: 15000 }).catch(() => {});
  const attr = await page.locator('.leaflet-control-attribution .lp-attr').first().textContent().catch(() => '');
  check(/^Positions approximate \(within .+\)$/.test((attr || '').trim()), 'map legend note present: ' + JSON.stringify(attr));

  // Main map pins: the circle follows the pointer and stays while a popup is open.
  const circles = () => page.locator('#leaflet-map path.lp-uncertainty, .leaflet-container path.lp-uncertainty').count();
  // A pin whose centre is on top (not under the map controls or another pin).
  const topmostPin = (sel) => page.waitForFunction((s) => {
    const els = Array.from(document.querySelectorAll(s));
    for (let i = 0; i < els.length; i++) {
      const r = els[i].getBoundingClientRect();
      const x = r.left + r.width / 2, y = r.top + r.height / 2;
      if (r.width && x > 0 && y > 0 && x < innerWidth && y < innerHeight) {
        const hit = document.elementFromPoint(x, y);
        if (hit && (hit === els[i] || els[i].contains(hit))) return i + 1;
      }
    }
    return 0;
  }, sel, { timeout: 15000 }).then((h) => h.jsonValue(), () => 0);
  // Markers are rebuilt as the node list settles, which closes any popup.
  await page.waitForLoadState('networkidle', { timeout: 10000 }).catch(() => {});
  const pinIdx = await topmostPin('.leaflet-marker-icon.meshcore-marker');
  if (!pinIdx) {
    console.log('  SKIP: no map pin to hover');
  } else {
    const pin = page.locator('.leaflet-marker-icon.meshcore-marker').nth(pinIdx - 1);
    check(await circles() === 0, 'no circle before a pin is hovered');
    await pin.hover();
    check(await circles() === 1, 'hovering a pin draws its uncertainty circle');
    await page.mouse.move(2, 2);
    check(await circles() === 0, 'circle goes when the pointer leaves the pin');
    await pin.click();
    await page.waitForSelector('.leaflet-popup', { timeout: 5000 }).catch(() => {});
    await page.mouse.move(2, 2);
    const popups = await page.locator('.leaflet-popup').count();
    check(await circles() === 1, 'circle stays while the pin popup is open (popups open: ' + popups + ')');
    await page.locator('.leaflet-popup-close-button').first().click({ force: true });
    check(await circles() === 0, 'circle goes when the popup closes');
  }

  // Live map: the selected node's circle stays while its detail panel is open.
  await page.goto(`${BASE}/#/live`, { waitUntil: 'domcontentloaded' });
  const liveIdx = await topmostPin('.live-node-marker');
  if (!liveIdx) {
    console.log('  SKIP: no live map node to select');
  } else {
    await page.locator('.live-node-marker').nth(liveIdx - 1).click();
    await page.waitForSelector('#liveNodeDetail:not(.hidden)', { timeout: 5000 }).catch(() => {});
    await page.mouse.move(2, 2);
    check(await circles() === 1, 'live map: selected node keeps its circle');
    await page.locator('#nodeDetailClose').click({ force: true });
    check(await circles() === 0, 'live map: circle goes when the detail panel closes');
  }

  // Planner notice (the planner is opt-in).
  await page.goto(`${BASE}/#/planner`, { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('.mc-pl-page, .mc-pl-off', { timeout: 15000 }).catch(() => {});
  if (await page.locator('.mc-pl-page').count()) {
    await page.waitForSelector('.mc-pl-page .lp-notice', { timeout: 10000 }).catch(() => {});
    const txt = await page.locator('.mc-pl-page .lp-notice').first().textContent().catch(() => '');
    check(/Node positions on this instance are approximate \(within .+\)\. Distances, line of sight and planning results are indicative\./.test(txt || ''), 'planner notice present');
  } else {
    console.log('  SKIP: planner not enabled on this server');
  }

  // A packet whose raw bytes are hidden.
  const list = (await getJson('/api/packets?limit=500')).packets || [];
  const hidden = list.find((p) => p.location_hidden === true);
  if (!hidden) {
    console.log('  SKIP: no location_hidden packet among the latest 500 (hidden-bytes check)');
  } else {
    check(hidden.raw_hex == null, 'API serves raw_hex null for the hidden packet');
    consoleErrors.length = 0;
    await page.goto(`${BASE}/#/packets/${hidden.hash}`, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('.lp-hidden-bytes', { timeout: 15000 }).catch(() => {});
    const msg = await page.locator('.lp-hidden-bytes').first().textContent().catch(() => '');
    check((msg || '').includes('Raw bytes hidden'), 'hidden-bytes message shown: ' + JSON.stringify(msg));
    check(await page.locator('.hex-dump').count() === 0, 'no hex dump for the hidden packet');
    check(consoleErrors.length === 0, 'no console errors on the hidden packet' + (consoleErrors.length ? ': ' + consoleErrors.join(' | ') : ''));
  }

  // One companion's coverage, refused to an anonymous viewer: a notice, not a
  // blank map, and it goes once the view is everyone's again. Any pubkey is
  // refused, so none from the fixture is needed.
  const rx = 'ab'.repeat(32);
  const gate = await fetch(`${BASE}/api/rx-coverage?bbox=-90,-180,90,180&z=8&rx=${rx}`);
  if (gate.status === 404 && process.env.LOCPRIV_REQUIRE !== '1') {
    console.log('  SKIP: clientRxCoverage not enabled on this server');
  } else {
    check(gate.status === 403, 'anonymous ?rx= coverage is refused: HTTP ' + gate.status);
    await page.goto(`${BASE}/#/rx-coverage?days=7&rx=${rx}`, { waitUntil: 'domcontentloaded' });
    const shown = await page.waitForSelector('#rxRefused:not([hidden])', { timeout: 15000 }).then(() => true, () => false);
    check(shown, 'refused per-companion view shows a notice');
    const note = await page.locator('#rxRefused').textContent().catch(() => '');
    check(/owner or an admin/.test(note || ''), 'notice says who can see it: ' + JSON.stringify(note));
    check(await page.locator('#rxMap path.leaflet-interactive').count() === 0, 'no hexes drawn under the notice');
    await page.goto(`${BASE}/#/rx-coverage?days=7`, { waitUntil: 'domcontentloaded' });
    const gone = await page.waitForFunction(() => {
      const el = document.getElementById('rxRefused');
      return el && el.hidden;
    }, null, { timeout: 15000 }).then(() => true, () => false);
    check(gone, 'notice hidden once no companion is selected');
  }

  await browser.close();
  if (failures) { console.error(`${NAME}: ${failures} failure(s)`); process.exit(1); }
  console.log(`${NAME}: OK`);
}

main().catch((e) => { console.error(NAME + ': ' + (e && e.stack || e)); process.exit(1); });

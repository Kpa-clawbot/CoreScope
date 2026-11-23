#!/usr/bin/env node
/* Location privacy (frontend). Runs only against a server started with
 * locationPrivacy.enabled; on a server with it off it prints SKIP and exits 0.
 *
 * Asserts (privacy on):
 *   - node detail says "Approximate location (within …)" and the mini-map
 *     draws the uncertainty circle;
 *   - the main map's attribution carries "Positions approximate";
 *   - the planner shows the approximate-positions notice (when the planner
 *     is enabled on this server);
 *   - a packet served with location_hidden shows the hidden-bytes message and
 *     the page logs no console errors.
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

  await browser.close();
  if (failures) { console.error(`${NAME}: ${failures} failure(s)`); process.exit(1); }
  console.log(`${NAME}: OK`);
}

main().catch((e) => { console.error(NAME + ': ' + (e && e.stack || e)); process.exit(1); });

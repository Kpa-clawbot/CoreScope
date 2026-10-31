#!/usr/bin/env node
/* MediaMesh (radio2) on Analytics and Live, in a real browser.
 *
 * The fixture has no radio2 traffic, so /api/analytics/mediamesh and the
 * relay-airtime share are answered here with radio2 data, shaped as the
 * server sends them. Checks:
 *   (1) Overview shows the MediaMesh card and the radio2 part of the
 *       Payload Type Mix and Relay Airtime Share;
 *   (2) the MediaMesh tab shows radio2 packets per hour, the repeaters and
 *       the observers;
 *   (3) Live has a "MediaMesh only" toggle that is remembered.
 * No page errors along the way.
 *
 * CHROMIUM_REQUIRE=1 makes Chromium-launch failure a HARD FAIL.
 */
'use strict';

const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';

let passes = 0, failures = 0;
function pass(msg) { console.log(`  ✓ ${msg}`); passes++; }
function fail(msg) { console.error(`  ✗ ${msg}`); failures++; }

const MM = {
  total_transmissions: 400, mediamesh_transmissions: 60, share_pct: 15,
  by_type: [{ name: 'GRP_TXT', count: 160, mediamesh: 40 }, { name: 'ADVERT', count: 100, mediamesh: 20 }],
  per_hour: [{ hour: '2026-10-30T14', count: 25 }, { hour: '2026-10-30T15', count: 35 }],
  observers: [{ id: 'obs1', name: 'First Hill', iata: 'SEA', mediamesh_observations: 60, total_observations: 400, share_pct: 15, last_heard: new Date().toISOString() }],
  repeaters: [{ public_key: 'aa11', name: 'Peak', role: 'repeater', mode: 'rxtx', radio: '911.15,250,7,5', source: 'status', last_seen: new Date().toISOString() }],
};

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
  const ctx = await browser.newContext({ viewport: { width: 1400, height: 1000 } });
  await ctx.route(/\/api\/analytics\/mediamesh/, (route) => route.fulfill({ json: MM }));
  await ctx.route(/\/api\/analytics\/relay-airtime-share/, async (route) => {
    const res = await route.fetch();
    let body;
    try { body = await res.json(); } catch { return route.fulfill({ response: res }); }
    if (Array.isArray(body.rows) && body.rows.length) {
      body.rows[0].mediamesh_count = 5;
      body.rows[0].mediamesh_airtime_pct = 12.5;
    }
    await route.fulfill({ response: res, json: body });
  });
  const page = await ctx.newPage();
  const errors = [];
  page.on('pageerror', (e) => errors.push(e.message));

  await page.goto(`${BASE}/#/analytics`, { waitUntil: 'domcontentloaded' });
  try {
    await page.waitForSelector('.stat-card-mediamesh', { timeout: 30000 });
    const ov = await page.evaluate(() => ({
      card: document.querySelector('.stat-card-mediamesh').textContent.replace(/\s+/g, ' '),
      segments: document.querySelectorAll('.mm-bar-segment').length,
      ring: document.querySelectorAll('.dumbbell-dot-mediamesh').length,
    }));
    if (/60/.test(ov.card) && /15\.0% of transmissions/.test(ov.card) && /1 repeater/.test(ov.card)) pass('(1a) Overview MediaMesh card');
    else fail(`(1a) card text: ${ov.card}`);
    if (ov.segments === 2) pass('(1b) Payload Type Mix shows the radio2 part of both types');
    else fail(`(1b) ${ov.segments} radio2 segments, expected 2`);
    // At least the row answered with radio2 data here; a server that already
    // has radio2 traffic marks more.
    if (ov.ring >= 1) pass(`(1c) Relay Airtime Share shows the radio2 ring (${ov.ring})`);
    else fail('(1c) no radio2 ring on Relay Airtime Share');
  } catch (e) {
    fail('(1) the Overview never showed the MediaMesh card: ' + e.message);
  }

  await page.click('[data-tab="mediamesh"]');
  try {
    await page.waitForSelector('.mm-chart svg', { timeout: 15000 });
    const tab = await page.evaluate(() => ({
      tables: [...document.querySelectorAll('#analyticsContent .analytics-table')].map((t) => t.textContent.replace(/\s+/g, ' ')),
      title: document.querySelector('.mm-chart h3').textContent,
    }));
    if (/radio2 packets/.test(tab.title)) pass('(2a) radio2 packets per hour');
    else fail(`(2a) chart title: ${tab.title}`);
    if (tab.tables.length === 2 && /Peak/.test(tab.tables[0]) && /listens and sends/.test(tab.tables[0]) && /First Hill/.test(tab.tables[1])) pass('(2b) repeaters and observers tables');
    else fail(`(2b) tables: ${JSON.stringify(tab.tables).slice(0, 300)}`);
  } catch (e) {
    fail('(2) the MediaMesh tab did not render: ' + e.message);
  }

  await page.goto(`${BASE}/#/live`, { waitUntil: 'domcontentloaded' });
  try {
    await page.waitForSelector('#liveMediaMeshToggle', { state: 'attached', timeout: 20000 });
    const stored = await page.evaluate(() => {
      const cb = document.getElementById('liveMediaMeshToggle');
      cb.checked = true; cb.dispatchEvent(new Event('change'));
      const v = localStorage.getItem('live-mediamesh-only');
      cb.checked = false; cb.dispatchEvent(new Event('change'));
      return v;
    });
    if (stored === 'true') pass('(3) Live "MediaMesh only" toggle is wired and remembered');
    else fail(`(3) stored value ${stored}`);
  } catch (e) {
    fail('(3) no MediaMesh toggle on Live: ' + e.message);
  }

  if (errors.length) fail('page errors: ' + errors.join(' | '));
  else pass('no page errors');

  await browser.close();
  console.log(`\ntest-analytics-mediamesh-e2e: ${passes} passed, ${failures} failed`);
  process.exit(failures ? 1 : 0);
}

main().catch((e) => { console.error(e); process.exit(1); });

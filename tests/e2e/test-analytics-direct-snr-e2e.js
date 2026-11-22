#!/usr/bin/env node
/* Analytics shows a node's SNR only from direct receptions.
 *
 * The SNR on an observation is the observer's reception of whichever node
 * transmitted that copy last. It belongs to a node only when the observer
 * heard that node's own transmission. This suite pins the four places on
 * #/analytics that used to show it otherwise:
 *
 *   (1) Nodes tab: the column reads "Direct SNR", a node without a direct
 *       reception shows a dash that says why, and it is not ranked under
 *       Best Signal Quality.
 *   (2) Distance tab: Top 20 Longest Hops has no SNR columns. A hop between
 *       two nodes has no measured SNR.
 *   (3) Topology tab: the "Hops vs SNR" card is gone.
 *   (4) RF tab: the page states that its values are observer receptions.
 *
 * The Nodes and Distance payloads are served by route interception so the
 * suite does not depend on the fixture holding a direct reception.
 *
 * CHROMIUM_REQUIRE=1 makes Chromium-launch failure a HARD FAIL.
 */
'use strict';

const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';

let passes = 0, failures = 0;
function pass(msg) { console.log(`  ✓ ${msg}`); passes++; }
function fail(msg) { console.error(`  ✗ ${msg}`); failures++; }

const PK_DIRECT = 'd1d1d1d1000000000000000000000000000000000000000000000000000000d1';
const PK_RELAYED = 'e2e2e2e2000000000000000000000000000000000000000000000000000000e2';
const recent = new Date(Date.now() - 60 * 1000).toISOString();

const NODES = {
  nodes: [
    { public_key: PK_DIRECT, name: 'DirectHeard', role: 'repeater', last_seen: recent, last_heard: recent },
    { public_key: PK_RELAYED, name: 'OnlyRelayed', role: 'repeater', last_seen: recent, last_heard: recent },
  ],
  total: 2,
};

const BULK_HEALTH = [
  {
    public_key: PK_DIRECT, name: 'DirectHeard', role: 'repeater',
    stats: { totalTransmissions: 10, totalPackets: 10, packetsToday: 2, avgSnr: 4.5, lastHeard: recent },
    observers: [{ observer_id: 'obs1', observer_name: 'Obs1', avgSnr: 4.5, avgRssi: -90, packetCount: 3, can_relay: null }],
    relayObserverCount: 0,
  },
  {
    public_key: PK_RELAYED, name: 'OnlyRelayed', role: 'repeater',
    stats: { totalTransmissions: 50, totalPackets: 50, packetsToday: 5, avgSnr: null, lastHeard: recent },
    observers: [],
    relayObserverCount: 4,
  },
];

const DISTANCE = {
  summary: { totalHops: 1, totalPaths: 1, avgDist: 12.5, maxDist: 12.5 },
  catStats: { 'R↔R': { count: 1, avg: 12.5, median: 12.5, min: 12.5, max: 12.5 } },
  distHistogram: { bins: [{ x: 12.5, count: 1 }] },
  distOverTime: [],
  topHops: [{
    fromName: 'DirectHeard', fromPk: PK_DIRECT, toName: 'OnlyRelayed', toPk: PK_RELAYED,
    dist: 12.5, type: 'R↔R', obsCount: 3, hash: 'abcd', timestamp: recent,
  }],
  topPaths: [],
};

async function contentText(page) {
  return page.evaluate(() => {
    const el = document.getElementById('analyticsContent');
    return el ? (el.innerText || '') : '';
  });
}

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
  await page.route('**/api/nodes/bulk-health*', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(BULK_HEALTH) }));
  await page.route(/\/api\/nodes\?/, (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(NODES) }));
  await page.route('**/api/analytics/distance*', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(DISTANCE) }));

  // (1) Nodes tab
  await page.goto(`${BASE}/#/analytics?tab=nodes`, { waitUntil: 'domcontentloaded' });
  try {
    await page.waitForFunction(() => /Best Signal Quality/.test((document.getElementById('analyticsContent') || {}).innerText || ''), null, { timeout: 15000 });
    const r = await page.evaluate(() => {
      const root = document.getElementById('analyticsContent');
      const heads = [...root.querySelectorAll('th')].map(th => th.textContent.trim());
      const h3 = [...root.querySelectorAll('h3')].find(h => /Best Signal Quality/.test(h.textContent));
      // The table may sit in a scroll wrapper: take the first one after the heading.
      let sib = h3 && h3.nextElementSibling, table = null;
      while (sib && !table) {
        table = sib.tagName === 'TABLE' ? sib : sib.querySelector('table');
        sib = sib.nextElementSibling;
      }
      const best = table ? table.innerText : '';
      const dash = [...root.querySelectorAll('td span[title="No direct reception"]')].map(s => s.textContent.trim());
      return { heads, best, dash };
    });
    if (r.heads.includes('Direct SNR') && !r.heads.includes('Avg SNR')) pass('(1a) the Nodes tab column reads "Direct SNR"');
    else fail(`(1a) Nodes tab headers: ${JSON.stringify(r.heads)}`);
    if (/DirectHeard/.test(r.best) && /4\.5 dB/.test(r.best) && !/OnlyRelayed/.test(r.best)) pass('(1b) only the directly heard node is ranked under Best Signal Quality');
    else fail(`(1b) Best Signal Quality table: ${JSON.stringify(r.best.slice(0, 300))}`);
    if (r.dash.length > 0 && r.dash.every(t => t === '—')) pass('(1c) a node without a direct reception shows a dash titled "No direct reception"');
    else fail(`(1c) no titled dash found: ${JSON.stringify(r.dash)}`);
  } catch (e) {
    fail(`(1) Nodes tab did not render: ${e.message}; showed ${JSON.stringify((await contentText(page)).slice(0, 300))}`);
  }

  // (1d) Nobody heard directly: Best Signal Quality says so instead of
  // rendering an empty table.
  const onlyRelayed = BULK_HEALTH.map(h => ({ ...h, stats: { ...h.stats, avgSnr: null }, observers: [] }));
  await page.unroute('**/api/nodes/bulk-health*');
  await page.route('**/api/nodes/bulk-health*', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(onlyRelayed) }));
  await page.goto(`${BASE}/#/packets`, { waitUntil: 'domcontentloaded' });
  await page.evaluate(() => { if (window.invalidateApiCache) window.invalidateApiCache('/nodes'); });
  await page.goto(`${BASE}/#/analytics?tab=nodes`, { waitUntil: 'domcontentloaded' });
  try {
    await page.waitForSelector('#analyticsContent .best-signal-empty', { timeout: 15000 });
    pass('(1d) with no direct reception, Best Signal Quality states that no node was heard directly');
  } catch (e) {
    fail(`(1d) no empty-state row in Best Signal Quality: ${e.message}`);
  }

  // (2) Distance tab
  await page.goto(`${BASE}/#/analytics?tab=distance`, { waitUntil: 'domcontentloaded' });
  try {
    await page.waitForFunction(() => /Longest Hops/.test((document.getElementById('analyticsContent') || {}).innerText || ''), null, { timeout: 15000 });
    const heads = await page.evaluate(() => {
      const h3 = [...document.querySelectorAll('#analyticsContent h3')].find(h => /Longest Hops/.test(h.textContent));
      const table = h3 && h3.parentElement.querySelector('table');
      return table ? [...table.querySelectorAll('th')].map(th => th.textContent.trim()) : null;
    });
    if (heads && heads.includes('Obs') && !heads.some(t => /SNR/.test(t))) pass('(2) Longest Hops has no SNR columns');
    else fail(`(2) Longest Hops headers: ${JSON.stringify(heads)}`);
  } catch (e) {
    fail(`(2) Distance tab did not render: ${e.message}`);
  }

  // (3) Topology tab
  await page.goto(`${BASE}/#/analytics?tab=topology`, { waitUntil: 'domcontentloaded' });
  try {
    await page.waitForFunction(() => /Repeater Pair Heatmap/.test((document.getElementById('analyticsContent') || {}).innerText || ''), null, { timeout: 20000 });
    const text = await contentText(page);
    if (!/Hops vs SNR/.test(text)) pass('(3) the Topology tab has no "Hops vs SNR" card');
    else fail('(3) the Topology tab still shows "Hops vs SNR"');
  } catch (e) {
    fail(`(3) Topology tab did not render: ${e.message}`);
  }

  // (4) RF tab
  await page.goto(`${BASE}/#/analytics?tab=rf`, { waitUntil: 'domcontentloaded' });
  try {
    await page.waitForSelector('#analyticsContent .rf-scope-note', { timeout: 20000 });
    const note = await page.textContent('#analyticsContent .rf-scope-note');
    if (/last hop → observer/.test(note)) pass('(4) the RF tab states that its values are observer receptions');
    else fail(`(4) RF note text: ${JSON.stringify(note)}`);
  } catch (e) {
    fail(`(4) RF tab note never appeared: ${e.message}`);
  }

  await browser.close();
  console.log(`\n${passes} passed, ${failures} failed`);
  process.exit(failures ? 1 : 0);
}

main().catch((e) => { console.error(e); process.exit(1); });

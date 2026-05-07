/**
 * E2E (#1151): Side-panel "Heard By" rows must not render orphan separators
 * when an observer's SNR and/or RSSI are null.
 *
 * Bug template (public/nodes.js):
 *   `${o.packetCount} pkts · ${snr ?: ''}${rssi ?: ''}`
 *   →  "110 pkts · " (trailing dot when both null)
 *   →  "110 pkts ·  · RSSI -52" (double dot when only SNR null)
 *
 * Fix: build a filtered parts array, then `.join(' · ')`.
 *
 * This test stubs /api/nodes/:pubkey/health via page.route() so we get
 * deterministic observer rows with all three null/non-null permutations
 * (the fixture DB has no real observers attached to a single node).
 *
 * Usage: BASE_URL=http://localhost:13581 node test-issue-1151-orphan-separators-e2e.js
 */
'use strict';
const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';

let passed = 0, failed = 0;
async function step(name, fn) {
  try { await fn(); passed++; console.log('  \u2713 ' + name); }
  catch (e) { failed++; console.error('  \u2717 ' + name + ': ' + e.message); }
}
function assert(c, m) { if (!c) throw new Error(m || 'assertion failed'); }

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });
  const ctx = await browser.newContext({ viewport: { width: 1400, height: 900 } });
  const page = await ctx.newPage();
  page.setDefaultTimeout(15000);
  page.on('pageerror', (e) => console.error('[pageerror]', e.message));

  console.log('\n=== #1151 orphan-separator E2E against ' + BASE + ' ===');

  // Pick any node from the API to drive the test.
  let pubkey = null;
  await step('fetch a real node pubkey from /api/nodes', async () => {
    const res = await page.request.get(BASE + '/api/nodes');
    assert(res.ok(), '/api/nodes returned ' + res.status());
    const body = await res.json();
    const arr = Array.isArray(body) ? body : (body.nodes || []);
    assert(arr.length > 0, 'fixture must contain at least one node');
    pubkey = arr[0].public_key || arr[0].pubkey || arr[0].id;
    assert(pubkey, 'first node must expose a public_key');
  });

  // Stub the health endpoint for this pubkey with three observer permutations:
  //   1. both null     →  pre-fix: "110 pkts ·  · " (trailing orphan after pkts)
  //   2. snr null only →  pre-fix: "55 pkts ·  · RSSI -50" (orphan between pkts and RSSI)
  //   3. rssi null only→  pre-fix: "22 pkts · SNR 5.5dB" (clean — control)
  //   4. both present  →  pre-fix: "11 pkts · SNR 7.0dB · RSSI -42" (clean — control)
  await page.route('**/api/nodes/' + encodeURIComponent(pubkey) + '/health', async (route) => {
    const stubBody = {
      node: { public_key: pubkey, name: 'TEST-NODE', role: 'repeater' },
      stats: { totalPackets: 200, packetsToday: 10, avgSnr: null, avgHops: 2, lastHeard: new Date().toISOString() },
      observers: [
        { observer_id: 'obs-both-null', observer_name: 'BothNull', iata: 'SJC', avgSnr: null, avgRssi: null, packetCount: 110 },
        { observer_id: 'obs-snr-null',  observer_name: 'SnrNull',  iata: 'SJC', avgSnr: null, avgRssi: -50, packetCount: 55 },
        { observer_id: 'obs-rssi-null', observer_name: 'RssiNull', iata: 'OAK', avgSnr: 5.5,  avgRssi: null, packetCount: 22 },
        { observer_id: 'obs-both-set',  observer_name: 'BothSet',  iata: 'OAK', avgSnr: 7.0,  avgRssi: -42, packetCount: 11 },
      ],
      recentPackets: [],
    };
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(stubBody) });
  });

  // Navigate to the nodes page and select the node so the side panel renders.
  await step('navigate /#/nodes/<pubkey> and wait for side-panel render', async () => {
    await page.goto(BASE + '/#/nodes/' + encodeURIComponent(pubkey), { waitUntil: 'domcontentloaded' });
    // The side panel renders into #nodesRight. Wait for the observer rows.
    await page.waitForSelector('#nodesRight .observer-row', { timeout: 15000 });
  });

  await step('side-panel "Heard By" rows render exactly 4 observers', async () => {
    const count = await page.$$eval('#nodesRight .observer-row', els => els.length);
    assert(count === 4, 'expected 4 observer rows, got ' + count);
  });

  await step('NO observer row contains an orphan separator (no "· ·" or trailing/leading " · ")', async () => {
    const rows = await page.$$eval('#nodesRight .observer-row', els => els.map(el => {
      // Read just the right-hand "stats" span (the suffix after the name).
      // Two <span> children per row; second one is the stats.
      const spans = el.querySelectorAll('span');
      const last = spans[spans.length - 1];
      // Normalize whitespace.
      return (last.textContent || '').replace(/\s+/g, ' ').trim();
    }));

    const offences = [];
    for (const text of rows) {
      // Adjacent middle-dot separators with optional spaces between them.
      if (/·\s*·/.test(text)) offences.push(['adjacent-dots', text]);
      // Trailing separator (e.g. "110 pkts ·").
      if (/·\s*$/.test(text)) offences.push(['trailing-dot', text]);
      // Leading separator (e.g. "· SNR ...").
      if (/^\s*·/.test(text)) offences.push(['leading-dot', text]);
    }

    if (offences.length) {
      const detail = offences.map(o => `[${o[0]}] "${o[1]}"`).join('\n      ');
      throw new Error('Found ' + offences.length + ' orphan-separator row(s):\n      ' + detail);
    }
  });

  await page.unroute('**/api/nodes/' + encodeURIComponent(pubkey) + '/health');
  await browser.close();

  console.log('\n=== #1151: ' + passed + ' passed, ' + failed + ' failed ===');
  process.exit(failed ? 1 : 0);
})().catch((e) => { console.error('FATAL', e); process.exit(2); });

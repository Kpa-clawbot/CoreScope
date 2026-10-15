#!/usr/bin/env node
/* Local browser regression: delayed analytics, theme refreshes and filter races. */
'use strict';
const assert = require('assert');
const { chromium } = require('playwright');
const BASE = process.env.BASE_URL || 'http://localhost:13581';
assert.ok(['localhost', '127.0.0.1', '[::1]'].includes(new URL(BASE).hostname), 'Use a local fixture server, never production');

function deferred() {
  let resolve;
  const promise = new Promise(r => { resolve = r; });
  return { promise, resolve };
}

const FIXTURES = {
  'hash-sizes': { total: 0, distribution: {}, hourly: [], topHops: [], multiByteNodes: [] },
  topology: { uniqueNodes: 4, avgHops: 1, maxHops: 1, hopDistribution: [] },
  channels: { activeChannels: 0, decryptable: 0 },
  'hash-collisions': {},
  'relay-airtime-share': { rows: [] },
  'neighbor-graph': {
    nodes: [
      { pubkey: 'aa'.repeat(32), role: 'companion', neighbor_count: 1 },
      { pubkey: 'bb'.repeat(32), role: 'companion', neighbor_count: 1 },
      { pubkey: 'cc'.repeat(32), role: 'repeater', neighbor_count: 1 },
      { pubkey: 'dd'.repeat(32), role: 'repeater', neighbor_count: 1 },
    ],
    edges: [
      { source: 'aa'.repeat(32), target: 'bb'.repeat(32), score: 0.9, ambiguous: false },
      { source: 'cc'.repeat(32), target: 'dd'.repeat(32), score: 0.9, ambiguous: false },
    ],
    stats: {},
  },
};

(async () => {
  const browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM_PATH || undefined });
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
    page.setDefaultTimeout(15000);
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    const gates = new Map(['', '1h', '24h', '7d'].map(window => [window, { response: deferred(), arrived: deferred() }]));
    await page.route('**/api/analytics/*', async route => {
      const url = new URL(route.request().url());
      const endpoint = url.pathname.split('/').pop();
      let body = FIXTURES[endpoint];
      if (endpoint === 'rf') {
        const gate = gates.get(url.searchParams.get('window') || '');
        gate.arrived.resolve(url.pathname.slice('/api'.length) + url.search);
        const total = await gate.response.promise;
        body = {
          totalTransmissions: total, totalPackets: total, packetsPerHour: [], payloadTypes: [],
          snr: { avg: 0, min: 0, max: 0 }, rssi: { avg: -100, min: -100, max: -100 },
          avgPacketSize: 20, minPacketSize: 20, maxPacketSize: 20, timeSpanHours: 1,
        };
      }
      if (body === undefined) return route.continue();
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });
    });

    async function release(window, total) {
      const gate = gates.get(window);
      const requestPath = await gate.arrived.promise;
      gate.response.resolve(total);
      // Wait until the real api() has consumed the response, not just until
      // the server has sent it; then its Promise.all continuation has run.
      await page.waitForFunction(path => _apiPerf.log.some(entry => entry.path === path), requestPath);
    }
    async function theme() {
      await page.evaluate(() => window.dispatchEvent(new CustomEvent('theme-refresh')));
    }
    async function selectWindow(window) {
      const request = page.waitForRequest(req => {
        const url = new URL(req.url());
        return url.pathname === '/api/analytics/rf' && url.searchParams.get('window') === window;
      });
      await page.locator('#analyticsTimeWindow').selectOption(window);
      await request;
    }

    await page.goto(BASE + '/#/analytics', { waitUntil: 'domcontentloaded' });
    await page.locator('#analyticsTimeWindow').waitFor();
    await theme();
    await page.locator('#analyticsTabs [data-tab="hashsizes"]').click();
    await theme();
    assert.match(await page.locator('#analyticsContent').innerText(), /Loading analytics/);
    assert.deepStrictEqual(errors, []);
    console.log('  ✓ pending core tabs and theme refresh retain the loading state');

    await page.locator('#analyticsTabs [data-tab="neighbor-graph"]').click();
    const companion = page.locator('#ngRoleChecks input[data-role="companion"]');
    await page.waitForFunction(() => document.querySelector('#ngStats .stat-value')?.textContent === '4');
    await companion.uncheck();
    const canvas = await page.locator('#ngCanvas').elementHandle();
    await release('', 111);
    await theme();
    assert.strictEqual(await companion.isChecked(), false, 'bulk completion and theme refresh must preserve graph filters');
    assert.strictEqual(await canvas.evaluate(el => el.isConnected), true, 'the graph DOM must not be rebuilt');
    console.log('  ✓ a lazy graph retains its filters and canvas after core data and theme refresh');

    await page.locator('#analyticsTabs [data-tab="overview"]').click();
    assert.strictEqual(await page.locator('#analyticsContent .stat-value').first().innerText(), '111');
    await selectWindow('1h');
    await theme();
    assert.match(await page.locator('#analyticsContent').innerText(), /Loading analytics/);
    await selectWindow('24h');
    await release('24h', 222);
    await page.waitForFunction(() => document.querySelector('#analyticsContent .stat-value')?.textContent === '222');
    await release('1h', 999);
    await theme();
    assert.strictEqual(await page.locator('#analyticsContent .stat-value').first().innerText(), '222');
    assert.strictEqual(await page.locator('#analyticsTimeWindow').inputValue(), '24h');
    console.log('  ✓ the newest time window wins when responses complete out of order');

    await selectWindow('7d');
    await page.evaluate(() => { location.hash = '#/home'; });
    await page.locator('#analyticsContent').waitFor({ state: 'detached' });
    await release('7d', 333);
    assert.strictEqual(await page.locator('#analyticsContent').count(), 0);
    assert.deepStrictEqual(errors, [], 'analytics must not throw during loading, refresh or page leave');
    console.log('  ✓ late completion after page leave is harmless');
    console.log('Analytics loading browser regression: 4 passed');
  } finally {
    await browser.close();
  }
})().catch(error => { console.error(error); process.exitCode = 1; });

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
  'subpaths-bulk': { results: Array.from({ length: 4 }, () => ({ subpaths: [] })) },
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
    const lazyGates = new Map(['E2E-OLD', 'E2E-NEW', 'E2E-LATE', 'E2E-ERROR', 'E2E-FAIL', 'E2E-LEFT'].map(region => [region, { response: deferred(), arrived: deferred() }]));
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
    // Use synthetic nodes and bulk health; hold the lazy response independently
    // of the six core requests so these exercise the actual renderer race.
    await page.route('**/api/nodes**', async route => {
      const url = new URL(route.request().url());
      const region = url.searchParams.get('region');
      const gate = lazyGates.get(region);
      if (!gate || !['/api/nodes', '/api/nodes/bulk-health'].includes(url.pathname)) return route.continue();
      const publicKey = 'ab'.repeat(32);
      let status = 200;
      let body = { nodes: [{ public_key: publicKey, name: region + ' synthetic node', role: 'repeater', last_seen: new Date().toISOString() }], has_more: false, counts: { repeater: 9000, companion: 8000, room: 7000 }, total: 1 };
      if (url.pathname.endsWith('/bulk-health')) {
        gate.arrived.resolve(url.pathname.slice('/api'.length) + url.search);
        status = await gate.response.promise;
        body = [{ public_key: publicKey, stats: { totalPackets: 22, packetsToday: 2, avgSnr: 3 }, observers: [] }];
      }
      await route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
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
    async function selectRegion(region, window) {
      await page.evaluate(region => RegionFilter.setSelected([region]), region);
      await selectWindow(window);
    }
    async function releaseLazy(region, status = 200) {
      const gate = lazyGates.get(region);
      const requestPath = await gate.arrived.promise;
      gate.response.resolve(status);
      // Errors do not enter _apiPerf.log. Wait for the real api() cleanup for
      // both success and rejection, then inspect the renderer's settled view.
      await page.waitForFunction(path => !_inflight.has(path), requestPath);
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

    await page.evaluate(() => {
      RegionFilter.setSelected(['E2E-OLD']);
      location.hash = '#/analytics?tab=nodes';
    });
    await lazyGates.get('E2E-OLD').arrived.promise;
    assert.match(await page.locator('#analyticsContent').innerText(), /Loading node analytics/);
    await selectRegion('E2E-NEW', '24h');
    await releaseLazy('E2E-NEW');
    await page.getByRole('link', { name: 'E2E-NEW synthetic node', exact: true }).first().waitFor();
    await releaseLazy('E2E-OLD');
    assert.match(await page.locator('#analyticsContent').innerText(), /E2E-NEW synthetic node/);
    assert.doesNotMatch(await page.locator('#analyticsContent').innerText(), /E2E-OLD synthetic node/);
    const subset = await page.evaluate(() => {
      const content = document.getElementById('analyticsContent');
      const heading = Array.from(content.querySelectorAll('h3')).find(el => el.textContent.trim() === 'Role Breakdown');
      return {
        roles: Array.from(heading.nextElementSibling.querySelectorAll('.badge'), el => el.textContent.trim()),
        total: content.querySelectorAll('.analytics-stat-card')[3].firstElementChild.textContent.trim(),
      };
    });
    assert.deepStrictEqual(subset.roles, ['repeater: 1', 'companion: 0', 'room: 0', 'sensor: 0', 'observer: 0', 'unknown: 0']);
    assert.strictEqual(subset.total, '1', 'role counts must agree with the selected subset total');
    console.log('  ✓ lazy Nodes keeps the newest filter and its role totals when the old response arrives last');

    for (const [region, status] of [['E2E-LATE', 200], ['E2E-ERROR', 500]]) {
      await page.evaluate(region => RegionFilter.setSelected([region]), region);
      await page.locator('#analyticsTabs [data-tab="nodes"]').click();
      await lazyGates.get(region).arrived.promise;
      await page.locator('#analyticsTabs [data-tab="subpaths"]').click();
      await page.getByRole('heading', { name: 'Route Pattern Analysis' }).waitFor();
      await releaseLazy(region, status);
      assert.match(await page.locator('#analyticsContent').innerText(), /Route Pattern Analysis/);
      assert.doesNotMatch(await page.locator('#analyticsContent').innerText(), /Failed to load node|synthetic node/);
    }
    console.log('  ✓ late lazy success and HTTP 500 failure preserve the newly selected tab');

    await page.evaluate(() => RegionFilter.setSelected(['E2E-FAIL']));
    await page.locator('#analyticsTabs [data-tab="nodes"]').click();
    await releaseLazy('E2E-FAIL', 500);
    await page.getByText(/Failed to load node analytics: API 500/).waitFor();
    assert.deepStrictEqual(errors, [], 'handled lazy HTTP failures must not become unhandled rejections');
    lazyGates.set('E2E-FAIL', { response: deferred(), arrived: deferred() });
    await theme();
    await releaseLazy('E2E-FAIL');
    await page.getByRole('link', { name: 'E2E-FAIL synthetic node', exact: true }).first().waitFor();
    console.log('  ✓ a current lazy failure is visible and a subsequent refresh recovers');

    await selectRegion('E2E-LEFT', '1h');
    await lazyGates.get('E2E-LEFT').arrived.promise;
    await page.evaluate(() => { location.hash = '#/home'; });
    await page.locator('#analyticsContent').waitFor({ state: 'detached' });
    await releaseLazy('E2E-LEFT');
    assert.strictEqual(await page.locator('#analyticsContent').count(), 0);
    assert.deepStrictEqual(errors, [], 'lazy completion after destroy must not throw or restart rendering');
    console.log('  ✓ a delayed lazy response after page leave is harmless');
    console.log('Analytics loading browser regression: 8 passed');
  } finally {
    await browser.close();
  }
})().catch(error => { console.error(error); process.exitCode = 1; });

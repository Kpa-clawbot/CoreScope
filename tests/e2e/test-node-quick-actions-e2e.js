'use strict';
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:3000';
assert.ok(['localhost', '127.0.0.1', '[::1]'].includes(new URL(BASE).hostname), 'Use a local fixture server');
const surfaces = [
  { name: 'desktop-full', width: 1400, selector: '#nodeFullBody', full: true },
  { name: 'desktop-side', width: 1400, selector: '#nodesRight' },
  { name: 'tablet-slide-over', width: 820, selector: '.slide-over-content' },
  { name: 'phone-full', width: 390, selector: '#nodeFullBody', full: true, touch: true },
];
let passed = 0, failed = 0;
async function test(name, run) {
  try { await run(); passed++; console.log('PASS ' + name); }
  catch (error) { failed++; console.error('FAIL ' + name + ': ' + error.message); }
}

(async () => {
  const browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM_PATH || undefined });
  try {
    const requestContext = await browser.newContext();
    const response = await requestContext.request.get(BASE + '/api/nodes?limit=1');
    assert.ok(response.ok(), 'fixture nodes API succeeds');
    const node = (await response.json()).nodes[0];
    assert.ok(node && node.public_key, 'fixture must contain a node');
    const detailResponse = await requestContext.request.get(BASE + '/api/nodes/' + encodeURIComponent(node.public_key));
    assert.ok(detailResponse.ok(), 'fixture detail API succeeds');
    const detail = await detailResponse.json();
    assert.ok(detail.node, 'actual detail response has a node field');
    await requestContext.close();

    async function open(surface, overrides = {}) {
      const ctx = await browser.newContext({
        viewport: { width: surface.width, height: 900 },
        hasTouch: !!surface.touch, isMobile: !!surface.touch,
      });
      await ctx.addInitScript(() => {
        localStorage.setItem('meshcore-time-window', '60');
        localStorage.setItem('map-view', JSON.stringify({ lat: 40, lng: -80, zoom: 3 }));
      });
      const page = await ctx.newPage();
      page.setDefaultTimeout(10000);
      const current = { ...detail.node, lat: 12.5, lon: 34.5, ...overrides };
      // Preserve the live API shape, replacing just the chosen node so edge
      // cases are deterministic; all application scripts and routing are real.
      await page.route(url => url.pathname === '/api/nodes', route => route.fulfill({ json: {
        nodes: [current], total: 1, has_more: false,
        counts: { repeaters: 1, rooms: 0, companions: 0, sensors: 0 },
      } }));
      await page.route(url => url.pathname === '/api/nodes/' + encodeURIComponent(node.public_key),
        route => route.fulfill({ json: { ...detail, node: current } }));
      await page.goto(BASE + '/#/nodes' + (surface.full ? '/' + encodeURIComponent(node.public_key) : ''), { waitUntil: 'domcontentloaded' });
      if (!surface.full) await page.locator('#nodesBody tr[data-action="select"]').first().click();
      const host = page.locator(surface.selector);
      const existing = surface.name === 'tablet-slide-over'
        ? host.getByRole('link', { name: 'Open full detail →', exact: true })
        : host.getByRole('link', { name: 'Analytics', exact: true });
      await existing.waitFor({ state: 'visible' });
      return { ctx, page, host, current };
    }

    for (const surface of surfaces) {
      for (const destination of ['map', 'packets']) {
        await test(surface.name + ' opens ' + destination, async () => {
          const { ctx, page, host, current } = await open(surface);
          try {
            const label = destination === 'map' ? 'Open in Map' : 'View packets';
            const link = host.getByRole('link', { name: label, exact: true });
            assert.equal(await link.count(), 1, label + ' must be present');
            const rect = await link.boundingBox();
            assert.ok(rect && rect.width > 0 && rect.x >= 0 && rect.x + rect.width <= surface.width,
              label + ' fits within the viewport');
            if (surface.touch) assert.ok(rect.height >= 44 && rect.width >= 44, 'phone actions have a 44px touch target');
            if (process.env.SCREENSHOT_DIR && destination === 'map') {
              fs.mkdirSync(process.env.SCREENSHOT_DIR, { recursive: true });
              await page.screenshot({ path: path.join(process.env.SCREENSHOT_DIR, surface.name + '.png') });
            }
            const regionBefore = await page.evaluate(() => localStorage.getItem('meshcore-region-filter'));
            const packetRequest = destination === 'packets'
              ? page.waitForRequest(request => {
                const url = new URL(request.url());
                return url.pathname === '/api/packets' && url.searchParams.get('node') === current.public_key;
              }) : null;
            // Native anchor keyboard behavior on desktop; actual tap on phone.
            if (surface.touch) await link.tap();
            else if (surface.name === 'desktop-full') { await link.focus(); await page.keyboard.press('Enter'); }
            else await link.click();
            await page.waitForURL(url => url.hash.startsWith('#/' + destination + '?'));
            const params = new URLSearchParams(new URL(page.url()).hash.split('?')[1]);
            assert.equal(params.get('node'), current.public_key);
            if (destination === 'map') {
              await page.locator('.leaflet-popup-content .map-popup').waitFor({ state: 'visible' });
              assert.ok((await page.locator('.leaflet-popup-content').innerText()).includes(current.name), 'selected node popup opens');
              const viewport = await page.evaluate(() => ({ center: window.__mc_map.getCenter(), zoom: window.__mc_map.getZoom() }));
              assert.ok(Math.abs(viewport.center.lat - current.lat) < .001 && Math.abs(viewport.center.lng - current.lon) < .001);
              assert.equal(viewport.zoom, 14);
            } else {
              await packetRequest;
              await page.locator('#fNode').waitFor({ state: 'visible' });
              assert.equal(await page.locator('#fNode').inputValue(), current.public_key.slice(0, 8));
              assert.equal(await page.evaluate(() => localStorage.getItem('meshcore-time-window')), '60');
            }
            assert.equal(await page.evaluate(() => localStorage.getItem('meshcore-region-filter')), regionBefore);
          } finally { await ctx.close(); }
        });
      }
    }

    for (const surface of [surfaces[0], surfaces[1], surfaces[2], surfaces[3]]) {
      for (const coords of [{ lat: null }, { lat: '12north' }, { lon: 181 }]) {
        await test(surface.name + ' suppresses Map for ' + JSON.stringify(coords), async () => {
          const { ctx, host } = await open(surface, coords);
          try {
            assert.equal(await host.getByRole('link', { name: 'Open in Map', exact: true }).count(), 0);
            assert.equal(await host.getByRole('link', { name: 'View packets', exact: true }).count(), 1);
          } finally { await ctx.close(); }
        });
      }
    }
    await test('zero coordinates override the saved map viewport', async () => {
      const { ctx, page, host } = await open(surfaces[3], { lat: 0, lon: 0 });
      try {
        const link = host.getByRole('link', { name: 'Open in Map', exact: true });
        assert.equal(await link.count(), 1);
        await link.tap();
        await page.waitForFunction(() => window.__mc_map && document.getElementById('leaflet-map'));
        const viewport = await page.evaluate(() => ({ center: window.__mc_map.getCenter(), zoom: window.__mc_map.getZoom() }));
        assert.ok(Math.abs(viewport.center.lat) < .001 && Math.abs(viewport.center.lng) < .001);
        assert.equal(viewport.zoom, 14);
      } finally { await ctx.close(); }
    });
    await test('hostile identifiers stay inside the node query parameter', async () => {
      const key = 'node" onfocus="bad &/?#%\'<value>';
      const { ctx, page, host } = await open(surfaces[0], { public_key: key });
      try {
        for (const label of ['Open in Map', 'View packets']) {
          const link = host.getByRole('link', { name: label, exact: true });
          assert.equal(await link.count(), 1);
          const attrs = await link.evaluate(element => ({ href: element.getAttribute('href'), injected: element.hasAttribute('onfocus') }));
          assert.equal(new URLSearchParams(attrs.href.split('?')[1]).get('node'), key);
          assert.equal(attrs.injected, false);
        }
        await host.getByRole('link', { name: 'View packets', exact: true }).click();
        await page.waitForURL(url => url.hash.startsWith('#/packets?'));
        assert.equal(new URLSearchParams(new URL(page.url()).hash.split('?')[1]).get('node'), key);
      } finally { await ctx.close(); }
    });
  } finally { await browser.close(); }
  console.log(`${passed} passed, ${failed} failed`);
  process.exitCode = failed ? 1 : 0;
})().catch(error => { console.error(error); process.exitCode = 1; });

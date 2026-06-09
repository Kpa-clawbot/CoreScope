/**
 * E2E: the Map view must paginate /api/nodes past the server's 500-row cap.
 *
 * Bug (this PR): map.js issued a single `/api/nodes?limit=10000` fetch. The
 * server (v3.8.3 / PR #1540 DoS guard) clamps ?limit to 500 and orders by
 * last_seen DESC, so on >500-node meshes the map silently dropped every node
 * whose last self-advert fell outside the top 500 — even ones relaying
 * constantly. The node still appeared in the (paginated, #1606) Nodes list but
 * vanished from the map.
 *
 * This test mocks /api/nodes with a 500-per-page server cap and a 501st node
 * ("PAGE2 RP") reachable only on the second page. After the map loads, the
 * app's node set (window.__mc_nodes, populated by map.js loadNodes() →
 * fetchAllNodes()) must contain all 501 nodes including the page-2 node, and a
 * marker for it must exist on the map. Pre-fix, __mc_nodes would hold 500 and
 * the page-2 node would be absent.
 *
 * Backend-independent: every /api/* call the map makes at load is mocked via
 * page.route, so it runs against any static host at BASE_URL (the CI fixture
 * server, or a plain static server locally).
 *
 * Run: BASE_URL=http://localhost:13581 node test-map-nodes-pagination-e2e.js
 */
'use strict';
const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';
const PAGE_CAP = 500;          // mirrors the real server's ?limit clamp
const PAGE2_KEY = 'page2deadbeef00000000000000000000000000000000000000000000000beef02';
const PAGE2_NAME = 'PAGE2 RP';

let passed = 0, failed = 0;
async function step(name, fn) {
  try { await fn(); passed++; console.log('  ✓ ' + name); }
  catch (e) { failed++; console.error('  ✗ ' + name + ': ' + e.message); }
}
function assert(c, m) { if (!c) throw new Error(m || 'assertion failed'); }

// Build the full fixture: 500 "page 1" repeaters + 1 page-2 repeater.
function buildFixture() {
  const nodes = [];
  for (let i = 0; i < PAGE_CAP; i++) {
    nodes.push({
      public_key: 'p1' + String(i).padStart(62, '0'),
      name: 'P1-' + i,
      role: 'repeater',
      lat: 51 + (i % 100) * 0.001,
      lon: 5 + Math.floor(i / 100) * 0.001,
      last_seen: '2026-06-09T07:40:00Z',
      hash_size: 1,
    });
  }
  nodes.push({
    public_key: PAGE2_KEY,
    name: PAGE2_NAME,
    role: 'repeater',
    lat: 50.5,
    lon: 4.5,
    last_seen: '2026-06-08T13:55:14Z', // older advert → would be cut by the 500 cap
    hash_size: 2,
  });
  return nodes;
}

(async () => {
  const launchOpts = {
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  };
  const browser = await chromium.launch(launchOpts);
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  const page = await ctx.newPage();
  page.setDefaultTimeout(15000);
  page.on('pageerror', (e) => console.error('[pageerror]', e.message));

  console.log(`\n=== Map /api/nodes pagination E2E against ${BASE} ===`);

  const fixture = buildFixture();
  let nodesRequests = 0;

  // Mock every /api/* call the map makes at load. /api/nodes?... is paginated
  // with the same 500-row clamp + offset semantics as the real server; all
  // other endpoints get harmless stubs so the test is backend-independent.
  await page.route('**/api/**', (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname;
    if (path === '/api/nodes') {
      nodesRequests++;
      const limit = Math.min(parseInt(url.searchParams.get('limit') || '50', 10), PAGE_CAP);
      const offset = parseInt(url.searchParams.get('offset') || '0', 10);
      const slice = fixture.slice(offset, offset + limit);
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          nodes: slice,
          total: slice.length, // server emits a clamped/unreliable total
          counts: { repeaters: fixture.length, rooms: 0, companions: 0, sensors: 0 },
        }),
      });
    }
    if (path === '/api/observers') {
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ observers: [] }) });
    }
    // Generic stub for config/regions/map/etc. — empty object is safe.
    return route.fulfill({ status: 200, contentType: 'application/json', body: '{}' });
  });

  await page.goto(BASE + '/#/map', { waitUntil: 'load', timeout: 60000 });
  await page.waitForSelector('#leaflet-map', { timeout: 15000 });

  await step('map loads all 501 nodes by paginating past the 500-row cap', async () => {
    // Wait until loadNodes() has populated the app node set.
    await page.waitForFunction(
      () => Array.isArray(window.__mc_nodes) && window.__mc_nodes.length >= 501,
      { timeout: 15000 }
    );
    const len = await page.evaluate(() => window.__mc_nodes.length);
    assert(len === 501, 'expected 501 nodes in __mc_nodes, got ' + len);
    assert(nodesRequests >= 2, 'expected ≥2 /api/nodes page requests, got ' + nodesRequests);
  });

  await step('the page-2 node (cut by the cap pre-fix) is present in the node set', async () => {
    const found = await page.evaluate(
      (key) => window.__mc_nodes.some((n) => n.public_key === key),
      PAGE2_KEY
    );
    assert(found, 'page-2 node ' + PAGE2_NAME + ' missing from __mc_nodes');
  });

  await step('a marker for the page-2 node is rendered on the map', async () => {
    const hasMarker = await page.evaluate((key) => {
      let found = false;
      const scan = (layer) => {
        if (found || !layer || !layer.eachLayer) return;
        layer.eachLayer((m) => {
          if (found) return;
          if (m._nodeKey === key) { found = true; return; }
          if (m.eachLayer) scan(m); // cluster groups nest their markers
        });
      };
      // markerLayer + clusterGroup are internal; reach them via the map's layers.
      if (window.__mc_map && window.__mc_map.eachLayer) window.__mc_map.eachLayer(scan);
      return found;
    }, PAGE2_KEY);
    assert(hasMarker, 'no marker with _nodeKey for the page-2 node was rendered');
  });

  await browser.close();
  console.log(`\n${passed} passed, ${failed} failed`);
  process.exit(failed > 0 ? 1 : 0);
})().catch((e) => { console.error(e); process.exit(1); });

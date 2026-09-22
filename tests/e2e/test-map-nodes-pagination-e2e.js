/**
 * E2E: the Map view must paginate /api/nodes past the server's per-request cap.
 *
 * Bug (this PR): map.js issued a single `/api/nodes?limit=10000` fetch. The
 * server clamps ?limit to listLimits.nodesMax (default 2000; originally 500 in
 * PR #1540) and orders by last_seen DESC, so on a mesh larger than the cap the
 * map silently dropped every node whose last self-advert fell outside the top
 * window — even ones relaying constantly. The node still appeared in the
 * (paginated, #1606) Nodes list but vanished from the map.
 *
 * This test mocks /api/nodes with a 500-per-page cap (the client page size) and
 * a 501st node ("PAGE2 RP") reachable only on the second page. After the map
 * loads, the app's node set (window.__mc_nodes, populated by map.js loadNodes()
 * → fetchAllNodes()) must contain all 501 nodes including the page-2 node, and a
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
const PAGE_CAP = 500;          // client page size; a node sits past it on page 2
const FILTERED_INDEX = 450;    // a page-1 row the server drops AFTER the SQL LIMIT
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

// #2030: release an old map's asynchronous response only after teardown,
// including after a replacement map has already finished loading.
async function checkMapTeardown(browser, stage, revisit) {
  const page = await browser.newPage();
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  page.on('console', message => {
    if (message.type() === 'error' && message.text().includes('Map load error')) errors.push(message.text());
  });
  await page.route('**/api/**', route => route.fulfill({ json: {} }));
  await page.goto(BASE + '/#/tools');
  await page.waitForFunction(() => typeof fetchAllNodes === 'function');
  await page.evaluate(stage => {
    window.__pendingMapResponse = null;
    let first = true;
    let load = 0;
    const defer = value => new Promise(resolve => { window.__pendingMapResponse = () => resolve(value); });
    const originalApi = window.api;
    window.api = async function (path, options) {
      if (path === '/config/regions' || path === '/observers') {
        const value = path === '/observers' ? { observers: [] } : {};
        if (first && path === stage) { first = false; return defer(value); }
        return value;
      }
      return originalApi(path, options);
    };
    window.fetchAllNodes = async function () {
      const name = ++load === 1 ? 'OLD MAP' : 'CURRENT MAP';
      const value = { nodes: [{ public_key: name === 'OLD MAP' ? 'aa'.repeat(32) : 'bb'.repeat(32), name, role: 'repeater', lat: 1, lon: 1, last_seen: new Date().toISOString() }], counts: { repeaters: 1 } };
      if (first && stage === 'nodes') { first = false; return defer(value); }
      return value;
    };
    location.hash = '#/map';
  }, stage);
  await page.waitForFunction(() => typeof window.__pendingMapResponse === 'function');
  await page.evaluate(() => { location.hash = '#/tools'; });
  await page.waitForSelector('#leaflet-map', { state: 'detached' });
  if (revisit) {
    await page.evaluate(() => { location.hash = '#/map'; });
    await page.waitForSelector('#leaflet-map[data-loaded="true"]');
  }
  const before = await page.evaluate(() => ({ nodes: JSON.stringify(window.__mc_nodes), html: document.getElementById('app').innerHTML }));
  await page.evaluate(async () => {
    window.__pendingMapResponse();
    // A rendering turn drains the released async chain; no race-prone sleep.
    await new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)));
  });
  const after = await page.evaluate(() => ({ nodes: JSON.stringify(window.__mc_nodes), html: document.getElementById('app').innerHTML }));
  try {
    assert(errors.length === 0, 'teardown must not log errors: ' + errors.join('; '));
    assert(after.nodes === before.nodes, 'old response must not overwrite the map node set');
    if (!revisit) assert(after.html === before.html, 'old response must not change the destination page');
    else {
      // A stale completion must not attach a second pane listener either.
      await page.locator('#mapControlsToggle').click();
      await page.locator('#mapPaneToggle').click();
      assert(await page.locator('#mapSidePane').evaluate(el => el.classList.contains('expanded')), 'replacement inspector must toggle exactly once');
    }
  } finally {
    await page.close();
  }
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
      const raw = fixture.slice(offset, offset + limit);
      // Model handleNodes exactly: the blacklist / hidden-prefix / geo-filter
      // passes run AFTER the SQL LIMIT, so FILTERED_INDEX is counted by the
      // limit and by COUNT(*) but removed from the page. Page 1 comes back a
      // row short WITHOUT being the last page — the shape that stranded every
      // node behind it once a client stopped on a short page.
      const slice = raw.filter((_n, i) => offset + i !== FILTERED_INDEX);
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          nodes: slice,
          total: slice.length, // deliberately wrong per-page total; the helper must ignore it
          has_more: offset + raw.length < fixture.length, // decided pre-filter, as the server does
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

  await step('a page shortened by post-LIMIT filtering does not end pagination', async () => {
    // 500 = 501 fixture nodes minus the one the mock filters out of page 1.
    // Pre-fix the run stops on that 499-row page and __mc_nodes holds 499.
    await page.waitForFunction(
      () => Array.isArray(window.__mc_nodes) && window.__mc_nodes.length >= 500,
      { timeout: 15000 }
    );
    const len = await page.evaluate(() => window.__mc_nodes.length);
    assert(len === 500, 'expected 500 nodes in __mc_nodes (501 minus the filtered row), got ' + len);
    assert(nodesRequests >= 2, 'expected ≥2 /api/nodes page requests, got ' + nodesRequests);
    const filteredGone = await page.evaluate(
      (k) => !window.__mc_nodes.some((n) => n.public_key === k),
      'p1' + String(FILTERED_INDEX).padStart(62, '0')
    );
    assert(filteredGone, 'the filtered row must stay filtered, not reappear');
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

  for (const stage of ['/config/regions', 'nodes', '/observers']) {
    for (const revisit of [false, true]) {
      await step('late ' + stage + ' response after ' + (revisit ? 'map replacement' : 'map teardown'), () => checkMapTeardown(browser, stage, revisit));
    }
  }
  await browser.close();
  console.log(`\n${passed} passed, ${failed} failed`);
  process.exit(failed > 0 ? 1 : 0);
})().catch((e) => { console.error(e); process.exit(1); });

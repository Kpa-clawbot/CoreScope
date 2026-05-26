/**
 * #1374 — Packet-route map view a11y + visual modernization.
 *
 * Asserts the rewritten `/#/map?route=N` renderer:
 *   - role-aware shape markers (reuses makeRoleMarkerSVG)
 *   - origin / destination semantically distinct from intermediate hops
 *   - sequence-number badges (separate from label text)
 *   - directional arrows on edges + per-edge aria-label
 *   - per-marker role="img" + aria-label "Hop N of M, <name>, <role>"
 *   - deconflictLabels reused — no overlapping label boxes
 *   - collapsible legend panel renders
 *   - partial-route handling: unresolved markers + "X of N hops resolved"
 *
 * Strategy: the production renderer is split into a pure
 * `window.MeshRoute.render(map, layer, positions, options)` that the test
 * drives directly with synthetic positions, so no DB is required. The
 * production `drawPacketRoute` resolves hops then calls the same function.
 *
 * Run: BASE_URL=http://localhost:13581 node test-issue-1374-route-map-a11y-e2e.js
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

// Synthetic 4-hop route in the Bay Area.
const ROUTE_FIXTURE = {
  origin:      { pubkey: 'aa00aa00aa00aa00', name: 'Originator Node',  role: 'companion', lat: 37.78, lon: -122.42, isOrigin: true },
  hops: [
    { pubkey: 'bb11bb11bb11bb11', name: 'Big Redwood Oakland', role: 'repeater', lat: 37.80, lon: -122.27, resolved: true },
    { pubkey: 'cc22cc22cc22cc22', name: 'San Carlos Rptr',     role: 'repeater', lat: 37.51, lon: -122.26, resolved: true },
    { pubkey: 'dd33dd33dd33dd33', name: 'Room Server SJ',      role: 'room',     lat: 37.34, lon: -121.89, resolved: true },
    { pubkey: 'ee44ee44ee44ee44', name: 'Destination Node',    role: 'sensor',   lat: 37.27, lon: -121.97, resolved: true, isDest: true },
  ]
};

const PARTIAL_FIXTURE = {
  origin: { pubkey: 'aa00aa00aa00aa00', name: 'Originator Node', role: 'companion', lat: 37.78, lon: -122.42, isOrigin: true },
  hops: [
    { pubkey: 'bb11bb11bb11bb11', name: 'Big Redwood Oakland', role: 'repeater', lat: 37.80, lon: -122.27, resolved: true },
    { pubkey: 'unresolved-xx',    name: 'unresol',              role: null,       resolved: false },
    { pubkey: 'dd33dd33dd33dd33', name: 'Destination Node',    role: 'sensor',   lat: 37.34, lon: -121.89, resolved: true, isDest: true },
  ]
};

async function renderRouteOnPage(page, fixture) {
  return await page.evaluate((fx) => {
    if (!window.MeshRoute || typeof window.MeshRoute.render !== 'function') {
      return { error: 'window.MeshRoute.render not present' };
    }
    // Build positions array: [origin, ...hops]
    const positions = [];
    if (fx.origin) positions.push(Object.assign({}, fx.origin));
    for (const h of fx.hops) positions.push(Object.assign({}, h));
    // Reset any existing route
    if (window.__mc_routeLayer && window.__mc_routeLayer.clearLayers) {
      window.__mc_routeLayer.clearLayers();
    }
    window.MeshRoute.render(window.__mc_map, window.__mc_routeLayer, positions, {
      timestamp: new Date('2025-01-01T12:00:00Z').toISOString()
    });
    return { ok: true, count: positions.length };
  }, fixture);
}

async function runViewport(browser, width, height, label) {
  console.log('\n=== Viewport ' + label + ' (' + width + 'x' + height + ') ===');
  const ctx = await browser.newContext({ viewport: { width, height } });
  const page = await ctx.newPage();
  page.on('pageerror', e => console.error('  pageerror:', e.message));
  await page.goto(BASE + '/#/map', { waitUntil: 'load', timeout: 60000 });
  await page.waitForSelector('#leaflet-map', { timeout: 10000 });
  // Wait for MeshRoute to register
  await page.waitForFunction(() => window.MeshRoute && window.__mc_map && window.__mc_routeLayer, { timeout: 10000 });
  await page.waitForTimeout(400);

  const r1 = await renderRouteOnPage(page, ROUTE_FIXTURE);
  assertNoError(r1);
  await page.waitForTimeout(500);

  await step(label + ': every hop marker has role="img" and informative aria-label', async () => {
    const data = await page.evaluate(() => {
      const markers = Array.from(document.querySelectorAll('.mc-route-marker[role="img"]'));
      return markers.map(m => m.getAttribute('aria-label') || '');
    });
    assert(data.length === 5, 'expected 5 markers, got ' + data.length);
    const re = /Hop \d+ of \d+, [^,]+, (repeater|companion|room|sensor|observer)/;
    for (const lbl of data) {
      assert(re.test(lbl), 'aria-label "' + lbl + '" does not match Hop N of M pattern');
    }
  });

  await step(label + ': origin aria-label contains "originator", destination contains "destination"', async () => {
    const data = await page.evaluate(() => {
      const markers = Array.from(document.querySelectorAll('.mc-route-marker[role="img"]'));
      return markers.map(m => m.getAttribute('aria-label') || '');
    });
    assert(/originator/i.test(data[0]), 'origin label missing "originator": ' + data[0]);
    assert(/destination/i.test(data[data.length - 1]), 'destination label missing "destination": ' + data[data.length - 1]);
  });

  await step(label + ': sequence-number badge present beside each marker (not in label text)', async () => {
    const data = await page.evaluate(() => {
      const badges = Array.from(document.querySelectorAll('.mc-route-seq-badge'));
      return badges.map(b => b.textContent.trim());
    });
    assert(data.length >= 5, 'expected >=5 sequence badges, got ' + data.length);
    // Badges should be numeric or numbered glyphs.
    for (const b of data) {
      assert(/^[\d①②③④⑤⑥⑦⑧⑨⑩▶⚑]+$/.test(b), 'badge "' + b + '" not numeric/glyph');
    }
  });

  await step(label + ': no two label boxes overlap (deconflict reused)', async () => {
    const rects = await page.evaluate(() => {
      const labels = Array.from(document.querySelectorAll('.mc-route-label'));
      return labels.map(l => {
        const r = l.getBoundingClientRect();
        return { x: r.x, y: r.y, w: r.width, h: r.height };
      });
    });
    assert(rects.length >= 2, 'expected at least 2 labels rendered, got ' + rects.length);
    for (let i = 0; i < rects.length; i++) {
      for (let j = i + 1; j < rects.length; j++) {
        const a = rects[i], b = rects[j];
        const overlap = a.x < b.x + b.w && a.x + a.w > b.x && a.y < b.y + b.h && a.y + a.h > b.y;
        assert(!overlap, 'labels ' + i + ' and ' + j + ' overlap');
      }
    }
  });

  await step(label + ': edges have aria-label "Hop N \u2192 N+1"', async () => {
    const data = await page.evaluate(() => {
      const edges = Array.from(document.querySelectorAll('path.mc-route-edge[aria-label]'));
      return edges.map(e => e.getAttribute('aria-label'));
    });
    assert(data.length >= 4, 'expected >=4 edge aria-labels, got ' + data.length);
    const re = /Hop \d+ \u2192 \d+/;
    for (const lbl of data) assert(re.test(lbl), 'edge label "' + lbl + '" missing arrow pattern');
  });

  await step(label + ': edges carry directionality marker (marker-end arrow)', async () => {
    const data = await page.evaluate(() => {
      const edges = Array.from(document.querySelectorAll('path.mc-route-edge'));
      const arrowDefs = document.querySelectorAll('marker[id^="mc-route-arrow"]');
      return {
        edgeCount: edges.length,
        withArrow: edges.filter(e => /url\(#mc-route-arrow/.test(e.getAttribute('marker-end') || '')).length,
        defCount: arrowDefs.length
      };
    });
    assert(data.defCount >= 1, 'expected at least one <marker id="mc-route-arrow…"> def, got ' + data.defCount);
    assert(data.withArrow >= data.edgeCount, 'not all edges have marker-end arrow: ' +
      data.withArrow + '/' + data.edgeCount);
  });

  await step(label + ': collapsible legend panel renders with role entries', async () => {
    const data = await page.evaluate(() => {
      const legend = document.querySelector('.mc-route-legend');
      if (!legend) return { found: false };
      const toggle = legend.querySelector('[aria-expanded]');
      const entries = legend.querySelectorAll('.mc-route-legend-entry, .mc-route-legend-role');
      const txt = legend.textContent.toLowerCase();
      return {
        found: true,
        hasToggle: !!toggle,
        entryCount: entries.length,
        hasRoleTerm: /repeater|companion|room|sensor/.test(txt),
        hasOriginTerm: /origin/.test(txt),
        hasDestTerm: /destin/.test(txt)
      };
    });
    assert(data.found, '.mc-route-legend not rendered');
    assert(data.hasToggle, 'legend toggle missing aria-expanded');
    assert(data.entryCount >= 3, 'expected >=3 legend entries, got ' + data.entryCount);
    assert(data.hasRoleTerm, 'legend missing role labels');
    assert(data.hasOriginTerm, 'legend missing origin/destination glyph entries');
    assert(data.hasDestTerm, 'legend missing destination glyph entry');
  });

  await step(label + ': toolbar shows "Route observed at <timestamp>" context label', async () => {
    const data = await page.evaluate(() => {
      const el = document.querySelector('.mc-route-context-label');
      return el ? el.textContent : null;
    });
    assert(data && /Route observed at/i.test(data), 'missing "Route observed at" label, got: ' + data);
  });

  // Partial route case
  const r2 = await page.evaluate(() => {
    if (window.__mc_routeLayer && window.__mc_routeLayer.clearLayers) window.__mc_routeLayer.clearLayers();
  });
  await renderRouteOnPage(page, PARTIAL_FIXTURE);
  await page.waitForTimeout(400);

  await step(label + ': partial-route — unresolved marker carries ch-unresolved class', async () => {
    const data = await page.evaluate(() => {
      return document.querySelectorAll('.mc-route-marker[class*="ch-unresolved"]').length;
    });
    assert(data >= 1, 'expected >=1 ch-unresolved marker, got ' + data);
  });

  await step(label + ': partial-route — "X of N hops resolved" badge present', async () => {
    const data = await page.evaluate(() => {
      const el = document.querySelector('.mc-route-resolved-badge');
      return el ? el.textContent : null;
    });
    assert(data && /\d+ of \d+ hops resolved/i.test(data), 'missing resolved badge, got: ' + data);
  });

  await ctx.close();
}

function assertNoError(r) {
  if (r && r.error) throw new Error(r.error);
}

async function run() {
  const launchOpts = { args: ['--no-sandbox'] };
  if (process.env.CHROMIUM_PATH) launchOpts.executablePath = process.env.CHROMIUM_PATH;
  const browser = await chromium.launch(launchOpts);
  try {
    await runViewport(browser, 375, 800, 'mobile');
    await runViewport(browser, 1920, 1080, 'desktop');
  } finally {
    await browser.close();
  }
  console.log('\n' + passed + ' passed, ' + failed + ' failed');
  if (failed > 0) process.exit(1);
}

run().catch(e => { console.error(e); process.exit(1); });

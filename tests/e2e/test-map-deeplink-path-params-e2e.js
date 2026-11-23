/**
 * E2E: map route deep-link path params.
 *
 *   #/map?packet=<hash>&observer=<pubkey or 8+ char prefix>
 *     → that observer's observation is the canonical route and its path is
 *       isolated in the path picker (row active), as if the row was clicked.
 *   #/map?packet=<hash>&paths=all
 *     → the picker opens in its "All" union view, as if All was clicked.
 *   #/map?packet=<hash>&paths=<hop>-<hop>-...
 *     → the picker opens with that path isolated.
 *   Clicking All or a row writes that view to the hash, so a reload shows it.
 *
 * Uses the CI-seeded #1486 transmission (SEED_HASH): three observations,
 * observers idx 1/2/3 on paths ["AA"], ["BB"], ["CC"]. When running
 * locally, seed the same row (see .github/workflows/deploy.yml for the SQL).
 * One-hop paths have no edge to draw, so the click cases serve that packet
 * with three two-hop paths between located fixture nodes.
 *
 * Usage: BASE_URL=http://localhost:13581 node test-map-deeplink-path-params-e2e.js
 */
'use strict';
const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';
const SEED_HASH = 'fae0c9e6d357a814';

let passed = 0, failed = 0;
async function step(name, fn) {
  try { await fn(); passed++; console.log('  ✓ ' + name); }
  catch (e) { failed++; console.error('  ✗ ' + name + ': ' + e.message); }
}
function assert(c, m) { if (!c) throw new Error(m || 'assertion failed'); }

// Cold-load a route deep link and wait until the path picker exists and the
// deep-linked view (if any) has had its frame to apply.
async function openRoute(page, query) {
  await page.goto(BASE + '/#/map?packet=' + SEED_HASH + query, { waitUntil: 'domcontentloaded' });
  await page.reload({ waitUntil: 'domcontentloaded' });
  await page.waitForSelector('.mc-rt-path-row', { timeout: 15000 });
  await page.evaluate(() => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r))));
  return page.evaluate(() => {
    const sb = document.querySelector('.mc-rt-sidebar');
    return {
      rows: [...document.querySelectorAll('.mc-rt-path-row')].map(r => ({
        key: r.dataset.pathKey, active: r.classList.contains('mc-rt-path-active')
      })),
      back: sb && sb.querySelector('.mc-rt-back-link').getAttribute('href'),
      union: !!sb && Array.isArray(sb._unionOverlay),
    };
  });
}
const active = s => s.rows.filter(r => r.active).map(r => r.key);

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });
  const ctx = await browser.newContext({ viewport: { width: 1400, height: 900 } });
  const page = await ctx.newPage();
  page.setDefaultTimeout(10000);
  page.on('pageerror', (e) => console.error('[pageerror]', e.message));

  console.log(`\n=== map deep-link path params E2E against ${BASE} ===`);

  const resp = await ctx.request.get(BASE + '/api/packets/' + SEED_HASH);
  const observations = (await resp.json()).observations || [];
  const target = observations.find(o => JSON.parse(o.path_json || '[]').join() === 'BB');
  assert(target, 'seeded observation with path ["BB"] not found');
  const pubkey = String(target.observer_id);

  await step('default view: no path isolated, no union view', async () => {
    const s = await openRoute(page, '');
    assert(s.rows.length === 3, 'expected 3 unique paths, got ' + s.rows.length);
    assert(active(s).length === 0, 'no row should be active, got ' + active(s));
    assert(!s.union, 'union view should not be open');
  });

  await step('observer=<8-char prefix> isolates that observer\'s path', async () => {
    const s = await openRoute(page, '&observer=' + pubkey.slice(0, 8).toLowerCase());
    assert(active(s).join() === 'BB', 'expected BB active, got ' + active(s));
    assert(s.back === '#/packets/' + SEED_HASH + '?obs=' + target.id, 'back link should name the observation, got ' + s.back);
  });

  await step('observer=<full public key> isolates the same path', async () => {
    const s = await openRoute(page, '&observer=' + pubkey);
    assert(active(s).join() === 'BB', 'expected BB active, got ' + active(s));
  });

  await step('unmatched obs falls through to observer; back link names the selected observation', async () => {
    const s = await openRoute(page, '&obs=999999999&observer=' + pubkey.slice(0, 8));
    assert(active(s).join() === 'BB', 'expected BB active, got ' + active(s));
    assert(s.back === '#/packets/' + SEED_HASH + '?obs=' + target.id, 'back link must not keep the unmatched obs, got ' + s.back);
  });

  await step('paths=all opens the All (union) view', async () => {
    const s = await openRoute(page, '&paths=all');
    assert(s.union, 'union overlay should be drawn');
    assert(active(s).length === 0, 'no row should be active, got ' + active(s));
  });

  await step('unknown observer falls back to the default view', async () => {
    const s = await openRoute(page, '&observer=00000000');
    assert(active(s).length === 0 && !s.union, 'expected the default view');
  });

  const nodes = ((await (await ctx.request.get(BASE + '/api/nodes?limit=2000')).json()).nodes || [])
    .filter(n => n.lat && n.lon && n.public_key);
  // Hops far enough apart that no two paths can share an edge.
  const byPrefix = new Map();
  const places = new Set();
  for (const n of nodes) {
    const prefix = n.public_key.slice(0, 6).toUpperCase();
    const place = n.lat + ',' + n.lon;
    if (byPrefix.has(prefix) || places.has(place)) continue;
    places.add(place);
    byPrefix.set(prefix, n);
    if (byPrefix.size === 6) break;
  }
  const hops = [...byPrefix.keys()];
  assert(hops.length === 6, 'fixture needs 6 located nodes, got ' + hops.length);
  const paths = [hops.slice(0, 2), hops.slice(2, 4), hops.slice(4, 6)];
  const targetIndex = observations.indexOf(target);
  // A path that is neither the observer's nor the first observation's.
  const otherIndex = [2, 1].find(i => i !== targetIndex);

  // The seeded packet with servedPaths, one per observation, on a page of its
  // own; use(p) gets that page.
  async function withRoute(query, use, servedPaths = paths, context = ctx) {
    const p = await context.newPage();
    p.setDefaultTimeout(15000);
    await p.route('**/api/packets/' + SEED_HASH, async route => {
      const response = await route.fetch();
      const json = await response.json();
      json.observations.forEach((o, i) => {
        o.path_json = JSON.stringify(servedPaths[i]);
        o.resolved_path = null;
      });
      await route.fulfill({ response, json });
    });
    try {
      await p.goto(BASE + '/#/map?packet=' + SEED_HASH + query, { waitUntil: 'domcontentloaded' });
      await settle(p);
      await use(p);
    } finally {
      await p.close();
    }
  }
  async function settle(p) {
    await p.waitForSelector('.mc-rt-path-row');
    await p.evaluate(() => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r))));
  }
  async function reload(p) {
    await p.reload({ waitUntil: 'domcontentloaded' });
    await settle(p);
  }
  const param = async (p, name) =>
    new URLSearchParams((await p.evaluate(() => location.hash)).split('?')[1]).get(name);
  const rowFor = (p, path) => p.locator('.mc-rt-path-row[data-path-key="' + path.join('\u2192') + '"]');
  // What the operator sees: the active row, the hop list and the back link.
  const screen = p => p.evaluate(() => JSON.stringify({
    active: [...document.querySelectorAll('.mc-rt-path-active')].map(r => r.dataset.pathKey),
    hops: [...document.querySelectorAll('.mc-rt-row .mc-rt-name')].map(r => r.textContent),
    union: document.querySelectorAll('path.mc-rt-edge-union').length,
    back: document.querySelector('.mc-rt-back-link').getAttribute('href'),
  }));
  // The isolated hops outside the visible map, once its fits have settled.
  async function hopsOffScreen(p) {
    await p.waitForTimeout(1200); // two 250 ms zooms and the 200 ms resize refit
    return p.evaluate(() => {
      const map = document.querySelector('#leaflet-map').getBoundingClientRect();
      return [...document.querySelectorAll('.mc-rt-iso-marker')].filter(m => {
        const r = m.getBoundingClientRect();
        const x = r.x + r.width / 2, y = r.y + r.height / 2;
        return x < map.left || x > map.right || y < map.top || y > map.bottom;
      }).length;
    });
  }
  const waitForAll = p => p.waitForFunction(
    want => document.querySelectorAll('path.mc-rt-edge-union').length === want, paths.length);

  await step('All writes paths=all to the hash, and a reload draws every path again', () =>
    withRoute('&observer=' + pubkey, async p => {
      await p.waitForSelector('.mc-rt-path-row.mc-rt-path-active');
      assert(await p.locator('path.mc-rt-edge-union').count() === 0, 'All view should not be drawn before the click');
      const entries = await p.evaluate(() => history.length);

      await p.click('.mc-rt-path-clear');
      await waitForAll(p);
      assert(await param(p, 'paths') === 'all', 'All should write paths=all to the hash');
      assert(await param(p, 'observer') === pubkey, 'All should keep observer=');
      assert(await param(p, 'packet') === SEED_HASH, 'All should keep packet=');
      assert(await p.evaluate(() => history.length) === entries, 'All should not add a history entry');

      await reload(p);
      await waitForAll(p);
      assert(await p.locator('.mc-rt-path-active').count() === 0, 'no path should be isolated after the reload');
      assert(await param(p, 'paths') === 'all', 'the reload should keep paths=all');
    }));

  for (const [name, query, key] of [
    ['from All with observer', '&observer=' + pubkey + '&paths=all', null],
    ['from the observer view', '&observer=' + pubkey, null],
    ['from an obs link', '&obs=' + target.id, null],
    ['from a packet-only link', '', null],
    ['by keyboard', '&observer=' + pubkey, 'Enter'],
    // This observer's path has a located hop, so the route's first fit zooms
    // in on it and must not swallow the picked path's fit.
    ['beside a located observer path', '&observer=' + observations[0].observer_id + '&paths=all', null],
  ]) {
    await step('a row picked ' + name + ' survives a reload', () => withRoute(query, async p => {
      const entries = await p.evaluate(() => history.length);
      const row = rowFor(p, paths[otherIndex]);
      if (key) await row.press(key); else await row.click();
      const picked = await screen(p);
      assert(await param(p, 'paths') === paths[otherIndex].join('-'),
        'the row should write its path, got ' + await param(p, 'paths'));
      for (const kept of ['observer', 'obs']) {
        assert(await param(p, kept) === new URLSearchParams(query).get(kept), kept + '= should be kept');
      }
      assert(await p.evaluate(() => history.length) === entries, 'a row click should not add a history entry');

      await reload(p);
      const reloaded = await screen(p);
      assert(reloaded === picked, 'reload shows ' + reloaded + ', expected ' + picked);
      const offScreen = await hopsOffScreen(p);
      assert(offScreen === 0, offScreen + ' hop(s) of the reloaded path are off screen');
    }));
  }

  await step('All replaces a picked path', () => withRoute('&paths=' + paths[otherIndex].join('-'), async p => {
    await p.click('.mc-rt-path-clear');
    assert(await param(p, 'paths') === 'all', 'All should replace the path');
    await reload(p);
    await waitForAll(p);
  }));

  await step('opening an observer link does not rewrite it', async () => {
    const query = '&observer=' + pubkey;
    await withRoute(query, async p => {
      await p.waitForSelector('.mc-rt-path-active');
      assert(await p.evaluate(() => location.hash) === '#/map?packet=' + SEED_HASH + query, 'the hash changed on load');
    });
  });

  await step('a redraw keeps the picked path, then the picked All view', () =>
    withRoute('&observer=' + pubkey, async p => {
      // The hide-1-byte-hops setting redraws the route in place.
      const redraw = async () => {
        await p.evaluate(() => {
          document.querySelector('.mc-rt-sidebar').dataset.beforeRedraw = '1';
          window.dispatchEvent(new CustomEvent('mc-hide-1byte-hops-changed'));
        });
        await p.waitForSelector('.mc-rt-sidebar:not([data-before-redraw])');
        await settle(p);
      };
      await rowFor(p, paths[otherIndex]).click();
      const picked = await screen(p);
      await redraw();
      const redrawn = await screen(p);
      assert(redrawn === picked, 'redraw shows ' + redrawn + ', expected ' + picked);

      await p.click('.mc-rt-path-clear');
      await waitForAll(p);
      await redraw();
      await waitForAll(p);
    }));

  await step('a picked path opens for someone with another 1-byte-hop setting', async () => {
    const mixed = paths.map((path, i) => ['0' + (i + 1)].concat(path));
    const sender = await browser.newContext({ viewport: { width: 1400, height: 900 } });
    await sender.addInitScript(() => localStorage.setItem('meshcore-hide-1byte-hops', 'true'));
    try {
      let shared;
      await withRoute('', async p => {
        await rowFor(p, paths[otherIndex]).click();
        shared = await param(p, 'paths');
      }, mixed, sender);
      assert(shared === mixed[otherIndex].join('-'), 'the link should carry the raw hops, got ' + shared);
      await withRoute('&paths=' + shared, async p => {
        assert(await p.locator('.mc-rt-path-active').getAttribute('data-path-key') === mixed[otherIndex].join('\u2192'),
          'the recipient should see the same path');
      }, mixed);
    } finally {
      await sender.close();
    }
  });

  await browser.close();
  console.log(`\n${passed} passed, ${failed} failed`);
  process.exit(failed === 0 ? 0 : 1);
})().catch(e => { console.error(e); process.exit(1); });

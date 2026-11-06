/**
 * E2E: map route deep-link path params.
 *
 *   #/map?packet=<hash>&observer=<pubkey or 8+ char prefix>
 *     → that observer's observation is the canonical route and its path is
 *       isolated in the path picker (row active), as if the row was clicked.
 *   #/map?packet=<hash>&paths=all
 *     → the picker opens in its "All" union view, as if All was clicked.
 *
 * Uses the CI-seeded #1486 transmission (SEED_HASH): three observations,
 * observers idx 1/2/3 on paths ["AA"], ["BB"], ["CC"]. When running
 * locally, seed the same row (see .github/workflows/deploy.yml for the SQL).
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

  await step('paths=all opens the All (union) view', async () => {
    const s = await openRoute(page, '&paths=all');
    assert(s.union, 'union overlay should be drawn');
    assert(active(s).length === 0, 'no row should be active, got ' + active(s));
  });

  await step('unknown observer falls back to the default view', async () => {
    const s = await openRoute(page, '&observer=00000000');
    assert(active(s).length === 0 && !s.union, 'expected the default view');
  });

  await browser.close();
  console.log(`\n${passed} passed, ${failed} failed`);
  process.exit(failed === 0 ? 0 : 1);
})().catch(e => { console.error(e); process.exit(1); });

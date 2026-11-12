/**
 * E2E: radio line-of-sight (LOS) on the map route view.
 *
 * The fixture server does not enable LOS, so the test stubs the two network
 * calls it depends on:
 *   GET /api/config/client          → real JSON plus a `los` block
 *   GET /api/elevation/profile?pts= → 202 (terrain loading) once, then three
 *                                     canned legs (clear, ridge, n/a)
 * A negative case runs without the config stub (LOS off → inert).
 *
 * Route setup has no URL param: hops go in sessionStorage 'map-route-hops',
 * then navigate to #/map?route=1.
 *
 * Usage: BASE_URL=http://localhost:13581 node test-route-los-e2e.js
 */
'use strict';
const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';

// PB Rep 2 -> Ocean St.Repeater -> Summit Store Repeater -> GY889 Repeater
const HOPS = [
  '12c2180e9bc4d78796a105f4dd63d261bf16f909222abe53904210c0c3ef1669',
  '3f7fbf7c4136e9cc274a3aa6939feda206e358983ef74c72ce7c55dfdc0681ba',
  '4ca3dcec6fdfe37fee1215f4e3ac36ec4af75ec3c81572d6568e8fdac96fe367',
  '1e598d0f11783a33c04d701ff3151fd8d8c0a96686d88267df70993f59ef760c',
];
const LOS_CFG = { frequencyMhz: 910.525, antennaHeightM: 5, kFactor: 1.3333, maxPoints: 16, maxLegKm: 300 };
const SOURCE = 'SRTM (AWS Open Data)';

let passed = 0, failed = 0;
async function step(name, fn) {
  try { await fn(); passed++; console.log('  ✓ ' + name); }
  catch (e) { failed++; console.error('  ✗ ' + name + ': ' + e.message); }
}
function assert(c, m) { if (!c) throw new Error(m || 'assertion failed'); }

function flat() { return Array.from({ length: 100 }, () => 10); }
function ridge() { const a = flat(); for (let i = 45; i <= 55; i++) a[i] = 900; return a; }

// Stubs the elevation endpoint; returns a live record of the calls. With
// loadingFirst the first call answers 202 (terrain still loading), as the
// server does while it downloads tiles; count is the 200s, polls the 202s.
async function stubElevation(page, opts) {
  const rec = { count: 0, polls: 0, pts: [], polledPts: [] };
  await page.route('**/api/elevation/profile*', async (route) => {
    const pts = new URL(route.request().url()).searchParams.get('pts');
    if (opts && opts.loadingFirst && rec.polls === 0) {
      rec.polls++;
      rec.polledPts.push(pts);
      await route.fulfill({
        status: 202, contentType: 'application/json',
        headers: { 'Retry-After': '1', 'Cache-Control': 'no-store' },
        body: JSON.stringify({ state: 'loading_terrain', progress: 0.5, retry_after_s: 1 }),
      });
      return;
    }
    rec.count++;
    rec.pts.push(pts);
    await route.fulfill({
      status: 200, contentType: 'application/json',
      body: JSON.stringify({
        source: SOURCE,
        legs: [
          { distance_km: 0.5, elev_m: flat() },
          { distance_km: 20.1, elev_m: ridge() },
          { distance_km: 31.7, error: 'unknown_point' },
        ],
      }),
    });
  });
  return rec;
}

async function stubLosConfig(page) {
  await page.route('**/api/config/client', async (route) => {
    const resp = await route.fetch();
    const json = await resp.json();
    await route.fulfill({ response: resp, contentType: 'application/json', body: JSON.stringify(Object.assign({}, json, { los: LOS_CFG })) });
  });
}

// The map consumes 'map-route-hops' once read, so seed it before every
// document load (initial navigation and reloads alike).
async function seedHops(page) {
  await page.addInitScript((hops) => {
    try { sessionStorage.setItem('map-route-hops', JSON.stringify({ hops })); } catch (e) { /* ignore */ }
  }, HOPS);
}

async function openRoute(page, query) {
  await page.goto(BASE + '/#/map?route=1' + (query || ''), { waitUntil: 'domcontentloaded' });
  await page.reload({ waitUntil: 'domcontentloaded' });
  await page.waitForSelector('li.mc-rt-row', { timeout: 15000 });
}

const hash = (page) => page.evaluate(() => location.hash);
const chipInfo = (page) => page.evaluate(() =>
  [...document.querySelectorAll('li.mc-rt-row')].map(r => {
    const c = r.querySelector('button.mc-los-chip');
    return c ? { text: c.textContent.trim(), cls: c.className } : null;
  }));

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });
  const ctx = await browser.newContext({ viewport: { width: 1400, height: 900 } });
  const errors = [];

  console.log(`\n=== route LOS E2E against ${BASE} ===`);

  // Real node coordinates, for the unrounded-pts assertion.
  const coords = [];
  for (const pk of HOPS) {
    const r = await ctx.request.get(BASE + '/api/nodes/' + pk);
    const j = await r.json();
    const n = j.node || j;
    coords.push([n.lat, n.lon]);
  }
  assert(coords.every(c => typeof c[0] === 'number' && typeof c[1] === 'number'), 'fixture nodes need lat/lon: ' + JSON.stringify(coords));

  // ── LOS enabled ──
  const page = await ctx.newPage();
  page.setDefaultTimeout(10000);
  page.on('pageerror', (e) => errors.push(e));
  await seedHops(page);
  const rec = await stubElevation(page, { loadingFirst: true });
  await stubLosConfig(page);

  await openRoute(page);
  await page.waitForSelector('button.mc-los-chip', { timeout: 15000 });
  await page.waitForSelector('.mc-los-obstruction', { state: 'attached', timeout: 15000 });

  await step('a 202 is re-polled at the same URL, then exactly 1 answer', async () => {
    assert(rec.polls === 1 && rec.polledPts[0] === rec.pts[0], '202 polls: ' + rec.polls + ', pts ' + JSON.stringify(rec.polledPts));
  });

  await step('exactly 1 elevation request with unrounded coordinates', async () => {
    assert(rec.count === 1, 'expected 1 request, got ' + rec.count);
    const nums = rec.pts[0].split(',').map(Number);
    assert(nums.length === 8, 'expected 8 numbers, got ' + nums.length);
    const want = coords.flat();
    nums.forEach((v, i) => assert(v === want[i], `pts[${i}]=${v} != node value ${want[i]}`));
  });

  await step('chips on rows 2-4 with good / bad / n/a verdicts', async () => {
    const c = await chipInfo(page);
    assert(c.length === 4, 'expected 4 rows, got ' + c.length);
    assert(c[0] === null, 'row 1 should have no chip');
    assert(/mc-los-tone-good/.test(c[1].cls) && /LOS clear/.test(c[1].text), 'row 2: ' + JSON.stringify(c[1]));
    assert(/mc-los-tone-warn/.test(c[2].cls) && /Terrain in path/.test(c[2].text), 'row 3: ' + JSON.stringify(c[2]));
    assert(/LOS n\/a/.test(c[3].text), 'row 4: ' + JSON.stringify(c[3]));
  });

  await step('exactly 1 obstruction marker', async () => {
    const n = await page.locator('.mc-los-obstruction').count();
    assert(n === 1, 'expected 1 marker, got ' + n);
  });

  await step('row-3 chip opens panel without expanding the hop detail', async () => {
    await page.locator('li.mc-rt-row').nth(2).locator('button.mc-los-chip').click();
    await page.waitForSelector('.mc-los-panel svg.mc-los-svg');
    const obst = await page.locator('.mc-los-panel .mc-los-obst').textContent();
    assert(/Terrain rises/.test(obst), 'obst text: ' + obst);
    const foot = await page.locator('.mc-los-panel .mc-los-foot').textContent();
    assert(foot === 'Elevation: ' + SOURCE + ' · terrain only, trees and buildings are not modelled', 'footer: ' + foot);
    assert(await page.locator('.mc-rt-detail-panel').count() === 0, 'hop detail panel should be absent');
    assert(/los=2/.test(await hash(page)), 'hash: ' + await hash(page));
  });

  await step('hovering the chart shows cursor and km readout', async () => {
    const box = await page.locator('.mc-los-hit').boundingBox();
    assert(box, 'hit area has no box');
    await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
    await page.waitForFunction(() => {
      const c = document.querySelector('.mc-los-cursor');
      return c && c.getAttribute('visibility') === 'visible';
    });
    const ro = await page.locator('.mc-los-readout').textContent();
    assert(/km/.test(ro), 'readout: ' + ro);
  });

  await step('next moves to leg 3 (n/a); Escape closes and clears the hash', async () => {
    await page.locator('.mc-los-next').click();
    await page.waitForFunction(() => /los=3/.test(location.hash));
    const meta = await page.locator('.mc-los-panel').textContent();
    assert(/n\/a/i.test(meta), 'panel should show n/a state: ' + meta.slice(0, 200));
    await page.keyboard.press('Escape');
    await page.waitForFunction(() => !document.querySelector('.mc-los-panel'));
    assert(!/los=/.test(await hash(page)), 'hash still has los=: ' + await hash(page));
  });

  await step('changing Antenna A re-analyses with no new elevation request', async () => {
    await page.locator('li.mc-rt-row').nth(2).locator('button.mc-los-chip').click();
    await page.waitForSelector('.mc-los-panel .mc-los-obst');
    await page.locator('#mc-los-in-antA').fill('200');
    await page.waitForTimeout(400);
    assert(rec.count === 1, 'elevation requests: ' + rec.count);
    const c = await chipInfo(page);
    assert(/mc-los-tone-(good|warn|bad)/.test(c[2].cls), 'row 3 chip lost its verdict: ' + JSON.stringify(c[2]));
    assert(/LOS (clear|marginal)|Terrain in path/.test(c[2].text), 'row 3 text: ' + c[2].text);
  });

  await step('reload with &los=2 opens the panel on leg 2', async () => {
    await page.goto(BASE + '/#/map?route=1&los=2', { waitUntil: 'domcontentloaded' });
    await page.reload({ waitUntil: 'domcontentloaded' });
    await page.waitForSelector('.mc-los-panel svg.mc-los-svg', { timeout: 15000 });
    const t = await page.locator('.mc-los-panel .mc-los-pos').textContent();
    assert(/Link 2 of 3/.test(t), 'position: ' + t);
    assert(/los=2/.test(await hash(page)), 'hash: ' + await hash(page));
  });

  await step('no pageerror from route-los.js or los.js', async () => {
    errors.forEach(e => console.log('    [pageerror]', e.message));
    const ours = errors.filter(e => /route-los\.js|los\.js/.test(e.stack || '') || /MeshRouteLOS|MeshLOS/.test(e.message));
    assert(ours.length === 0, ours.map(e => e.message).join('; '));
  });
  await page.close();

  // ── LOS disabled (config not stubbed) ──
  const off = await ctx.newPage();
  off.setDefaultTimeout(10000);
  const offErrors = [];
  off.on('pageerror', (e) => offErrors.push(e));
  await seedHops(off);
  const offRec = await stubElevation(off);

  await step('LOS off: route renders, no chips, no elevation requests', async () => {
    await openRoute(off);
    await off.evaluate(() => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r))));
    await off.waitForTimeout(500);
    assert(await off.locator('li.mc-rt-row').count() === 4, 'expected 4 rows');
    assert(await off.locator('button.mc-los-chip').count() === 0, 'chips should be absent');
    assert(offRec.count === 0, 'elevation requests: ' + offRec.count);
    const ours = offErrors.filter(e => /route-los\.js|los\.js/.test(e.stack || ''));
    assert(ours.length === 0, ours.map(e => e.message).join('; '));
  });

  await browser.close();
  console.log(`\n${passed} passed, ${failed} failed`);
  process.exit(failed ? 1 : 0);
})().catch((e) => { console.error(e); process.exit(1); });

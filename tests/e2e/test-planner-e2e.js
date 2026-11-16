/**
 * E2E: Repeater Planner page (#/tools/planner), all three modes.
 *
 * The fixture server does not enable the planner and has no terrain source, so
 * the test stubs the network calls the page depends on:
 *   GET /api/config/client     → real JSON plus a `planner` block
 *   GET /api/planner/profile   → canned legs (flat, 900 m ridge)
 *   GET /api/planner/relay     → job protocol: 202 then 200
 *   GET /api/planner/coverage  → job protocol: 202 then 200, or an error
 * Negative case: without the config stub the page shows its disabled state.
 *
 * Usage: BASE_URL=http://localhost:13581 node test-planner-e2e.js
 */
'use strict';
const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';

const PLANNER_CFG = {
  defaultRangeKm: 30, rangesKm: [10, 15, 20, 30, 40, 50, 60], newAntennaM: 10, existingAntennaM: 5,
  frequencyMhz: 910.525, kFactor: 1.3333, maxPoints: 16, maxLegKm: 100, maxAreaKm: 100, maxSites: 10,
  minClearance: 0.6,
};
const XSS = '"><img src=x onerror=alert(1)>';

let passed = 0, failed = 0;
async function step(name, fn) {
  try { await fn(); passed++; console.log('  ✓ ' + name); }
  catch (e) { failed++; console.error('  ✗ ' + name + ': ' + e.message); }
}
function assert(c, m) { if (!c) throw new Error(m || 'assertion failed'); }

// A valley between two 100 m ends is comfortably clear; a 900 m ridge blocks.
function flat() { return Array.from({ length: 100 }, (_, i) => (i === 0 || i === 99 ? 100 : 0)); }
function ridge() { const a = flat(); for (let i = 45; i <= 55; i++) a[i] = 900; return a; }

const json = (status, body, headers) => ({
  status, contentType: 'application/json', headers: headers || {}, body: JSON.stringify(body),
});

async function stubConfig(page) {
  await page.route('**/api/config/client', async (route) => {
    const resp = await route.fetch();
    const j = await resp.json();
    await route.fulfill({ response: resp, contentType: 'application/json', body: JSON.stringify(Object.assign({}, j, { planner: PLANNER_CFG })) });
  });
}

// Stubs one planner endpoint with a response sequence (the last entry repeats).
// An entry may carry `gate`, a promise the response waits on, so the test can
// observe an in-flight state deterministically. Returns a live call record.
async function stubJob(page, pattern, seq) {
  const rec = { count: 0, urls: [] };
  await page.route(pattern, async (route) => {
    const i = Math.min(rec.count, seq.length - 1);
    rec.count++;
    rec.urls.push(route.request().url());
    const e = seq[i];
    if (e.gate) await e.gate;
    await route.fulfill(e.fulfill);
  });
  return rec;
}

// Profile stub: one canned leg per consecutive point pair (cycling flat, ridge).
async function stubProfile(page, kinds) {
  const rec = { count: 0, urls: [] };
  await page.route('**/api/planner/profile*', async (route) => {
    rec.count++;
    const url = route.request().url();
    rec.urls.push(url);
    const n = new URL(url).searchParams.get('pts').split(',').length / 2;
    const legs = [];
    for (let i = 0; i < n - 1; i++) {
      const kind = (kinds || ['flat', 'ridge'])[i % (kinds || ['flat', 'ridge']).length];
      legs.push({ distance_km: 20 + i, elev_m: kind === 'ridge' ? ridge() : flat() });
    }
    await route.fulfill(json(200, { source: 'e2e-stub', legs }));
  });
  return rec;
}

function link(verdict, clearance, km, elev) {
  return { distance_km: km, clearance_pct: clearance, verdict, worst_km: km / 2, obstruction_m: clearance < 0 ? 400 : 0, elev_m: elev };
}

function relayBody() {
  const opt = (kind, name, lat, lon, a, b) => ({
    kind, pubkey: kind === 'existing' ? 'ab'.repeat(32) : undefined, name, lat, lon, elev_m: 300, antenna_m: kind === 'existing' ? 5 : 10,
    leg_a: a, leg_b: b, bottleneck_pct: Math.min(a.clearance_pct, b.clearance_pct),
  });
  const good = (km) => link('clear', 120, km, flat());
  return {
    source: 'e2e-stub',
    direct: link('blocked', -999, 41.3, ridge()),
    existing: [
      opt('existing', XSS, 37.8, -122.2, good(20), good(20)),
      opt('existing', 'Hilltop Relay', 37.75, -122.15, good(22), good(18)),
    ],
    open: [
      opt('open', '', 37.78, -122.18, good(21), good(19)),
      opt('open', '', 37.76, -122.12, good(23), good(17)),
    ],
    params: {}, warnings: ['terrain missing for 1 of 4 tiles (read as sea level)'],
  };
}

function areaBody() {
  const names = ['Alpha', 'Bravo', 'Charlie', 'Delta', 'Echo', 'Foxtrot', XSS, 'Golf'];
  const nodes = names.map((name, i) => ({
    pubkey: String(i).repeat(64).slice(0, 64), name, role: 'repeater',
    lat: 30.1 + (i % 4) * 0.1, lon: -98.1 + Math.floor(i / 4) * 0.4 + (i >= 6 ? 0.2 : 0),
    stale: false, in_area: true, component: i >= 6 ? 1 : 0,
  }));
  const sum = (c, l, iso, cut) => ({ repeaters: 8, components: c, largest_component: l, isolated: iso, cut_vertices: cut, stale_excluded: 0 });
  return {
    area: { s: 30, w: -98.2, n: 30.6, e: -97.6 },
    params: { sites: 3, range_km: 30, hr: 10, he: 5, baseline: 'predicted', stale: false },
    source: 'e2e-stub', status: 'improved',
    baseline: sum(2, 6, 0, 1), final: sum(1, 8, 0, 1),
    nodes,
    edges: [{ a: 0, b: 1, class: 'solid', distance_km: 10, clearance_pct: 90 }, { a: 6, b: 7, class: 'marginal', distance_km: 9, clearance_pct: 50 },
      { a: 1, b: 2, class: 'marginal', distance_km: 8, clearance_pct: 40 }, { a: 2, b: 3, class: 'marginal', distance_km: 8, clearance_pct: 30 }],
    sites: [{
      step: 1, lat: 30.3, lon: -97.9, elev_m: 250, antenna_m: 10, reason: 'joins',
      gain: { components_joined: 1, cut_vertices_removed: 0, repeaters_reached: 2 },
      after: sum(1, 8, 0, 1),
      links: [
        { node: 2, distance_km: 12, clearance_pct: 80, class: 'solid' },
        { node: 6, distance_km: 14, clearance_pct: 70, class: 'solid' },
      ],
      joins_components: [1],
    }],
    warnings: [], truncated: false, computed_at: new Date().toISOString(),
  };
}

const job202 = (gate) => ({ gate, fulfill: json(202, { state: 'loading_terrain', progress: 0.4, retry_after_s: 1 }) });

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });
  const ctx = await browser.newContext({ viewport: { width: 1400, height: 900 } });
  const errors = [];
  const dialogs = [];

  // A fresh page per scenario: the planner keeps module-level caches and a
  // hash-only navigation would not reload them.
  async function newPage(withConfig) {
    const page = await ctx.newPage();
    page.setDefaultTimeout(15000);
    page.on('pageerror', (e) => errors.push(e));
    page.on('dialog', async (d) => { dialogs.push(d.message()); await d.dismiss(); });
    if (withConfig) await stubConfig(page);
    return page;
  }
  async function openPlanner(page, query) {
    await page.goto(BASE + '/#/tools/planner' + (query || ''), { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('.mc-pl-page', { timeout: 15000 });
    await page.waitForSelector('.leaflet-container', { timeout: 15000 });
  }

  console.log(`\n=== planner E2E against ${BASE} ===`);

  // Real fixture coordinates for the chain / relay points.
  const nr = await ctx.request.get(BASE + '/api/nodes?limit=2000');
  const nodes = ((await nr.json()).nodes || []).filter(n => (n.role === 'repeater' || n.role === 'room') && typeof n.lat === 'number' && typeof n.lon === 'number' && n.lat && n.lon);
  assert(nodes.length >= 3, 'fixture needs 3 positioned repeaters');
  const P = nodes.slice(0, 3).map(n => [n.lat, n.lon]);
  const pts = P.map(p => p.join(',')).join(',');

  // ── Disabled ──
  await step('no planner config: page shows the disabled message, Tools has no planner card', async () => {
    const page = await newPage(false);
    await page.goto(BASE + '/#/tools/planner', { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('.mc-pl-off-card');
    const t = await page.locator('.mc-pl-off-card').textContent();
    assert(/not enabled/i.test(t), 'text: ' + t);
    assert(await page.locator('.mc-pl-map').count() === 0, 'map should not mount');
    await page.goto(BASE + '/#/tools', { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('.tools-card');
    assert(await page.locator('a[href="#/tools/planner"]').count() === 0, 'planner card should be absent');
    await page.close();
  });

  // ── Tools card ──
  await step('planner config: Tools landing shows a card linking to #/tools/planner', async () => {
    const page = await newPage(true);
    await page.goto(BASE + '/#/tools', { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('a.tools-card[href="#/tools/planner"]');
    const t = await page.locator('a.tools-card[href="#/tools/planner"]').textContent();
    assert(/Repeater Planner/.test(t), 'card text: ' + t);
    await page.close();
  });

  // ── Chain ──
  {
    const page = await newPage(true);
    const rec = await stubProfile(page);
    await openPlanner(page, '?mode=chain&pts=' + pts);
    await page.waitForSelector('[data-leg-open="1"].mc-los-tone-bad');
    await page.waitForSelector('.mc-los-obstruction', { state: 'attached' });

    await step('chain: exactly 1 profile request carrying the 3 points', async () => {
      assert(rec.count === 1, 'expected 1 request, got ' + rec.count);
      const nums = new URL(rec.urls[0]).searchParams.get('pts').split(',').map(Number);
      assert(nums.length === 6, 'expected 6 numbers, got ' + nums.length);
    });

    await step('chain: 2 leg chips, good then bad', async () => {
      const chips = await page.evaluate(() => [...document.querySelectorAll('[data-leg-open]')].map(b => ({ text: b.textContent.trim(), cls: b.className })));
      assert(chips.length === 2, 'expected 2 chips, got ' + chips.length);
      assert(/mc-los-tone-good/.test(chips[0].cls) && /clear/i.test(chips[0].text), 'chip 1: ' + JSON.stringify(chips[0]));
      assert(/mc-los-tone-bad/.test(chips[1].cls) && /blocked/i.test(chips[1].text), 'chip 2: ' + JSON.stringify(chips[1]));
    });

    await step('chain: exactly 1 warning marker on the map', async () => {
      const n = await page.locator('.mc-los-obstruction').count();
      assert(n === 1, 'expected 1 marker, got ' + n);
    });

    await step('chain: blocked chip opens a panel with an SVG; Escape closes it', async () => {
      await page.locator('[data-leg-open="1"]').click();
      await page.waitForSelector('.mc-los-panel svg.mc-los-svg');
      assert(/Terrain rises/.test(await page.locator('.mc-los-panel .mc-los-obst').textContent()), 'obstruction text missing');
      await page.keyboard.press('Escape');
      await page.waitForFunction(() => !document.querySelector('.mc-los-panel'));
    });

    await step('chain: editing an antenna re-analyses with no new request', async () => {
      const title = () => page.locator('[data-leg-open="1"]').getAttribute('title');
      const before = await title();
      await page.locator('input[data-h="1"]').fill('60');
      await page.waitForFunction((b) => document.querySelector('[data-leg-open="1"]').title !== b, before);
      assert(rec.count === 1, 'profile requests: ' + rec.count);
      assert(/mc-los-tone-/.test(await page.locator('[data-leg-open="1"]').getAttribute('class')), 'chip lost its verdict');
    });
    await page.close();
  }

  // ── Relay ──
  {
    const page = await newPage(true);
    let open;
    const gate = new Promise((r) => { open = r; });
    const rec = await stubJob(page, '**/api/planner/relay*', [job202(), { gate, fulfill: json(200, relayBody()) }]);
    const prof = await stubProfile(page);
    await openPlanner(page, '?mode=relay&a=' + P[0].join(',') + '&b=' + P[1].join(','));

    await step('relay: progress UI shows while the job runs (202 then 200)', async () => {
      try {
        // The 2nd poll is held on the gate, so the 202 state stays observable.
        await page.waitForFunction(() => /terrain/i.test((document.querySelector('[data-status="relay"] .mc-pl-progress-text') || {}).textContent || ''));
        assert(await page.locator('[data-status="relay"] [role="progressbar"]').count() === 1, 'progress bar');
      } finally { open(); }
      await page.waitForSelector('.mc-pl-opts');
      assert(rec.count === 2, 'relay requests: ' + rec.count);
    });

    await step('relay: direct card, 2 existing and 2 open options render', async () => {
      assert(/Terrain blocks/.test(await page.locator('.mc-pl-direct .mc-pl-note').textContent()), 'direct note');
      const sub = await page.locator('.mc-pl-direct .mc-pl-sub').textContent();
      assert(sub.includes('terrain blocks the path') && !sub.includes('999'), 'direct link line: ' + sub);
      const warn = await page.locator('.mc-pl-warnings').textContent();
      assert(warn.includes('terrain missing for 1 of 4 tiles'), 'relay warning: ' + warn);
      assert(await page.locator('[data-opt^="e"]').count() === 2, 'existing options');
      assert(await page.locator('[data-opt^="o"]').count() === 2, 'open options');
      assert(await page.locator('.mc-pl-cand').count() === 4, 'candidate markers');
    });

    await step('relay: selecting an option draws the route, opens two profiles, writes sel= to the URL', async () => {
      await page.locator('[data-opt="o1"]').click();
      await page.waitForSelector('.mc-los-panel .mc-pl-prof');
      assert(await page.locator('.mc-los-panel .mc-pl-prof').count() === 2, 'expected 2 profiles');
      assert(await page.locator('path.mc-pl-leg:not(.mc-pl-direct)').count() === 2, 'expected 2 route legs');
      assert(/[?&]sel=o1(&|$)/.test(await page.evaluate(() => location.hash)), 'hash: ' + await page.evaluate(() => location.hash));
      assert(prof.count === 0, 'profiles come from the relay result, not a request');
    });

    await step('relay XSS: a hostile node name renders as text', async () => {
      const names = await page.locator('[data-opt="e1"] .mc-pl-name').textContent();
      assert(names === XSS, 'name text: ' + names);
      assert(await page.locator('img[src="x"]').count() === 0, 'injected <img> found');
    });
    await page.close();
  }

  // ── Area ──
  {
    const page = await newPage(true);
    const cov = await stubJob(page, '**/api/planner/coverage*', [job202(), { fulfill: json(200, areaBody()) }]);
    const prof = await stubProfile(page);
    await openPlanner(page, '?mode=area&bbox=30.0,-98.2,30.6,-97.6');

    await step('area: progress shown, then a summary table with clusters 2 -> 1', async () => {
      await page.waitForSelector('.mc-pa-summary');
      assert(cov.count === 2, 'coverage requests: ' + cov.count);
      const row = await page.evaluate(() => {
        const r = [...document.querySelectorAll('.mc-pa-summary tbody tr')].find(tr => /^Clusters$/.test(tr.querySelector('th').textContent));
        return r ? [...r.querySelectorAll('td')].map(td => td.textContent.trim()) : null;
      });
      assert(row && row[0] === '2' && row[1] === '1', 'Clusters row: ' + JSON.stringify(row));
      assert(/improve/i.test(await page.locator('.mc-pa-banner').textContent()), 'status banner');
    });

    await step('area: site card explains the join; numbered site marker on the map', async () => {
      assert(await page.locator('.mc-pa-card').count() === 1, 'expected 1 site card');
      const reason = await page.locator('.mc-pa-reason').textContent();
      assert(/Joins/.test(reason), 'reason: ' + reason);
      assert(await page.locator('.leaflet-marker-icon.mc-pa-site').count() === 1, 'site marker');
      assert((await page.locator('.mc-pa-site span').textContent()).trim() === '1', 'site marker number');
    });

    await step('area: marginal links hidden by default, shown after toggling, marginal=1 in the hash', async () => {
      const edges = () => page.locator('path.mc-pa-edge:not(.mc-pa-edge-marginal)').count();
      const marg = () => page.locator('path.mc-pa-edge-marginal').count();
      assert(await marg() === 0 && await edges() === 1, 'default edges: ' + await edges() + ' marginal: ' + await marg());
      assert(await page.locator('[data-legend="marginal"]').isHidden(), 'legend marginal row hidden');
      const before = cov.count;
      await page.locator('[data-f="marginal"]').check();
      assert(await marg() === 3, 'marginal edges shown: ' + await marg());
      assert(await page.locator('[data-legend="marginal"]').isVisible(), 'legend marginal row visible');
      assert(/[?&]marginal=1(&|$)/.test(await page.evaluate(() => location.hash)), 'hash: ' + await page.evaluate(() => location.hash));
      assert(cov.count === before, 'toggle must not refetch');
      await page.locator('[data-f="marginal"]').uncheck();
      assert(await marg() === 0, 'hidden again');
    });

    await step('area: Show profiles triggers a profile request and opens the panel', async () => {
      await page.locator('[data-act="profiles"][data-step="1"]').click();
      await page.waitForSelector('.mc-los-panel svg.mc-los-svg');
      assert(prof.count >= 1, 'profile requests: ' + prof.count);
    });

    await step('area XSS: a hostile node name renders as text', async () => {
      const links = await page.locator('.mc-pa-links').textContent();
      assert(links.includes(XSS), 'links text: ' + links);
      assert(await page.locator('img[src="x"]').count() === 0, 'injected <img> found');
    });
    await page.close();
  }

  // ── Errors ──
  await step('area error: 400 no_nodes_in_area shows a friendly message', async () => {
    const page = await newPage(true);
    await stubJob(page, '**/api/planner/coverage*', [{ fulfill: json(400, { error: 'no_nodes_in_area' }) }]);
    await openPlanner(page, '?mode=area&bbox=30.0,-98.2,30.6,-97.6');
    await page.waitForFunction(() => { const e = document.querySelector('[data-f="error"]'); return e && !e.hidden && e.textContent; });
    const t = await page.locator('[data-f="error"]').textContent();
    assert(/No known repeaters/.test(t), 'error text: ' + t);
    assert(!/no_nodes_in_area/.test(t), 'raw error code leaked: ' + t);
    await page.close();
  });

  await step('area error: 429 with Retry-After says to try again', async () => {
    const page = await newPage(true);
    await stubJob(page, '**/api/planner/coverage*', [{ fulfill: json(429, { error: 'rate_limited' }, { 'Retry-After': '7' }) }]);
    await openPlanner(page, '?mode=area&bbox=30.0,-98.2,30.6,-97.6');
    await page.waitForFunction(() => { const e = document.querySelector('[data-f="error"]'); return e && !e.hidden && e.textContent; });
    const t = await page.locator('[data-f="error"]').textContent();
    assert(/try again/i.test(t) && /7/.test(t), 'error text: ' + t);
    await page.close();
  });

  // ── Cross-cutting ──
  await step('no dialog fired (no XSS execution)', async () => {
    assert(dialogs.length === 0, 'dialogs: ' + JSON.stringify(dialogs));
  });

  await step('no pageerror from planner*.js', async () => {
    errors.forEach(e => console.log('    [pageerror]', e.message));
    const ours = errors.filter(e => /planner[\w-]*\.js/.test(e.stack || '') || /MeshPlanner/.test(e.message));
    assert(ours.length === 0, ours.map(e => e.message).join('; '));
  });

  await browser.close();
  console.log(`\n${passed} passed, ${failed} failed`);
  process.exit(failed ? 1 : 0);
})().catch((e) => { console.error(e); process.exit(1); });

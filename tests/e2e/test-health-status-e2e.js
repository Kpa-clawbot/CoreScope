/**
 * E2E test: the favorites dropdown, the "My Nodes" cards and the home health
 * card judge a node by its role (repeater and room: degraded after 24 h,
 * silent after 72 h; other roles: 1 h and 24 h), the same as My Repeaters.
 *
 * /api/nodes/<pk>/health is intercepted with page.route for five synthetic
 * nodes, so the ages are exact regardless of the fixture's dates. One repeater
 * has no packet in the store window, only the DB's last_seen, which these
 * views fall back to like My Repeaters does.
 *
 * Usage: BASE_URL=http://localhost:13581 node test-health-status-e2e.js
 */
const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:3000';
const H = 3600 * 1000;

async function test(name, fn) {
  try {
    await fn();
    console.log(`  ✅ ${name}`);
  } catch (err) {
    console.log(`  ❌ ${name}: ${err.message}`);
    process.exit(1);
  }
}

function assert(cond, msg) {
  if (!cond) throw new Error(msg || 'Assertion failed');
}

const pk = (c) => c.repeat(64);
const ago = (ms) => new Date(Date.now() - ms).toISOString();
// want: the status each view must show. On the role-blind code the two
// repeaters at 5 h and 30 h showed degraded and silent.
const NODES = [
  { pk: pk('a'), name: 'E2E Rptr 5h', role: 'repeater', lastHeard: ago(5 * H), want: 'healthy' },
  { pk: pk('b'), name: 'E2E Rptr 30h', role: 'repeater', lastHeard: ago(30 * H), want: 'degraded' },
  { pk: pk('c'), name: 'E2E Room 80h', role: 'room', lastHeard: ago(80 * H), want: 'silent' },
  { pk: pk('d'), name: 'E2E Comp 30h', role: 'companion', lastHeard: ago(30 * H), want: 'silent' },
  { pk: pk('e'), name: 'E2E Rptr DB 30h', role: 'repeater', lastHeard: null, lastSeen: ago(30 * H), want: 'degraded' },
  // Never heard at all: silent on the cards, "unknown" in the dropdown.
  { pk: pk('f'), name: 'E2E Sensor never', role: 'sensor', lastHeard: null, want: 'silent', favTitle: 'unknown' },
];
const byPk = Object.fromEntries(NODES.map((n) => [n.pk, n]));

function healthFor(n) {
  return {
    node: { public_key: n.pk, name: n.name, role: n.role, last_seen: n.lastSeen || n.lastHeard },
    stats: { lastHeard: n.lastHeard, packetsToday: 0, avgSnr: null, avgHops: null },
    observers: [],
    recentPackets: [],
  };
}

async function run() {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage']
  });
  const ctx = await browser.newContext({ viewport: { width: 1400, height: 900 } });
  await ctx.addInitScript((nodes) => {
    localStorage.setItem('meshcore-user-level', 'experienced');
    localStorage.setItem('meshcore-my-nodes', JSON.stringify(nodes.map((n) => ({ pubkey: n.pk, name: n.name, addedAt: 0 }))));
    localStorage.setItem('meshcore-favorites', JSON.stringify(nodes.map((n) => n.pk)));
  }, NODES);
  const page = await ctx.newPage();
  page.setDefaultTimeout(15000);
  const pageErrors = [];
  page.on('pageerror', (e) => pageErrors.push(e.message));

  const served = new Set();
  await page.route(/\/api\/nodes\/([0-9a-f]{64})\/health/, (route) => {
    const n = byPk[route.request().url().match(/\/api\/nodes\/([0-9a-f]{64})\/health/)[1]];
    if (!n) return route.continue();
    served.add(n.pk);
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(healthFor(n)) });
  });

  console.log(`\nRunning role-aware health status E2E tests against ${BASE}\n`);

  await page.goto(`${BASE}/#/home`, { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('#myNodesGrid .my-node-card');

  await test('every mocked node was fetched from /api/nodes/<pk>/health', async () => {
    await page.waitForFunction((n) => document.querySelectorAll('#myNodesGrid .my-node-card').length === n, NODES.length);
    for (const n of NODES) assert(served.has(n.pk), `health for ${n.name} was not requested`);
  });

  const TEXT = { healthy: 'Active', degraded: 'Degraded', silent: 'Silent' };
  await test('"My Nodes" cards show each node\'s status by its role', async () => {
    for (const n of NODES) {
      const card = page.locator(`#myNodesGrid .my-node-card[data-key="${n.pk}"]`);
      const cls = await card.getAttribute('class');
      const text = (await card.locator('.mnc-status-text').innerText()).trim();
      assert(cls.split(/\s+/).includes(n.want), `${n.name}: card class "${cls}", want ${n.want}`);
      assert(text.startsWith(TEXT[n.want]), `${n.name}: status text "${text}", want ${TEXT[n.want]}`);
    }
  });

  await test('the DB-only repeater shows how long ago it was heard, not nothing', async () => {
    const text = await page.locator(`#myNodesGrid .my-node-card[data-key="${pk('e')}"] .mnc-status-text`).innerText();
    assert(/·/.test(text), `status text "${text}" should include the time since last_seen`);
  });

  const BANNER = { healthy: 'green', degraded: 'yellow', silent: 'red' };
  await test('the home health card agrees with the "My Nodes" card', async () => {
    for (const n of NODES) {
      await page.locator(`#myNodesGrid .my-node-card[data-key="${n.pk}"] .mnc-name`).click();
      const banner = page.locator('#homeHealth .health-banner');
      await page.waitForFunction((name) => {
        const b = document.querySelector('#homeHealth .health-banner');
        return b && b.textContent.includes(name);
      }, n.name);
      const cls = await banner.getAttribute('class');
      const label = await banner.locator('[aria-label]').first().getAttribute('aria-label');
      assert(cls.split(/\s+/).includes(BANNER[n.want]), `${n.name}: banner class "${cls}", want ${BANNER[n.want]}`);
      assert(label === n.want, `${n.name}: banner aria-label "${label}", want ${n.want}`);
      const msg = (n.lastHeard || n.lastSeen) ? /Last heard/ : /No packets in the current window/;
      assert(msg.test(await banner.innerText()), `${n.name}: banner text should match ${msg}`);
    }
  });

  const DOT = { healthy: 'status-ok', degraded: 'status-warn', silent: 'status-err' };
  await test('the favorites dropdown agrees as well', async () => {
    await page.click('#favToggle');
    await page.waitForSelector(`#favDropdown .fav-dd-item[data-key="${pk('e')}"] .fav-dd-status`);
    for (const n of NODES) {
      const dot = page.locator(`#favDropdown .fav-dd-item[data-key="${n.pk}"] .fav-dd-status`);
      const cls = await dot.getAttribute('class');
      const title = await dot.getAttribute('title');
      assert(cls.split(/\s+/).includes(DOT[n.want]), `${n.name}: dot class "${cls}", want ${DOT[n.want]}`);
      assert(title === (n.favTitle || n.want), `${n.name}: dot title "${title}", want ${n.favTitle || n.want}`);
    }
    const meta = await page.locator(`#favDropdown .fav-dd-item[data-key="${pk('e')}"] .fav-dd-meta`).innerText();
    assert(meta.trim() !== 'never', 'DB-only repeater should show the time since last_seen, not "never"');
  });

  await test('no page errors', async () => {
    assert(pageErrors.length === 0, 'page errors: ' + pageErrors.join(' | '));
  });
  await ctx.close();

  // The analytics network status and the live node panel use the same helper
  // with their own timestamps (last_heard || last_seen, and last_seen).
  const listNode = (n) => ({ public_key: n.pk, name: n.name, role: n.role, last_heard: n.lastHeard, last_seen: n.lastSeen || n.lastHeard });
  const nodeListUrl = /\/api\/nodes(?:\?.*)?$/;

  await test('analytics status and role counts follow the selected nodes (#2213)', async () => {
    const actx = await browser.newContext({ viewport: { width: 1400, height: 900 } });
    const p = await actx.newPage();
    const errs = [];
    p.on('pageerror', (e) => errs.push(e.message));
    await p.route(nodeListUrl, (route) => {
      const params = new URL(route.request().url()).searchParams;
      let nodes = params.get('region') === 'SJC' ? NODES.slice(0, 3) : NODES;
      if (params.get('area') === 'rooms') nodes = nodes.filter(n => n.role === 'room');
      if (params.get('area') === 'empty') nodes = [];
      return route.fulfill({ json: {
        nodes: nodes.map(listNode), total: nodes.length, has_more: false,
        counts: { repeater: 9000, companion: 8000, room: 7000, sensor: 6000, observer: 5000, unknown: 4000 },
      } });
    });
    await p.route(/\/api\/nodes\/bulk-health/, (route) => route.fulfill({ json: [] }));
    await p.route('**/api/config/regions', (route) => route.fulfill({ json: { SJC: 'Test Region' } }));
    await p.route('**/api/config/areas', (route) => route.fulfill({ json: [
      { key: 'rooms', label: 'Rooms' }, { key: 'empty', label: 'Empty' },
    ] }));
    await p.goto(`${BASE}/#/analytics?tab=nodes`, { waitUntil: 'domcontentloaded' });
    const cards = p.locator('.analytics-section', { hasText: 'Network Status' }).first().locator('.analytics-stat-card');
    await cards.first().waitFor();
    const count = async (i) => Number((await cards.nth(i).locator('div').first().innerText()).trim());
    const got = [await count(0), await count(1), await count(2)];
    // active: rptr 5h; degraded: rptr 30h, rptr DB 30h; silent: room 80h, comp 30h, sensor never
    assert(JSON.stringify(got) === '[1,2,3]', `active/degraded/silent = ${JSON.stringify(got)}, want [1,2,3]`);
    async function checkRoles(expected, total) {
      await p.waitForFunction(want => {
        const cards = document.querySelectorAll('#analyticsContent .analytics-stat-card');
        return cards.length === 4 && Number(cards[3].firstElementChild.textContent) === want;
      }, total);
      const counts = await p.evaluate(() => {
        const heading = [...document.querySelectorAll('#analyticsContent h3')].find(h => h.textContent.includes('Role Breakdown'));
        return Object.fromEntries([...heading.nextElementSibling.querySelectorAll('.badge')].map(b => {
          const [role, count] = b.textContent.trim().split(': ');
          return [role, Number(count)];
        }));
      });
      const want = { repeater: 0, companion: 0, room: 0, sensor: 0, observer: 0, unknown: 0, ...expected };
      assert(Object.keys(counts).length === Object.keys(want).length &&
        Object.entries(want).every(([role, count]) => counts[role] === count),
      'filtered role counts: ' + JSON.stringify(counts) + ', expected ' + JSON.stringify(want));
    }
    await checkRoles({ repeater: 3, companion: 1, room: 1, sensor: 1 }, 6);
    await p.evaluate(() => window.RegionFilter.setSelected(['SJC']));
    await checkRoles({ repeater: 2, room: 1 }, 3);
    for (const area of ['rooms', 'empty']) {
      await p.locator('#analyticsAreaFilter .region-dropdown-trigger').click();
      await p.locator(`#analyticsAreaFilter [data-area="${area}"]`).click();
      await checkRoles(area === 'rooms' ? { room: 1 } : {}, area === 'rooms' ? 1 : 0);
    }
    assert(errs.length === 0, 'page errors: ' + errs.join(' | '));
    await actx.close();
  });

  const LIVE = { healthy: ['health-green', 'Online'], degraded: ['health-yellow', 'Degraded'], silent: ['health-red', 'Offline'] };
  await test('live node panel shows each node\'s status by its role', async () => {
    for (const n of [byPk[pk('a')], byPk[pk('e')], byPk[pk('d')]]) {
      const lctx = await browser.newContext({ viewport: { width: 1400, height: 900 } });
      const p = await lctx.newPage();
      const node = listNode(n);
      await p.route(nodeListUrl, (route) => route.fulfill({ json: { nodes: [{ ...node, lat: 1, lon: 1 }], total: 1 } }));
      await p.route('**/api/nodes/' + n.pk, (route) => route.fulfill({ json: { node, recentAdverts: [] } }));
      await p.route('**/api/nodes/' + n.pk + '/health', (route) => route.fulfill({ json: healthFor(n) }));
      // live.js re-renders the panel on the first 'theme-refresh'; assert after it.
      await p.addInitScript(() => {
        window.addEventListener('theme-refresh', () => { window.__themeReady = true; }, { once: true });
      });
      await p.goto(`${BASE}/#/live?lat=1&lon=1&zoom=10`, { waitUntil: 'domcontentloaded' });
      await p.locator('#liveMap .live-node-marker').first().click();
      await p.waitForFunction(() => window.__themeReady === true, null, { timeout: 8000 });
      await p.waitForFunction((name) => {
        const c = document.getElementById('nodeDetailContent');
        return c && c.textContent.includes(name) && /health-(green|yellow|red)/.test(c.innerHTML);
      }, n.name);
      const [dotCls, label] = LIVE[n.want];
      const dot = await p.locator('#nodeDetailContent [class^="health-"]').first().getAttribute('class');
      assert(dot === dotCls, `${n.name}: dot "${dot}", want ${dotCls}`);
      const labels = await p.locator('#nodeDetailContent span').filter({ hasText: new RegExp(`^${label}$`) }).count();
      assert(labels >= 1, `${n.name}: no "${label}" label in the live panel`);
      await lctx.close();
    }
  });

  await browser.close();
  console.log('\nAll role-aware health status E2E tests passed.');
}

run().catch((e) => { console.error(e); process.exit(1); });

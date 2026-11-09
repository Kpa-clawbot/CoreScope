/**
 * E2E: the anonymous static build, served from one origin, against a stock
 * CoreScope server on another that lists it in CORS_ALLOWED_ORIGINS. No
 * server code is involved beyond that env var.
 *
 * Checks, in a real browser with real CORS:
 *   - every top-level page renders, with no page error and no /api request
 *     left on the static origin;
 *   - no API request carries the API host's cookies (the embed tier never
 *     allows credentials);
 *   - the WebSocket connects to the API host's /ws;
 *   - the three JSON POSTs go as text/plain, with no preflight;
 *   - the badge is shown, and account and admin point to classic;
 *   - a blocked request and an HTML answer each show their notice.
 *
 * Setup (what CI does):
 *   CHANNEL=dev COMMIT=e2e API_BASE=http://localhost:13591 AUTH=false \
 *     CLASSIC_URL=http://localhost:13591 bash scripts/static/build.sh
 *   CORS_ALLOWED_ORIGINS=http://localhost:8788 ./corescope-server -port 13591 \
 *     -db test-fixtures/e2e-fixture.db -public /nonexistent &
 *   node scripts/dev-static.mjs --root dist/web --port 8788 &
 *
 * Usage: SPLIT_URL=http://localhost:8788 API_URL=http://localhost:13591 node test-split-anonymous-e2e.js
 */
const { chromium } = require('playwright');

const SPLIT = process.env.SPLIT_URL || 'http://localhost:8788';
const API = process.env.API_URL || 'http://localhost:13591';
const PAGES = ['#/home', '#/packets', '#/live', '#/map', '#/channels', '#/nodes', '#/analytics', '#/observers', '#/tools', '#/perf', '#/compare'];

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

async function run() {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });
  const context = await browser.newContext({ viewport: { width: 1400, height: 900 } });
  // A session cookie for the API host, as a visitor logged in on classic has.
  // The anonymous build must never send it.
  await context.addCookies([{ name: 'cs_session', value: 'e2e-classic-session', url: API }]);
  const page = await context.newPage();

  const errors = [];
  const apiRequests = [];
  const sockets = [];
  page.on('pageerror', (e) => errors.push(e.message));
  page.on('request', (r) => { if (r.url().includes('/api/')) apiRequests.push(r); });
  page.on('websocket', (ws) => sockets.push(ws.url()));

  console.log(`Split anonymous E2E: static ${SPLIT}, API ${API}`);

  await test('every top-level page renders from the static origin', async () => {
    for (const hash of PAGES) {
      await page.goto(SPLIT + '/' + hash, { waitUntil: 'domcontentloaded' });
      await page.waitForFunction(() => {
        const app = document.getElementById('app');
        return app && app.innerText.trim().length > 20;
      }, null, { timeout: 15000 });
      await page.waitForTimeout(800);
    }
    assert(errors.length === 0, 'page errors: ' + errors.join(' | '));
  });

  await test('every API request goes to the API origin', async () => {
    const stray = apiRequests.filter((r) => r.url().startsWith(SPLIT + '/api/'));
    assert(stray.length === 0, 'requests left on the static origin: ' + stray.map((r) => r.url()).join(', '));
    const toApi = apiRequests.filter((r) => r.url().startsWith(API + '/api/'));
    assert(toApi.length >= 10, 'expected the pages to call the API, saw ' + toApi.length);
  });

  await test('no API request carries the API host\'s cookies', async () => {
    const withCookie = [];
    for (const r of apiRequests) {
      const h = await r.allHeaders();
      if (h.cookie) withCookie.push(r.url());
    }
    assert(withCookie.length === 0, 'cookies sent to: ' + withCookie.slice(0, 5).join(', '));
  });

  await test('the WebSocket connects to the API host\'s /ws', async () => {
    const wsBase = API.replace(/^http/, 'ws');
    assert(sockets.length > 0, 'no WebSocket opened');
    assert(sockets.every((u) => u === wsBase + '/ws'), 'unexpected WebSocket URLs: ' + sockets.join(', '));
    await page.goto(SPLIT + '/#/live', { waitUntil: 'domcontentloaded' });
    await page.waitForFunction(() => typeof ws !== 'undefined' && ws && ws.readyState === 1, null, { timeout: 10000 });
  });

  await test('the three JSON POSTs go as text/plain with no preflight, and succeed', async () => {
    const seen = [];
    const onReq = (r) => { if (r.url().startsWith(API + '/api/')) seen.push({ method: r.method(), url: r.url(), type: r.headers()['content-type'] }); };
    page.on('request', onReq);
    const result = await page.evaluate(async () => {
      const pk = await (await CSApi.fetch('/api/packets?limit=2')).json();
      const hashes = (pk.packets || []).map((p) => p.hash).filter(Boolean);
      const a = await CSApi.postJSON('/api/packets/observations', { hashes });
      const b = await CSApi.postJSON('/api/decode', { hex: '1100' });
      const c = await CSApi.postJSON('/api/paths/inspect', { prefixes: ['aa', 'bb'] });
      return { obs: [a.status, Object.keys(await a.json())], decode: [b.status, Object.keys(await b.json())], inspect: [c.status] };
    });
    page.off('request', onReq);
    assert(result.obs[0] === 200 && result.obs[1].includes('results'), 'observations: ' + JSON.stringify(result.obs));
    assert(result.decode[0] === 200 && result.decode[1].includes('decoded'), 'decode: ' + JSON.stringify(result.decode));
    assert(result.inspect[0] === 200 || result.inspect[0] === 503, 'inspect: ' + JSON.stringify(result.inspect));
    const posts = seen.filter((s) => s.method === 'POST');
    assert(posts.length === 3, 'expected 3 POSTs, saw ' + JSON.stringify(seen));
    assert(posts.every((p) => /^text\/plain/.test(p.type)), 'POST content types: ' + posts.map((p) => p.type).join(', '));
    assert(!seen.some((s) => s.method === 'OPTIONS'), 'a preflight was sent');
  });

  await test('the channel badge is shown and opens a pre-filled issue', async () => {
    await page.goto(SPLIT + '/#/nodes', { waitUntil: 'domcontentloaded' });
    const badge = page.locator('#csChannelBadge');
    await badge.waitFor({ timeout: 5000 });
    const href = await badge.getAttribute('href');
    const body = new URL(href).searchParams.get('body') || '';
    assert(/\/issues\/new\?title=/.test(href) && body.includes('**Page:** #/nodes') && body.includes('Channel: dev'), 'badge href: ' + href);
    const classic = await page.locator('#csClassicLink').getAttribute('href');
    assert(classic === API + '/#/nodes', 'classic link: ' + classic);
  });

  await test('account and admin point to classic and offer no login', async () => {
    for (const [hash, text] of [['#/account/login', 'Logging in is available on the classic site.'], ['#/admin', 'Administration is available on the classic site.']]) {
      await page.goto(SPLIT + '/' + hash, { waitUntil: 'domcontentloaded' });
      await page.waitForFunction((t) => document.getElementById('app').innerText.includes(t), text, { timeout: 5000 });
      assert(await page.locator('#app input[type=password]').count() === 0, hash + ' shows a password field');
    }
  });

  await test('account tokens stay out of issue reports and remain in classic transfer links', async () => {
    for (const view of ['activate', 'reset', 'confirm-email', 'unsubscribe']) {
      const route = '#/account/' + view;
      const hash = route + '?token=e2e-private-link-token';
      await page.goto(SPLIT + '/' + hash, { waitUntil: 'domcontentloaded' });
      const classic = page.locator('#app .cs-classic-link');
      await classic.waitFor({ timeout: 5000 });
      const report = new URL(await page.locator('#csChannelBadge').getAttribute('href')).searchParams.get('body');
      assert(report.startsWith('**Page:** ' + route + '\n'), 'report should retain only the account route: ' + report);
      assert(!report.includes('e2e-private-link-token'), 'account token leaked into issue report');
      assert(await page.locator('#csClassicLink').getAttribute('href') === API + '/' + hash, 'nav transfer lost token');
      assert(await classic.getAttribute('href') === API + '/' + hash, 'account transfer lost token');
    }
  });

  await test('a blocked API request shows "Can\'t reach the CoreScope API"', async () => {
    const p = await context.newPage();
    await p.route(API + '/api/stats', (route) => route.abort('failed'));
    await p.goto(SPLIT + '/#/home', { waitUntil: 'domcontentloaded' });
    await p.waitForFunction(() => {
      const t = document.getElementById('csAuthToast');
      return t && t.textContent === "Can't reach the CoreScope API";
    }, null, { timeout: 20000 });
    await p.close();
  });

  for (const status of [200, 404]) await test('an HTML ' + status + ' answer shows "Server returned an unexpected response"', async () => {
    const p = await context.newPage();
    await p.route(API + '/api/stats', (route) => route.fulfill({ status, contentType: 'text/html', body: '<!doctype html><p>static host page</p>',
      headers: { 'Access-Control-Allow-Origin': SPLIT } }));
    await p.goto(SPLIT + '/#/home', { waitUntil: 'domcontentloaded' });
    await p.waitForFunction(() => {
      const t = document.getElementById('csAuthToast');
      return t && t.textContent === 'Server returned an unexpected response';
    }, null, { timeout: 20000 });
    await p.close();
  });

  if (process.env.SPLIT_SCREENSHOT) {
    await page.goto(SPLIT + '/#/nodes', { waitUntil: 'domcontentloaded' });
    await page.waitForTimeout(1500);
    await page.screenshot({ path: process.env.SPLIT_SCREENSHOT });
  }

  await browser.close();
  console.log('Split anonymous E2E: all passed');
}

run().catch((e) => { console.error(e); process.exit(1); });

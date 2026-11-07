/**
 * E2E (#2218): a Node Analytics response that lands after the user went back
 * to Nodes must not replace the Nodes list.
 *
 * The /api/nodes/:key/analytics request is held with page.route(), the page
 * navigates to #/nodes, and only then is the held request released (as a
 * success, then as an error). The Nodes rows must still be there afterwards.
 * A final step checks that an unheld visit still renders the analytics.
 *
 * Usage: BASE_URL=http://localhost:13581 node tests/e2e/test-issue-2218-node-analytics-stale-e2e.js
 */
'use strict';
const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:3000';
const ANALYTICS_RE = /\/api\/nodes\/[^/]+\/analytics\?/;
const ROWS = 'table tbody tr:not([id^=vscroll])';

let passed = 0, failed = 0;
async function step(name, fn) {
  try { await fn(); passed++; console.log('  ✓ ' + name); }
  catch (e) { failed++; console.error('  ✗ ' + name + ': ' + e.message); }
}
function assert(c, m) { if (!c) throw new Error(m || 'assertion failed'); }

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });
  const page = await (await browser.newContext({ viewport: { width: 1400, height: 900 }, timezoneId: 'America/New_York' })).newPage();
  page.setDefaultTimeout(15000);
  const errors = [];
  page.on('pageerror', (e) => errors.push(e.message));

  console.log(`\n=== #2218 node analytics stale response E2E against ${BASE} ===`);

  const nodes = (await (await page.request.get(`${BASE}/api/nodes?limit=50`)).json()).nodes || [];
  assert(nodes.length > 0, 'fixture has no nodes');

  let held = null;
  let onHeld = () => {};
  await page.route(ANALYTICS_RE, (route) => { held = route; onHeld(); });

  // Opens a node's analytics with its response held, then goes back to Nodes.
  async function leaveWhileHeld(pk) {
    held = null;
    const isHeld = new Promise((resolve) => { onHeld = resolve; });
    await page.goto(`${BASE}/#/nodes/${encodeURIComponent(pk)}/analytics`, { waitUntil: 'domcontentloaded' });
    await isHeld;
    await page.waitForFunction(() => /Loading analytics/.test(document.getElementById('app').textContent));
    await page.evaluate(() => { location.hash = '#/nodes'; });
    await page.waitForSelector('[data-loaded="true"]');
    await page.waitForSelector(ROWS);
    return page.$$eval(ROWS, (rows) => rows.length);
  }
  async function assertNodesIntact(before) {
    // Let the released response reach the page and any render run.
    await page.waitForTimeout(500);
    const hash = await page.evaluate(() => location.hash);
    assert(hash === '#/nodes', 'hash changed to ' + hash);
    const after = await page.$$eval(ROWS, (rows) => rows.length);
    assert(after > 0 && after === before, `Nodes rows ${before} before release, ${after} after`);
    const heading = await page.evaluate(() => /— Analytics/.test(document.getElementById('app').textContent));
    assert(!heading, 'the Analytics heading replaced the Nodes list');
  }

  await step('a late success does not replace the Nodes list', async () => {
    const before = await leaveWhileHeld(nodes[0].public_key);
    const done = page.waitForEvent('requestfinished', (r) => ANALYTICS_RE.test(r.url()));
    await held.continue();
    await done;
    await assertNodesIntact(before);
  });

  await step('a late error does not replace the Nodes list', async () => {
    const pk = (nodes[1] || nodes[0]).public_key;
    const before = await leaveWhileHeld(pk);
    await held.fulfill({ status: 500, contentType: 'application/json', body: '{"error":"held"}' });
    await assertNodesIntact(before);
    assert(!(await page.evaluate(() => /Failed to load analytics/.test(document.getElementById('app').textContent))),
      'the error replaced the Nodes list');
  });

  await step('the current visit still renders its analytics', async () => {
    await page.unroute(ANALYTICS_RE);
    const pk = (nodes[2] || nodes[0]).public_key;
    await page.goto(`${BASE}/#/nodes/${encodeURIComponent(pk)}/analytics`, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#timeRangeBtns');
    assert(await page.evaluate(() => /— Analytics/.test(document.getElementById('app').textContent)), 'no Analytics heading');
  });

  // #1154 U10: retain the actual server response, replacing only peer contacts
  // so the timestamp contract is deterministic even when the fixture has none.
  await page.route(ANALYTICS_RE, async (route) => {
    const response = await route.fetch();
    const data = await response.json();
    data.peerInteractions = ['2024-01-02T03:04:05.123Z', null, '', 'not-a-date'].map((lastContact, i) => ({
      peer_key: ['ab', 'cd', 'ef', '12'][i].repeat(32), peer_name: 'Example Peer ' + (i + 1), messageCount: 4 - i, lastContact,
    }));
    await route.fulfill({ response, json: data });
  });
  for (const width of [1400, 320]) {
    for (const [mode, timezone, format, absolute] of [
      ['ago', 'utc', 'iso', '2024-01-02 03:04:05'],
      ['absolute', 'utc', 'iso-seconds', '2024-01-02 03:04:05.123'],
      ['ago', 'local', 'iso', '2024-01-01 22:04:05'],
    ]) {
      await step(`Last Contact at ${width}px: ${mode}, ${timezone}, ${format}`, async () => {
        await page.setViewportSize({ width, height: 900 });
        await page.evaluate(({ mode, timezone, format }) => {
          localStorage.setItem('meshcore-timestamp-mode', mode);
          localStorage.setItem('meshcore-timestamp-timezone', timezone);
          localStorage.setItem('meshcore-timestamp-format', format);
          localStorage.removeItem('meshcore-timestamp-custom-format');
        }, { mode, timezone, format });
        await page.reload({ waitUntil: 'domcontentloaded' });
        await page.waitForSelector('.analytics-peer-table tbody tr');
        const cells = page.locator('.analytics-peer-table tbody td:nth-child(3)');
        assert(await cells.count() === 4, 'all peer contacts render');
        const contact = cells.first().locator('[title]');
        assert(await contact.count() === 1, 'Last Contact needs a timestamp tooltip');
        await contact.hover();
        const text = await contact.textContent();
        const title = await contact.getAttribute('title');
        assert((mode === 'ago' ? title : text) === absolute, 'absolute timestamp respects timezone and format');
        assert(/^\d+d ago$/.test(mode === 'ago' ? text : title), 'relative timestamp remains available');
        for (let i = 1; i < 4; i++) {
          assert((await cells.nth(i).textContent()).trim() === '—', 'missing or invalid contact stays a dash');
          assert(await cells.nth(i).locator('[title]').getAttribute('title') === '—', 'missing contact has no invented date');
        }
        if (process.env.SCREENSHOT_DIR) {
          const path = require('path');
          require('fs').mkdirSync(process.env.SCREENSHOT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.SCREENSHOT_DIR, `last-contact-${width}-${mode}-${timezone}.png`) });
        }
      });
    }
  }

  // api() reports every failed request as an unhandled "API <status>:"
  // rejection of its in-flight bookkeeping; the held 500 above is one of them.
  await step('no page errors besides failed requests', async () => {
    const other = errors.filter((m) => !/^API \d+: /.test(m));
    assert(other.length === 0, other.join(' | '));
  });

  await browser.close();
  console.log(`\n${passed} passed, ${failed} failed`);
  process.exit(failed > 0 ? 1 : 0);
})().catch((e) => { console.error(e); process.exit(1); });

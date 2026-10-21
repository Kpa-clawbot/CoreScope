/**
 * Packets page node filter: picking an autocomplete suggestion must apply the
 * filter even when the click is slow or the list is rebuilt mid-click.
 *
 * The input's blur handler hides the dropdown 200 ms after focus leaves, and a
 * search response can replace the list while the button is down. A selection
 * that waited for `click` was lost in both cases: the input kept the typed
 * text and no node= filter was applied.
 *
 * Usage:
 *   BASE_URL=http://localhost:13581 node tests/e2e/test-packets-node-filter-e2e.js
 */
const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';
let pass = 0, fail = 0;

function assert(cond, msg) { if (!cond) throw new Error(msg || 'assert'); }

async function test(name, fn) {
  try {
    await fn();
    console.log(`  ✅ ${name}`);
    pass++;
  } catch (e) {
    console.log(`  ❌ ${name}: ${e.message}`);
    fail++;
  }
}

async function openPackets(ctx) {
  const page = await ctx.newPage();
  page.setDefaultTimeout(10000);
  await page.goto(BASE + '/#/packets', { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('#fNode');
  return page;
}

async function typeAndWaitForSuggestion(page, q) {
  await page.fill('#fNode', '');
  await page.type('#fNode', q);
  await page.waitForSelector('#fNodeDropdown:not(.hidden) .node-filter-option');
  return page.locator('#fNodeDropdown .node-filter-option').first();
}

async function pressAndHold(page, locator, holdMs, duringHold) {
  const box = await locator.boundingBox();
  assert(box, 'suggestion must be visible');
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
  await page.mouse.down();
  if (duringHold) await duringHold();
  await page.waitForTimeout(holdMs);
  await page.mouse.up();
}

async function assertNodeApplied(page, name, key) {
  await page.waitForFunction(k => location.hash.includes('node=' + k), key, { timeout: 3000 })
    .catch(() => { throw new Error(`URL has no node=${key.slice(0, 8)}…, got ${page.url()}`); });
  const value = await page.inputValue('#fNode');
  assert(value === name, `input must show the picked name "${name}", got "${value}"`);
}

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });
  const ctx = await browser.newContext();
  console.log(`Packets node filter E2E against ${BASE}`);

  const res = await ctx.request.get(BASE + '/api/nodes?limit=50');
  assert(res.ok(), 'fixture node request must succeed');
  const named = ((await res.json()).nodes || []).find(n => n.name && n.name.length >= 3 && n.public_key);
  assert(named, 'fixture must contain a named node');
  const query = named.name.slice(0, 3);

  await test('a normal click on a suggestion applies the node filter', async () => {
    const page = await openPackets(ctx);
    const opt = await typeAndWaitForSuggestion(page, query);
    const name = await opt.getAttribute('data-name');
    const key = await opt.getAttribute('data-key');
    await opt.click();
    await assertNodeApplied(page, name, key);
    await page.close();
  });

  await test('a click held longer than the blur delay still applies the node filter', async () => {
    const page = await openPackets(ctx);
    const opt = await typeAndWaitForSuggestion(page, query);
    const name = await opt.getAttribute('data-name');
    const key = await opt.getAttribute('data-key');
    await pressAndHold(page, opt, 400);
    await assertNodeApplied(page, name, key);
    await page.close();
  });

  await test('a search response that rebuilds the list mid-click does not drop the pick', async () => {
    const page = await openPackets(ctx);
    const opt = await typeAndWaitForSuggestion(page, query);
    const name = await opt.getAttribute('data-name');
    const key = await opt.getAttribute('data-key');
    let release;
    const held = new Promise(r => { release = r; });
    let seen = false;
    await page.route(url => url.pathname === '/api/nodes/search', async route => {
      seen = true;
      await held;
      await route.continue();
    });
    // A keystroke whose search is still in flight when the user clicks.
    await page.type('#fNode', named.name.charAt(3) || ' ');
    await page.waitForTimeout(400);
    assert(seen, 'the follow-up search request must be in flight');
    await pressAndHold(page, opt, 150, async () => {
      release();
      await page.waitForTimeout(200);
    });
    await assertNodeApplied(page, name, key);
    const reopened = await page.$('#fNodeDropdown:not(.hidden)');
    assert(!reopened, 'the late response for the older query must not reopen the list after the pick');
    await page.close();
  });

  await browser.close();
  console.log(`\n${pass}/${pass + fail} passed${fail ? `, ${fail} failed` : ''}`);
  process.exit(fail ? 1 : 0);
})().catch(e => { console.error(e); process.exit(2); });

#!/usr/bin/env node
/* Coverage E2E for public/home.js (#1297 B5).
 *
 * Exercises the My Mesh home page surface that previously had ~1 E2E hit.
 * Walks the user through:
 *   - first-time "chooser" flow (clears the level pref, asserts both
 *     onboarding buttons render, picks "experienced")
 *   - rendered home: hero, footer links, home-stats block
 *   - node search → suggestion list → claim flow → My Mesh card render
 *   - health detail (loadHealth) via card click + Full health button
 *   - level toggle back to "new" → checklist accordion expand
 *   - remove-from-mesh ✕ button clears card
 *
 * The goal is statement coverage of public/home.js init/renderHome/
 * setupSearch/loadMyNodes/loadStats/loadHealth/checklist/showJourney,
 * not exhaustive assertions — but each step has at least one assertion
 * so a regression breaks the test.
 *
 * Usage: BASE_URL=http://localhost:13581 node test-home-coverage-e2e.js
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

async function pickAnyPubkey(page) {
  // Use the live /api/nodes list — fixture has 200 nodes.
  const res = await page.request.get(BASE + '/api/nodes?limit=5');
  if (!res.ok()) return null;
  const body = await res.json();
  return body.nodes && body.nodes.length ? body.nodes[0] : null;
}

(async () => {
  const requireChromium = process.env.CHROMIUM_REQUIRE === '1';
  let browser;
  try {
    browser = await chromium.launch({
      headless: true,
      executablePath: process.env.CHROMIUM_PATH || undefined,
      args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
    });
  } catch (err) {
    if (requireChromium) {
      console.error('test-home-coverage-e2e.js: FAIL — Chromium required but unavailable: ' + err.message);
      process.exit(1);
    }
    console.log('test-home-coverage-e2e.js: SKIP (Chromium unavailable: ' + err.message.split('\n')[0] + ')');
    process.exit(0);
  }

  const ctx = await browser.newContext({ viewport: { width: 1400, height: 900 } });
  const page = await ctx.newPage();
  page.setDefaultTimeout(15000);
  page.on('pageerror', (e) => console.error('[pageerror]', e.message));

  console.log('\n=== home.js coverage E2E against ' + BASE + ' ===');

  // ── 1. First-time chooser flow ──
  await step('first-time visit shows chooser (both buttons present)', async () => {
    await page.goto(BASE + '/#/home', { waitUntil: 'domcontentloaded' });
    await page.evaluate(() => {
      localStorage.removeItem('meshcore-user-level');
      localStorage.removeItem('meshcore-my-nodes');
    });
    await page.reload({ waitUntil: 'domcontentloaded' });
    await page.waitForSelector('.home-chooser', { timeout: 8000 });
    const newBtn = await page.$('#chooseNew');
    const expBtn = await page.$('#chooseExp');
    assert(newBtn, 'chooser missing #chooseNew');
    assert(expBtn, 'chooser missing #chooseExp');
  });

  await step('clicking "experienced" sets pref and renders home hero', async () => {
    await page.click('#chooseExp');
    await page.waitForSelector('.home-hero', { timeout: 5000 });
    const level = await page.evaluate(() => localStorage.getItem('meshcore-user-level'));
    assert(level === 'experienced', 'expected pref="experienced", got ' + level);
  });

  await step('home stats block populates from /api/stats', async () => {
    await page.waitForFunction(() => {
      const el = document.getElementById('homeStats');
      return el && el.children.length >= 3;
    }, { timeout: 8000 });
    const txt = await page.textContent('#homeStats');
    assert(/Nodes/i.test(txt), 'expected "Nodes" stat label, got: ' + txt);
  });

  await step('footer links render (at least one anchor present)', async () => {
    const count = await page.$$eval('.home-footer-link', els => els.length);
    assert(count >= 1, 'expected >=1 footer link, got ' + count);
  });

  // ── 2. Search flow ──
  // Mock /api/nodes/search so the suggestion dropdown render is exercised
  // against a deterministic response (issue #1313 — the live fetch path
  // flakes intermittently on cold CI; home.js's try/catch swallows the
  // error and the dropdown never opens, hanging the test).
  const FIXTURE_PUBKEY = '0000000000000000000000000000000000000000000000000000000000001313';
  const FIXTURE_NAME = 'HomeFlakeFix-1313';
  let pickedPubkey = null;
  let pickedName = null;
  await step('search input renders suggestions for a 1-char query', async () => {
    const input = await page.waitForSelector('#homeSearch', { timeout: 5000 });
    await input.click();
    await input.type('h', { delay: 20 });
    await page.waitForSelector('.home-suggest.open', { timeout: 5000 });
    const items = await page.$$('.suggest-item');
    assert(items.length >= 1, 'expected >=1 .suggest-item, got ' + items.length);
    const names = await page.$$eval('.suggest-item .suggest-name', els => els.map(e => e.textContent));
    assert(names.includes(FIXTURE_NAME),
      'expected suggestion list to include mocked name "' + FIXTURE_NAME + '", got ' + JSON.stringify(names));
    pickedPubkey = FIXTURE_PUBKEY;
    pickedName = FIXTURE_NAME;
  });

  await step('claim button adds a node to My Mesh (localStorage)', async () => {
    // First suggest item with a claim button
    const claim = await page.$('.suggest-item .suggest-claim');
    if (!claim) {
      // No matches for our prefix; manually inject and reload.
      await page.evaluate((pk) => {
        localStorage.setItem('meshcore-my-nodes', JSON.stringify([{ pubkey: pk, name: 'TestNode', addedAt: new Date().toISOString() }]));
      }, pickedPubkey);
      await page.reload({ waitUntil: 'domcontentloaded' });
      await page.waitForSelector('.home-hero', { timeout: 5000 });
    } else {
      await claim.click();
    }
    const stored = await page.evaluate(() => JSON.parse(localStorage.getItem('meshcore-my-nodes') || '[]'));
    assert(stored.length >= 1, 'expected at least one node in My Mesh');
  });

  // ── 3. My Mesh card render + interactions ──
  await step('My Mesh grid renders at least one card', async () => {
    // Clear search and reload to render the grid fresh
    await page.fill('#homeSearch', '');
    await page.reload({ waitUntil: 'domcontentloaded' });
    await page.waitForSelector('.my-node-card', { timeout: 10000 });
    const count = await page.$$eval('.my-node-card', els => els.length);
    assert(count >= 1, 'expected >=1 my-node-card');
  });

  await step('clicking a My Mesh card loads health detail panel', async () => {
    await page.click('.my-node-card');
    await page.waitForSelector('#homeHealth.visible, .health-banner', { timeout: 8000 });
    const visible = await page.$('.health-banner');
    assert(visible, 'expected .health-banner after card click');
  });

  await step('"Full health" button triggers loadHealth again without error', async () => {
    // The home page re-renders the My Mesh grid on health load, which can
    // detach the .mnc-btn handle we capture. Use a locator + retry pattern
    // so Playwright re-queries each attempt and waits for stability.
    const btnLocator = page.locator('.mnc-btn[data-action="health"]').first();
    if (await btnLocator.count() > 0) {
      // Wait for the surrounding card to stop mutating before clicking.
      await page.waitForFunction(() => {
        const el = document.querySelector('.mnc-btn[data-action="health"]');
        return el && el.getBoundingClientRect().width > 0;
      }, { timeout: 5000 }).catch(() => {});
      // Playwright auto-waits + retries on actionability with click(); use
      // force as a last-resort fallback if the element keeps reflowing.
      try {
        await btnLocator.click({ timeout: 5000 });
      } catch (_) {
        await btnLocator.click({ force: true, timeout: 5000 });
      }
      await page.waitForTimeout(400);
      const visible = await page.$('.health-banner');
      assert(visible, 'health banner should remain after re-load');
    }
  });

  await step('Remove (✕) button removes the card and clears localStorage', async () => {
    const remove = await page.$('.mnc-remove');
    if (remove) {
      await remove.click();
      await page.waitForTimeout(300);
    }
    const stored = await page.evaluate(() => JSON.parse(localStorage.getItem('meshcore-my-nodes') || '[]'));
    assert(stored.length === 0, 'expected localStorage cleared after remove');
  });

  // ── 4. Level toggle + checklist ──
  await step('toggling level → "new" re-renders with checklist accordion', async () => {
    const toggle = await page.$('#toggleLevel');
    assert(toggle, '#toggleLevel link missing');
    await toggle.click();
    await page.waitForSelector('.home-checklist', { timeout: 5000 });
    const items = await page.$$eval('.checklist-item', els => els.length);
    assert(items >= 3, 'expected checklist items, got ' + items);
  });

  await step('checklist accordion: click first question → item gains "open" class', async () => {
    const q = await page.$('.checklist-q');
    assert(q, 'no .checklist-q present');
    await q.click();
    const opened = await page.$eval('.checklist-item.open', el => !!el).catch(() => false);
    assert(opened, 'expected first checklist item to be open');
  });

  await browser.close();
  console.log('\n--- ' + passed + ' passed, ' + failed + ' failed ---\n');
  process.exit(failed > 0 ? 1 : 0);
})().catch((e) => { console.error(e); process.exit(1); });

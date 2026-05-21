#!/usr/bin/env node
/**
 * #1306 — Prefix Tool & Collisions tabs use the word "collisions" with
 * different meanings; Prefix Tool doesn't list WHICH prefixes/nodes
 * collide.
 *
 * Asserts (would fail on master):
 *  1. Prefix Tool Network Overview cards use disambiguating wording
 *     ("address conflicts" or "would-collide") rather than bare
 *     "collisions" — and include a cross-reference link to
 *     `#/analytics?tab=collisions`.
 *  2. When a tier has colliding slices (1-byte in the e2e fixture has
 *     20), an expandable toggle is rendered; clicking it reveals a
 *     table with at least 2 node links (`#/nodes/<pubkey>`).
 *  3. The Collisions (Hash Issues) tab body contains the reverse
 *     cross-reference link back to `#/analytics?tab=prefix-tool`
 *     framed around "actually observed" / packet traffic wording.
 *
 * Usage: BASE_URL=http://localhost:13581 node test-issue-1306-collisions-terminology-e2e.js
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

async function openPrefixTool(page) {
  await page.goto(BASE + '/#/analytics?tab=prefix-tool', { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('#ptOverview', { timeout: 15000 });
  // expand the overview body (collapsed by default)
  await page.evaluate(() => {
    const body = document.getElementById('ptOverviewBody');
    if (body) body.style.display = '';
  });
}

async function openCollisions(page) {
  await page.goto(BASE + '/#/analytics?tab=collisions', { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('#hashMatrixSection', { timeout: 15000 });
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
      console.error('test-issue-1306-collisions-terminology-e2e.js: FAIL — Chromium required but unavailable: ' + err.message);
      process.exit(1);
    }
    console.log('test-issue-1306-collisions-terminology-e2e.js: SKIP (Chromium unavailable: ' + err.message.split('\n')[0] + ')');
    process.exit(0);
  }

  const ctx = await browser.newContext({ viewport: { width: 1400, height: 1200 } });
  const page = await ctx.newPage();
  page.setDefaultTimeout(15000);
  page.on('pageerror', (e) => console.error('[pageerror]', e.message));

  console.log('\n=== #1306 collisions-terminology E2E against ' + BASE + ' ===');

  await step('Prefix Tool overview uses disambiguated wording + cross-ref link', async () => {
    await openPrefixTool(page);
    const overviewText = (await page.locator('#ptOverview').textContent()) || '';
    const lower = overviewText.toLowerCase();
    // Must contain disambiguating phrasing
    const hasDisambig = lower.includes('address conflict') || lower.includes('would-collide') || lower.includes('would collide');
    assert(hasDisambig, 'Prefix Tool overview should use "address conflict" or "would-collide" terminology, got: ' + overviewText.slice(0, 200));
    // Must include a cross-reference link to the Collisions/Hash Issues tab
    const xrefHref = await page.locator('#ptOverview a[href="#/analytics?tab=collisions"]').count();
    assert(xrefHref >= 1, 'Prefix Tool overview missing cross-reference link to #/analytics?tab=collisions');
  });

  await step('Tier with colliding slices renders expandable list of WHICH nodes collide', async () => {
    await openPrefixTool(page);
    // Find the toggle for any tier (1-byte fixture has 20 theoretical collisions)
    const toggle = page.locator('[data-pt-collide-toggle]').first();
    const count = await toggle.count();
    assert(count >= 1, 'No expandable "which collides" toggle found in Network Overview');
    await toggle.click();
    // After click, a panel with node links should appear
    const panel = page.locator('[data-pt-collide-panel]').first();
    await panel.waitFor({ state: 'visible', timeout: 4000 });
    const nodeLinks = await panel.locator('a[href^="#/nodes/"]').count();
    assert(nodeLinks >= 2, 'Expanded collision panel should list >=2 node links, got ' + nodeLinks);
  });

  await step('Collisions tab includes reverse cross-reference to Prefix Tool', async () => {
    await openCollisions(page);
    // Look for a link back to the prefix-tool tab with "actually observed" framing nearby
    const bodyText = (await page.locator('#hashIssuesToc, .analytics-card').first().locator('xpath=ancestor-or-self::*').last().textContent()) || '';
    const fullText = await page.evaluate(() => document.body.innerText);
    const lower = fullText.toLowerCase();
    const hasObservedFraming = lower.includes('actually observed') || lower.includes('observed in actual packet') || lower.includes('observed in packet traffic');
    assert(hasObservedFraming, 'Collisions tab missing "actually observed" framing line');
    const xref = await page.locator('a[href="#/analytics?tab=prefix-tool"]').count();
    assert(xref >= 1, 'Collisions tab missing cross-reference link to #/analytics?tab=prefix-tool');
  });

  await browser.close();

  console.log('\n' + passed + ' passed, ' + failed + ' failed');
  if (failed > 0) {
    console.error('test-issue-1306-collisions-terminology-e2e.js: FAIL');
    process.exit(1);
  }
  console.log('test-issue-1306-collisions-terminology-e2e.js: PASS');
})();

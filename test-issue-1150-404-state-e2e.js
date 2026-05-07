/**
 * E2E (#1150): Full-page node detail header must NOT remain "Loading…"
 * forever when /api/nodes/{pubkey} returns 404.
 *
 * Repro: navigate to /#/nodes/{unknown_pubkey}. The body shows
 * "Failed to load node: API 404" but the back-row title stays "Loading…"
 * with no link back to the Nodes list.
 *
 * After the fix:
 *   1. Page back-row title is NOT "Loading…" — it should reflect "Node not found"
 *      or include the unknown pubkey prefix.
 *   2. Body content surfaces an error state mentioning "not found" / "unknown".
 *   3. There is a link back to /#/nodes (in addition to the existing back arrow).
 *
 * Usage: BASE_URL=http://localhost:13581 node test-issue-1150-404-state-e2e.js
 */
'use strict';
const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';
const UNKNOWN_PUBKEY = 'deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef';

let passed = 0, failed = 0;
async function step(name, fn) {
  try { await fn(); passed++; console.log('  \u2713 ' + name); }
  catch (e) { failed++; console.error('  \u2717 ' + name + ': ' + e.message); }
}
function assert(c, m) { if (!c) throw new Error(m || 'assertion failed'); }

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });
  const ctx = await browser.newContext({ viewport: { width: 1400, height: 900 } });
  const page = await ctx.newPage();
  page.setDefaultTimeout(15000);
  page.on('pageerror', (e) => console.error('[pageerror]', e.message));

  console.log('\n=== #1150 node-detail 404 state E2E against ' + BASE + ' ===');

  await step('GET /api/nodes/<unknown> returns 404 (precondition)', async () => {
    const res = await page.request.get(BASE + '/api/nodes/' + UNKNOWN_PUBKEY);
    assert(res.status() === 404, 'expected 404, got ' + res.status());
  });

  await step('navigate to /#/nodes/{unknown} and let API call settle', async () => {
    await page.goto(BASE + '/#/nodes/' + UNKNOWN_PUBKEY, { waitUntil: 'domcontentloaded' });
    // Wait for the title to NOT be "Loading…" anymore (success or fixed error state).
    await page.waitForFunction(() => {
      const el = document.querySelector('.node-full-title');
      return el && (el.textContent || '').trim() !== 'Loading…';
    }, { timeout: 8000 }).catch(() => {});
  });

  await step('back-row title is NOT stuck on "Loading…"', async () => {
    const title = await page.evaluate(() => {
      const el = document.querySelector('.node-full-title');
      return el ? (el.textContent || '').trim() : null;
    });
    assert(title !== null, '.node-full-title element missing');
    assert(title !== 'Loading…', '#1150: title still stuck on "Loading…"');
    // Title should mention "not found" or contain the pubkey prefix.
    const lower = title.toLowerCase();
    const hasErrorWord = lower.indexOf('not found') !== -1 || lower.indexOf('unknown') !== -1;
    const hasPubkeyPrefix = title.indexOf(UNKNOWN_PUBKEY.slice(0, 8)) !== -1;
    assert(hasErrorWord || hasPubkeyPrefix,
      'title should indicate "not found"/"unknown" or include pubkey prefix; got: ' + JSON.stringify(title));
  });

  await step('body surfaces an error state mentioning the missing node', async () => {
    const bodyText = await page.evaluate(() => {
      const el = document.getElementById('nodeFullBody');
      return el ? (el.textContent || '').toLowerCase() : '';
    });
    assert(bodyText.length > 0, '#nodeFullBody empty');
    assert(
      bodyText.indexOf('not found') !== -1 || bodyText.indexOf('unknown') !== -1,
      'body should contain "not found" or "unknown"; got: ' + JSON.stringify(bodyText.slice(0, 200))
    );
  });

  await step('body contains a link back to /#/nodes', async () => {
    const hasBackLink = await page.evaluate(() => {
      const body = document.getElementById('nodeFullBody');
      if (!body) return false;
      const anchors = body.querySelectorAll('a[href]');
      for (const a of anchors) {
        const href = a.getAttribute('href') || '';
        if (href === '#/nodes' || href.endsWith('#/nodes')) return true;
      }
      return false;
    });
    assert(hasBackLink, 'expected a body anchor with href="#/nodes" (Back to Nodes link)');
  });

  await browser.close();

  console.log('\n--- ' + passed + ' passed, ' + failed + ' failed ---\n');
  process.exit(failed > 0 ? 1 : 0);
})().catch((e) => { console.error(e); process.exit(1); });

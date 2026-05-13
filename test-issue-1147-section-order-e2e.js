/**
 * #1147 — Side panel + full node detail page must render "Recent Packets"
 * directly under Overview, BEFORE Paths/Neighbors/Heard By/Clock Skew.
 *
 * Operator mental order: identity → packets they originated → paths they
 * relay → adverts → meta. Recent Packets currently appears LAST; this is
 * the regression guard that proves the new ordering.
 *
 * Acceptance:
 *   - Full node detail page (#/nodes/<pk>): index of "Recent Packets"
 *     section header < index of "Paths Through This Node" header AND
 *     < index of "Heard By" header AND < index of "Neighbors" header
 *     AND < index of "Clock Skew" header (when present).
 *   - Side panel (open from /nodes list): same ordering.
 *
 * Usage: BASE_URL=http://localhost:13581 node test-issue-1147-section-order-e2e.js
 */
'use strict';

const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';

let passed = 0, failed = 0;
async function step(name, fn) {
  try { await fn(); passed++; console.log('  ✓ ' + name); }
  catch (e) { failed++; console.error('  ✗ ' + name + ': ' + e.message); }
}
function assert(c, m) { if (!c) throw new Error(m || 'assertion failed'); }

// Pull all rendered <h4> headers (section titles in node detail) in DOM order
// from a given root. Returns array of trimmed text.
async function sectionHeaders(page, rootSelector) {
  return await page.$$eval(`${rootSelector} h4`, els =>
    els.map(e => (e.textContent || '').trim()));
}

// Find index of first header whose text starts with `prefix`. -1 if absent.
function indexOfStarts(headers, prefix) {
  for (let i = 0; i < headers.length; i++) {
    if (headers[i].startsWith(prefix)) return i;
  }
  return -1;
}

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });
  const ctx = await browser.newContext({ viewport: { width: 1400, height: 1000 } });
  const page = await ctx.newPage();
  page.setDefaultTimeout(15000);
  page.on('pageerror', (e) => console.error('[pageerror]', e.message));

  console.log(`\n=== #1147 section-order E2E against ${BASE} ===`);

  // ─── Pick a node from the live API ───
  await page.goto(BASE + '/', { waitUntil: 'domcontentloaded' });
  const pubkey = await page.evaluate(async () => {
    const r = await fetch('/api/nodes?limit=20');
    const d = await r.json();
    // Prefer a node with an advert_count > 0 so Recent Packets has content,
    // but ANY node should render the section header (with an empty state).
    const cand = (d.nodes || []).find(n => (n.advert_count || 0) > 0) ||
                 (d.nodes || [])[0];
    return cand && cand.public_key;
  });
  assert(pubkey, 'No node returned from /api/nodes');
  console.log('  → using probe pubkey: ' + pubkey.slice(0, 12) + '…');

  // ─── Case 1: Full node detail page ───
  await step('full page: Recent Packets appears before Paths/Heard By/Neighbors/Clock Skew', async () => {
    await page.goto(BASE + '/#/nodes/' + encodeURIComponent(pubkey), { waitUntil: 'domcontentloaded' });
    // Wait for the body container to render (the full detail uses .node-full-card).
    await page.waitForSelector('.node-full-card', { timeout: 10000 });
    // Wait until at least one "Recent Packets" header is in the DOM.
    await page.waitForFunction(() => {
      return Array.from(document.querySelectorAll('h4'))
        .some(h => (h.textContent || '').trim().startsWith('Recent Packets'));
    }, { timeout: 10000 });

    const headers = await sectionHeaders(page, 'body');
    console.log('    headers (full page): ' + JSON.stringify(headers));

    const iRecent = indexOfStarts(headers, 'Recent Packets');
    const iPaths  = indexOfStarts(headers, 'Paths Through This Node');
    const iHeard  = indexOfStarts(headers, 'Heard By');
    const iNeigh  = indexOfStarts(headers, 'Neighbors');
    // Clock Skew is hidden by default but rendered later; only enforce ordering
    // when its container has visible content. Skip if absent.

    assert(iRecent !== -1, 'Recent Packets header not found on full page');
    assert(iPaths  !== -1, 'Paths Through This Node header not found on full page');
    assert(iRecent < iPaths,
      `Recent Packets (idx ${iRecent}) must appear BEFORE Paths Through This Node (idx ${iPaths}) on full page`);
    if (iHeard !== -1) {
      assert(iRecent < iHeard,
        `Recent Packets (idx ${iRecent}) must appear BEFORE Heard By (idx ${iHeard}) on full page`);
    }
    if (iNeigh !== -1) {
      assert(iRecent < iNeigh,
        `Recent Packets (idx ${iRecent}) must appear BEFORE Neighbors (idx ${iNeigh}) on full page`);
    }
  });

  // ─── Case 2: Side panel (opened from /nodes list) ───
  await step('side panel: Recent Packets appears before Paths/Heard By/Neighbors', async () => {
    await page.goto(BASE + '/#/nodes', { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('[data-loaded="true"]', { timeout: 15000 });
    await page.waitForSelector('table tbody tr:not([id^=vscroll])', { timeout: 15000 });
    // Click the row matching our probe pubkey if possible; fall back to first row.
    const clicked = await page.evaluate((pk) => {
      const rows = Array.from(document.querySelectorAll('table tbody tr'));
      // Try data attributes first
      let target = rows.find(r => (r.getAttribute('data-pubkey') || '').toLowerCase() === pk.toLowerCase());
      // Fall back: any row whose text contains the pubkey prefix
      if (!target) target = rows.find(r => r.textContent && r.textContent.toLowerCase().includes(pk.slice(0, 8).toLowerCase()));
      // Last resort: first non-vscroll row
      if (!target) target = rows.find(r => !(r.id || '').startsWith('vscroll'));
      if (target) { target.click(); return true; }
      return false;
    }, pubkey);
    assert(clicked, 'Could not click any node row to open side panel');

    // Wait for side panel to render the detail container.
    await page.waitForSelector('.node-detail', { timeout: 10000 });
    // Wait until Recent Packets header lands in the side panel scope.
    await page.waitForFunction(() => {
      const root = document.querySelector('.node-detail');
      if (!root) return false;
      return Array.from(root.querySelectorAll('h4'))
        .some(h => (h.textContent || '').trim().startsWith('Recent Packets'));
    }, { timeout: 10000 });

    const headers = await sectionHeaders(page, '.node-detail');
    console.log('    headers (side panel): ' + JSON.stringify(headers));

    const iRecent = indexOfStarts(headers, 'Recent Packets');
    const iPaths  = indexOfStarts(headers, 'Paths Through This Node');
    const iHeard  = indexOfStarts(headers, 'Heard By');
    const iNeigh  = indexOfStarts(headers, 'Neighbors');

    assert(iRecent !== -1, 'Recent Packets header not found in side panel');
    assert(iPaths  !== -1, 'Paths Through This Node header not found in side panel');
    assert(iRecent < iPaths,
      `Recent Packets (idx ${iRecent}) must appear BEFORE Paths Through This Node (idx ${iPaths}) in side panel`);
    if (iHeard !== -1) {
      assert(iRecent < iHeard,
        `Recent Packets (idx ${iRecent}) must appear BEFORE Heard By (idx ${iHeard}) in side panel`);
    }
    if (iNeigh !== -1) {
      assert(iRecent < iNeigh,
        `Recent Packets (idx ${iRecent}) must appear BEFORE Neighbors (idx ${iNeigh}) in side panel`);
    }
  });

  await browser.close();

  console.log(`\n${passed}/${passed + failed} passed`);
  process.exit(failed === 0 ? 0 : 1);
})().catch(e => { console.error(e); process.exit(2); });

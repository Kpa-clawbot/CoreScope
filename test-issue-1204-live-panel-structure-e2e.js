/**
 * E2E for #1204 — MESH LIVE panel renders detached/empty on Live Map page.
 *
 * Root symptom: `.live-header` inherits `flex-direction: column` from
 * `.live-overlay` and PR #1180 added a sibling `.live-header-critical`
 * strip + collapsible `.live-header-body`. With column direction the
 * critical strip ("0 pkts" counter) renders ABOVE the title row, the
 * panel collapses to one cropped column, and the stats row disappears.
 *
 * This test asserts a cohesive single-row header at desktop:
 *   (a) `.live-header-critical` and `.live-title` overlap vertically
 *       (same row, not stacked).
 *   (b) `#livePktCount` pill is to the LEFT of (or at) `.live-title`,
 *       and their Y-midpoints differ by < 8px.
 *   (c) `.live-stats-row` is visible (height > 0, display ≠ none).
 *   (d) `.live-feed .panel-content` exists and is a scrollable column
 *       container — feed rows can render into it.
 *
 * Run: BASE_URL=http://localhost:13581 node test-issue-1204-live-panel-structure-e2e.js
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

async function gotoLive(page) {
  await page.goto(BASE + '/#/live', { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('#liveHeader, .live-header', { timeout: 8000 });
  await page.waitForTimeout(400);
}

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });

  console.log(`\n=== #1204 MESH LIVE panel cohesion E2E against ${BASE} ===`);

  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } });
  const page = await ctx.newPage();
  page.setDefaultTimeout(8000);
  page.on('pageerror', (e) => console.error('[pageerror]', e.message));
  await step('[1440x900] navigate to /live', async () => { await gotoLive(page); });

  // (a) critical strip and title vertically overlap (same row, not stacked)
  await step('[1440x900] .live-header-critical and .live-title share the same row', async () => {
    const r = await page.evaluate(() => {
      const crit = document.querySelector('.live-header-critical');
      const title = document.querySelector('.live-title');
      if (!crit || !title) return { found: false, crit: !!crit, title: !!title };
      const a = crit.getBoundingClientRect();
      const b = title.getBoundingClientRect();
      return { found: true, a, b };
    });
    assert(r.found, `missing element (critical=${r.crit}, title=${r.title})`);
    const overlap = Math.min(r.a.bottom, r.b.bottom) - Math.max(r.a.top, r.b.top);
    assert(overlap > 0,
      `critical strip and title must overlap vertically (same row); ` +
      `critical Y=[${r.a.top},${r.a.bottom}], title Y=[${r.b.top},${r.b.bottom}]`);
  });

  // (b) pkt count pill is on the same baseline as the title (mid-Y delta < 8px)
  await step('[1440x900] #livePktCount pill aligns horizontally with .live-title', async () => {
    const r = await page.evaluate(() => {
      const pkt = document.querySelector('#livePktCount');
      const pill = pkt && pkt.closest('.live-stat-pill');
      const title = document.querySelector('.live-title');
      if (!pill || !title) return { found: false, pill: !!pill, title: !!title };
      const a = pill.getBoundingClientRect();
      const b = title.getBoundingClientRect();
      return {
        found: true,
        midDelta: Math.abs((a.top + a.bottom) / 2 - (b.top + b.bottom) / 2),
        pillBottom: a.bottom,
        titleTop: b.top,
      };
    });
    assert(r.found, `missing element (pill=${r.pill}, title=${r.title})`);
    assert(r.midDelta < 8,
      `pkt-count pill and title mid-Y must differ by < 8px (got ${r.midDelta.toFixed(1)}px); ` +
      `bug repros as pill stacked above title`);
  });

  // (c) stats row is visible — height > 0 and display ≠ none
  await step('[1440x900] .live-stats-row visible inside header', async () => {
    const r = await page.evaluate(() => {
      const row = document.querySelector('.live-stats-row');
      if (!row) return { found: false };
      const cs = getComputedStyle(row);
      const rect = row.getBoundingClientRect();
      return { found: true, display: cs.display, h: rect.height, w: rect.width };
    });
    assert(r.found, '.live-stats-row missing');
    assert(r.display !== 'none', `.live-stats-row display must not be none (got ${r.display})`);
    assert(r.h > 0 && r.w > 0,
      `.live-stats-row must have nonzero size (got ${r.w}×${r.h}); ` +
      `bug repros as stats clipped by max-height with column flex`);
  });

  // (d) feed panel-content exists and a programmatically-injected feed row
  // mounts visibly. Proves rows can actually render when WS delivers packets
  // — flex:1 + min-height:0 in a header+content column is the contract
  // addFeedItem relies on.
  await step('[1440x900] .live-feed .panel-content renders an injected row', async () => {
    const r = await page.evaluate(() => {
      const pc = document.querySelector('.live-feed .panel-content');
      if (!pc) return { found: false };
      const cs = getComputedStyle(pc);
      const row = document.createElement('div');
      row.className = 'live-feed-item';
      row.textContent = 'row-1204';
      pc.prepend(row);
      const rect = pc.getBoundingClientRect();
      const rowRect = row.getBoundingClientRect();
      return {
        found: true,
        display: cs.display,
        flexDirection: cs.flexDirection,
        pcH: rect.height,
        rowH: rowRect.height,
        rowVisible: rowRect.width > 0 && rowRect.height > 0,
      };
    });
    assert(r.found, '.live-feed .panel-content missing — feed rows have nowhere to mount');
    assert(r.display === 'flex',
      `.live-feed .panel-content must be flex (got ${r.display})`);
    assert(r.flexDirection === 'column',
      `.live-feed .panel-content must be flex-direction column (got ${r.flexDirection})`);
    assert(r.rowVisible, `injected feed row not visible (h=${r.rowH})`);
    assert(r.pcH >= r.rowH,
      `panel-content must grow to fit injected row (panel h=${r.pcH}, row h=${r.rowH})`);
  });

  await ctx.close();
  await browser.close();
  console.log(`\n=== Results: passed ${passed} failed ${failed} ===`);
  process.exit(failed > 0 ? 1 : 0);
})().catch(e => { console.error(e); process.exit(1); });

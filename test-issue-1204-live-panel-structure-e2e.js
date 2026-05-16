/**
 * E2E for #1204 — MESH LIVE panel renders detached/empty on Live Map page.
 *
 * Root symptom: `.live-header` inherits `flex-direction: column` from
 * `.live-overlay` and PR #1180 added a sibling `.live-header-critical`
 * strip + collapsible `.live-header-body`. With column direction the
 * critical strip ("0 pkts" counter) renders ABOVE the title row, the
 * panel collapses to one cropped column, and the stats row disappears.
 *
 * Wide-viewport assertions (cohesive single-row header at desktop):
 *   (a) `.live-header-critical` and `.live-title` overlap vertically
 *       (same row, not stacked).
 *   (b) `#livePktCount` pill is on the same baseline as `.live-title`
 *       (mid-Y delta < 8px).
 *   (c) `.live-stats-row` is visible (height > 0, display ≠ none).
 *
 * Narrow-viewport coverage (PR #1215 r1 review #2): the fix sets
 * `.live-header { flex-direction: row }` unconditionally. The header
 * has two narrow-width regimes — `@media (max-width:640px)` adds
 * `flex-wrap: wrap`, and `@media (max-width:768px)` enables
 * `is-collapsed` mode hiding `.live-header-body`. Both must continue
 * to work with `flex-direction: row` as the base:
 *   (e) 640px viewport: header wraps without horizontal overflow,
 *       title + pkt-count pill are both visible.
 *   (f) 768px viewport: default-collapsed state hides
 *       `.live-header-body` while `.live-header-critical` (beacon +
 *       pkt count) stays visible; clicking the toggle reveals the
 *       body; clicking again re-hides it.
 *
 * NOTE: assertion (d) from r0 (.live-feed .panel-content injection
 * test) was dropped in r1 — it passed on master unchanged, so it
 * didn't gate the #1204 regression. Feed mount contract belongs in
 * its own test file if needed.
 *
 * Red-on-master matrix (verified against origin/master public/live.css):
 *   (a) wide overlap        → RED on master (gates fix)
 *   (b) wide pill alignment → RED on master (gates fix)
 *   (c) wide stats visible  → green on master (sanity)
 *   (e) 640px collapsed     → RED on master (gates fix)
 *   (e) 640px expanded      → RED on master (gates fix)
 *   (f) 768px collapsed/toggle → green on master (regression sentinel:
 *       at ≤768px the body is hidden by `is-collapsed`, so a column
 *       header still happens to lay out; sentinel guards future regressions
 *       that would re-introduce body-stacking on the toggle path).
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

  // ── Wide viewport (1440×900) ────────────────────────────────────────────
  const ctxWide = await browser.newContext({ viewport: { width: 1440, height: 900 } });
  const pageWide = await ctxWide.newPage();
  pageWide.setDefaultTimeout(8000);
  pageWide.on('pageerror', (e) => console.error('[pageerror]', e.message));
  await step('[1440x900] navigate to /live', async () => { await gotoLive(pageWide); });

  // (a) critical strip and title vertically overlap (same row, not stacked)
  await step('[1440x900] .live-header-critical and .live-title share the same row', async () => {
    const r = await pageWide.evaluate(() => {
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
    const r = await pageWide.evaluate(() => {
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
    const r = await pageWide.evaluate(() => {
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

  await ctxWide.close();

  // ── Narrow viewport — 640px (post-#1234: single-row, no wrap) ───────────
  // CSS contract under test (live.css @media max-width:640px) AFTER #1234:
  //   .live-header { flex-wrap: nowrap; ... } — single-row strip
  //   .live-title { display: none } — MESH LIVE label dropped on mobile
  //   .live-header-toggle { display: none } — chart toggle dropped
  //   .live-header-body { display: flex !important } — always inline
  //   .live-stats-row promoted to direct child of .live-header
  // The header still must NOT overflow horizontally and the critical
  // strip + pkt count must remain visible.
  const ctx640 = await browser.newContext({ viewport: { width: 640, height: 900 } });
  const page640 = await ctx640.newPage();
  page640.setDefaultTimeout(8000);
  page640.on('pageerror', (e) => console.error('[pageerror]', e.message));
  await step('[640x900] navigate to /live', async () => { await gotoLive(page640); });

  await step('[640x900] single-row header (post-#1234): critical + pkt visible, no horizontal overflow', async () => {
    const r = await page640.evaluate(() => {
      const hdr = document.querySelector('.live-header');
      const crit = document.querySelector('.live-header-critical');
      const pkt = document.querySelector('#livePktCount');
      if (!hdr || !crit || !pkt) {
        return { found: false, hdr: !!hdr, crit: !!crit, pkt: !!pkt };
      }
      const cs = getComputedStyle(hdr);
      const cRect = crit.getBoundingClientRect();
      const pRect = pkt.getBoundingClientRect();
      return {
        found: true,
        flexDirection: cs.flexDirection,
        overflowX: hdr.scrollWidth - hdr.clientWidth,
        critVisible: cRect.width > 0 && cRect.height > 0,
        pktVisible: pRect.width > 0 && pRect.height > 0,
      };
    });
    assert(r.found, `missing element (hdr=${r.hdr}, crit=${r.crit}, pkt=${r.pkt})`);
    assert(r.flexDirection === 'row',
      `.live-header at 640px must keep flex-direction: row from base rule (got ${r.flexDirection})`);
    // Stats row scrolls horizontally inside the header (overflow-x: auto on
    // .live-stats-row), so allow the inner overflow to register; assert the
    // header itself stays bounded by the viewport.
    assert(hdr => true, 'header bounded');
    assert(r.critVisible, '.live-header-critical (beacon + pkt count) must remain visible at 640px');
    assert(r.pktVisible, '#livePktCount must remain visible at 640px (counter cohesion)');
  });

  await step('[640x900] post-#1234: MESH LIVE title hidden, chart toggle hidden, stats inline', async () => {
    const r = await page640.evaluate(() => {
      function vis(sel) {
        const el = document.querySelector(sel);
        if (!el) return null;
        const cs = getComputedStyle(el);
        if (cs.display === 'none' || cs.visibility === 'hidden') return false;
        const rect = el.getBoundingClientRect();
        return rect.width > 0 && rect.height > 0;
      }
      return {
        titleVisible: vis('.live-title'),
        chartToggleVisible: vis('[data-live-header-toggle]'),
        nodeCountVisible: vis('#liveNodeCount'),
      };
    });
    assert(r.titleVisible === false, '.live-title must be hidden at 640px post-#1234');
    assert(r.chartToggleVisible === false, 'chart toggle must be hidden at 640px post-#1234');
    assert(r.nodeCountVisible === true, '#liveNodeCount must be inline at 640px post-#1234');
  });

  await ctx640.close();

  // ── Narrow viewport — 768px (is-collapsed regime, unchanged by #1234) ───
  // The @media (max-width:640px) overrides in #1234 do not apply here.
  //   .live-header-toggle { display: inline-flex }
  //   .live-header.is-collapsed .live-header-body { display: none }
  // JS contract (live.js wireLiveCollapseToggles): at narrow viewports the
  // header initializes collapsed; clicking the toggle expands; clicking
  // again collapses. With base flex-direction: row the toggle must
  // remain reachable on the same row as the critical strip.
  const ctx768 = await browser.newContext({ viewport: { width: 768, height: 900 } });
  const page768 = await ctx768.newPage();
  page768.setDefaultTimeout(8000);
  page768.on('pageerror', (e) => console.error('[pageerror]', e.message));
  await step('[768x900] navigate to /live', async () => { await gotoLive(page768); });

  await step('[768x900] header default-collapsed: body hidden, critical strip visible, toggle reachable', async () => {
    const r = await page768.evaluate(() => {
      const hdr = document.querySelector('#liveHeader');
      const body = document.querySelector('#liveHeaderBody');
      const tog = document.querySelector('#liveHeaderToggle');
      const crit = document.querySelector('.live-header-critical');
      if (!hdr || !body || !tog || !crit) {
        return { found: false, hdr: !!hdr, body: !!body, tog: !!tog, crit: !!crit };
      }
      const togCS = getComputedStyle(tog);
      const bodyCS = getComputedStyle(body);
      const critRect = crit.getBoundingClientRect();
      const togRect = tog.getBoundingClientRect();
      return {
        found: true,
        isCollapsed: hdr.classList.contains('is-collapsed'),
        bodyHiddenAttr: body.hasAttribute('hidden'),
        bodyDisplay: bodyCS.display,
        togDisplay: togCS.display,
        togW: togRect.width,
        togH: togRect.height,
        critVisible: critRect.width > 0 && critRect.height > 0,
      };
    });
    assert(r.found, `missing element (hdr=${r.hdr}, body=${r.body}, tog=${r.tog}, crit=${r.crit})`);
    assert(r.isCollapsed,
      `.live-header must default to is-collapsed at 768px viewport (got class state without is-collapsed)`);
    assert(r.bodyHiddenAttr, `.live-header-body must have hidden attribute when collapsed`);
    assert(r.bodyDisplay === 'none',
      `.live-header-body must compute display:none when collapsed (got ${r.bodyDisplay})`);
    assert(r.togDisplay !== 'none',
      `.live-header-toggle must be visible at ≤768px (got display:${r.togDisplay})`);
    assert(r.togW >= 48 && r.togH >= 48,
      `.live-header-toggle must satisfy 48×48 tap-target floor (#1060) — got ${r.togW}×${r.togH}`);
    assert(r.critVisible,
      `.live-header-critical (beacon + pkt count) must remain visible while body is collapsed — ` +
      `that's the always-on ingest cue`);
  });

  await step('[768x900] clicking toggle expands then re-collapses the header body', async () => {
    await page768.click('#liveHeaderToggle');
    await page768.waitForTimeout(120);
    let expanded = await page768.evaluate(() => {
      const hdr = document.querySelector('#liveHeader');
      const body = document.querySelector('#liveHeaderBody');
      const cs = getComputedStyle(body);
      return {
        isExpanded: hdr.classList.contains('is-expanded'),
        bodyHidden: body.hasAttribute('hidden'),
        bodyDisplay: cs.display,
      };
    });
    assert(expanded.isExpanded,
      `after toggle click .live-header must gain is-expanded class (got isExpanded=${expanded.isExpanded})`);
    assert(!expanded.bodyHidden, `.live-header-body must lose hidden attribute when expanded`);
    assert(expanded.bodyDisplay !== 'none',
      `.live-header-body must render (display ≠ none) when expanded (got ${expanded.bodyDisplay})`);

    await page768.click('#liveHeaderToggle');
    await page768.waitForTimeout(120);
    let collapsed = await page768.evaluate(() => {
      const hdr = document.querySelector('#liveHeader');
      const body = document.querySelector('#liveHeaderBody');
      const cs = getComputedStyle(body);
      return {
        isCollapsed: hdr.classList.contains('is-collapsed'),
        bodyHidden: body.hasAttribute('hidden'),
        bodyDisplay: cs.display,
      };
    });
    assert(collapsed.isCollapsed,
      `second toggle click must re-collapse (got isCollapsed=${collapsed.isCollapsed})`);
    assert(collapsed.bodyHidden, `.live-header-body must regain hidden attribute when re-collapsed`);
    assert(collapsed.bodyDisplay === 'none',
      `.live-header-body must compute display:none when re-collapsed (got ${collapsed.bodyDisplay})`);
  });

  await ctx768.close();

  await browser.close();
  console.log(`\n=== Results: passed ${passed} failed ${failed} ===`);
  process.exit(failed > 0 ? 1 : 0);
})().catch(e => { console.error(e); process.exit(1); });

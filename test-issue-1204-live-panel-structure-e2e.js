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

  // ── Narrow viewport — 640px (flex-wrap regime) ──────────────────────────
  // CSS contract under test (live.css @media max-width:640px):
  //   .live-header { flex-wrap: wrap; ... max-width: calc(100vw - 16px) }
  // With base flex-direction: row from r0 fix, wrap must produce children
  // that fit within the header's width (no horizontal overflow) and both
  // the critical strip and stats row must remain visible.
  const ctx640 = await browser.newContext({ viewport: { width: 640, height: 900 } });
  const page640 = await ctx640.newPage();
  page640.setDefaultTimeout(8000);
  page640.on('pageerror', (e) => console.error('[pageerror]', e.message));
  await step('[640x900] navigate to /live', async () => { await gotoLive(page640); });

  // Collapsed default (≤768px also covers 640px): critical strip + toggle
  // are visible inline; .live-title sits inside .live-header-body, so verify
  // it once we expand. Wrap behavior matters in both states because the
  // base rule is flex-direction: row.
  await step('[640x900] collapsed state: critical + toggle inline, no horizontal overflow', async () => {
    const r = await page640.evaluate(() => {
      const hdr = document.querySelector('.live-header');
      const crit = document.querySelector('.live-header-critical');
      const tog = document.querySelector('#liveHeaderToggle');
      const pkt = document.querySelector('#livePktCount');
      if (!hdr || !crit || !tog || !pkt) {
        return { found: false, hdr: !!hdr, crit: !!crit, tog: !!tog, pkt: !!pkt };
      }
      const cs = getComputedStyle(hdr);
      const cRect = crit.getBoundingClientRect();
      const pRect = pkt.getBoundingClientRect();
      return {
        found: true,
        flexWrap: cs.flexWrap,
        flexDirection: cs.flexDirection,
        overflowX: hdr.scrollWidth - hdr.clientWidth,
        critVisible: cRect.width > 0 && cRect.height > 0,
        pktVisible: pRect.width > 0 && pRect.height > 0,
      };
    });
    assert(r.found, `missing element (hdr=${r.hdr}, crit=${r.crit}, tog=${r.tog}, pkt=${r.pkt})`);
    assert(r.flexWrap === 'wrap',
      `.live-header at 640px must have flex-wrap: wrap (got ${r.flexWrap}); ` +
      `@media (max-width:640px) rule failed to apply`);
    assert(r.flexDirection === 'row',
      `.live-header at 640px must keep flex-direction: row from base rule (got ${r.flexDirection})`);
    // Allow 1px sub-pixel slop. Real horizontal overflow = bug.
    assert(r.overflowX <= 1,
      `.live-header must not overflow horizontally at 640px ` +
      `(scrollWidth - clientWidth = ${r.overflowX}px); wrap should keep children inside`);
    assert(r.critVisible, '.live-header-critical (beacon + pkt count) must remain visible at 640px');
    assert(r.pktVisible, '#livePktCount must remain visible at 640px (counter cohesion)');
  });

  // Expanded state at 640px — the actual wrap scenario worth gating:
  // body becomes visible alongside the critical strip, and the row must
  // wrap to fit width. Title now lives in the rendered tree.
  await step('[640x900] expanded state: header wraps, critical + title both visible, no overflow', async () => {
    await page640.click('#liveHeaderToggle');
    await page640.waitForTimeout(120);
    const r = await page640.evaluate(() => {
      const hdr = document.querySelector('.live-header');
      const crit = document.querySelector('.live-header-critical');
      const title = document.querySelector('.live-title');
      const pkt = document.querySelector('#livePktCount');
      if (!hdr || !crit || !title || !pkt) {
        return { found: false, hdr: !!hdr, crit: !!crit, title: !!title, pkt: !!pkt };
      }
      const cs = getComputedStyle(hdr);
      const cRect = crit.getBoundingClientRect();
      const tRect = title.getBoundingClientRect();
      const pRect = pkt.getBoundingClientRect();
      return {
        found: true,
        flexWrap: cs.flexWrap,
        flexDirection: cs.flexDirection,
        overflowX: hdr.scrollWidth - hdr.clientWidth,
        critVisible: cRect.width > 0 && cRect.height > 0,
        titleVisible: tRect.width > 0 && tRect.height > 0,
        pktVisible: pRect.width > 0 && pRect.height > 0,
      };
    });
    assert(r.found, `missing element (hdr=${r.hdr}, crit=${r.crit}, title=${r.title}, pkt=${r.pkt})`);
    assert(r.flexWrap === 'wrap', `.live-header expanded at 640px must wrap (got ${r.flexWrap})`);
    assert(r.flexDirection === 'row',
      `.live-header expanded at 640px must keep flex-direction: row (got ${r.flexDirection})`);
    assert(r.overflowX <= 1,
      `.live-header expanded must not overflow horizontally at 640px ` +
      `(scrollWidth - clientWidth = ${r.overflowX}px)`);
    assert(r.critVisible, '.live-header-critical must remain visible when expanded at 640px');
    assert(r.titleVisible, '.live-title must be visible when header body is expanded at 640px');
    assert(r.pktVisible, '#livePktCount must remain visible (counter + title cohesion)');
  });

  await ctx640.close();

  // ── Narrow viewport — 768px (is-collapsed regime) ───────────────────────
  // CSS contract under test (live.css @media max-width:768px):
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

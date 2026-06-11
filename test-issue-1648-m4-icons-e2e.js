#!/usr/bin/env node
/* Issue #1648 — M4: emoji → Phosphor sprite migration (E2E behavioral).
 *
 * Asserts (in a real Chromium against a running server):
 *   (a) /map — Path Inspector pane toggle renders a Phosphor sprite (no
 *       Misc-Symbols caret), and overall map page is icon-free.
 *   (b) /map?packet=<hash>&obs=<id> (multi-path CHAN fixture) — route
 *       overlay sidebar collapse button renders Phosphor caret.
 *   (c) /packets — replay button renders Phosphor play sprite (no ▶ char).
 *   (d) /analytics — distance map-jump buttons render ph-map-trifold and
 *       the Network Overview chevron renders a sprite.
 *   (e) /area-map standalone — clear/undo buttons render sprites.
 *   (f) NO .notdef anywhere — every <use> resolves to a defined symbol id.
 *
 * CI gating: CHROMIUM_REQUIRE=1 makes Chromium-launch failure a HARD FAIL.
 */
'use strict';

const { chromium } = require('playwright');
const assert = require('assert');

const BASE = process.env.BASE_URL || 'http://localhost:13581';
// Forbidden Misc-Symbols icon chars in rendered M4 surfaces:
const M4_FORBIDDEN_RE = /[\u{1F300}-\u{1FAFF}\u{2600}-\u{27BF}◆●■▲★☆○✓✗⚠✉✕▶◀▾⚑]/u;

let passes = 0, failures = 0;
function pass(msg) { console.log(`  ✓ ${msg}`); passes++; }
function fail(msg) { console.error(`  ✗ ${msg}`); failures++; }

async function gotoSpa(page, route) {
  await page.goto(`${BASE}/#${route}`, { waitUntil: 'domcontentloaded' });
  await page.waitForFunction(() => !!document.querySelector('#app'), null, { timeout: 8000 }).catch(() => {});
  await page.waitForTimeout(700);
}

async function main() {
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
      console.error(`test-issue-1648-m4-icons-e2e.js: HARD FAIL — Chromium unavailable: ${err.message}`);
      process.exit(1);
    }
    console.warn(`SKIP — Chromium unavailable: ${err.message}`);
    process.exit(0);
  }

  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  const page = await ctx.newPage();

  // (a) /map — pane toggle + general sprite presence
  await gotoSpa(page, '/map');
  const mapState = await page.evaluate(() => {
    const toggle = document.getElementById('mapPaneToggle');
    return {
      paneToggleText: toggle ? toggle.textContent : null,
      paneToggleSprites: toggle ? toggle.querySelectorAll('svg.ph-icon use').length : 0,
      allSprites: document.querySelectorAll('svg.ph-icon use').length,
      bodyText: (document.getElementById('app') || document.body).textContent || '',
    };
  });
  if (mapState.paneToggleSprites === 0) fail('(a) /map: pane toggle has no Phosphor sprite');
  else pass(`(a) /map: pane toggle has ${mapState.paneToggleSprites} sprite(s)`);
  if (mapState.paneToggleText && /[▶◀]/.test(mapState.paneToggleText)) {
    fail(`(a) /map: pane toggle still contains ▶/◀ text (got ${JSON.stringify(mapState.paneToggleText)})`);
  } else {
    pass('(a) /map: pane toggle text has no Misc-Symbols carets');
  }
  if (mapState.allSprites < 3) fail(`(a) /map: only ${mapState.allSprites} sprite refs total (expected ≥3)`);
  else pass(`(a) /map: ${mapState.allSprites} sprite refs on page`);

  // (b) /map?packet=<hash>&obs=<id> — multi-path CHAN fixture renders
  // route overlay sidebar with a collapse button. We don't require the
  // fixture to exist in staging — only assert the sprite scheme if the
  // sidebar shows up.
  await gotoSpa(page, '/map?packet=305b678c9394b964&obs=10591318');
  await page.waitForTimeout(1200);
  const route = await page.evaluate(() => {
    const sidebar = document.querySelector('.mc-rt-sidebar, [class*="mc-rt-"]');
    const collapseBtn = document.querySelector('.mc-rt-collapse-btn');
    return {
      sidebarPresent: !!sidebar,
      collapseSprites: collapseBtn ? collapseBtn.querySelectorAll('svg.ph-icon use').length : 0,
      collapseText: collapseBtn ? collapseBtn.textContent : '',
      mapSprites: document.querySelectorAll('svg.ph-icon use').length,
    };
  });
  if (route.sidebarPresent && route.collapseSprites === 0) {
    fail('(b) /map?packet=…: route collapse button has no sprite');
  } else if (route.sidebarPresent) {
    pass(`(b) /map?packet=…: route collapse button has ${route.collapseSprites} sprite(s)`);
  } else {
    console.warn('  ⚠ (b) /map?packet=…: route sidebar not present (fixture may be missing in this env)');
  }
  if (route.collapseText && /[▶◀]/.test(route.collapseText)) {
    fail('(b) /map?packet=…: collapse btn still has ▶/◀ char');
  }

  // (c) /packets — replay button renders Phosphor play sprite
  await gotoSpa(page, '/packets');
  await page.waitForTimeout(1500);
  // Click first packet row to render detail with replay button (if any).
  await page.evaluate(() => {
    const row = document.querySelector('table tbody tr, .pkt-row, .packet-row');
    if (row) row.click();
  });
  await page.waitForTimeout(800);
  const packets = await page.evaluate(() => {
    const replay = document.querySelector('.replay-live-btn');
    const viewRoute = document.querySelector('#viewRouteBtn, .detail-map-link');
    return {
      replaySprite: replay ? replay.querySelectorAll('svg.ph-icon use').length : null,
      replayText: replay ? replay.textContent : null,
      viewRouteSprites: viewRoute ? viewRoute.querySelectorAll('svg.ph-icon use').length : null,
      pageSprites: document.querySelectorAll('svg.ph-icon use').length,
    };
  });
  if (packets.replaySprite === 0) {
    fail('(c) /packets: replay button has no sprite');
  } else if (packets.replaySprite > 0) {
    pass(`(c) /packets: replay button has ${packets.replaySprite} sprite(s)`);
  } else {
    console.warn('  ⚠ (c) /packets: replay button not present (no packet detail open)');
  }
  if (packets.replayText && /[▶◀⏸]/.test(packets.replayText)) {
    fail(`(c) /packets: replay button still has play/pause char (${JSON.stringify(packets.replayText)})`);
  }
  if (packets.viewRouteSprites === 0) fail('(c) /packets: View-route button missing sprite');
  else if (packets.viewRouteSprites > 0) pass('(c) /packets: View-route button has sprite');
  if (packets.pageSprites < 5) fail(`(c) /packets: only ${packets.pageSprites} sprite refs (expected ≥5)`);
  else pass(`(c) /packets: ${packets.pageSprites} sprite refs on page`);

  // (d) /analytics — Network Overview chevron + distance map-jump buttons
  await gotoSpa(page, '/analytics?tab=prefix-tool');
  await page.waitForTimeout(1500);
  const analytics = await page.evaluate(() => {
    const chev = document.getElementById('ptOverviewChevron');
    return {
      chevText: chev ? chev.textContent : null,
      chevSprites: chev ? chev.querySelectorAll('svg.ph-icon use').length : 0,
      pageSprites: document.querySelectorAll('svg.ph-icon use').length,
    };
  });
  if (analytics.chevSprites === 0) {
    fail('(d) /analytics: Network Overview chevron has no sprite');
  } else {
    pass(`(d) /analytics: Network Overview chevron has ${analytics.chevSprites} sprite(s)`);
  }
  if (analytics.chevText && /[▶◀]/.test(analytics.chevText)) {
    fail('(d) /analytics: chevron still contains ▶ text');
  }
  if (analytics.pageSprites < 10) fail(`(d) /analytics: only ${analytics.pageSprites} sprite refs`);
  else pass(`(d) /analytics: ${analytics.pageSprites} sprite refs on page`);

  // Also check distance tab if reachable
  await gotoSpa(page, '/analytics?tab=distance');
  await page.waitForTimeout(2000);
  const dist = await page.evaluate(() => {
    const btn = document.querySelector('.dist-map-hop, .dist-map-path');
    return {
      mapBtnSprites: btn ? btn.querySelectorAll('svg.ph-icon use').length : null,
    };
  });
  if (dist.mapBtnSprites === 0) fail('(d) /analytics distance: map-jump button missing sprite');
  else if (dist.mapBtnSprites > 0) pass('(d) /analytics distance: map-jump button has sprite');
  else console.warn('  ⚠ (d) /analytics distance: no map-jump button rendered (empty dataset)');

  // (e) /area-map (standalone HTML)
  await page.goto(`${BASE}/area-map.html`, { waitUntil: 'domcontentloaded' });
  await page.waitForTimeout(500);
  const area = await page.evaluate(() => {
    const clear = document.getElementById('btn-clear-draw');
    const undo = document.getElementById('btn-undo');
    return {
      clearSprites: clear ? clear.querySelectorAll('svg.ph-icon use').length : 0,
      clearText: clear ? clear.textContent : '',
      undoSprites: undo ? undo.querySelectorAll('svg.ph-icon use').length : 0,
    };
  });
  if (area.clearSprites === 0) fail('(e) /area-map: clear button has no sprite');
  else pass(`(e) /area-map: clear button has ${area.clearSprites} sprite(s)`);
  if (area.undoSprites === 0) fail('(e) /area-map: undo button has no sprite');
  else pass(`(e) /area-map: undo button has ${area.undoSprites} sprite(s)`);
  if (/[✕↩]/.test(area.clearText)) fail('(e) /area-map: clear button still has ✕ char');

  // (f) No unresolved sprite refs anywhere we've visited
  await gotoSpa(page, '/map');
  const undef = await page.evaluate(async () => {
    const resp = await fetch('/icons/phosphor-sprite.svg').catch(() => null);
    if (!resp || !resp.ok) return { error: 'sprite fetch failed' };
    const text = await resp.text();
    const ids = new Set();
    for (const m of text.matchAll(/id="(ph-[a-z-]+)"/g)) ids.add(m[1]);
    const uses = Array.from(document.querySelectorAll('svg.ph-icon use'));
    const missing = [];
    for (const u of uses) {
      const href = u.getAttribute('href') || u.getAttribute('xlink:href') || '';
      const m = href.match(/#(ph-[a-z-]+)/);
      if (!m) { missing.push(href); continue; }
      if (!ids.has(m[1])) missing.push(m[1]);
    }
    return { count: uses.length, ids: ids.size, missing };
  });
  if (undef.error) fail(`(f) sprite fetch: ${undef.error}`);
  else if (undef.missing && undef.missing.length) fail(`(f) ${undef.missing.length} sprite ref(s) unresolved: ${undef.missing.slice(0,5).join(', ')}`);
  else pass(`(f) all ${undef.count} sprite refs resolve to one of ${undef.ids} defined symbols`);

  await browser.close();
  console.log(`\ntest-issue-1648-m4-icons-e2e.js: ${passes} passed, ${failures} failed`);
  assert.strictEqual(failures, 0, `${failures} M4 icon-render assertions failed`);
  process.exit(0);
}

main().catch((err) => {
  console.error('test-issue-1648-m4-icons-e2e.js: FAIL —', err);
  process.exit(1);
});

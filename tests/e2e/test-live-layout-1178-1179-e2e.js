/**
 * E2E tests for #1178 (Live header compactness + collapse toggle)
 * and #1179 (Live controls pinned bottom-right + collapse toggle).
 *
 * Run: BASE_URL=http://localhost:13581 node test-live-layout-1178-1179-e2e.js
 *
 * Assertions:
 *   Desktop (1440x900):
 *     (a) .live-header bounding-rect height ≤ 40px.
 *     (b) .live-controls computed position is 'fixed' or 'absolute';
 *         right ≤ 24px; bottom is non-zero (safe-area / nav reservation).
 *   Narrow (360x800):
 *     (c) [data-live-header-toggle] visible; live-stats body hidden until click.
 *     (d) Clicking header toggle reveals the stats body.
 *     (e) [data-live-controls-toggle] visible; controls body hidden until click.
 *     (f) Clicking controls toggle reveals controls; expanded panel bottom +8 <
 *         (window.innerHeight − bottomNavHeight). Bottom-nav height defaults
 *         to 56 if .bottom-nav is not present in the DOM.
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

  console.log(`\n=== #1178/#1179 live layout E2E against ${BASE} ===`);

  // ───── Desktop assertions ─────
  {
    const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } });
    const page = await ctx.newPage();
    page.setDefaultTimeout(8000);
    page.on('pageerror', (e) => console.error('[pageerror]', e.message));
    await step('[1440x900] navigate to /live', async () => { await gotoLive(page); });

    // (a) #1178 critical row (beacon + pkt count) stays compact ≤ 40px.
    //     The header itself wraps post-#1205 to host #liveControls as a
    //     second flex row, so we assert on the critical strip instead of
    //     the whole header bounding rect.
    await step('[1440x900] .live-header-critical bounding-rect height ≤ 40px', async () => {
      const h = await page.$eval('.live-header-critical', el => el.getBoundingClientRect().height);
      assert(h <= 40, `expected ≤40px, got ${h}px`);
    });

    // (b) #1205 retired the "fixed/absolute bottom-right" contract.
    //     New contract: .live-controls is a static child of #liveHeader.
    await step('[1440x900] .live-controls is in-flow inside #liveHeader (#1205)', async () => {
      const info = await page.evaluate(() => {
        const el = document.querySelector('.live-controls');
        const hdr = document.getElementById('liveHeader');
        if (!el || !hdr) return null;
        const cs = getComputedStyle(el);
        return { position: cs.position, inHeader: hdr.contains(el) };
      });
      assert(info, '.live-controls or #liveHeader not found');
      assert(info.position === 'static',
        `.live-controls position must be 'static' after #1205, got ${info.position}`);
      assert(info.inHeader, '.live-controls must be a descendant of #liveHeader (#1205)');
    });

    await ctx.close();
  }

  // ───── Narrow assertions ─────
  {
    const ctx = await browser.newContext({ viewport: { width: 360, height: 800 } });
    const page = await ctx.newPage();
    page.setDefaultTimeout(8000);
    page.on('pageerror', (e) => console.error('[pageerror]', e.message));
    await step('[360x800] navigate to /live', async () => { await gotoLive(page); });

    // (c0) Mesh-Operator review #1180: beacon + pkt count must remain visible
    //      at narrow widths. Post-#1234 the header body no longer collapses
    //      on mobile (chart toggle was dropped); stats are inline always.
    await step('[360x800] beacon + pkt count visible in compact header', async () => {
      const r = await page.evaluate(() => {
        const beacon = document.querySelector('.live-beacon');
        const pkt = document.querySelector('#livePktCount');
        function vis(el) {
          if (!el) return false;
          const cs = getComputedStyle(el);
          if (cs.display === 'none' || cs.visibility === 'hidden') return false;
          const rect = el.getBoundingClientRect();
          return rect.width > 0 && rect.height > 0;
        }
        return {
          beaconVisible: vis(beacon),
          pktVisible: vis(pkt),
          pktPillVisible: vis(pkt && pkt.closest('.live-stat-pill')),
        };
      });
      assert(r.beaconVisible, '.live-beacon must remain visible at narrow widths');
      assert(r.pktVisible, '#livePktCount must remain visible at narrow widths');
      assert(r.pktPillVisible, 'pkt-count pill must remain visible at narrow widths');
    });

    // (c1) Post-#1234: controls (gear) toggle shrunk to 36×36 on mobile so
    //      it fits inside the ≤44px single-row header. Assert ≥36×36 floor.
    await step('[360x800] controls toggle ≥36×36 tap target (post-#1234 mobile shrink)', async () => {
      const r = await page.evaluate(() => {
        function box(sel) {
          const el = document.querySelector(sel);
          if (!el) return null;
          const rect = el.getBoundingClientRect();
          return { w: rect.width, h: rect.height };
        }
        return { controls: box('[data-live-controls-toggle]') };
      });
      assert(r.controls, '[data-live-controls-toggle] not found');
      assert(r.controls.w >= 36 && r.controls.h >= 36,
        `controls toggle must be ≥36×36, got ${r.controls.w}×${r.controls.h}`);
    });

    // (c) Post-#1234: header chart toggle dropped on mobile; stats body
    //     renders inline as part of the single-row header. Assert hidden.
    await step('[360x800] header chart toggle hidden on mobile (#1234)', async () => {
      const r = await page.evaluate(() => {
        const tog = document.querySelector('[data-live-header-toggle]');
        if (!tog) return { tog: false };
        const cs = getComputedStyle(tog);
        return { tog: true, display: cs.display };
      });
      assert(r.tog, '[data-live-header-toggle] not found in DOM');
      assert(r.display === 'none',
        `chart toggle must be display:none on mobile post-#1234 (got ${r.display})`);
    });

    // (d) Post-#1234: stats row is a direct child of .live-header on mobile;
    //     .live-header-body (now only the hidden MESH LIVE title) is fully
    //     collapsed via display:none. Assert that stats row is visible inline.
    await step('[360x800] stats row inline post-#1234 (#liveNodeCount visible)', async () => {
      const visible = await page.evaluate(() => {
        const stats = document.querySelector('[data-live-stats-row], .live-stats-row');
        const nodeCount = document.querySelector('#liveNodeCount');
        if (!stats || !nodeCount) return false;
        const cs = getComputedStyle(stats);
        if (cs.display === 'none') return false;
        const rect = nodeCount.getBoundingClientRect();
        return rect.width > 0 && rect.height > 0;
      });
      assert(visible, 'stats row must be inline + visible on mobile post-#1234');
    });

    // (e)
    await step('[360x800] controls toggle visible; controls body hidden until click', async () => {
      const r = await page.evaluate(() => {
        const tog = document.querySelector('[data-live-controls-toggle]');
        const body = document.querySelector('[data-live-controls-body]');
        if (!tog || !body) return { tog: !!tog, body: !!body };
        const togVis = getComputedStyle(tog).display !== 'none' &&
                       getComputedStyle(tog).visibility !== 'hidden';
        const bodyHidden = body.hasAttribute('hidden') ||
                           getComputedStyle(body).display === 'none';
        return { tog: true, body: true, togVis, bodyHidden };
      });
      assert(r.tog, '[data-live-controls-toggle] not found');
      assert(r.body, '[data-live-controls-body] not found');
      assert(r.togVis, '[data-live-controls-toggle] not visible at 360px');
      assert(r.bodyHidden, 'controls body must be hidden until toggle click');
    });

    // (f)
    await step('[360x800] clicking controls toggle reveals; no overlap with bottom-nav region', async () => {
      await page.click('[data-live-controls-toggle]');
      const r = await page.evaluate(() => {
        const root = document.querySelector('.live-controls');
        const body = document.querySelector('[data-live-controls-body]');
        const nav = document.querySelector('.bottom-nav');
        const navH = nav ? nav.getBoundingClientRect().height : 56;
        const bodyVisible = body && !body.hasAttribute('hidden') &&
                            getComputedStyle(body).display !== 'none';
        const expandedRect = root ? root.getBoundingClientRect() : null;
        return {
          bodyVisible,
          expandedBottom: expandedRect ? expandedRect.bottom : null,
          innerH: window.innerHeight,
          navH,
          isExpandedClass: root ? root.classList.contains('is-expanded') : false,
        };
      });
      assert(r.bodyVisible, 'controls body not visible after click');
      assert(r.expandedBottom !== null, '.live-controls element missing');
      assert(r.expandedBottom + 8 < r.innerH - r.navH,
        `expanded panel bottom (${r.expandedBottom}) + 8 must be < innerHeight (${r.innerH}) − navH (${r.navH})`);
    });

    await ctx.close();
  }

  await browser.close();
  console.log(`\n=== Results: passed ${passed} failed ${failed} ===`);
  process.exit(failed > 0 ? 1 : 0);
})().catch(e => { console.error(e); process.exit(1); });

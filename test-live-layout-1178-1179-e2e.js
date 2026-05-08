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

    // (a)
    await step('[1440x900] .live-header bounding-rect height ≤ 40px', async () => {
      const h = await page.$eval('.live-header', el => el.getBoundingClientRect().height);
      assert(h <= 40, `expected ≤40px, got ${h}px`);
    });

    // (b)
    await step('[1440x900] .live-controls fixed/absolute, right ≤ 24px, bottom > 0', async () => {
      const info = await page.evaluate(() => {
        const el = document.querySelector('.live-controls');
        if (!el) return null;
        const cs = getComputedStyle(el);
        const r = el.getBoundingClientRect();
        return {
          position: cs.position,
          right: parseFloat(cs.right),
          bottom: parseFloat(cs.bottom),
          rectRight: r.right,
          vw: window.innerWidth,
        };
      });
      assert(info, '.live-controls element not found');
      assert(info.position === 'fixed' || info.position === 'absolute',
        `.live-controls position must be fixed/absolute, got ${info.position}`);
      assert(info.right <= 24, `.live-controls right must be ≤24px, got ${info.right}px`);
      assert(info.bottom > 0,
        `.live-controls bottom must reserve space for safe-area/nav, got ${info.bottom}px`);
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

    // (c)
    await step('[360x800] header toggle visible; stats body hidden until click', async () => {
      const r = await page.evaluate(() => {
        const tog = document.querySelector('[data-live-header-toggle]');
        const body = document.querySelector('[data-live-header-body]');
        if (!tog || !body) return { tog: !!tog, body: !!body };
        const togVis = getComputedStyle(tog).display !== 'none' &&
                       getComputedStyle(tog).visibility !== 'hidden';
        const bodyHidden = body.hasAttribute('hidden') ||
                           getComputedStyle(body).display === 'none';
        return { tog: true, body: true, togVis, bodyHidden };
      });
      assert(r.tog, '[data-live-header-toggle] not found');
      assert(r.body, '[data-live-header-body] not found');
      assert(r.togVis, '[data-live-header-toggle] not visible at 360px');
      assert(r.bodyHidden, 'stats body must be hidden until toggle click');
    });

    // (d)
    await step('[360x800] clicking header toggle reveals stats body', async () => {
      await page.click('[data-live-header-toggle]');
      const visible = await page.evaluate(() => {
        const body = document.querySelector('[data-live-header-body]');
        if (!body) return false;
        return !body.hasAttribute('hidden') && getComputedStyle(body).display !== 'none';
      });
      assert(visible, 'stats body not visible after click');
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

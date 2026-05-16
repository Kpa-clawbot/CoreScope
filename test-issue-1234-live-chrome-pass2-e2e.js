/**
 * E2E for #1234 — Live page mobile chrome-reduction pass 2.
 *
 * At 375x800 the Live page must:
 *   (1) render `.live-header` as a single row, height ≤44px
 *   (2) hide the top `.top-nav` (display:none) on /live route at ≤640px
 *   (3) collapse VCR scope buttons >6h into one overflow `More` dropdown;
 *       the inline button count (excluding the dropdown menu) must be ≤3
 *       (currently 1h + 6h + More on mobile).
 *
 * Desktop (≥768px) sanity: top-nav visible, all 4 scope buttons visible
 * (More button hidden).
 *
 * Run: BASE_URL=http://localhost:13581 node test-issue-1234-live-chrome-pass2-e2e.js
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
  await page.waitForTimeout(500);
}

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });

  console.log(`\n=== #1234 Live mobile chrome pass 2 E2E against ${BASE} ===`);

  // ── Mobile 375x800 ──────────────────────────────────────────────────────
  {
    const ctx = await browser.newContext({ viewport: { width: 375, height: 800 } });
    const page = await ctx.newPage();
    page.setDefaultTimeout(8000);
    page.on('pageerror', (e) => console.error('[pageerror]', e.message));
    await step('[375x800] navigate to /live', async () => { await gotoLive(page); });

    // (1) Single-row header, height ≤44px.
    await step('[375x800] .live-header height ≤44px (single row, no MESH LIVE label, no chart toggle)', async () => {
      const r = await page.evaluate(() => {
        const h = document.getElementById('liveHeader');
        const r = h.getBoundingClientRect();
        const title = document.querySelector('.live-title');
        const titleVisible = title && getComputedStyle(title).display !== 'none' &&
                             title.getBoundingClientRect().height > 0;
        const chartBtn = document.getElementById('liveHeaderToggle');
        const chartVisible = chartBtn && getComputedStyle(chartBtn).display !== 'none' &&
                             chartBtn.getBoundingClientRect().height > 0;
        return { height: r.height, titleVisible, chartVisible };
      });
      assert(r.height <= 44, `live-header height must be ≤44px (got ${r.height}px)`);
      assert(!r.titleVisible, 'MESH LIVE title label must not be visible at 375px');
      assert(!r.chartVisible, 'chart-icon header toggle (📊) must not be visible at 375px');
    });

    // (2) Top nav hidden on /live route at mobile.
    await step('[375x800] top-nav hidden on /live route', async () => {
      const r = await page.evaluate(() => {
        const nav = document.querySelector('.top-nav');
        if (!nav) return { found: false };
        const cs = getComputedStyle(nav);
        const rect = nav.getBoundingClientRect();
        return { found: true, display: cs.display, height: rect.height };
      });
      assert(r.found, '.top-nav element missing');
      assert(r.display === 'none',
        `.top-nav must be display:none on /live at ≤640px (got display=${r.display}, height=${r.height})`);
    });

    // (3) VCR scope buttons: >6h collapsed into overflow.
    await step('[375x800] VCR scope row: ≤3 inline buttons (1h, 6h, More); 12h/24h hidden inline', async () => {
      const r = await page.evaluate(() => {
        const container = document.querySelector('.vcr-scope-btns');
        if (!container) return { found: false };
        function vis(el) {
          if (!el) return false;
          const cs = getComputedStyle(el);
          if (cs.display === 'none' || cs.visibility === 'hidden') return false;
          const rect = el.getBoundingClientRect();
          return rect.width > 0 && rect.height > 0;
        }
        const scopeBtns = Array.from(container.querySelectorAll('.vcr-scope-btn'));
        const more = container.querySelector('.vcr-scope-more, [data-vcr-scope-more]');
        const inlineScopes = scopeBtns.filter(vis).map(b => b.dataset.scope);
        return {
          found: true,
          inlineScopes,
          moreVisible: vis(more),
          totalInline: inlineScopes.length + (vis(more) ? 1 : 0),
        };
      });
      assert(r.found, '.vcr-scope-btns container missing');
      assert(r.moreVisible, 'More overflow button must be visible at 375px');
      assert(r.totalInline <= 3,
        `inline VCR scope row must have ≤3 buttons at 375px (got ${r.totalInline}: ${JSON.stringify(r.inlineScopes)} + more=${r.moreVisible})`);
      // 12h and 24h must NOT be inline (they live in the More dropdown).
      assert(!r.inlineScopes.includes('43200000'),
        `12h scope button must not be inline at 375px (got inline: ${JSON.stringify(r.inlineScopes)})`);
      assert(!r.inlineScopes.includes('86400000'),
        `24h scope button must not be inline at 375px (got inline: ${JSON.stringify(r.inlineScopes)})`);
    });

    await ctx.close();
  }

  // ── Desktop 1280x800 sanity ─────────────────────────────────────────────
  {
    const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 } });
    const page = await ctx.newPage();
    page.setDefaultTimeout(8000);
    await step('[1280x800] navigate to /live', async () => { await gotoLive(page); });

    await step('[1280x800] top-nav visible (desktop unaffected)', async () => {
      const r = await page.evaluate(() => {
        const nav = document.querySelector('.top-nav');
        const cs = nav && getComputedStyle(nav);
        return { display: cs && cs.display, height: nav && nav.getBoundingClientRect().height };
      });
      assert(r.display !== 'none', `.top-nav must remain visible on desktop (got display=${r.display})`);
      assert(r.height >= 40, `.top-nav must have nonzero height on desktop (got ${r.height})`);
    });

    await step('[1280x800] all 4 VCR scopes visible inline on desktop', async () => {
      const r = await page.evaluate(() => {
        const btns = Array.from(document.querySelectorAll('.vcr-scope-btns .vcr-scope-btn'));
        function vis(el) {
          const cs = getComputedStyle(el);
          if (cs.display === 'none' || cs.visibility === 'hidden') return false;
          const rect = el.getBoundingClientRect();
          return rect.width > 0 && rect.height > 0;
        }
        return {
          visibleScopes: btns.filter(vis).map(b => b.dataset.scope),
        };
      });
      assert(r.visibleScopes.length === 4,
        `desktop must show 4 inline scope buttons (got ${r.visibleScopes.length}: ${JSON.stringify(r.visibleScopes)})`);
    });

    await ctx.close();
  }

  await browser.close();
  console.log(`\n=== Results: passed ${passed} failed ${failed} ===`);
  process.exit(failed > 0 ? 1 : 0);
})().catch(e => { console.error(e); process.exit(1); });

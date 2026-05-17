/**
 * E2E for #1244 — Live mobile (375x800):
 *   A) VCR controls must lay out as a single row of children inside
 *      `.vcr-bar` (no wrap) — all direct children share a common top
 *      coordinate (within tolerance).
 *   B) First-visit gesture hints (`.gesture-hint`, "Got it" pills from
 *      PR #1186) must NOT be present on the /live route — they get
 *      buried below the VCR bar + safe-area + (potential) bottom nav
 *      and read as orphan litter. Fixed by disabling gesture hints
 *      on /live entirely (option (a) in the issue).
 *
 * Desktop (1280x800) sanity: VCR still renders, no regression to the
 * existing single-row desktop layout.
 *
 * Run: BASE_URL=http://localhost:13581 node test-issue-1244-live-vcr-row-hints-e2e.js
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

async function gotoLive(page) {
  // Reset gesture-hint localStorage so the first-visit pills WOULD fire
  // (they're suppressed once "seen"). Otherwise the test is a no-op for
  // sub-issue B on a previously-visited fixture.
  await page.goto(BASE + '/', { waitUntil: 'domcontentloaded' });
  await page.evaluate(() => {
    try {
      Object.keys(localStorage)
        .filter(k => k.indexOf('meshcore-gesture-hints-') === 0)
        .forEach(k => localStorage.removeItem(k));
    } catch (_e) {}
  });
  await page.goto(BASE + '/#/live', { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('#vcrBar', { timeout: 10000 });
  // Wait past gesture-hints SHOW_DELAY_MS (800ms) so any hint that
  // _would_ render has had its chance to appear.
  await page.waitForTimeout(1500);
}

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });

  console.log(`\n=== #1244 Live mobile VCR single row + no orphan hints E2E against ${BASE} ===`);

  // ── Mobile 375x800 ──────────────────────────────────────────────────────
  {
    const ctx = await browser.newContext({ viewport: { width: 375, height: 800 } });
    const page = await ctx.newPage();
    page.setDefaultTimeout(10000);
    page.on('pageerror', (e) => console.error('[pageerror]', e.message));
    await step('[375x800] navigate to /live', async () => { await gotoLive(page); });

    // (A) VCR-bar direct children share one row (no wrap).
    await step('[375x800] .vcr-bar direct children share top coordinate (single row, no wrap)', async () => {
      const r = await page.evaluate(() => {
        const bar = document.getElementById('vcrBar');
        if (!bar) return { found: false };
        const cs = getComputedStyle(bar);
        const kids = Array.from(bar.children).filter(el => {
          const k = getComputedStyle(el);
          if (k.display === 'none' || k.visibility === 'hidden') return false;
          if (el.id === 'panelPositionAnnounce') return false; // sr-only sibling
          if (el.classList && el.classList.contains('sr-only')) return false;
          if (el.id === 'vcrPrompt' && el.classList.contains('hidden')) return false;
          const rr = el.getBoundingClientRect();
          return rr.width > 0 && rr.height > 0;
        });
        const tops = kids.map(el => Math.round(el.getBoundingClientRect().top));
        const minTop = Math.min.apply(null, tops);
        const maxTop = Math.max.apply(null, tops);
        return {
          found: true,
          flexWrap: cs.flexWrap,
          flexDirection: cs.flexDirection,
          tops, minTop, maxTop,
          spread: maxTop - minTop,
          kidTags: kids.map(el => (el.tagName + (el.id ? '#' + el.id : '') + '.' +
                                   (typeof el.className === 'string' ? el.className : '')).trim()),
        };
      });
      assert(r.found, '#vcrBar element missing');
      // Either nowrap is set (canonical fix) OR all children genuinely share
      // the same top (≤8px spread for sub-pixel + line-height jitter).
      const sharedRow = r.spread <= 8;
      assert(r.flexWrap === 'nowrap' || sharedRow,
        '#vcr-bar must lay children out on a single row at 375x800 ' +
        '(flexWrap=' + r.flexWrap + ', topSpread=' + r.spread + 'px, ' +
        'tops=' + JSON.stringify(r.tops) + ', children=' + JSON.stringify(r.kidTags) + ')');
    });

    // (B) No orphan gesture-hint pills on /live.
    await step('[375x800] no .gesture-hint pills on /live route (option (a) — disabled on Live)', async () => {
      const r = await page.evaluate(() => {
        const hints = Array.from(document.querySelectorAll('.gesture-hint, [data-gesture-hint]'));
        const vh = window.innerHeight;
        return {
          count: hints.length,
          buried: hints.filter(el => {
            const rr = el.getBoundingClientRect();
            return rr.height > 0 && rr.top > vh; // below the fold
          }).length,
          texts: hints.map(el => (el.textContent || '').trim().slice(0, 60)),
        };
      });
      assert(r.count === 0,
        'no .gesture-hint elements may exist on /live (found ' + r.count +
        ', buried-below-fold=' + r.buried + ', texts=' + JSON.stringify(r.texts) + ')');
    });

    await ctx.close();
  }

  // ── Desktop 1280x800 sanity ─────────────────────────────────────────────
  {
    const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 } });
    const page = await ctx.newPage();
    page.setDefaultTimeout(10000);
    await step('[1280x800] navigate to /live', async () => { await gotoLive(page); });

    await step('[1280x800] VCR bar renders, controls + LCD still inline (no desktop regression)', async () => {
      const r = await page.evaluate(() => {
        const bar = document.getElementById('vcrBar');
        const ctrl = document.querySelector('.vcr-controls');
        const lcd = document.querySelector('.vcr-lcd');
        const cr = ctrl.getBoundingClientRect();
        const lr = lcd.getBoundingClientRect();
        // Same row check (vertical overlap).
        const sameRow = !(cr.bottom <= lr.top || cr.top >= lr.bottom);
        return { hasBar: !!bar, sameRow, ctrl: { top: cr.top, bottom: cr.bottom }, lcd: { top: lr.top, bottom: lr.bottom } };
      });
      assert(r.hasBar, '#vcrBar missing on desktop');
      assert(r.sameRow,
        'desktop regression — controls and LCD must share a row ' +
        '(ctrl=' + JSON.stringify(r.ctrl) + ', lcd=' + JSON.stringify(r.lcd) + ')');
    });

    await ctx.close();
  }

  await browser.close();
  console.log(`\n=== Results: passed ${passed} failed ${failed} ===`);
  process.exit(failed > 0 ? 1 : 0);
})().catch(e => { console.error(e); process.exit(1); });

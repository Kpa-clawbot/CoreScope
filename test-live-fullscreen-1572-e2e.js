#!/usr/bin/env node
/* Issue #1572 round-1 — Live fullscreen behavioral E2E.
 *
 * Replaces source-grep assertions (which a hidden no-op button or a
 * dead-branch input guard would pass) with computed-style /
 * keystroke-bus assertions in a real browser.
 *
 * Findings under test:
 *   A. body.live-fullscreen MUST be cleared on SPA route exit (mobile
 *      .bottom-nav is hidden by that class — leaking it strands the
 *      user on a navless page).
 *   B. Escape MUST exit fullscreen (no F-key dance to escape).
 *   C. F-key input guard: typing 'f' in an input MUST land in the input
 *      and MUST NOT enter fullscreen.
 *   D. Toggle round-trip: click → in fullscreen + .live-header-body
 *      computed display:none; click again → out + visible.
 */
'use strict';

const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';

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
      console.error(`test-live-fullscreen-1572-e2e.js: FAIL — Chromium required but unavailable: ${err.message}`);
      process.exit(1);
    }
    console.log(`test-live-fullscreen-1572-e2e.js: SKIP (Chromium unavailable: ${err.message.split('\n')[0]})`);
    process.exit(0);
  }

  let failures = 0, passes = 0;
  const fail = (m) => { failures++; console.error('  FAIL: ' + m); };
  const pass = (m) => { passes++; console.log('  PASS: ' + m); };

  const ctx = await browser.newContext();
  const page = await ctx.newPage();
  page.setDefaultTimeout(15000);

  // Ensure no fullscreen pref leaks from a previous test run.
  await page.addInitScript(() => {
    try { localStorage.removeItem('live-fullscreen'); } catch (_) {}
  });

  async function waitForLive() {
    await page.waitForSelector('.live-page', { timeout: 15000 });
    await page.waitForSelector('#liveFullscreenToggle', { timeout: 15000 });
  }

  try {
    // ─────────────────────────────────────────────────────────────
    // Mobile viewport so .bottom-nav is the active nav (finding A).
    await page.setViewportSize({ width: 390, height: 844 });

    // ── D. Toggle round-trip ────────────────────────────────────
    await page.goto(`${BASE}/#/live`, { waitUntil: 'domcontentloaded' });
    await waitForLive();

    let state = await page.evaluate(() => ({
      hasClass: document.body.classList.contains('live-fullscreen'),
      headerDisplay: getComputedStyle(document.querySelector('.live-header-body')).display,
    }));
    if (!state.hasClass) pass('D: body lacks live-fullscreen on initial load');
    else fail(`D: body has live-fullscreen on initial load (display=${state.headerDisplay})`);

    await page.click('#liveFullscreenToggle');
    state = await page.evaluate(() => ({
      hasClass: document.body.classList.contains('live-fullscreen'),
      headerDisplay: getComputedStyle(document.querySelector('.live-header-body')).display,
    }));
    if (state.hasClass && state.headerDisplay === 'none') {
      pass('D: click #1 → body.live-fullscreen AND .live-header-body display:none');
    } else {
      fail(`D: click #1 expected (true, none), got (${state.hasClass}, ${state.headerDisplay})`);
    }

    await page.click('#liveFullscreenToggle');
    state = await page.evaluate(() => {
      const bn = document.querySelector('[data-bottom-nav], .bottom-nav');
      const sr = document.querySelector('.live-stats-row');
      return {
        hasClass: document.body.classList.contains('live-fullscreen'),
        bnDisplay: bn ? getComputedStyle(bn).display : null,
        statsPosition: sr ? getComputedStyle(sr).position : null,
      };
    });
    // Round-trip success: body class cleared AND the user-visible
    // fullscreen-only side-effects revert. `.bottom-nav` going back to
    // display!=none is the key mobile signal; `.live-stats-row` losing
    // its fixed/absolute pin is the desktop one. At least one must
    // revert (depends on viewport and whether the nav is present).
    if (!state.hasClass) {
      const reverted =
        (state.bnDisplay && state.bnDisplay !== 'none') ||
        (state.statsPosition && state.statsPosition !== 'fixed' && state.statsPosition !== 'absolute');
      if (reverted) {
        pass(`D: click #2 → body cleared, fullscreen side-effects reverted (bnDisplay=${state.bnDisplay}, statsPos=${state.statsPosition})`);
      } else {
        fail(`D: click #2 cleared body but side-effects did not revert (bnDisplay=${state.bnDisplay}, statsPos=${state.statsPosition})`);
      }
    } else {
      fail(`D: click #2 did NOT clear body.live-fullscreen`);
    }

    // ── B. Escape exits fullscreen ──────────────────────────────
    await page.click('#liveFullscreenToggle'); // re-enter fullscreen
    state = await page.evaluate(() => document.body.classList.contains('live-fullscreen'));
    if (state) pass('B: re-entered fullscreen before Escape test');
    else fail('B: setup — re-enter fullscreen failed');

    // Press Escape on body (not in an input).
    await page.evaluate(() => document.body.focus());
    await page.keyboard.press('Escape');
    state = await page.evaluate(() => document.body.classList.contains('live-fullscreen'));
    if (!state) pass('B: Escape cleared body.live-fullscreen');
    else fail('B: Escape did NOT clear body.live-fullscreen');

    // ── C. F-key input guard (behavioral) ───────────────────────
    // Make sure we are NOT in fullscreen.
    let fs = await page.evaluate(() => document.body.classList.contains('live-fullscreen'));
    if (fs) await page.click('#liveFullscreenToggle');

    const filterSel = '#liveNodeFilterInput';
    // Controls are collapsed by default — expand so the filter input is
    // focusable. If the toggle is absent for some reason, fall through.
    const ctlToggle = await page.$('#liveControlsToggle');
    if (ctlToggle) await ctlToggle.click();
    await page.waitForTimeout(100);
    const hasFilter = await page.$(filterSel);
    if (!hasFilter) {
      fail('C: #liveNodeFilterInput not present — cannot run input-guard test');
    } else {
      const visible = await page.evaluate((sel) => {
        const el = document.querySelector(sel);
        const cs = el ? getComputedStyle(el) : null;
        const r = el ? el.getBoundingClientRect() : null;
        return !!el && cs.display !== 'none' && cs.visibility !== 'hidden' && r.width > 0 && r.height > 0;
      }, filterSel);
      if (!visible) {
        fail('C: #liveNodeFilterInput is not visible after expanding controls');
      } else {
      await page.focus(filterSel);
      await page.keyboard.type('f');
      const result = await page.evaluate((sel) => ({
        hasClass: document.body.classList.contains('live-fullscreen'),
        value: document.querySelector(sel).value,
      }), filterSel);
      if (!result.hasClass && /f/i.test(result.value)) {
        pass(`C: typing 'f' in #liveNodeFilterInput did NOT toggle fullscreen (input value="${result.value}")`);
      } else {
        fail(`C: F-key leaked into toggle — hasClass=${result.hasClass}, value="${result.value}"`);
      }
      }
    }

    // ── A. body class cleared on route exit ─────────────────────
    // Enter fullscreen, then navigate to /#/nodes; assert body class
    // gone AND .bottom-nav not display:none.
    fs = await page.evaluate(() => document.body.classList.contains('live-fullscreen'));
    if (!fs) await page.click('#liveFullscreenToggle');
    fs = await page.evaluate(() => document.body.classList.contains('live-fullscreen'));
    if (!fs) {
      fail('A: setup — could not enter fullscreen');
    } else {
      pass('A: setup — in fullscreen before SPA nav');
    }

    // SPA navigate via hash change (no full reload).
    await page.evaluate(() => { location.hash = '#/nodes'; });
    await page.waitForFunction(() => location.hash.indexOf('#/nodes') === 0);
    // Give the router and destroy() a tick.
    await page.waitForTimeout(150);

    const post = await page.evaluate(() => {
      const bn = document.querySelector('[data-bottom-nav], .bottom-nav');
      return {
        hasClass: document.body.classList.contains('live-fullscreen'),
        bnPresent: !!bn,
        bnDisplay: bn ? getComputedStyle(bn).display : null,
      };
    });
    if (!post.hasClass) pass('A: body.live-fullscreen cleared after SPA nav to /#/nodes');
    else fail('A: body.live-fullscreen LEAKED after SPA nav (.bottom-nav would be hidden on mobile)');

    if (post.bnPresent && post.bnDisplay && post.bnDisplay !== 'none') {
      pass(`A: .bottom-nav visible after nav (display=${post.bnDisplay})`);
    } else if (!post.bnPresent) {
      fail('A: .bottom-nav not present in DOM after SPA nav');
    } else {
      fail(`A: .bottom-nav has display:none after SPA nav — user stranded`);
    }
  } finally {
    await browser.close();
  }

  console.log(`\n#1572 fullscreen E2E: ${passes} passed, ${failures} failed`);
  process.exit(failures > 0 ? 1 : 0);
}

main().catch((e) => { console.error(e); process.exit(1); });

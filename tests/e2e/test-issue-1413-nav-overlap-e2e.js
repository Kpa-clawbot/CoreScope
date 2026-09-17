#!/usr/bin/env node
/* Issue #1413 — More button overlaps nav-stats badge at vw~1200px.
 *
 * Symptom: at viewport ~1101..1599px on a non-mobile page (e.g.
 * /#/packets), the ".nav-more-btn" (in .nav-left) and ".nav-stats"
 * (in .nav-right) overlap horizontally. CDP-confirmed: at vw=1200,
 * .nav-more-btn rect (x=499..556) sat on top of .nav-stats (x=502..961),
 * a ~54px x-axis overlap. Visually the stats badge number rendered on
 * top of the "More" text and the chevron.
 *
 * Acceptance (from issue #1413):
 *   - At vw=1101..1920 (sample step), .nav-more-btn.right + GAP <=
 *     .nav-stats.left, where GAP >= 8px.
 *   - At vw <= 1100, .nav-stats is display:none (no change).
 *   - Nav doesn't horizontally scroll at any viewport.
 *
 * Root cause: .top-nav uses display:flex with justify-content:
 * space-between, but .nav-left had no flex-grow and .nav-links had no
 * flex-grow either, so .nav-left only consumed its content's intrinsic
 * width. .nav-right (flex-shrink:0) then sat at its natural position
 * computed from total content — and the JS Priority+ fits() check
 * succeeded based on intrinsic widths that under-reported the real
 * collision because .top-nav has overflow:hidden masking it.
 *
 * Fix (verified via CDP at vw 1101..1920): `.nav-links { flex: 1 1
 * auto; min-width: 0 }` + `.top-nav { column-gap: 16px }`. Reverting
 * either part of the fix reintroduces overlap at vw=1200.
 *
 * Mutation guard: revert the CSS fix → this test fails at vw=1200.
 */
'use strict';

const assert = require('node:assert');
const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';
const WIDTHS = [1101, 1200, 1366, 1440, 1600, 1920];
const HEIGHT = 800;
const MIN_GAP_PX = 8;

async function main() {
  let browser;
  try {
    browser = await chromium.launch({
      headless: true,
      executablePath: process.env.CHROMIUM_PATH || undefined,
      args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
    });
  } catch (err) {
    if (process.env.CHROMIUM_REQUIRE === '1') {
      console.error(`test-issue-1413-nav-overlap-e2e.js: FAIL — Chromium required but unavailable: ${err.message}`);
      process.exit(1);
    }
    console.log(`test-issue-1413-nav-overlap-e2e.js: SKIP (Chromium unavailable: ${err.message.split('\n')[0]})`);
    process.exit(0);
  }

  let failures = 0;
  let passes = 0;
  const ctx = await browser.newContext();
  const page = await ctx.newPage();
  page.setDefaultTimeout(15000);

  for (const w of WIDTHS) {
    await page.setViewportSize({ width: w, height: HEIGHT });
    await page.goto(`${BASE}/#/packets`, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('.top-nav .nav-links');
    await page.evaluate(() => document.fonts && document.fonts.ready ? document.fonts.ready : null);
    // Settle layout: two consecutive frames identical for nav-right.
    await page.waitForFunction(() => {
      const el = document.querySelector('.top-nav .nav-right');
      if (!el) return false;
      const r1 = el.getBoundingClientRect();
      return new Promise((resolve) => {
        requestAnimationFrame(() => requestAnimationFrame(() => {
          const r2 = el.getBoundingClientRect();
          resolve(r1.right === r2.right && r1.left === r2.left);
        }));
      });
    }, null, { timeout: 5000 });
    await page.evaluate(() => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r))));

    const data = await page.evaluate(() => {
      const more = document.querySelector('.nav-more-btn');
      const stats = document.querySelector('.nav-stats');
      const moreVisible = more && getComputedStyle(more).display !== 'none' &&
                          getComputedStyle(more.parentElement).display !== 'none' &&
                          !more.parentElement.classList.contains('is-hidden');
      const statsVisible = stats && getComputedStyle(stats).display !== 'none';
      const mb = more ? more.getBoundingClientRect() : null;
      const sb = stats ? stats.getBoundingClientRect() : null;
      const topNav = document.querySelector('.top-nav');
      const tnScrollW = topNav ? topNav.scrollWidth : 0;
      const tnClientW = topNav ? topNav.clientWidth : 0;
      return {
        moreVisible, statsVisible,
        more: mb ? { x: mb.x, right: mb.right, w: mb.width } : null,
        stats: sb ? { x: sb.x, right: sb.right, w: sb.width } : null,
        tnScrollW, tnClientW,
      };
    });

    let status = 'PASS';
    const reasons = [];

    // Acceptance: if both visible, more.right + 8 <= stats.left.
    if (data.moreVisible && data.statsVisible && data.more && data.stats) {
      const gap = data.stats.x - data.more.right;
      if (gap < MIN_GAP_PX) {
        status = 'FAIL';
        reasons.push(`overlap: more.right=${data.more.right.toFixed(1)} stats.left=${data.stats.x.toFixed(1)} gap=${gap.toFixed(1)} (need >= ${MIN_GAP_PX})`);
      }
    }
    // No horizontal scroll in nav.
    if (data.tnScrollW > data.tnClientW + 1) {
      status = 'FAIL';
      reasons.push(`top-nav h-scroll: scrollW=${data.tnScrollW} clientW=${data.tnClientW}`);
    }

    if (status === 'FAIL') {
      failures++;
      console.error(`vw=${w} #/packets ${status}: ${reasons.join('; ')}`);
    } else {
      passes++;
      console.log(`vw=${w} #/packets PASS (more.right=${data.more && data.more.right.toFixed(1)} stats.left=${data.stats && data.stats.x.toFixed(1)})`);
    }
  }

  await browser.close();
  console.log(`\ntest-issue-1413-nav-overlap-e2e.js: ${passes} pass, ${failures} fail`);
  process.exit(failures > 0 ? 1 : 0);
}

main().catch((err) => { console.error('test-issue-1413-nav-overlap-e2e.js: ERROR', err); process.exit(1); });

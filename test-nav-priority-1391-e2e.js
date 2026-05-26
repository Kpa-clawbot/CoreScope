#!/usr/bin/env node
/* Issue #1391 — 20th Priority+ nav regression.
 *
 * Symptom: at viewport ~1080-1200px on a non-high-priority active route
 * (e.g. /#/perf, /#/audio-lab), the active-route pill is shoved into the
 * More dropdown instead of staying visible inline. Operator screenshot at
 * ~1080px on /#/perf showed the navbar with only the "Perf" pill visible
 * (or, in the inverse failure mode, NO inline pill at all, with More
 * containing only the orphaned active route).
 *
 * Acceptance (from issue #1391):
 *   - Active-route pill MUST always be visible inline (never overflowed
 *     to More) at any viewport ≥768px.
 *   - If active route is NOT a high-priority link (e.g. /#/perf), the
 *     high-priority links MUST still be inline ≥768px.
 *   - Every link in overflow MUST be reachable via the More dropdown
 *     (the existing #1311/#1139 contract — don't regress).
 *
 * Mutation guard: removing the "pin active inline" rule in applyNavPriority
 * must make this test fail (active link gets overflowed at 1080px on /#/perf).
 */
'use strict';

const assert = require('node:assert');
const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';
const HIGH_PRIORITY_HREFS = ['#/home', '#/packets', '#/map', '#/live', '#/nodes'];

// Routes whose link is NOT data-priority="high" (verified via
// `grep data-priority public/index.html`). These exercise the
// "active pill is non-high" branch where the bug surfaces.
const NON_HIGH_ROUTES = ['#/perf', '#/audio-lab', '#/analytics', '#/observers'];

// Operator screenshot was ~1080px. Cover the narrow-desktop CSS branch
// (≤1100) AND the measurement-loop branch (>1100) — bug reproduces in
// both, and the #1311 fix only addressed >1100.
const WIDTHS = [1024, 1080, 1100, 1101, 1200, 1300];
const HEIGHT = 800;

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
      console.error(`test-nav-priority-1391-e2e.js: FAIL — Chromium required but unavailable: ${err.message}`);
      process.exit(1);
    }
    console.log(`test-nav-priority-1391-e2e.js: SKIP (Chromium unavailable: ${err.message.split('\n')[0]})`);
    process.exit(0);
  }

  let failures = 0;
  let passes = 0;
  const ctx = await browser.newContext();
  const page = await ctx.newPage();
  page.setDefaultTimeout(15000);

  for (const w of WIDTHS) {
    for (const route of NON_HIGH_ROUTES) {
      await page.setViewportSize({ width: w, height: HEIGHT });
      await page.goto(`${BASE}/${route}`, { waitUntil: 'domcontentloaded' });
      await page.waitForSelector('.top-nav .nav-links');
      await page.evaluate(() => document.fonts && document.fonts.ready ? document.fonts.ready : null);
      // Settle layout (two consecutive frames identical for nav-right).
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

      const data = await page.evaluate((route) => {
        const links = Array.from(document.querySelectorAll('.nav-links .nav-link'));
        let activeHref = null;
        let activeOverflowed = false;
        let activeWidth = 0;
        const visibleHighPri = [];
        const overflowedHighPri = [];
        for (const a of links) {
          const href = a.getAttribute('href');
          const isActive = a.classList.contains('active');
          const isOverflow = a.classList.contains('is-overflow');
          const w = a.getBoundingClientRect().width;
          if (isActive) {
            activeHref = href;
            activeOverflowed = isOverflow;
            activeWidth = w;
          }
          if (a.dataset.priority === 'high') {
            if (isOverflow || w === 0) overflowedHighPri.push({ href, isOverflow, w });
            else visibleHighPri.push(href);
          }
        }
        // Open More dropdown and capture its items (clones live in
        // .nav-more-menu, the originals stay in .nav-links).
        const moreBtn = document.getElementById('navMoreBtn');
        const moreWrap = document.querySelector('.nav-more-wrap');
        const moreMenu = document.getElementById('navMoreMenu');
        const moreVisible = moreWrap && !moreWrap.classList.contains('is-hidden');
        const moreItems = moreMenu
          ? Array.from(moreMenu.querySelectorAll('.nav-link')).map(a => a.getAttribute('href'))
          : [];
        // Every inline-overflowed link must appear in the More dropdown
        // (otherwise it's unreachable).
        const overflowedHrefs = links
          .filter(a => a.classList.contains('is-overflow'))
          .map(a => a.getAttribute('href'));
        const missingFromMore = overflowedHrefs.filter(h => !moreItems.includes(h));
        return {
          activeHref, activeOverflowed, activeWidth,
          visibleHighPri, overflowedHighPri,
          moreVisible, moreItems, overflowedHrefs, missingFromMore,
        };
      }, route);

      const tag = `${w}px @ ${route}`;
      const expectedActive = route;

      try {
        // (1) Active pill is correctly identified and present inline.
        assert.strictEqual(
          data.activeHref, expectedActive,
          `${tag}: expected active=${expectedActive}, got ${data.activeHref}`
        );
        assert.strictEqual(
          data.activeOverflowed, false,
          `${tag}: active-route pill ${expectedActive} MUST NOT be in overflow ` +
          `(was overflowed=${data.activeOverflowed}, width=${data.activeWidth})`
        );
        assert.ok(
          data.activeWidth > 0,
          `${tag}: active-route pill ${expectedActive} must have non-zero width inline ` +
          `(got width=${data.activeWidth})`
        );

        // (2) All high-priority links must be inline (regression guard for #1311).
        assert.deepStrictEqual(
          [...data.visibleHighPri].sort(),
          [...HIGH_PRIORITY_HREFS].sort(),
          `${tag}: expected all 5 high-pri inline, got [${data.visibleHighPri.join(', ')}] ` +
          `overflowed=[${data.overflowedHighPri.map(o => o.href).join(', ')}]`
        );

        // (3) Every overflowed link is reachable via the More dropdown
        //     (no orphaned overflow links).
        assert.deepStrictEqual(
          data.missingFromMore, [],
          `${tag}: overflowed links missing from More dropdown: [${data.missingFromMore.join(', ')}] ` +
          `(more=[${data.moreItems.join(', ')}])`
        );

        passes++;
        console.log(`  ✅ ${tag}: active inline + ${data.visibleHighPri.length}/5 high-pri inline + ` +
                    `More has ${data.moreItems.length} item(s)`);
      } catch (e) {
        failures++;
        console.log(`  ❌ ${tag}: ${e.message}`);
      }
    }
  }

  await browser.close();
  const total = WIDTHS.length * NON_HIGH_ROUTES.length;
  console.log(`\ntest-nav-priority-1391-e2e.js: ${failures === 0 ? 'OK' : 'FAIL'} — ${passes}/${total} passed`);
  process.exit(failures === 0 ? 0 : 1);
}

main().catch((err) => {
  console.error('test-nav-priority-1391-e2e.js: fatal', err);
  process.exit(1);
});

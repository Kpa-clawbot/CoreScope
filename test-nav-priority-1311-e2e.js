#!/usr/bin/env node
/* Issue #1311 — Active non-high-priority route causes ALL high-priority
 * links to overflow into the More menu at narrow-desktop widths.
 *
 * Bug: applyNavPriority's iterative overflow loop in public/app.js had no
 * floor — when fits() kept returning false (because the active-route pill
 * is wider than other links), it walked off the end of the non-high tail
 * and started dropping high-priority links too. On /#/perf at ~900-1100px
 * in Firefox/Edge Windows, this nuked ALL 5 high-priority links from the
 * inline strip, leaving only "More ▾" + the active pill visible.
 *
 * Acceptance:
 *  - At 900px / 1024px / 1100px width on /#/perf, /#/audio-lab,
 *    /#/analytics, /#/observers (active route is non-high-priority):
 *    ALL 5 high-priority links (#/home, #/packets, #/map, #/live, #/nodes)
 *    are visible inline (.is-overflow not present, getBoundingClientRect
 *    width > 0). The More menu may contain low-priority links — that's
 *    fine — but never high-priority links.
 *
 * Mutation guard: removing the `priority === 'high'` floor in the loop
 * makes this test fail (loop walks into the high-priority tail).
 */
'use strict';

const assert = require('node:assert');
const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';
const HIGH_PRIORITY_HREFS = ['#/home', '#/packets', '#/map', '#/live', '#/nodes'];

// Active routes that are NOT high-priority. The bug surfaced when the
// active-route pill (which has wider padding/background) is one of these.
const NON_HIGH_ROUTES = ['#/perf', '#/audio-lab', '#/analytics', '#/observers'];

// Widths in the narrow-desktop band where the bug reproduces. The 768-1100
// branch in applyNavPriority is data-priority-only (already correct), so
// the regression actually surfaces just above 1100 where the iterative
// loop runs. We include 1101/1200 to cover that band, and 900/1024 for
// belt-and-braces (the CSS branch).
const WIDTHS = [900, 1024, 1101, 1200];
const HEIGHT = 900;

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
      console.error(`test-nav-priority-1311-e2e.js: FAIL — Chromium required but unavailable: ${err.message}`);
      process.exit(1);
    }
    console.log(`test-nav-priority-1311-e2e.js: SKIP (Chromium unavailable: ${err.message.split('\n')[0]})`);
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
      // Give applyNavPriority (wired to hashchange + rAF resize) a frame.
      await page.evaluate(() => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r))));

      const data = await page.evaluate((HIGH) => {
        const links = Array.from(document.querySelectorAll('.nav-links .nav-link'));
        const overflowedHighPri = [];
        const visibleHighPri = [];
        for (const a of links) {
          const href = a.getAttribute('href');
          if (!HIGH.includes(href)) continue;
          const isOverflow = a.classList.contains('is-overflow');
          const width = a.getBoundingClientRect().width;
          if (isOverflow || width === 0) {
            overflowedHighPri.push({ href, isOverflow, width });
          } else {
            visibleHighPri.push(href);
          }
        }
        return { overflowedHighPri, visibleHighPri };
      }, HIGH_PRIORITY_HREFS);

      const tag = `${w}px @ ${route}`;
      // Hard assertion (preflight gate requires `assert` call): every
      // high-priority link href is in the visible set, never overflowed.
      try {
        assert.deepStrictEqual(
          [...data.visibleHighPri].sort(),
          [...HIGH_PRIORITY_HREFS].sort(),
          `${tag}: expected all 5 high-priority links inline, got [${data.visibleHighPri.join(', ')}]`
        );
        passes++;
        console.log(`  ✅ ${tag}: 5/5 high-pri visible inline`);
      } catch (e) {
        failures++;
        const detail = data.overflowedHighPri
          .map(o => `${o.href}(overflow=${o.isOverflow},w=${o.width})`)
          .join(', ');
        console.log(`  ❌ ${tag}: high-pri overflowed/hidden: ${detail || '(none)'} ` +
                    `| visible=[${data.visibleHighPri.join(', ')}]`);
      }
    }
  }

  await browser.close();
  const total = WIDTHS.length * NON_HIGH_ROUTES.length;
  console.log(`\ntest-nav-priority-1311-e2e.js: ${failures === 0 ? 'OK' : 'FAIL'} — ${passes}/${total} passed`);
  process.exit(failures === 0 ? 0 : 1);
}

main().catch((err) => {
  console.error('test-nav-priority-1311-e2e.js: fatal', err);
  process.exit(1);
});

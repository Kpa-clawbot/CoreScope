#!/usr/bin/env node
/* Issue #1055 — Nav fluid Priority+ adaptation at all widths.
 *
 * Asserts the top-nav never overflows the viewport at common widths:
 * the right edge of `.nav-right` MUST be ≤ document.documentElement.clientWidth.
 *
 * Pre-fix behavior: the Priority+ collapse rule was scoped to
 * `(min-width: 768px) and (max-width: 1279px)`, so at 1280/1440/1920 the
 * full link strip + nav-stats + nav-right buttons could push past the
 * viewport's right edge (no collapse happened above 1279px).
 *
 * Post-fix: Priority+ collapses at all widths >=768px when needed.
 *
 * Run against a CoreScope server (defaults to localhost:13581 with the
 * E2E fixture DB, matching the playwright job in .github/workflows/deploy.yml).
 *
 * CI gating: when CHROMIUM_REQUIRE=1 (set by the GH Actions workflow) a
 * missing/broken Chromium is a HARD FAIL — no soft-skip. Locally the
 * test is allowed to skip so devs without Playwright browsers installed
 * can still run other tests.
 */
'use strict';

const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';
// Common widths the nav must stay clean at. 1280/1440 are the historic
// failure window: the Priority+ rule used to stop at 1279px but the full
// link strip + nav-right buttons don't fit on one row until ~1600px+.
// Below 768 the top nav is intentionally display:none (bottom-nav active).
const VIEWPORTS = [769, 1024, 1280, 1440, 1920];
// Routes asserted at every viewport. The pre-#1097 version only checked
// /#/home, but the bug reproduces on every top-level page since they
// all share the same .top-nav. Cover the four primary routes.
const ROUTES = ['/#/home', '/#/packets', '/#/nodes', '/#/map'];
const HEIGHT = 900;
// Whitespace tolerance (px) for the overflow/overlap assertions.
// Browsers occasionally hand back layout coordinates with sub-pixel
// rounding noise (≈0.1–0.4px) even when the box model is clean. We
// allow up to 0.5px so the test doesn't false-fail on rounding while
// still catching real overlaps (the bug this guards against was
// ~20px). Tighter than 0.5 caused intermittent CI flakes; looser
// would risk masking 1px regressions.
const SUBPIXEL_TOL = 0.5;

async function main() {
  const requireChromium = process.env.CHROMIUM_REQUIRE === '1' ||
                          process.env.NAV_FLUID_REQUIRE === '1';
  let browser;
  try {
    browser = await chromium.launch({
      headless: true,
      executablePath: process.env.CHROMIUM_PATH || undefined,
      args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
    });
  } catch (err) {
    if (requireChromium) {
      console.error(`test-nav-fluid-1055-e2e.js: FAIL — Chromium required (CHROMIUM_REQUIRE=1) but unavailable: ${err.message}`);
      process.exit(1);
    }
    console.log(`test-nav-fluid-1055-e2e.js: SKIP (Chromium unavailable: ${err.message.split('\n')[0]})`);
    process.exit(0);
  }

  let failures = 0;
  let passes = 0;
  const context = await browser.newContext();
  const page = await context.newPage();
  page.setDefaultTimeout(15000);

  for (const route of ROUTES) {
    for (const w of VIEWPORTS) {
      await page.setViewportSize({ width: w, height: HEIGHT });
      await page.goto(`${BASE}${route}`, { waitUntil: 'domcontentloaded' });
      await page.waitForSelector('.top-nav .nav-right');
      // Wait for fonts (which affect text measurement) AND for the nav
      // layout to settle: the .nav-right bounding box must hold steady
      // for two consecutive animation frames at the same coordinates.
      // This replaces a magic 150ms sleep with a deterministic gate
      // that asserts what we actually care about (layout has stopped
      // moving) before measuring.
      await page.evaluate(() => document.fonts && document.fonts.ready ? document.fonts.ready : null);
      await page.waitForFunction(() => {
        const el = document.querySelector('.top-nav .nav-right');
        if (!el) return false;
        const r1 = el.getBoundingClientRect();
        return new Promise((resolve) => {
          requestAnimationFrame(() => requestAnimationFrame(() => {
            const r2 = el.getBoundingClientRect();
            resolve(r1.right === r2.right && r1.left === r2.left && r1.top === r2.top);
          }));
        });
      }, null, { timeout: 5000 });

      const data = await page.evaluate(() => {
        const navRight = document.querySelector('.top-nav .nav-right');
        const navLeft  = document.querySelector('.top-nav .nav-left');
        const topNav   = document.querySelector('.top-nav');
        const more     = document.querySelector('.nav-more-wrap');
        const moreCs   = more ? getComputedStyle(more) : null;
        const links    = Array.from(document.querySelectorAll('.nav-links .nav-link'));
        const visible  = links.filter(a => getComputedStyle(a).display !== 'none');
        const lastVisible = visible[visible.length - 1] || null;
        return {
          clientW:    document.documentElement.clientWidth,
          navScroll:  topNav.scrollWidth,
          navClient:  topNav.clientWidth,
          navRight:   navRight.getBoundingClientRect().right,
          navRightL:  navRight.getBoundingClientRect().left,
          navLeftR:   navLeft.getBoundingClientRect().right,
          lastLinkR:  lastVisible ? lastVisible.getBoundingClientRect().right : -1,
          moreVisible: moreCs ? moreCs.display !== 'none' : false,
          visibleLinks: visible.length,
          totalLinks: links.length,
        };
      });

      const tag = `${route} @ ${w}px`;
      const reasons = [];
      // 1. .nav-right must not extend past the viewport's right edge.
      if (data.navRight > data.clientW + SUBPIXEL_TOL) {
        reasons.push(`nav-right.right=${data.navRight.toFixed(1)} > clientWidth=${data.clientW} ` +
                     `(excess ${(data.navRight - data.clientW).toFixed(1)}px)`);
      }
      // 2. The visible link strip must not overlap .nav-right (parent overflow:hidden
      //    masks this visually but it still hides the rightmost links — the actual bug).
      if (data.lastLinkR > data.navRightL + SUBPIXEL_TOL) {
        reasons.push(`last visible link right=${data.lastLinkR.toFixed(1)} > nav-right.left=${data.navRightL.toFixed(1)} ` +
                     `(${(data.lastLinkR - data.navRightL).toFixed(1)}px overlap)`);
      }
      // 3. The nav row itself must not require horizontal scrolling.
      if (data.navScroll > data.navClient + SUBPIXEL_TOL) {
        reasons.push(`top-nav scrollWidth=${data.navScroll} > clientWidth=${data.navClient}`);
      }

      if (reasons.length === 0) {
        passes++;
        console.log(`  ✅ ${tag}: clean (visible links ${data.visibleLinks}/${data.totalLinks}, more=${data.moreVisible})`);
      } else {
        failures++;
        console.log(`  ❌ ${tag}: ${reasons.join(' | ')} ` +
                    `(visible links ${data.visibleLinks}/${data.totalLinks}, more=${data.moreVisible})`);
      }
    }
  }

  // Regression: long nav stats/version text must not paint underneath the
  // right-side icon controls. Flex can shrink the .nav-stats box while its
  // nowrap children keep overflowing unless the stats item clips its contents.
  await page.setViewportSize({ width: 1912, height: HEIGHT });
  await page.goto(`${BASE}/#/live`, { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('.top-nav .nav-right');
  await page.evaluate(() => {
    const stats = document.getElementById('navStats');
    if (!stats) return;
    stats.innerHTML =
      '<span class="stat-val">66675</span> pkts · ' +
      '<span class="stat-val">3090</span> nodes · ' +
      '<span class="stat-val">93</span> obs ' +
      '<span class="version-badge">cornv3.7.1-875-g7dd0b4a0 · 7dd0b4a (12m ago)</span>';
    window.dispatchEvent(new Event('resize'));
  });
  await page.evaluate(() => document.fonts && document.fonts.ready ? document.fonts.ready : null);
  await page.evaluate(() => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r))));

  const statsClip = await page.evaluate(() => {
    const stats = document.getElementById('navStats');
    const fav = document.getElementById('favToggle');
    if (!stats || !fav) return { missing: true };
    const statsRect = stats.getBoundingClientRect();
    const favRect = fav.getBoundingClientRect();
    const statsStyle = getComputedStyle(stats);
    return {
      missing: false,
      statsRight: statsRect.right,
      favLeft: favRect.left,
      statsScrollWidth: stats.scrollWidth,
      statsClientWidth: stats.clientWidth,
      statsOverflowX: statsStyle.overflowX,
      navRight: document.querySelector('.top-nav .nav-right').getBoundingClientRect().right,
      clientW: document.documentElement.clientWidth,
    };
  });
  const statsReasons = [];
  if (statsClip.missing) {
    statsReasons.push('missing nav stats or favorites button');
  } else {
    if (statsClip.statsRight > statsClip.favLeft + SUBPIXEL_TOL) {
      statsReasons.push(`navStats.right=${statsClip.statsRight.toFixed(1)} > fav.left=${statsClip.favLeft.toFixed(1)}`);
    }
    if (statsClip.navRight > statsClip.clientW + SUBPIXEL_TOL) {
      statsReasons.push(`nav-right.right=${statsClip.navRight.toFixed(1)} > clientWidth=${statsClip.clientW}`);
    }
    if (statsClip.statsScrollWidth > statsClip.statsClientWidth + SUBPIXEL_TOL &&
        statsClip.statsOverflowX === 'visible') {
      statsReasons.push(`navStats has overflowing text (${statsClip.statsScrollWidth}px > ${statsClip.statsClientWidth}px) with overflow-x:visible`);
    }
  }
  if (statsReasons.length === 0) {
    passes++;
    console.log('  ✅ long nav-stats @1912px: clipped before right controls');
  } else {
    failures++;
    console.log(`  ❌ long nav-stats @1912px: ${statsReasons.join(' | ')}`);
  }

  await page.setViewportSize({ width: 1440, height: HEIGHT });
  await page.goto(`${BASE}/#/live`, { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('.top-nav .nav-right');
  await page.evaluate(() => {
    const stats = document.getElementById('navStats');
    if (!stats) return;
    stats.innerHTML =
      '<span class="stat-val">64945</span> pkts · ' +
      '<span class="stat-val">3140</span> nodes · ' +
      '<span class="stat-val">93</span> obs ' +
      '<span class="version-badge">cornv3.7.1-875-g7dd0b4a0 · 7dd0b4a (12m ago)</span>';
    window.dispatchEvent(new Event('resize'));
  });
  await page.evaluate(() => document.fonts && document.fonts.ready ? document.fonts.ready : null);
  await page.evaluate(() => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r))));
  const centerGuard = await page.evaluate(() => {
    const stats = document.getElementById('navStats');
    const links = Array.from(document.querySelectorAll('.nav-links .nav-link'))
      .filter(a => getComputedStyle(a).display !== 'none');
    const lastVisible = links[links.length - 1] || null;
    const statsStyle = stats ? getComputedStyle(stats) : null;
    const versionText = document.querySelector('.version-badge') ? document.querySelector('.version-badge').textContent : '';
    return {
      missing: !stats || !lastVisible,
      statsDisplay: statsStyle ? statsStyle.display : '',
      statsLeft: stats ? stats.getBoundingClientRect().left : -1,
      lastLinkRight: lastVisible ? lastVisible.getBoundingClientRect().right : -1,
      versionText,
    };
  });
  const centerReasons = [];
  if (centerGuard.missing) {
    centerReasons.push('missing stats or visible nav link');
  } else {
    if (centerGuard.statsDisplay !== 'none' && centerGuard.statsLeft < centerGuard.lastLinkRight - SUBPIXEL_TOL) {
      centerReasons.push(`navStats.left=${centerGuard.statsLeft.toFixed(1)} < lastLink.right=${centerGuard.lastLinkRight.toFixed(1)}`);
    }
    if (/\s·\sgo$/.test(centerGuard.versionText)) {
      centerReasons.push(`version badge still includes engine suffix: ${centerGuard.versionText}`);
    }
  }
  if (centerReasons.length === 0) {
    passes++;
    console.log('  ✅ nav-stats @1440px: does not cover center nav, no engine suffix');
  } else {
    failures++;
    console.log(`  ❌ nav-stats @1440px: ${centerReasons.join(' | ')}`);
  }

  const versionBadge = await page.evaluate(() => {
    if (typeof formatVersionBadge !== 'function') return { missing: true };
    return { html: formatVersionBadge('v3.7.1', '7dd0b4a0', 'go', new Date(Date.now() - 12 * 60 * 1000).toISOString()) };
  });
  const versionReasons = [];
  if (versionBadge.missing) {
    versionReasons.push('formatVersionBadge missing');
  } else if (/engine-badge|>\s*go\s*<|·\s*go/.test(versionBadge.html)) {
    versionReasons.push(`formatVersionBadge still renders engine: ${versionBadge.html}`);
  }
  if (versionReasons.length === 0) {
    passes++;
    console.log('  ✅ formatVersionBadge: omits engine suffix');
  } else {
    failures++;
    console.log(`  ❌ formatVersionBadge: ${versionReasons.join(' | ')}`);
  }

  await page.setViewportSize({ width: 2560, height: HEIGHT });
  await page.goto(`${BASE}/#/home`, { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('.top-nav .nav-action-btn');
  const actionLabels = await page.evaluate(() => {
    return Array.from(document.querySelectorAll('.top-nav .nav-action-label')).map((label) => ({
      text: label.textContent.trim(),
      display: getComputedStyle(label).display,
      width: label.getBoundingClientRect().width,
    }));
  });
  const labelReasons = [];
  if (actionLabels.length !== 2) {
    labelReasons.push(`expected 2 nav action labels in markup, got ${actionLabels.length}`);
  }
  const visibleLabels = actionLabels.filter(label => label.display !== 'none' || label.width > SUBPIXEL_TOL);
  if (visibleLabels.length) {
    labelReasons.push(`nav action labels visible: ${visibleLabels.map(label => `${label.text}(display=${label.display},w=${label.width.toFixed(1)})`).join(', ')}`);
  }
  if (labelReasons.length === 0) {
    passes++;
    console.log('  ✅ nav action labels @2560px: hidden, icons only');
  } else {
    failures++;
    console.log(`  ❌ nav action labels @2560px: ${labelReasons.join(' | ')}`);
  }

  await browser.close();

  const total = ROUTES.length * VIEWPORTS.length + 4;
  console.log(`\ntest-nav-fluid-1055-e2e.js: ${failures === 0 ? 'OK' : 'FAIL'} — ${passes}/${total} passed`);
  process.exit(failures === 0 ? 0 : 1);
}

main().catch((err) => {
  console.error('test-nav-fluid-1055-e2e.js: fatal', err);
  process.exit(1);
});

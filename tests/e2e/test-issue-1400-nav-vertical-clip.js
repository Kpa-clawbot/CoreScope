#!/usr/bin/env node
/* Issue #1400 — root cause of recurring nav-vanishing class of bugs.
 *
 * Symptom: at desktop viewports (1024..1711), the `.nav-links` strip
 * rendered at NEGATIVE y (operator probe: y=-57, height=56), entirely
 * above the visible 0..52 band of `.top-nav` which has `overflow:hidden`.
 *
 * Root cause: PR #1060 (commit eaf14a61) added a global
 *   .nav-link { min-height: 48px; display:inline-flex; align-items:center; }
 * The 48px link + padding inflated `.nav-links` to 56px tall inside a 52px
 * `.top-nav` with `overflow:hidden`. With `align-items: center`, Firefox
 * centers the over-tall flex item at a negative y → strip clipped above
 * viewport.
 *
 * Acceptance (from #1400):
 *   - Desktop: `.nav-links` rect.y >= 0 AND every `.nav-links > a` is
 *     vertically inside the visible top-nav band (y >= 0 AND y+height <= 60).
 *   - Mobile (<768px): touch-target preserved — `.nav-link` min-height
 *     computed style >= 48px (regression guard for #1060).
 *
 * Mutation guard: re-adding `min-height: 48px` to global `.nav-link`
 * must make this test fail with negative y at desktop widths.
 */
'use strict';

const assert = require('node:assert');
const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';

const DESKTOP_WIDTHS = [1024, 1366, 1711];
const MOBILE_WIDTH = 480;
const HEIGHT = 800;
const TOPNAV_HEIGHT_MAX = 60; // 52px nominal + a few px slack

async function settleNav(page) {
  await page.waitForSelector('.top-nav .nav-links');
  await page.evaluate(() => document.fonts && document.fonts.ready ? document.fonts.ready : null);
  await page.waitForFunction(() => {
    const el = document.querySelector('.top-nav .nav-links');
    if (!el) return false;
    const r1 = el.getBoundingClientRect();
    return new Promise((resolve) => {
      requestAnimationFrame(() => requestAnimationFrame(() => {
        const r2 = el.getBoundingClientRect();
        resolve(r1.top === r2.top && r1.height === r2.height);
      }));
    });
  });
}

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
      console.error(`test-issue-1400-nav-vertical-clip.js: FAIL — Chromium required but unavailable: ${err.message}`);
      process.exit(1);
    }
    console.log(`test-issue-1400-nav-vertical-clip.js: SKIP (Chromium unavailable: ${err.message.split('\n')[0]})`);
    process.exit(0);
  }

  let failures = 0;
  let passes = 0;
  const ctx = await browser.newContext();
  const page = await ctx.newPage();
  page.setDefaultTimeout(15000);

  // === Desktop: vertical clip guard ===
  for (const w of DESKTOP_WIDTHS) {
    await page.setViewportSize({ width: w, height: HEIGHT });
    await page.goto(`${BASE}/#/home`, { waitUntil: 'domcontentloaded' });
    await settleNav(page);

    const probe = await page.evaluate(() => {
      const nav = document.querySelector('.top-nav');
      const links = document.querySelector('.nav-links');
      const anchors = Array.from(document.querySelectorAll('.nav-links > a'));
      const r = (el) => {
        if (!el) return null;
        const b = el.getBoundingClientRect();
        return { y: b.y, height: b.height, bottom: b.y + b.height };
      };
      return {
        nav: r(nav),
        links: r(links),
        anchors: anchors.map((a) => ({ href: a.getAttribute('href'), ...r(a) })),
      };
    });

    const tag = `vw=${w}`;
    if (!probe.links) {
      console.error(`FAIL ${tag}: .nav-links not found`);
      failures++;
      continue;
    }

    try {
      assert.ok(
        probe.links.y >= 0,
        `${tag}: .nav-links y=${probe.links.y} must be >= 0 (issue #1400 root-cause regression: clipped above viewport)`,
      );
      assert.ok(
        probe.anchors.length > 0,
        `${tag}: expected >=1 .nav-links > a, got 0`,
      );
      for (const a of probe.anchors) {
        assert.ok(
          a.y >= 0,
          `${tag}: nav-link href=${a.href} y=${a.y} must be >= 0`,
        );
        assert.ok(
          a.bottom <= TOPNAV_HEIGHT_MAX,
          `${tag}: nav-link href=${a.href} bottom=${a.bottom} must be <= ${TOPNAV_HEIGHT_MAX} (overflowing 52px top-nav)`,
        );
      }
      console.log(`PASS ${tag}: .nav-links y=${probe.links.y.toFixed(1)} h=${probe.links.height.toFixed(1)}; ${probe.anchors.length} anchors all inside top-nav band`);
      passes++;
    } catch (err) {
      console.error(`FAIL ${tag}: ${err.message}`);
      console.error(`  probe: ${JSON.stringify(probe)}`);
      failures++;
    }
  }

  // === Mobile: touch-target preserved (#1060 regression guard) ===
  await page.setViewportSize({ width: MOBILE_WIDTH, height: HEIGHT });
  await page.goto(`${BASE}/#/home`, { waitUntil: 'domcontentloaded' });
  // open hamburger so .nav-link is rendered (display:none otherwise on mobile until .open)
  await page.evaluate(() => {
    const links = document.querySelector('.nav-links');
    if (links) links.classList.add('open');
  });
  await page.waitForTimeout(50);

  const mobileProbe = await page.evaluate(() => {
    const anchors = Array.from(document.querySelectorAll('.nav-links > a'));
    return anchors.slice(0, 3).map((a) => {
      const cs = getComputedStyle(a);
      return { href: a.getAttribute('href'), minHeight: parseFloat(cs.minHeight) || 0 };
    });
  });

  const tag = `vw=${MOBILE_WIDTH}`;
  try {
    assert.ok(mobileProbe.length > 0, `${tag}: expected mobile nav-links anchors, got 0`);
    for (const a of mobileProbe) {
      assert.ok(
        a.minHeight >= 48,
        `${tag}: nav-link href=${a.href} min-height=${a.minHeight} must be >= 48 (touch-target regression of #1060)`,
      );
    }
    console.log(`PASS ${tag}: mobile .nav-link min-height >= 48 (touch-target preserved per #1060)`);
    passes++;
  } catch (err) {
    console.error(`FAIL ${tag}: ${err.message}`);
    console.error(`  probe: ${JSON.stringify(mobileProbe)}`);
    failures++;
  }

  await browser.close();

  console.log(`\ntest-issue-1400-nav-vertical-clip.js: ${passes} passed, ${failures} failed`);
  if (failures > 0) process.exit(1);
}

main().catch((err) => {
  console.error('test-issue-1400-nav-vertical-clip.js: ERROR', err);
  process.exit(1);
});

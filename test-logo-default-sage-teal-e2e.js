#!/usr/bin/env node
/* Logo default-brand E2E — verifies that the navbar + hero wordmarks
 * render the sage/teal brand identity OUT OF THE BOX (no operator
 * customizer override active), AND that the customizer can still
 * override those colors when an operator picks a theme.
 *
 * Asserts:
 *   1. Default load (clean localStorage, no overrides):
 *        navbar CORE.fill  === rgb(207, 217, 201)   // sage / fog
 *        navbar SCOPE.fill === rgb(44, 140, 140)    // teal / water
 *        hero CORE/SCOPE same.
 *   2. After a customizer override that sets accent=red (#dc2626) and
 *      accentHover=red-hover (#ef4444), the wordmark CORE+SCOPE recolors
 *      to follow the override (NOT sage/teal anymore).
 *
 * This is the contract from PR #1157 follow-up: sage/teal are the brand
 * default, but the customizer remains the canonical theming surface.
 *
 * On master this test FAILS step 1 because the default --accent is
 * #4a9eff (blue), so --logo-accent resolves to blue — not sage.
 */
'use strict';

const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';
const SAGE = 'rgb(207, 217, 201)';
const TEAL = 'rgb(44, 140, 140)';

function fail(msg) {
  console.error(`test-logo-default-sage-teal-e2e.js: FAIL — ${msg}`);
  process.exit(1);
}

async function readWordmark(page, sel) {
  return await page.evaluate((s) => {
    const root = document.querySelector(s);
    if (!root) return { error: s + ' missing' };
    const out = {};
    root.querySelectorAll('svg text').forEach((t) => {
      const tc = (t.textContent || '').trim();
      if (tc === 'CORE' || tc === 'SCOPE') out[tc] = getComputedStyle(t).fill;
    });
    return { out };
  }, sel);
}

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
      console.error(`test-logo-default-sage-teal-e2e.js: FAIL — Chromium required but unavailable: ${err.message}`);
      process.exit(1);
    }
    console.log(`test-logo-default-sage-teal-e2e.js: SKIP (Chromium unavailable: ${err.message.split('\n')[0]})`);
    process.exit(0);
  }

  let passed = 0;
  const total = 4;
  try {
    // ── Step 1: clean localStorage, default load → sage/teal ──
    const ctx1 = await browser.newContext({ viewport: { width: 1280, height: 900 } });
    const page1 = await ctx1.newPage();
    page1.setDefaultTimeout(10000);
    // Defensive: ensure no customizer overrides leak in.
    await page1.addInitScript(() => {
      try { localStorage.removeItem('cs-theme-overrides'); } catch (_) {}
      try { localStorage.setItem('meshcore-user-level', 'experienced'); } catch (_) {}
    });
    await page1.goto(BASE + '/#/', { waitUntil: 'domcontentloaded' });
    await page1.waitForSelector('.nav-brand svg.brand-logo text', { timeout: 8000 });

    const navDefault = await readWordmark(page1, '.nav-brand');
    if (navDefault.error) fail(navDefault.error);
    if (!navDefault.out.CORE || !navDefault.out.SCOPE) {
      fail(`default navbar CORE/SCOPE missing: ${JSON.stringify(navDefault.out)}`);
    }
    if (navDefault.out.CORE !== SAGE) {
      fail(`default navbar CORE fill = ${navDefault.out.CORE}; expected sage ${SAGE}`);
    }
    if (navDefault.out.SCOPE !== TEAL) {
      fail(`default navbar SCOPE fill = ${navDefault.out.SCOPE}; expected teal ${TEAL}`);
    }
    console.log(`  ✅ default navbar wordmark is sage/teal (CORE=${navDefault.out.CORE}, SCOPE=${navDefault.out.SCOPE})`);
    passed++;

    await page1.evaluate(() => { window.location.hash = '#/home'; });
    await page1.waitForFunction(() => location.hash === '#/home');
    await page1.waitForSelector('.home-hero', { timeout: 8000 });
    // Hero SVG can render after the route swap; wait for the wordmark text
    // to actually exist before reading fills.
    await page1.waitForFunction(() => {
      const h = document.querySelector('.home-hero');
      return !!(h && h.querySelector('svg text'));
    }, null, { timeout: 8000 });
    const heroDefault = await readWordmark(page1, '.home-hero');
    if (heroDefault.error) fail(heroDefault.error);
    if (heroDefault.out.CORE !== SAGE) {
      fail(`default hero CORE fill = ${heroDefault.out.CORE}; expected sage ${SAGE}`);
    }
    if (heroDefault.out.SCOPE !== TEAL) {
      fail(`default hero SCOPE fill = ${heroDefault.out.SCOPE}; expected teal ${TEAL}`);
    }
    console.log(`  ✅ default hero wordmark is sage/teal (CORE=${heroDefault.out.CORE}, SCOPE=${heroDefault.out.SCOPE})`);
    passed++;
    await ctx1.close();

    // ── Step 2: customizer override → red wordmark ──
    const ctx2 = await browser.newContext({ viewport: { width: 1280, height: 900 } });
    const page2 = await ctx2.newPage();
    page2.setDefaultTimeout(10000);
    // Seed the customizer override BEFORE first paint. customize-v2.js reads
    // 'cs-theme-overrides' from localStorage on init and writes the matching
    // CSS vars (including --logo-accent / --logo-accent-hi after this fix).
    await page2.addInitScript(() => {
      try {
        localStorage.setItem('cs-theme-overrides', JSON.stringify({
          theme:     { accent: '#dc2626', accentHover: '#ef4444' },
          themeDark: { accent: '#dc2626', accentHover: '#ef4444' },
        }));
        localStorage.setItem('meshcore-user-level', 'experienced');
      } catch (_) {}
    });
    await page2.goto(BASE + '/#/', { waitUntil: 'domcontentloaded' });
    await page2.waitForSelector('.nav-brand svg.brand-logo text', { timeout: 8000 });
    // Settle one frame for early-apply to run.
    await page2.waitForTimeout(200);

    const navOverride = await readWordmark(page2, '.nav-brand');
    if (navOverride.error) fail(navOverride.error);
    if (navOverride.out.CORE === SAGE || navOverride.out.SCOPE === TEAL) {
      fail(`customizer override did NOT reach the logo — still sage/teal: ${JSON.stringify(navOverride.out)}. Customizer must mirror --accent → --logo-accent.`);
    }
    // Both halves should follow the override (CORE ← accent, SCOPE ← accentHover).
    if (navOverride.out.CORE !== 'rgb(220, 38, 38)') {
      fail(`navbar CORE under customizer override = ${navOverride.out.CORE}; expected rgb(220, 38, 38)`);
    }
    if (navOverride.out.SCOPE !== 'rgb(239, 68, 68)') {
      fail(`navbar SCOPE under customizer override = ${navOverride.out.SCOPE}; expected rgb(239, 68, 68)`);
    }
    console.log(`  ✅ navbar wordmark follows customizer override (CORE=${navOverride.out.CORE}, SCOPE=${navOverride.out.SCOPE})`);
    passed++;

    await page2.evaluate(() => { window.location.hash = '#/home'; });
    await page2.waitForFunction(() => location.hash === '#/home');
    await page2.waitForSelector('.home-hero', { timeout: 8000 });
    await page2.waitForFunction(() => {
      const h = document.querySelector('.home-hero');
      return !!(h && h.querySelector('svg text'));
    }, null, { timeout: 8000 });
    const heroOverride = await readWordmark(page2, '.home-hero');
    if (heroOverride.error) fail(heroOverride.error);
    if (heroOverride.out.CORE !== 'rgb(220, 38, 38)' || heroOverride.out.SCOPE !== 'rgb(239, 68, 68)') {
      fail(`hero wordmark under customizer override = ${JSON.stringify(heroOverride.out)}; expected CORE=rgb(220,38,38), SCOPE=rgb(239,68,68)`);
    }
    console.log(`  ✅ hero wordmark follows customizer override (CORE=${heroOverride.out.CORE}, SCOPE=${heroOverride.out.SCOPE})`);
    passed++;

    await ctx2.close();
    await browser.close();
    console.log(`\ntest-logo-default-sage-teal-e2e.js: ${passed}/${total} PASS`);
  } catch (err) {
    try { await browser.close(); } catch (_) {}
    console.error(`test-logo-default-sage-teal-e2e.js: FAIL — ${err.message}`);
    process.exit(1);
  }
}

main();

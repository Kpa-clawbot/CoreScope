#!/usr/bin/env node
/* Logo theme reactivity E2E — verifies that the navbar + hero logos
 * inherit page CSS custom properties and remain visible when the user
 * switches to the Light theme.
 *
 * Asserts:
 *   1. With data-theme="light", the navbar wordmark CORE/SCOPE elements
 *      have a computed fill that is NOT the legacy hardcoded sage
 *      (#cfd9c9 / rgb(207,217,201)).
 *   2. The hero SVG does NOT contain a full-canvas opaque background
 *      rect (no <rect width=1200 height=300> with a non-transparent fill
 *      reachable via the inline SVG in the home-hero region).
 *   3. The hero wordmark CORE/SCOPE compute-fills also drop the legacy
 *      sage hex when the page theme is Light.
 *   4. The navbar wordmark is duotone — CORE fill !== SCOPE fill — and
 *      remains so under both default (dark) and Light themes. Proves the
 *      fog/teal split survives the light-theme rebind.
 *   5. The hero wordmark is also duotone (CORE !== SCOPE) under both
 *      themes.
 *   6. At mobile width (360x640), the navbar swaps to a mark-only
 *      .brand-mark-only inline SVG (visible) while the full .brand-logo
 *      is display:none — preventing the SCOPE→SCOF clip seen with the
 *      99px mobile pin from #1137. Also asserts the visible navbar logo
 *      fits within .nav-left's right edge (no horizontal overflow).
 *
 * Designed to FAIL on the pre-fix branch (where the SVGs are loaded as
 * <img>, the wordmark fill is baked to #cfd9c9, and the hero SVG ships a
 * solid <rect fill="var(--logo-bg, #0e1714)">).
 */
'use strict';

const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';
// Note: rgb(207, 217, 201) is the brand sage default for --logo-accent
// (see test-logo-default-sage-teal-e2e.js). It is NO LONGER a failure
// signal here; the original "must not be sage" assertion was written
// when sage meant "baked-into-SVG-attr regression" and the wordmark was
// supposed to follow --accent (then blue). Now sage is the intentional
// brand identity and the test below asserts theme-reactivity by mutating
// --logo-accent directly and observing the fill change instead.

function fail(msg) {
  console.error(`test-logo-theme-e2e.js: FAIL — ${msg}`);
  process.exit(1);
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
      console.error(`test-logo-theme-e2e.js: FAIL — Chromium required but unavailable: ${err.message}`);
      process.exit(1);
    }
    console.log(`test-logo-theme-e2e.js: SKIP (Chromium unavailable: ${err.message.split('\n')[0]})`);
    process.exit(0);
  }

  let passed = 0;
  const total = 7;
  try {
    const context = await browser.newContext({ viewport: { width: 1280, height: 900 } });
    const page = await context.newPage();
    page.setDefaultTimeout(10000);

    // Force Light theme BEFORE first navigation so initial paint uses it.
    await page.addInitScript(() => {
      try { localStorage.setItem('meshcore-user-level', 'experienced'); } catch (_) {}
    });

    await page.goto(BASE + '/#/', { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('.nav-brand', { timeout: 8000 });
    await page.evaluate(() => { document.documentElement.setAttribute('data-theme', 'light'); });

    // 1. Navbar wordmark must be inline-SVG <text> (not <img>) and computed
    //    fill must be theme-reactive: setting --logo-accent / --logo-accent-hi
    //    on :root must repaint the wordmark.
    const navWordmarkFills = await page.evaluate(() => {
      const out = [];
      const root = document.querySelector('.nav-brand');
      if (!root) return { error: '.nav-brand missing' };
      const texts = root.querySelectorAll('svg text');
      texts.forEach((t) => {
        const tc = (t.textContent || '').trim();
        if (tc === 'CORE' || tc === 'SCOPE') {
          out.push({ tc, fill: getComputedStyle(t).fill });
        }
      });
      return { out };
    });
    if (navWordmarkFills.error) fail(navWordmarkFills.error);
    if (!navWordmarkFills.out || navWordmarkFills.out.length < 2) {
      fail(`navbar inline-SVG wordmark <text> CORE/SCOPE not found (found: ${JSON.stringify(navWordmarkFills.out)}). Navbar logo must be inline <svg> so CSS vars apply.`);
    }
    // Theme-reactivity probe: override --logo-accent / --logo-accent-hi and
    // confirm fills change. This replaces the old "must not be legacy sage"
    // assertion (sage is now the brand default — see test-logo-default-sage-teal-e2e.js).
    const navReact = await page.evaluate(() => {
      const root = document.querySelector('.nav-brand');
      const before = {};
      root.querySelectorAll('svg text').forEach((t) => {
        const tc = (t.textContent || '').trim();
        if (tc === 'CORE' || tc === 'SCOPE') before[tc] = getComputedStyle(t).fill;
      });
      document.documentElement.style.setProperty('--logo-accent', '#123456');
      document.documentElement.style.setProperty('--logo-accent-hi', '#abcdef');
      const after = {};
      root.querySelectorAll('svg text').forEach((t) => {
        const tc = (t.textContent || '').trim();
        if (tc === 'CORE' || tc === 'SCOPE') after[tc] = getComputedStyle(t).fill;
      });
      // Reset so later assertions on default colors aren't polluted.
      document.documentElement.style.removeProperty('--logo-accent');
      document.documentElement.style.removeProperty('--logo-accent-hi');
      return { before, after };
    });
    if (navReact.before.CORE === navReact.after.CORE) {
      fail(`navbar CORE fill did not change when --logo-accent was overridden (${navReact.before.CORE} → ${navReact.after.CORE}); wordmark must theme via --logo-accent`);
    }
    if (navReact.before.SCOPE === navReact.after.SCOPE) {
      fail(`navbar SCOPE fill did not change when --logo-accent-hi was overridden (${navReact.before.SCOPE} → ${navReact.after.SCOPE}); wordmark must theme via --logo-accent-hi`);
    }
    console.log(`  ✅ navbar wordmark fills are theme-reactive (CORE ${navReact.before.CORE}→${navReact.after.CORE}, SCOPE ${navReact.before.SCOPE}→${navReact.after.SCOPE})`);
    passed++;

    // 2. Hero SVG must NOT have a full-canvas opaque background rect.
    await page.evaluate(() => { window.location.hash = '#/home'; });
    await page.waitForFunction(() => location.hash === '#/home' || location.hash === '#/');
    await page.waitForSelector('.home-hero', { timeout: 8000 });
    // Ensure light theme survives reload.
    await page.evaluate(() => { document.documentElement.setAttribute('data-theme', 'light'); });

    const heroBg = await page.evaluate(() => {
      const hero = document.querySelector('.home-hero');
      if (!hero) return { error: '.home-hero missing' };
      const svg = hero.querySelector('svg');
      if (!svg) return { error: '.home-hero has no inline <svg> child (hero must be inline so CSS vars apply)' };
      // Look for a child <rect> that covers the entire viewBox with a non-transparent fill.
      const rects = svg.querySelectorAll('rect');
      const offending = [];
      rects.forEach((r) => {
        const w = r.getAttribute('width') || '';
        const h = r.getAttribute('height') || '';
        const cs = getComputedStyle(r);
        const fill = cs.fill || '';
        const op = parseFloat(cs.fillOpacity || '1');
        // legacy hero shipped <rect width=1200 height=300 fill=var(--logo-bg, #0e1714)>
        if ((w === '1200' || w === '100%') && (h === '300' || h === '100%') && fill && fill !== 'none' && fill !== 'rgba(0, 0, 0, 0)' && op > 0.05) {
          offending.push({ w, h, fill, op });
        }
      });
      return { offending, rectCount: rects.length };
    });
    if (heroBg.error) fail(heroBg.error);
    if (heroBg.offending && heroBg.offending.length > 0) {
      fail(`hero SVG has full-canvas opaque background rect — paints over light theme: ${JSON.stringify(heroBg.offending)}`);
    }
    console.log(`  ✅ hero SVG has no full-canvas opaque background rect`);
    passed++;

    // 3. Hero wordmark CORE/SCOPE must be theme-reactive — overriding
    //    --logo-accent / --logo-accent-hi must repaint the hero wordmark too.
    const heroWordmarkFills = await page.evaluate(() => {
      const hero = document.querySelector('.home-hero');
      if (!hero) return { error: '.home-hero missing' };
      const out = [];
      hero.querySelectorAll('svg text').forEach((t) => {
        const tc = (t.textContent || '').trim();
        if (tc === 'CORE' || tc === 'SCOPE') {
          out.push({ tc, fill: getComputedStyle(t).fill });
        }
      });
      return { out };
    });
    if (heroWordmarkFills.error) fail(heroWordmarkFills.error);
    if (!heroWordmarkFills.out || heroWordmarkFills.out.length < 2) {
      fail(`hero inline-SVG wordmark <text> CORE/SCOPE not found (found: ${JSON.stringify(heroWordmarkFills.out)})`);
    }
    const heroReact = await page.evaluate(() => {
      const hero = document.querySelector('.home-hero');
      const before = {};
      hero.querySelectorAll('svg text').forEach((t) => {
        const tc = (t.textContent || '').trim();
        if (tc === 'CORE' || tc === 'SCOPE') before[tc] = getComputedStyle(t).fill;
      });
      document.documentElement.style.setProperty('--logo-accent', '#654321');
      document.documentElement.style.setProperty('--logo-accent-hi', '#fedcba');
      const after = {};
      hero.querySelectorAll('svg text').forEach((t) => {
        const tc = (t.textContent || '').trim();
        if (tc === 'CORE' || tc === 'SCOPE') after[tc] = getComputedStyle(t).fill;
      });
      document.documentElement.style.removeProperty('--logo-accent');
      document.documentElement.style.removeProperty('--logo-accent-hi');
      return { before, after };
    });
    if (heroReact.before.CORE === heroReact.after.CORE) {
      fail(`hero CORE fill did not change when --logo-accent was overridden (${heroReact.before.CORE} → ${heroReact.after.CORE})`);
    }
    if (heroReact.before.SCOPE === heroReact.after.SCOPE) {
      fail(`hero SCOPE fill did not change when --logo-accent-hi was overridden (${heroReact.before.SCOPE} → ${heroReact.after.SCOPE})`);
    }
    console.log(`  ✅ hero wordmark fills are theme-reactive (CORE ${heroReact.before.CORE}→${heroReact.after.CORE}, SCOPE ${heroReact.before.SCOPE}→${heroReact.after.SCOPE})`);
    passed++;

    // 4 & 5. Duotone — CORE fill must differ from SCOPE fill in BOTH navbar
    //   and hero, under BOTH default (dark) and Light themes. Proves the
    //   fog/teal split is preserved across theme rebinds.
    async function fillsByText(rootSelector) {
      return await page.evaluate((sel) => {
        const root = document.querySelector(sel);
        if (!root) return { error: sel + ' missing' };
        const m = {};
        root.querySelectorAll('svg text').forEach((t) => {
          const tc = (t.textContent || '').trim();
          if (tc === 'CORE' || tc === 'SCOPE') m[tc] = getComputedStyle(t).fill;
        });
        return { m };
      }, rootSelector);
    }
    function isNearWhiteOrBlack(rgb) {
      const m = String(rgb).match(/rgb\((\d+),\s*(\d+),\s*(\d+)/);
      if (!m) return false;
      const [r, g, b] = [+m[1], +m[2], +m[3]];
      const max = Math.max(r, g, b), min = Math.min(r, g, b);
      // near-white: all >= 235.  near-black: all <= 25 AND low chroma.
      if (r >= 235 && g >= 235 && b >= 235) return true;
      if (r <= 25 && g <= 25 && b <= 25) return true;
      // also flag fully-desaturated greys (chroma < 10)
      if ((max - min) < 10 && max > 60 && max < 200) return true;
      return false;
    }

    // Navigate back to root + force DEFAULT (dark) theme.
    await page.evaluate(() => { window.location.hash = '#/'; });
    await page.waitForFunction(() => location.hash === '#/home' || location.hash === '#/');
    await page.waitForSelector('.nav-brand', { timeout: 8000 });
    await page.evaluate(() => { document.documentElement.removeAttribute('data-theme'); });

    const navDark = await fillsByText('.nav-brand');
    if (navDark.error) fail(navDark.error);
    if (!navDark.m.CORE || !navDark.m.SCOPE) fail(`navbar (dark) missing CORE/SCOPE: ${JSON.stringify(navDark.m)}`);
    if (navDark.m.CORE === navDark.m.SCOPE) {
      fail(`navbar (dark) wordmark is monotone — CORE=${navDark.m.CORE} SCOPE=${navDark.m.SCOPE}; duotone (fog/teal) must be preserved`);
    }
    if (isNearWhiteOrBlack(navDark.m.CORE)) fail(`navbar (dark) CORE fill is near-white/black/grey: ${navDark.m.CORE}`);
    if (isNearWhiteOrBlack(navDark.m.SCOPE)) fail(`navbar (dark) SCOPE fill is near-white/black/grey: ${navDark.m.SCOPE}`);

    // Light theme
    await page.evaluate(() => { document.documentElement.setAttribute('data-theme', 'light'); });
    const navLight = await fillsByText('.nav-brand');
    if (navLight.error) fail(navLight.error);
    if (navLight.m.CORE === navLight.m.SCOPE) {
      fail(`navbar (light) wordmark is monotone — CORE=${navLight.m.CORE} SCOPE=${navLight.m.SCOPE}; duotone must survive light-theme rebind`);
    }
    console.log(`  ✅ navbar duotone preserved (dark: CORE=${navDark.m.CORE} SCOPE=${navDark.m.SCOPE}; light: CORE=${navLight.m.CORE} SCOPE=${navLight.m.SCOPE})`);
    passed++;

    // Hero duotone
    await page.evaluate(() => { window.location.hash = '#/home'; });
    await page.waitForFunction(() => location.hash === '#/home' || location.hash === '#/');
    await page.waitForSelector('.home-hero', { timeout: 8000 });
    await page.evaluate(() => { document.documentElement.removeAttribute('data-theme'); });
    const heroDark = await fillsByText('.home-hero');
    if (heroDark.error) fail(heroDark.error);
    if (heroDark.m.CORE === heroDark.m.SCOPE) {
      fail(`hero (dark) wordmark is monotone — CORE=${heroDark.m.CORE} SCOPE=${heroDark.m.SCOPE}; duotone must be preserved`);
    }
    if (isNearWhiteOrBlack(heroDark.m.CORE)) fail(`hero (dark) CORE fill is near-white/black/grey: ${heroDark.m.CORE}`);
    if (isNearWhiteOrBlack(heroDark.m.SCOPE)) fail(`hero (dark) SCOPE fill is near-white/black/grey: ${heroDark.m.SCOPE}`);

    await page.evaluate(() => { document.documentElement.setAttribute('data-theme', 'light'); });
    const heroLight = await fillsByText('.home-hero');
    if (heroLight.error) fail(heroLight.error);
    if (heroLight.m.CORE === heroLight.m.SCOPE) {
      fail(`hero (light) wordmark is monotone — CORE=${heroLight.m.CORE} SCOPE=${heroLight.m.SCOPE}; duotone must survive light-theme rebind`);
    }
    console.log(`  ✅ hero duotone preserved (dark: CORE=${heroDark.m.CORE} SCOPE=${heroDark.m.SCOPE}; light: CORE=${heroLight.m.CORE} SCOPE=${heroLight.m.SCOPE})`);
    passed++;

    // 6. Mobile fit: at 360x640 the full wordmark logo must be hidden and
    //    a mark-only .brand-mark-only inline SVG must take its place. Also
    //    asserts the visible logo's right edge does not overflow .nav-left.
    await page.setViewportSize({ width: 360, height: 640 });
    await page.evaluate(() => { window.location.hash = '#/'; });
    await page.waitForFunction(() => location.hash === '#/home' || location.hash === '#/');
    await page.waitForSelector('.nav-brand', { timeout: 8000 });
    // Allow CSS media query to settle.
    await page.waitForTimeout(100);

    const mobile = await page.evaluate(() => {
      const brand = document.querySelector('.nav-brand');
      if (!brand) return { error: '.nav-brand missing' };
      const full = brand.querySelector('svg.brand-logo');
      const mark = brand.querySelector('svg.brand-mark-only');
      const left = document.querySelector('.nav-left');
      const fullVisible = full ? getComputedStyle(full).display !== 'none' : null;
      const markVisible = mark ? getComputedStyle(mark).display !== 'none' : null;
      const visibleSvg = (mark && markVisible) ? mark : (full && fullVisible) ? full : null;
      const visRect = visibleSvg ? visibleSvg.getBoundingClientRect() : null;
      const leftRect = left ? left.getBoundingClientRect() : null;
      return {
        hasFull: !!full,
        hasMark: !!mark,
        fullVisible,
        markVisible,
        visRectRight: visRect ? visRect.right : null,
        leftRectRight: leftRect ? leftRect.right : null,
        viewportWidth: window.innerWidth,
      };
    });
    if (mobile.error) fail(mobile.error);
    if (!mobile.hasMark) {
      fail(`mobile: .brand-mark-only inline SVG missing — required to avoid SCOPE→SCOF clip on ≤400px viewports`);
    }
    if (!mobile.markVisible) {
      fail(`mobile: .brand-mark-only is hidden at 360px — must be display!=none on ≤400px viewports (computed: hidden)`);
    }
    if (mobile.fullVisible) {
      fail(`mobile: .brand-logo (full wordmark SVG) still display!=none at 360px — must be hidden so it cannot clip; visibleRight=${mobile.visRectRight}`);
    }
    if (mobile.visRectRight !== null && mobile.viewportWidth > 0 && mobile.visRectRight > mobile.viewportWidth) {
      fail(`mobile: visible navbar logo right edge ${mobile.visRectRight}px overflows viewport (${mobile.viewportWidth}px)`);
    }
    console.log(`  ✅ mobile (360px): mark-only swap active (full hidden, mark visible, right=${mobile.visRectRight}px ≤ viewport ${mobile.viewportWidth}px)`);
    passed++;

    // 7. Desktop wordmark must NOT clip — every <text> element's bbox in
    //    user-space coords must lie fully inside the SVG's viewBox. The
    //    original navbar SVG ships with viewBox "170 10 860 280" (right
    //    edge x=1030), but the SCOPE <text> with text-anchor="start" at
    //    x=773.8 + width≈338 extends to x≈1111 — clipped to "SCOP" at
    //    every desktop viewport width. Fix: widen the viewBox so the
    //    wordmark fits.
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.evaluate(() => { window.location.hash = '#/'; });
    await page.waitForFunction(() => location.hash === '#/home' || location.hash === '#/');
    await page.waitForSelector('.nav-brand svg.brand-logo', { timeout: 8000 });
    await page.waitForTimeout(150);
    const clip = await page.evaluate(() => {
      const svg = document.querySelector('.nav-brand svg.brand-logo');
      if (!svg) return { error: '.nav-brand svg.brand-logo missing' };
      const vb = (svg.getAttribute('viewBox') || '').split(/\s+/).map(Number);
      if (vb.length !== 4) return { error: 'viewBox malformed: ' + svg.getAttribute('viewBox') };
      const [vx, vy, vw, vh] = vb;
      const offenders = [];
      svg.querySelectorAll('text').forEach((t) => {
        const tc = (t.textContent || '').trim();
        if (tc !== 'CORE' && tc !== 'SCOPE') return;
        const bb = t.getBBox();
        if (bb.x < vx - 0.5 || bb.x + bb.width > vx + vw + 0.5) {
          offenders.push({ text: tc, bboxX: bb.x, bboxRight: bb.x + bb.width, vbX: vx, vbRight: vx + vw });
        }
      });
      return { viewBox: vb, offenders };
    });
    if (clip.error) fail(clip.error);
    if (clip.offenders && clip.offenders.length) {
      fail(`desktop: wordmark <text> overflows SVG viewBox (will be clipped): ${JSON.stringify(clip.offenders)}`);
    }
    console.log(`  ✅ desktop (1280px): CORE/SCOPE bboxes fit inside viewBox ${JSON.stringify(clip.viewBox)}`);
    passed++;

    await browser.close();
    console.log(`\ntest-logo-theme-e2e.js: ${passed}/${total} PASS`);
  } catch (err) {
    try { await browser.close(); } catch (_) {}
    console.error(`test-logo-theme-e2e.js: FAIL — ${err.message}`);
    process.exit(1);
  }
}

main();

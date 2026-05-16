#!/usr/bin/env node
/* Cornmeister logo E2E.
 *
 * Verifies the default navbar and home hero use the Cornmeister radio-node
 * SVG mark from the Dutch Meshcore Toolbox, paired with the required
 * CORNMEISTER.NL / Dutch mesh analyzer text. This intentionally does not
 * assert the old CORE/SCOPE wordmark.
 */
'use strict';

const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';

function fail(msg) {
  console.error(`test-logo-theme-e2e.js: FAIL - ${msg}`);
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
    if (requireChromium) fail(`Chromium required but unavailable: ${err.message}`);
    console.log(`test-logo-theme-e2e.js: SKIP (${err.message.split('\n')[0]})`);
    process.exit(0);
  }

  const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
  page.setDefaultTimeout(10000);
  await page.addInitScript(() => {
    try { localStorage.setItem('meshcore-user-level', 'experienced'); } catch (_) {}
  });

  await page.goto(BASE + '/#/', { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('.nav-brand svg.brand-logo.cornmeister-logo');

  const nav = await page.evaluate(() => {
    const root = document.querySelector('.nav-brand');
    const svg = root && root.querySelector('svg.brand-logo.cornmeister-logo');
    return {
      title: root && root.querySelector('.brand-title')?.textContent.trim(),
      subtitle: root && root.querySelector('.brand-subtitle')?.textContent.trim(),
      circles: svg ? svg.querySelectorAll('circle').length : 0,
      paths: svg ? svg.querySelectorAll('path').length : 0,
      gradients: svg ? svg.querySelectorAll('linearGradient stop[stop-color="#3b82f6"], linearGradient stop[stop-color="#1d4ed8"]').length : 0,
      oldWordmark: !!(root && [...root.querySelectorAll('svg text')].some(t => /CORE|SCOPE/.test(t.textContent || ''))),
    };
  });

  if (nav.title !== 'CORNMEISTER.NL') fail(`navbar title was ${JSON.stringify(nav.title)}`);
  if (nav.subtitle !== 'Dutch mesh analyzer') fail(`navbar subtitle was ${JSON.stringify(nav.subtitle)}`);
  if (nav.circles < 1 || nav.paths < 6) fail(`navbar Cornmeister mark shape missing: ${JSON.stringify(nav)}`);
  if (nav.gradients < 2) fail(`navbar Cornmeister gradient stops missing: ${JSON.stringify(nav)}`);
  if (nav.oldWordmark) fail('navbar still contains old CORE/SCOPE SVG wordmark');

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
    await page.evaluate(() => { window.location.hash = '#/home'; });
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
    await page.evaluate(() => { window.location.hash = '#/home'; });
    await page.waitForFunction(() => location.hash === '#/home' || location.hash === '#/');
    await page.waitForSelector('.nav-brand', { timeout: 8000 });
    // Allow CSS media query to settle.
    await page.waitForTimeout(100);

    const mobile = await page.evaluate(() => {
      const brand = document.querySelector('.nav-brand');
      const text = document.querySelector('.nav-brand .brand-text');
      const title = document.querySelector('.nav-brand .brand-title');
      const firstLink = document.querySelector('.nav-links .nav-link:not(.is-overflow)');
      const logoRect = logo && logo.getBoundingClientRect();
      const dotRect = dot && dot.getBoundingClientRect();
      const brandRect = brand && brand.getBoundingClientRect();
      const textRect = text && text.getBoundingClientRect();
      const titleStyle = title && getComputedStyle(title);
      const textStyle = text && getComputedStyle(text);
      const firstLinkRect = firstLink && firstLink.getBoundingClientRect();
      return {
        logoWidth: logoRect ? logoRect.width : 0,
        logoHeight: logoRect ? logoRect.height : 0,
        dotWidth: dotRect ? dotRect.width : 0,
        gap: logoRect && dotRect ? dotRect.left - logoRect.right : -1,
        titleVisible: !!(titleStyle && titleStyle.display !== 'none' && textStyle && textStyle.display !== 'none'),
        titleWidth: title ? title.getBoundingClientRect().width : 0,
        titleScrollWidth: title ? title.scrollWidth : 0,
        textWidth: textRect ? textRect.width : 0,
        textScrollWidth: text ? text.scrollWidth : 0,
        brandRight: brandRect ? brandRect.right : 0,
        firstLinkLeft: firstLinkRect ? firstLinkRect.left : 0,
      };
    });
    if (layout.logoWidth < 31 || layout.logoWidth > 37 || layout.logoHeight < 31 || layout.logoHeight > 37) {
      fail(`navbar logo did not keep a compact square size at ${width}px: ${JSON.stringify(layout)}`);
    }
    if (layout.dotWidth < 7.5 || layout.gap < 6) {
      fail(`navbar live-dot too close to logo at ${width}px: ${JSON.stringify(layout)}`);
    }
    if (layout.titleVisible && (layout.textWidth + 1 < layout.textScrollWidth || layout.titleWidth + 1 < layout.titleScrollWidth)) {
      fail(`navbar brand text is clipped/squeezed at ${width}px: ${JSON.stringify(layout)}`);
    }
    if (layout.firstLinkLeft && layout.firstLinkLeft + 0.5 < layout.brandRight) {
      fail(`navbar brand overlaps first nav link at ${width}px: ${JSON.stringify(layout)}`);
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
    await page.evaluate(() => { window.location.hash = '#/home'; });
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

  const imageLogoLayout = await page.evaluate(() => {
    const root = document.querySelector('.nav-brand');
    const oldLogo = root && root.querySelector('.brand-logo');
    if (!root || !oldLogo) return null;
    const img = document.createElement('img');
    img.className = 'brand-logo';
    img.setAttribute('width', '240');
    img.setAttribute('height', '72');
    img.src = 'data:image/svg+xml,' + encodeURIComponent('<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 240 72"><rect width="240" height="72" fill="blue"/></svg>');
    oldLogo.replaceWith(img);
    const logoRect = img.getBoundingClientRect();
    const dotRect = root.querySelector('.live-dot').getBoundingClientRect();
    return {
      logoWidth: logoRect.width,
      logoHeight: logoRect.height,
      gap: dotRect.left - logoRect.right,
    };
  });
  if (!imageLogoLayout || imageLogoLayout.logoWidth <= imageLogoLayout.logoHeight || imageLogoLayout.logoWidth > 125 || imageLogoLayout.gap < 6) {
    fail(`navbar image logo scaling regressed: ${JSON.stringify(imageLogoLayout)}`);
  }

  await page.evaluate(() => { window.location.hash = '#/home'; });
  await page.waitForFunction(() => location.hash === '#/home');
  await page.waitForSelector('.home-hero svg.home-hero-logo.cornmeister-logo');

  const hero = await page.evaluate(() => {
    const root = document.querySelector('.home-hero');
    const svg = root && root.querySelector('svg.home-hero-logo.cornmeister-logo');
    return {
      title: root && root.querySelector('.home-hero-brand-name')?.textContent.trim(),
      subtitle: root && root.querySelector('.home-hero-brand-subtitle')?.textContent.trim(),
      circles: svg ? svg.querySelectorAll('circle').length : 0,
      paths: svg ? svg.querySelectorAll('path').length : 0,
      gradients: svg ? svg.querySelectorAll('linearGradient stop[stop-color="#3b82f6"], linearGradient stop[stop-color="#1d4ed8"]').length : 0,
      oldWordmark: !!(root && [...root.querySelectorAll('svg text')].some(t => /CORE|SCOPE/.test(t.textContent || ''))),
    };
  });

  if (hero.title !== 'CORNMEISTER.NL') fail(`hero title was ${JSON.stringify(hero.title)}`);
  if (hero.subtitle !== 'Dutch mesh analyzer') fail(`hero subtitle was ${JSON.stringify(hero.subtitle)}`);
  if (hero.circles < 1 || hero.paths < 6) fail(`hero Cornmeister mark shape missing: ${JSON.stringify(hero)}`);
  if (hero.gradients < 2) fail(`hero Cornmeister gradient stops missing: ${JSON.stringify(hero)}`);
  if (hero.oldWordmark) fail('hero still contains old CORE/SCOPE SVG wordmark');

  await browser.close();
  console.log('test-logo-theme-e2e.js: PASS');
}

main().catch(async (err) => {
  console.error(`test-logo-theme-e2e.js: FAIL - ${err.message}`);
  process.exit(1);
});

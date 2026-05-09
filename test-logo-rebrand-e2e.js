#!/usr/bin/env node
/* Logo rebrand E2E — verifies the new CoreScope SVG logo is wired into
 * the navbar (replacing the 🍄 emoji + "CoreScope" text) and that the
 * homepage renders a hero version of the logo above the H1.
 *
 * Asserts (in order):
 *   1. Navbar has an <img> whose src ends with /img/corescope-logo.svg
 *      OR an inline <svg class="brand-logo"> (PR #1137 inlined the SVG so
 *      it can inherit page CSS vars and theme on light/dark).
 *      The brand element must be INSIDE the .nav-brand link (so the brand
 *      link stays clickable).
 *   2. Old .brand-icon (🍄) and .brand-text spans are gone.
 *   3. The .live-dot WS-status indicator is still present and visible
 *      and sits to the right of the logo (left edge of dot ≥ right edge of img).
 *   4. The home page (#/home) renders an <img.home-hero-logo> whose src
 *      ends with /img/corescope-hero.svg, ABOVE the .home-hero h1.
 *   5. Both SVG assets resolve with HTTP 200 and content-type contains
 *      "svg" (catches a missing file regression cleanly).
 *
 * CI gating mirrors the existing playwright e2e tests: with
 * CHROMIUM_REQUIRE=1 a missing Chromium is a HARD FAIL.
 */
'use strict';

const { chromium } = require('playwright');
const http = require('http');

const BASE = process.env.BASE_URL || 'http://localhost:13581';

function fail(msg) {
  console.error(`test-logo-rebrand-e2e.js: FAIL — ${msg}`);
  process.exit(1);
}
function assert(cond, msg) { if (!cond) fail(msg || 'assertion failed'); }

async function head(url) {
  return new Promise((resolve, reject) => {
    const u = new URL(url);
    const req = http.request({
      method: 'GET',
      hostname: u.hostname,
      port: u.port || 80,
      path: u.pathname + (u.search || ''),
    }, (res) => {
      let body = '';
      res.on('data', (c) => { body += c; if (body.length > 4096) body = body.slice(0, 4096); });
      res.on('end', () => resolve({ status: res.statusCode, ct: res.headers['content-type'] || '', sample: body }));
    });
    req.on('error', reject);
    req.end();
  });
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
      console.error(`test-logo-rebrand-e2e.js: FAIL — Chromium required but unavailable: ${err.message}`);
      process.exit(1);
    }
    console.log(`test-logo-rebrand-e2e.js: SKIP (Chromium unavailable: ${err.message.split('\n')[0]})`);
    process.exit(0);
  }

  let passed = 0;
  const total = 6;
  try {
    const context = await browser.newContext({ viewport: { width: 1280, height: 900 } });
    const page = await context.newPage();
    page.setDefaultTimeout(10000);

    // 1. Navbar has the brand logo inside .nav-brand. Post PR #1137 the
    //    default is an inline <svg.brand-logo>; if an operator overrode
    //    branding.logoUrl the customizer swaps it for an <img.brand-logo>.
    await page.goto(BASE + '/#/', { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('.nav-brand', { timeout: 8000 });
    const navBrand = await page.evaluate(() => {
      const el = document.querySelector('.nav-brand .brand-logo');
      if (!el) return { ok: false, reason: 'no .brand-logo in .nav-brand' };
      const tag = el.tagName.toLowerCase();
      if (tag === 'img') {
        const src = el.getAttribute('src') || '';
        return { ok: /corescope-logo\.svg($|\?)/.test(src), tag, src };
      }
      if (tag === 'svg') {
        // Inline SVG default — verify it actually renders the brand artwork.
        const hasText = !!el.querySelector('text');
        return { ok: hasText, tag, src: '<inline-svg>' };
      }
      return { ok: false, reason: 'unexpected .brand-logo tag: ' + tag };
    });
    if (!navBrand.ok) {
      fail(`navbar .brand-logo invalid (${navBrand.reason || 'tag=' + navBrand.tag + ' src=' + navBrand.src})`);
    }
    console.log(`  ✅ navbar contains .brand-logo (${navBrand.tag})`);
    passed++;

    // 2. Old emoji + brand-text are gone
    const oldIcon = await page.$('.nav-brand .brand-icon');
    const oldText = await page.$('.nav-brand .brand-text');
    if (oldIcon || oldText) fail('legacy .brand-icon / .brand-text still present (should be replaced by SVG logo)');
    console.log('  ✅ legacy mushroom emoji + "CoreScope" text removed');
    passed++;

    // 3. WS connection state indicator: #1173 replaced .live-dot with the
    // packet-driven brand-logo pulse. The state surface is the .brand-logo
    // SVG itself (gains .logo-disconnected on close, removes it on open),
    // and the test seam at window.__corescopeLogo.
    //
    // Note: the previous version of this test asserted the geometry of
    // the .live-dot relative to the brand-logo (dot must be to the right
    // of the SVG). That coverage is replaced with a brand-logo layout
    // assertion (visible, non-zero box, sensible aspect) so SVG rendering
    // regressions are still caught — they simply moved targets.
    const noLegacyDot = await page.$('.nav-brand .live-dot, .nav-brand #liveDot');
    if (noLegacyDot) fail('.live-dot / #liveDot still present — should have been removed by #1173');
    const seam = await page.evaluate(() => {
      return !!(window.__corescopeLogo && typeof window.__corescopeLogo.setConnected === 'function' && typeof window.__corescopeLogo.pulse === 'function');
    });
    if (!seam) fail('window.__corescopeLogo (setConnected + pulse) is the new WS-state seam — missing');
    // Brand-logo layout sanity (replaces the dot-right-of-logo geometry assertion).
    const brandLayout = await page.evaluate(() => {
      const i = document.querySelector('.nav-brand .brand-logo');
      if (!i) return { ok: false, reason: 'no .brand-logo' };
      const r = i.getBoundingClientRect();
      const cs = getComputedStyle(i);
      return {
        ok: true,
        w: r.width, h: r.height,
        visible: cs.display !== 'none' && cs.visibility !== 'hidden' && parseFloat(cs.opacity || '1') > 0,
      };
    });
    // assert: brand-logo is visibly rendered with a sensible box.
    assert(brandLayout.ok, 'brand-logo layout probe failed: ' + brandLayout.reason);
    assert(brandLayout.visible, 'brand-logo not visible (display/visibility/opacity)');
    assert(brandLayout.w >= 60 && brandLayout.h >= 16,
      `brand-logo too small: ${brandLayout.w.toFixed(1)}×${brandLayout.h.toFixed(1)} (expected ≥60×16)`);
    console.log('  ✅ legacy .live-dot removed; brand-logo Logo state seam present; brand-logo layout sane');
    passed++;

    // 4. Home hero image — ensure user level is set so we render the hero,
    // not the new-user chooser screen.
    await page.evaluate(() => { try { localStorage.setItem('meshcore-user-level', 'experienced'); } catch (_) {} });
    await page.evaluate(() => { window.location.hash = '#/home'; });
    await page.waitForFunction(() => location.hash === '#/home');
    // Reload so the SPA router picks up the route AND localStorage is honored.
    await page.reload({ waitUntil: 'domcontentloaded' });
    await page.waitForSelector('.home-hero', { timeout: 8000 });
    const heroBrand = await page.evaluate(() => {
      const hero = document.querySelector('.home-hero');
      if (!hero) return { ok: false, reason: '.home-hero missing' };
      // PR #1137: inline <svg.home-hero-logo> by default; legacy <img> still
      // valid for any operator who shipped a custom build.
      const el = hero.querySelector('.home-hero-logo');
      if (!el) return { ok: false, reason: '.home-hero-logo missing inside .home-hero' };
      const tag = el.tagName.toLowerCase();
      if (tag === 'img') {
        const src = el.getAttribute('src') || '';
        return { ok: /corescope-hero\.svg($|\?)/.test(src), tag, src };
      }
      if (tag === 'svg') {
        const hasText = !!el.querySelector('text');
        return { ok: hasText, tag };
      }
      return { ok: false, reason: 'unexpected .home-hero-logo tag: ' + tag };
    });
    if (!heroBrand.ok) {
      fail(`home page .home-hero-logo invalid (${heroBrand.reason || 'tag=' + heroBrand.tag})`);
    }
    const order = await page.evaluate(() => {
      const hero = document.querySelector('.home-hero');
      if (!hero) return -1;
      const img = hero.querySelector('.home-hero-logo');
      const h1 = hero.querySelector('h1');
      if (!img || !h1) return -2;
      return (img.compareDocumentPosition(h1) & Node.DOCUMENT_POSITION_FOLLOWING) ? 1 : 0;
    });
    if (order !== 1) fail(`home-hero brand element must precede the <h1> (compareDocumentPosition=${order})`);
    console.log(`  ✅ home page hero contains .home-hero-logo (${heroBrand.tag}) above the h1`);
    passed++;

    // 5. Both assets actually serve
    const [a, b] = await Promise.all([
      head(BASE + '/img/corescope-logo.svg'),
      head(BASE + '/img/corescope-hero.svg'),
    ]);
    if (a.status !== 200 || !/svg/i.test(a.ct)) fail(`/img/corescope-logo.svg → status=${a.status} ct=${a.ct}`);
    if (b.status !== 200 || !/svg/i.test(b.ct)) fail(`/img/corescope-hero.svg → status=${b.status} ct=${b.ct}`);
    console.log('  ✅ both /img/corescope-{logo,hero}.svg return 200 with svg content-type');
    passed++;

    // 6. Customizer override path still works after the rebrand. Operators
    // can override branding.siteName + branding.logoUrl via the customizer
    // (cs-theme-overrides localStorage key in customize-v2.js); the old
    // code mutated .brand-text / .brand-icon (which no longer exist), so
    // a naive removal silently breaks the override flow. Verify the navbar
    // logo <img> picks up the override on next load.
    await page.evaluate(() => {
      try {
        // customize-v2.js storage key for live overrides.
        localStorage.setItem('cs-theme-overrides', JSON.stringify({
          branding: { siteName: 'OverrideSite', logoUrl: '/img/corescope-logo.svg?override=1' }
        }));
      } catch (_) {}
    });
    await page.goto(BASE + '/#/', { waitUntil: 'networkidle' });
    // PR #1137: default brand is inline <svg>; the override path swaps it
    // for an <img>. Wait for either tag to be present (boot-time render).
    await page.waitForSelector('.nav-brand .brand-logo', { timeout: 8000 });
    // Force-apply the override pipeline (in case _customizerV2.init was racing
    // /api/config/theme — production code's DOMContentLoaded boot path runs
    // synchronously, but instrumented JS in CI can be slower).
    await page.evaluate(() => {
      try {
        if (window._customizerV2 && typeof window._customizerV2.init === 'function') {
          window._customizerV2.init(window.SITE_CONFIG || {});
        }
      } catch (_) {}
    });
    // Give pipeline a moment to settle: the helper swaps inline-<svg> → <img>.
    await page.waitForFunction(() => {
      var img = document.querySelector('.nav-brand img');
      return img && /override=1/.test(img.getAttribute('src') || '');
    }, { timeout: 5000 }).catch(() => {});
    const overrideState = await page.evaluate(() => {
      var img = document.querySelector('.nav-brand img');
      return {
        src: img ? img.getAttribute('src') || '' : null,
        alt: img ? img.getAttribute('alt') || '' : null,
        title: document.title,
        hasV2: !!window._customizerV2,
        ovStored: localStorage.getItem('cs-theme-overrides'),
      };
    });
    if (!overrideState.src || !/override=1/.test(overrideState.src)) {
      fail(`customizer logoUrl override did not propagate to navbar img (src=${overrideState.src} hasV2=${overrideState.hasV2} ovStored=${overrideState.ovStored})`);
    }
    if (overrideState.title !== 'OverrideSite') {
      fail(`customizer siteName override did not update document.title (got: ${overrideState.title})`);
    }
    console.log('  ✅ customizer branding.siteName + branding.logoUrl overrides still apply post-rebrand');
    passed++;
    // Clean up the override so subsequent test runs aren't polluted.
    await page.evaluate(() => { try { localStorage.removeItem('cs-theme-overrides'); } catch (_) {} });

    await browser.close();
    console.log(`\ntest-logo-rebrand-e2e.js: ${passed}/${total} PASS`);
  } catch (err) {
    try { await browser.close(); } catch (_) {}
    console.error(`test-logo-rebrand-e2e.js: FAIL — ${err.message}`);
    process.exit(1);
  }
}

main();

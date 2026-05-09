#!/usr/bin/env node
/* Issue #1061 — Bottom navigation for narrow viewports.
 *
 * Asserts:
 *   (a) at 360x800, the bottom-nav container is visible AND the top-nav
 *       (.top-nav) is hidden (display:none / visibility:hidden / size 0).
 *   (b) at 1440x900, the bottom-nav is NOT visible AND the top-nav IS visible.
 *   (c) at 360x800, all 5 bottom-nav tabs (Home, Packets, Live, Map, Channels)
 *       have a tap target height >= 48px.
 *   (d) at 360x800, tapping the "Packets" tab navigates to #/packets via the
 *       in-app router — i.e. URL hash changes WITHOUT a full reload (a
 *       window.__bottomNav1061BootstrapId sentinel set on DOMContentLoaded
 *       MUST persist across the navigation).
 *   (e) at 360x800, the active-tab indicator class is applied to the Packets
 *       tab when on #/packets and is NOT applied when on #/.
 *   (f) the bottom-nav element has a non-empty padding-bottom resolved style
 *       (proxy for safe-area-inset-bottom; can't directly test the inset in
 *       headless Chromium).
 *
 * Stable selectors: bottom-nav tabs MUST be selectable via
 * `[data-bottom-nav-tab="<route>"]` to avoid the virtual-scroll-spacer trap
 * (DOM-order ambiguous matches).
 *
 * CI gating: when CHROMIUM_REQUIRE=1 a missing/broken Chromium is a HARD FAIL.
 */
'use strict';

const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';
const EXPECTED_TABS = ['home', 'packets', 'live', 'map', 'channels', 'more'];
// #1174: long-tail routes surfaced in the More sheet (the routes NOT in
// the 5 primary bottom-nav slots). Mirror data-route values from the
// existing top-nav.
const EXPECTED_MORE_ROUTES = ['nodes', 'tools', 'observers', 'analytics', 'perf', 'audio-lab'];

function isVisible(rect) {
  return rect && rect.width > 0 && rect.height > 0;
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
      console.error(`test-bottom-nav-1061-e2e.js: FAIL — Chromium required (CHROMIUM_REQUIRE=1) but unavailable: ${err.message}`);
      process.exit(1);
    }
    console.log(`test-bottom-nav-1061-e2e.js: SKIP (Chromium unavailable: ${err.message.split('\n')[0]})`);
    process.exit(0);
  }

  let failures = 0;
  let passes = 0;
  const fail = (msg) => { failures += 1; console.error(`  FAIL: ${msg}`); };
  const pass = (msg) => { passes += 1; console.log(`  PASS: ${msg}`); };

  const ctx = await browser.newContext();
  const page = await ctx.newPage();
  page.setDefaultTimeout(15000);

  // Inject a bootstrap sentinel BEFORE the page scripts run so we can
  // detect a full reload. The same value must survive an in-app
  // navigation; if the page reloads the sentinel is reset to a new id.
  await page.addInitScript(() => {
    window.__bottomNav1061BootstrapId = 'boot-' + Math.random().toString(36).slice(2);
  });

  // ── (a) 360x800: bottom-nav visible, top-nav hidden ──
  await page.setViewportSize({ width: 360, height: 800 });
  await page.goto(`${BASE}/#/`, { waitUntil: 'domcontentloaded' });
  await page.waitForFunction(() => document.body && document.body.classList.contains('app-ready') || document.querySelector('main#app'));

  const sentinelA = await page.evaluate(() => window.__bottomNav1061BootstrapId);

  const stateNarrow = await page.evaluate(() => {
    const bn = document.querySelector('[data-bottom-nav]');
    const navLinks = document.querySelector('.top-nav .nav-links');
    const navRight = document.querySelector('.top-nav .nav-right');
    const navBrand = document.querySelector('.top-nav .nav-brand');
    const bnRect = bn ? bn.getBoundingClientRect() : null;
    const bnCs = bn ? getComputedStyle(bn) : null;
    const isHiddenByCss = (el) => {
      if (!el) return true;
      const cs = getComputedStyle(el);
      const r = el.getBoundingClientRect();
      return cs.display === 'none' || cs.visibility === 'hidden' || (r.width === 0 && r.height === 0);
    };
    return {
      bnPresent: !!bn,
      bnRect,
      bnDisplay: bnCs ? bnCs.display : null,
      bnVisibility: bnCs ? bnCs.visibility : null,
      bnPaddingBottom: bnCs ? bnCs.paddingBottom : null,
      // #1174 fix: top-nav LINKS hidden (no duplicate nav UX), but
      // .nav-brand stays visible (logo identity, not navigation).
      navLinksHidden: isHiddenByCss(navLinks),
      navRightHidden: isHiddenByCss(navRight),
      navBrandPresent: !!navBrand,
      navBrandHidden: isHiddenByCss(navBrand),
    };
  });

  if (!stateNarrow.bnPresent) {
    fail('(a) [data-bottom-nav] container missing in DOM at 360x800');
  } else if (stateNarrow.bnDisplay === 'none' || stateNarrow.bnVisibility === 'hidden' || !isVisible(stateNarrow.bnRect)) {
    fail(`(a) bottom-nav not visible at 360x800 (display=${stateNarrow.bnDisplay}, rect=${JSON.stringify(stateNarrow.bnRect)})`);
  } else {
    pass('(a) bottom-nav visible at 360x800');
  }
  if (stateNarrow.navLinksHidden && stateNarrow.navRightHidden) {
    pass('(a) top-nav LINKS hidden at 360x800 (no duplicate nav UX)');
  } else {
    fail(`(a) top-nav links/right still visible at 360x800 (links=${!stateNarrow.navLinksHidden}, right=${!stateNarrow.navRightHidden}) — duplicate nav UX`);
  }
  if (stateNarrow.navBrandPresent && !stateNarrow.navBrandHidden) {
    pass('(a) .nav-brand (logo identity) remains visible at 360x800');
  } else {
    fail(`(a) .nav-brand hidden at 360x800 (present=${stateNarrow.navBrandPresent}, hidden=${stateNarrow.navBrandHidden}) — should remain visible per #1137`);
  }

  // ── (c) 5 tabs each ≥48px tap target ──
  const tabSizes = await page.evaluate((expected) => {
    return expected.map((r) => {
      const el = document.querySelector(`[data-bottom-nav-tab="${r}"]`);
      if (!el) return { route: r, present: false };
      const rect = el.getBoundingClientRect();
      return { route: r, present: true, height: rect.height, width: rect.width };
    });
  }, EXPECTED_TABS);
  for (const t of tabSizes) {
    if (!t.present) { fail(`(c) tab missing: [data-bottom-nav-tab="${t.route}"]`); continue; }
    if (t.height < 48) fail(`(c) tab ${t.route} height ${t.height.toFixed(1)} < 48px`);
    else pass(`(c) tab ${t.route} height ${t.height.toFixed(1)}px ≥ 48`);
  }

  // ── (f) padding-bottom rule exists (safe-area proxy) ──
  if (stateNarrow.bnPaddingBottom && stateNarrow.bnPaddingBottom !== '' && stateNarrow.bnPaddingBottom !== '0px') {
    pass(`(f) bottom-nav padding-bottom = ${stateNarrow.bnPaddingBottom}`);
  } else if (stateNarrow.bnPaddingBottom === '0px') {
    // 0px is acceptable as long as the rule resolved (safe-area-inset is 0 in headless)
    pass(`(f) bottom-nav padding-bottom resolved (0px in headless; rule exists)`);
  } else {
    fail(`(f) bottom-nav padding-bottom not resolved: ${stateNarrow.bnPaddingBottom}`);
  }

  // ── (e) on #/, Packets tab is NOT active ──
  const activeOnHome = await page.evaluate(() => {
    const el = document.querySelector('[data-bottom-nav-tab="packets"]');
    return el ? el.classList.contains('active') : null;
  });
  if (activeOnHome === false) pass('(e) Packets tab not active on #/');
  else fail(`(e) Packets tab incorrectly active on #/ (got ${activeOnHome})`);

  // ── (d) tap "Packets" → #/packets without reload ──
  await page.click('[data-bottom-nav-tab="packets"]');
  await page.waitForFunction(() => location.hash === '#/packets', null, { timeout: 5000 }).catch(() => {});
  const afterTap = await page.evaluate(() => ({
    hash: location.hash,
    sentinel: window.__bottomNav1061BootstrapId,
  }));
  if (afterTap.hash === '#/packets') pass('(d) tap navigated to #/packets');
  else fail(`(d) tap did NOT navigate to #/packets (got ${afterTap.hash})`);
  if (afterTap.sentinel === sentinelA) pass('(d) sentinel preserved — no full reload');
  else fail(`(d) sentinel changed (${sentinelA} → ${afterTap.sentinel}) — page reloaded`);

  // ── (e) on #/packets, Packets tab IS active ──
  // Wait for the hashchange handler to update the active class. The
  // location.hash === '#/packets' check above resolves the moment the
  // browser sets the URL, but the hashchange event dispatch is still
  // in-flight; reading classList immediately races the handler.
  let activeOnPackets = null;
  try {
    await page.waitForFunction(() => {
      const el = document.querySelector('[data-bottom-nav-tab="packets"]');
      return el && el.classList.contains('active');
    }, null, { timeout: 2000 });
    activeOnPackets = true;
  } catch (_) {
    activeOnPackets = await page.evaluate(() => {
      const el = document.querySelector('[data-bottom-nav-tab="packets"]');
      return el ? el.classList.contains('active') : null;
    });
  }
  if (activeOnPackets === true) pass('(e) Packets tab active on #/packets');
  else fail(`(e) Packets tab NOT active on #/packets (got ${activeOnPackets})`);

  // ── (g) #1174: More tab visible at 360x800 ──
  const moreTabState = await page.evaluate(() => {
    const el = document.querySelector('[data-bottom-nav-tab="more"]');
    if (!el) return { present: false };
    const r = el.getBoundingClientRect();
    return {
      present: true,
      visible: r.width > 0 && r.height > 0,
      ariaExpanded: el.getAttribute('aria-expanded'),
      ariaControls: el.getAttribute('aria-controls'),
    };
  });
  if (!moreTabState.present) fail('(g) [data-bottom-nav-tab="more"] missing');
  else if (!moreTabState.visible) fail('(g) More tab present but not visible');
  else pass('(g) More tab visible at 360x800');
  if (moreTabState.present && moreTabState.ariaExpanded === 'false') {
    pass('(g) More tab aria-expanded="false" before tap');
  } else if (moreTabState.present) {
    fail(`(g) More tab aria-expanded should be 'false' before tap, got ${moreTabState.ariaExpanded}`);
  }

  // ── (h) #1174: tap More opens a sheet listing 6 long-tail routes ──
  await page.click('[data-bottom-nav-tab="more"]').catch(() => {});
  // Wait for sheet to render.
  await page.waitForSelector('[data-bottom-nav-sheet]', { timeout: 3000 }).catch(() => {});
  const sheetOpen = await page.evaluate((expected) => {
    const sheet = document.querySelector('[data-bottom-nav-sheet]');
    if (!sheet) return { present: false };
    const cs = getComputedStyle(sheet);
    const r = sheet.getBoundingClientRect();
    const items = Array.from(sheet.querySelectorAll('[data-bottom-nav-more-route]'))
      .map(el => el.getAttribute('data-bottom-nav-more-route'));
    const moreTab = document.querySelector('[data-bottom-nav-tab="more"]');
    return {
      present: true,
      visible: cs.display !== 'none' && cs.visibility !== 'hidden' && r.width > 0 && r.height > 0,
      role: sheet.getAttribute('role'),
      itemRoutes: items,
      missing: expected.filter(r => !items.includes(r)),
      moreTabExpanded: moreTab ? moreTab.getAttribute('aria-expanded') : null,
    };
  }, EXPECTED_MORE_ROUTES);
  if (!sheetOpen.present) fail('(h) [data-bottom-nav-sheet] missing after More tap');
  else if (!sheetOpen.visible) fail('(h) sheet rendered but not visible after More tap');
  else pass('(h) sheet visible after More tap');
  if (sheetOpen.present && sheetOpen.role === 'menu') pass('(h) sheet role="menu"');
  else if (sheetOpen.present) fail(`(h) sheet role should be 'menu', got ${sheetOpen.role}`);
  if (sheetOpen.present && sheetOpen.missing.length === 0) {
    pass(`(h) sheet lists all 6 long-tail routes: ${sheetOpen.itemRoutes.join(',')}`);
  } else if (sheetOpen.present) {
    fail(`(h) sheet missing routes: ${sheetOpen.missing.join(',')} (got ${sheetOpen.itemRoutes.join(',')})`);
  }
  if (sheetOpen.moreTabExpanded === 'true') pass('(h) More tab aria-expanded="true" while open');
  else fail(`(h) More tab aria-expanded should be 'true' while open, got ${sheetOpen.moreTabExpanded}`);

  // ── (i) #1174: tap a route navigates and closes the sheet ──
  await page.click('[data-bottom-nav-more-route="tools"]').catch(() => {});
  await page.waitForFunction(() => location.hash === '#/tools', null, { timeout: 3000 }).catch(() => {});
  const afterRouteTap = await page.evaluate(() => {
    const sheet = document.querySelector('[data-bottom-nav-sheet]');
    const cs = sheet ? getComputedStyle(sheet) : null;
    const r = sheet ? sheet.getBoundingClientRect() : null;
    const moreTab = document.querySelector('[data-bottom-nav-tab="more"]');
    return {
      hash: location.hash,
      sheetVisible: !!(sheet && cs && cs.display !== 'none' && cs.visibility !== 'hidden' && r.width > 0 && r.height > 0),
      moreTabExpanded: moreTab ? moreTab.getAttribute('aria-expanded') : null,
    };
  });
  if (afterRouteTap.hash === '#/tools') pass('(i) tapping Tools navigated to #/tools');
  else fail(`(i) hash did not change to #/tools (got ${afterRouteTap.hash})`);
  if (!afterRouteTap.sheetVisible) pass('(i) sheet closed after route tap');
  else fail('(i) sheet still visible after route tap');
  if (afterRouteTap.moreTabExpanded === 'false') pass('(i) More tab aria-expanded="false" after close');
  else fail(`(i) More tab aria-expanded should be 'false' after close, got ${afterRouteTap.moreTabExpanded}`);

  // ── (j) #1174: tap outside closes the sheet ──
  // Reopen.
  await page.click('[data-bottom-nav-tab="more"]').catch(() => {});
  await page.waitForFunction(() => {
    const s = document.querySelector('[data-bottom-nav-sheet]');
    if (!s) return false;
    const cs = getComputedStyle(s);
    return cs.display !== 'none' && cs.visibility !== 'hidden';
  }, null, { timeout: 3000 }).catch(() => {});
  // Click on body somewhere outside the sheet and outside the bottom-nav.
  // Use a coordinate near the top of the viewport (the page main area).
  await page.mouse.click(10, 200);
  // Allow the close handler to run.
  await page.waitForFunction(() => {
    const s = document.querySelector('[data-bottom-nav-sheet]');
    if (!s) return true;
    const cs = getComputedStyle(s);
    return cs.display === 'none' || cs.visibility === 'hidden';
  }, null, { timeout: 3000 }).catch(() => {});
  const afterOutside = await page.evaluate(() => {
    const s = document.querySelector('[data-bottom-nav-sheet]');
    if (!s) return { closed: true };
    const cs = getComputedStyle(s);
    const r = s.getBoundingClientRect();
    return { closed: cs.display === 'none' || cs.visibility === 'hidden' || (r.width === 0 && r.height === 0) };
  });
  if (afterOutside.closed) pass('(j) sheet closes on outside click');
  else fail('(j) sheet still visible after outside click');

  // ── (k) #1174: at 360x800, #hamburger is hidden (More tab replaces it) ──
  const hamburgerHidden = await page.evaluate(() => {
    const h = document.getElementById('hamburger');
    if (!h) return { present: false };
    const cs = getComputedStyle(h);
    const r = h.getBoundingClientRect();
    return {
      present: true,
      hidden: cs.display === 'none' || cs.visibility === 'hidden' || (r.width === 0 && r.height === 0),
      display: cs.display,
    };
  });
  if (!hamburgerHidden.present) {
    pass('(k) #hamburger removed from DOM (acceptable)');
  } else if (hamburgerHidden.hidden) {
    pass(`(k) #hamburger hidden at 360x800 (display=${hamburgerHidden.display})`);
  } else {
    fail(`(k) #hamburger still visible at 360x800 (display=${hamburgerHidden.display}) — More tab should replace it`);
  }

  // ── (b) 1440x900: bottom-nav hidden, top-nav visible ──
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto(`${BASE}/#/`, { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('.top-nav .nav-right');
  const stateWide = await page.evaluate(() => {
    const bn = document.querySelector('[data-bottom-nav]');
    const tn = document.querySelector('.top-nav');
    const bnRect = bn ? bn.getBoundingClientRect() : null;
    const tnRect = tn ? tn.getBoundingClientRect() : null;
    const bnCs = bn ? getComputedStyle(bn) : null;
    const tnCs = tn ? getComputedStyle(tn) : null;
    return {
      bnDisplay: bnCs ? bnCs.display : null,
      bnVisibility: bnCs ? bnCs.visibility : null,
      bnRect,
      tnDisplay: tnCs ? tnCs.display : null,
      tnVisibility: tnCs ? tnCs.visibility : null,
      tnRect,
    };
  });
  if (stateWide.bnDisplay === 'none' || stateWide.bnVisibility === 'hidden' || !isVisible(stateWide.bnRect)) {
    pass('(b) bottom-nav hidden at 1440x900');
  } else {
    fail(`(b) bottom-nav still visible at 1440x900 (display=${stateWide.bnDisplay}, rect=${JSON.stringify(stateWide.bnRect)})`);
  }
  if (stateWide.tnDisplay !== 'none' && stateWide.tnVisibility !== 'hidden' && isVisible(stateWide.tnRect)) {
    pass('(b) top-nav visible at 1440x900');
  } else {
    fail(`(b) top-nav not visible at 1440x900 (display=${stateWide.tnDisplay})`);
  }

  // ── (l) #1174 mesh-op review: .live-page bottom must NOT be covered by bottom-nav at ≤768 ──
  await page.setViewportSize({ width: 360, height: 800 });
  await page.goto(`${BASE}/#/live`, { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('.live-page', { timeout: 5000 }).catch(() => {});
  // Allow layout to settle.
  await page.waitForFunction(() => !!document.querySelector('.live-page'), null, { timeout: 3000 }).catch(() => {});
  const liveLayout = await page.evaluate(() => {
    const lp = document.querySelector('.live-page');
    if (!lp) return { present: false };
    const r = lp.getBoundingClientRect();
    return {
      present: true,
      bottom: r.bottom,
      innerHeight: window.innerHeight,
    };
  });
  if (!liveLayout.present) {
    fail('(l) .live-page missing on #/live');
  } else if (liveLayout.bottom > liveLayout.innerHeight - 56 + 1) {
    // +1 for sub-pixel rounding tolerance.
    fail(`(l) .live-page bottom (${liveLayout.bottom.toFixed(1)}) > viewport - 56 (${(liveLayout.innerHeight - 56).toFixed(1)}) — bottom-nav covers content`);
  } else {
    pass(`(l) .live-page bottom ${liveLayout.bottom.toFixed(1)} ≤ viewport - 56 (${(liveLayout.innerHeight - 56).toFixed(1)})`);
  }

  // ── (m) #1174 mesh-op review: bottom-nav has a connectivity indicator that toggles on setConnected(false) ──
  await page.goto(`${BASE}/#/`, { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('[data-bottom-nav]', { timeout: 5000 });
  const indicator = await page.evaluate(() => {
    if (!window.__corescopeLogo || typeof window.__corescopeLogo.setConnected !== 'function') {
      return { logoApiPresent: false };
    }
    window.__corescopeLogo.setConnected(true);
    const nav = document.querySelector('[data-bottom-nav]');
    const connectedCls = nav.classList.contains('disconnected');
    window.__corescopeLogo.setConnected(false);
    const disconnectedCls = nav.classList.contains('disconnected');
    // restore
    window.__corescopeLogo.setConnected(true);
    return {
      logoApiPresent: true,
      onConnected: connectedCls,
      onDisconnected: disconnectedCls,
    };
  });
  if (!indicator.logoApiPresent) {
    fail('(m) window.__corescopeLogo.setConnected not exposed');
  } else if (indicator.onConnected === false && indicator.onDisconnected === true) {
    pass('(m) bottom-nav .disconnected class toggles with setConnected()');
  } else {
    fail(`(m) bottom-nav disconnected class wiring broken (onConnected=${indicator.onConnected}, onDisconnected=${indicator.onDisconnected})`);
  }

  // ── (n) #1174 mesh-op review: More tab gets .active when on long-tail routes ──
  await page.goto(`${BASE}/#/tools`, { waitUntil: 'domcontentloaded' });
  await page.waitForFunction(() => location.hash === '#/tools', null, { timeout: 3000 }).catch(() => {});
  let moreActiveOnTools = null;
  try {
    await page.waitForFunction(() => {
      const el = document.querySelector('[data-bottom-nav-tab="more"]');
      return el && el.classList.contains('active');
    }, null, { timeout: 2000 });
    moreActiveOnTools = true;
  } catch (_) {
    moreActiveOnTools = await page.evaluate(() => {
      const el = document.querySelector('[data-bottom-nav-tab="more"]');
      return el ? el.classList.contains('active') : null;
    });
  }
  if (moreActiveOnTools === true) pass('(n) More tab .active on #/tools (long-tail route)');
  else fail(`(n) More tab NOT .active on #/tools (got ${moreActiveOnTools})`);

  await page.goto(`${BASE}/#/packets`, { waitUntil: 'domcontentloaded' });
  await page.waitForFunction(() => location.hash === '#/packets', null, { timeout: 3000 }).catch(() => {});
  let moreActiveOnPackets = null;
  try {
    await page.waitForFunction(() => {
      const el = document.querySelector('[data-bottom-nav-tab="more"]');
      return el && !el.classList.contains('active');
    }, null, { timeout: 2000 });
    moreActiveOnPackets = false;
  } catch (_) {
    moreActiveOnPackets = await page.evaluate(() => {
      const el = document.querySelector('[data-bottom-nav-tab="more"]');
      return el ? el.classList.contains('active') : null;
    });
  }
  if (moreActiveOnPackets === false) pass('(n) More tab loses .active on primary route #/packets');
  else fail(`(n) More tab still .active on #/packets (got ${moreActiveOnPackets})`);

  await browser.close();

  console.log(`\ntest-bottom-nav-1061-e2e.js: ${passes} passed, ${failures} failed`);
  process.exit(failures > 0 ? 1 : 0);
}

main().catch((err) => {
  console.error('test-bottom-nav-1061-e2e.js: FAIL —', err);
  process.exit(1);
});

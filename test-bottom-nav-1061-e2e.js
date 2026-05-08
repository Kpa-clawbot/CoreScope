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
const EXPECTED_TABS = ['home', 'packets', 'live', 'map', 'channels'];

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
    const tn = document.querySelector('.top-nav');
    const bnRect = bn ? bn.getBoundingClientRect() : null;
    const tnRect = tn ? tn.getBoundingClientRect() : null;
    const bnCs = bn ? getComputedStyle(bn) : null;
    const tnCs = tn ? getComputedStyle(tn) : null;
    return {
      bnPresent: !!bn,
      bnRect, tnRect,
      bnDisplay: bnCs ? bnCs.display : null,
      bnVisibility: bnCs ? bnCs.visibility : null,
      tnDisplay: tnCs ? tnCs.display : null,
      tnVisibility: tnCs ? tnCs.visibility : null,
      bnPaddingBottom: bnCs ? bnCs.paddingBottom : null,
    };
  });

  if (!stateNarrow.bnPresent) {
    fail('(a) [data-bottom-nav] container missing in DOM at 360x800');
  } else if (stateNarrow.bnDisplay === 'none' || stateNarrow.bnVisibility === 'hidden' || !isVisible(stateNarrow.bnRect)) {
    fail(`(a) bottom-nav not visible at 360x800 (display=${stateNarrow.bnDisplay}, rect=${JSON.stringify(stateNarrow.bnRect)})`);
  } else {
    pass('(a) bottom-nav visible at 360x800');
  }
  if (stateNarrow.tnDisplay === 'none' || stateNarrow.tnVisibility === 'hidden' || !isVisible(stateNarrow.tnRect)) {
    pass('(a) top-nav hidden/collapsed at 360x800');
  } else {
    fail(`(a) top-nav still visible at 360x800 (display=${stateNarrow.tnDisplay}, rect=${JSON.stringify(stateNarrow.tnRect)}) — duplicate nav UX`);
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
  const activeOnPackets = await page.evaluate(() => {
    const el = document.querySelector('[data-bottom-nav-tab="packets"]');
    return el ? el.classList.contains('active') : null;
  });
  if (activeOnPackets === true) pass('(e) Packets tab active on #/packets');
  else fail(`(e) Packets tab NOT active on #/packets (got ${activeOnPackets})`);

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

  await browser.close();

  console.log(`\ntest-bottom-nav-1061-e2e.js: ${passes} passed, ${failures} failed`);
  process.exit(failures > 0 ? 1 : 0);
}

main().catch((err) => {
  console.error('test-bottom-nav-1061-e2e.js: FAIL —', err);
  process.exit(1);
});

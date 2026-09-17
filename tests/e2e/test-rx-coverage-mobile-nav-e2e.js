#!/usr/bin/env node
/**
 * Coverage route reachability on mobile navigation.
 *
 * The "Coverage" route (#/rx-coverage) is opt-in: roles.js injects its link
 * into the DESKTOP top-nav (.nav-links) only, when window.MC_CLIENT_RX_COVERAGE
 * is true. The two mobile navigation surfaces build their long-tail route lists
 * independently:
 *   - public/bottom-nav.js  → the "More" sheet (phones, ≤768px)
 *   - public/nav-drawer.js  → the edge-swipe drawer (tablets, >768px)
 * Both used hardcoded arrays that omitted rx-coverage, so when Coverage was
 * enabled it was present on desktop but UNREACHABLE on mobile. This test pins
 * that, once enabled, Coverage appears in BOTH mobile surfaces, ordered right
 * after Analytics (matching the desktop top-nav insertion point).
 *
 * Skips cleanly when:
 *   - Chromium is unavailable (unless CHROMIUM_REQUIRE=1 → hard fail), or
 *   - the deployment under test has clientRxCoverage disabled (default off).
 *
 * Stable selectors consumed here:
 *   [data-bottom-nav-tab="more"]                  — More tab (opens the sheet)
 *   [data-bottom-nav-more-route="rx-coverage"]    — Coverage row in the sheet
 *   window.__navDrawer.open()                      — opens the edge-swipe drawer
 *   [data-nav-drawer-item="rx-coverage"]          — Coverage row in the drawer
 */
'use strict';

const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:3000';

let passed = 0, failed = 0;
async function step(name, fn) {
  try { await fn(); passed++; console.log('  ✓ ' + name); }
  catch (e) { failed++; console.error('  ✗ ' + name + ': ' + e.message); }
}
function assert(c, m) { if (!c) throw new Error(m || 'assertion failed'); }

(async () => {
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
      console.error(`test-rx-coverage-mobile-nav-e2e.js: FAIL — Chromium required but unavailable: ${err.message}`);
      process.exit(1);
    }
    console.log(`test-rx-coverage-mobile-nav-e2e.js: SKIP (Chromium unavailable: ${err.message.split('\n')[0]})`);
    process.exit(0);
  }

  console.log(`\n=== Coverage mobile-nav reachability E2E against ${BASE} ===`);

  // Coverage is opt-in (config flag, default off). Skip when the deployment
  // under test has it disabled — mirrors test-node-reach-coverage-e2e.js.
  const probeCtx = await browser.newContext();
  const probe = await probeCtx.newPage();
  let enabled = false;
  try {
    const cfg = await (await probe.request.get(BASE + '/api/config/client')).json();
    enabled = cfg.clientRxCoverage === true;
  } catch (e) {
    console.log(`test-rx-coverage-mobile-nav-e2e.js: SKIP (could not read /api/config/client: ${e.message})`);
    await browser.close();
    process.exit(0);
  }
  await probeCtx.close();
  if (!enabled) {
    console.log('test-rx-coverage-mobile-nav-e2e.js: SKIP (clientRxCoverage disabled on this deployment)');
    await browser.close();
    process.exit(0);
  }

  // ── Phone viewport: bottom-nav "More" sheet ──
  const phoneCtx = await browser.newContext({ viewport: { width: 360, height: 800 } });
  const phone = await phoneCtx.newPage();
  phone.setDefaultTimeout(10000);
  phone.on('pageerror', (e) => console.error('[pageerror]', e.message));

  await phone.goto(BASE + '/#/packets', { waitUntil: 'domcontentloaded' });
  await phone.waitForSelector('main#app', { timeout: 8000 });
  // Wait until roles.js has resolved the config flag (the lazy sheet build
  // reads window.MC_CLIENT_RX_COVERAGE at open time).
  await phone.waitForFunction(() => window.MC_CLIENT_RX_COVERAGE === true, null, { timeout: 8000 });

  await step('(a) bottom-nav More sheet contains Coverage, ordered after Analytics', async () => {
    await phone.click('[data-bottom-nav-tab="more"]');
    await phone.waitForTimeout(150);
    const info = await phone.evaluate(() => {
      const cov = document.querySelector('[data-bottom-nav-more-route="rx-coverage"]');
      if (!cov) return { present: false };
      const items = Array.prototype.map.call(
        document.querySelectorAll('[data-bottom-nav-more-route]'),
        (a) => a.getAttribute('data-bottom-nav-more-route')
      );
      const label = (cov.textContent || '').trim();
      return {
        present: true,
        label,
        afterAnalytics: items.indexOf('rx-coverage') === items.indexOf('analytics') + 1,
        items,
      };
    });
    assert(info.present, 'Coverage row missing from bottom-nav More sheet');
    assert(/Coverage/.test(info.label), `expected label "Coverage", got "${info.label}"`);
    assert(info.afterAnalytics, `Coverage not directly after Analytics: ${info.items.join(', ')}`);
  });

  await step('(b) tapping Coverage in the sheet navigates to #/rx-coverage', async () => {
    await phone.click('[data-bottom-nav-more-route="rx-coverage"]');
    await phone.waitForTimeout(200);
    const hash = await phone.evaluate(() => location.hash);
    assert(hash.indexOf('#/rx-coverage') === 0, `expected hash #/rx-coverage, got ${hash}`);
  });

  await phoneCtx.close();

  // ── Tablet viewport: edge-swipe drawer (enabled > 768px) ──
  const tabletCtx = await browser.newContext({ viewport: { width: 1024, height: 800 } });
  const tablet = await tabletCtx.newPage();
  tablet.setDefaultTimeout(10000);
  tablet.on('pageerror', (e) => console.error('[pageerror]', e.message));

  await tablet.goto(BASE + '/#/packets', { waitUntil: 'domcontentloaded' });
  await tablet.waitForSelector('main#app', { timeout: 8000 });
  await tablet.waitForFunction(() => window.MC_CLIENT_RX_COVERAGE === true, null, { timeout: 8000 });

  await step('(c) edge-swipe drawer contains Coverage, ordered after Analytics', async () => {
    await tablet.evaluate(() => window.__navDrawer && window.__navDrawer.open && window.__navDrawer.open());
    await tablet.waitForTimeout(150);
    const info = await tablet.evaluate(() => {
      const cov = document.querySelector('[data-nav-drawer-item="rx-coverage"]');
      if (!cov) return { present: false };
      const items = Array.prototype.map.call(
        document.querySelectorAll('[data-nav-drawer-item]'),
        (a) => a.getAttribute('data-nav-drawer-item')
      );
      return {
        present: true,
        label: (cov.textContent || '').trim(),
        afterAnalytics: items.indexOf('rx-coverage') === items.indexOf('analytics') + 1,
        items,
      };
    });
    assert(info.present, 'Coverage row missing from edge-swipe drawer');
    assert(/Coverage/.test(info.label), `expected label "Coverage", got "${info.label}"`);
    assert(info.afterAnalytics, `Coverage not directly after Analytics: ${info.items.join(', ')}`);
  });

  await tabletCtx.close();
  await browser.close();
  console.log(`\n=== Results: passed ${passed} failed ${failed} ===`);
  process.exit(failed > 0 ? 1 : 0);
})().catch((e) => { console.error(e); process.exit(1); });

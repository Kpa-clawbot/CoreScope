#!/usr/bin/env node
/* Path Inspector — the map side pane, the legacy trace redirect, and the
 * tools landing.
 *
 * Scope note: test-path-inspector-coverage-e2e.js (wired, runs every build)
 * already covers the standalone /#/tools/path-inspector page, including its
 * validation paths and the ?prefixes= deep-link auto-fill. It was written
 * precisely because this file could not run. So this file no longer repeats
 * the standalone page. What it keeps is what nothing else covers:
 *
 *   - the side pane on /#/map: present, collapsed, expands, submits
 *   - Show on Map draws a route, and switching candidates replaces it
 *     rather than stacking (public/map.js clears routeLayer first)
 *   - /#/traces/<hash> still redirects to /#/tools/trace/<hash>
 *   - /#/tools lists both tools
 *
 * Ported from the @playwright/test version, which was written against a
 * runner this project does not install and so had never run (#2037). The
 * original's "switching candidate clears prior polyline" case ended after
 * the click with a comment and no assertion, which is the same
 * no-coverage-but-green problem #2037 is about; it is a real assertion here.
 *
 * CHROMIUM_REQUIRE=1 makes Chromium-launch failure a HARD FAIL.
 */
'use strict';

const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';
const PREFIXES = '2c,a1';

let passes = 0, failures = 0, skips = 0;
function pass(msg) { console.log(`  ✓ ${msg}`); passes++; }
function fail(msg) { console.error(`  ✗ ${msg}`); failures++; }
function skip(msg) { console.log(`  ○ SKIP ${msg}`); skips++; }

// Count the polylines Leaflet has drawn. preferCanvas is on for markers, but
// drawPacketRoute's polylines land in the SVG overlay, so they are countable.
function countRoutePaths(page) {
  return page.evaluate(() => document.querySelectorAll('#leaflet-map .leaflet-overlay-pane svg path, .leaflet-overlay-pane svg path').length);
}

async function openPaneAndSubmit(page) {
  await page.goto(`${BASE}/#/map`, { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('#mapPaneToggle', { timeout: 15000 });
  await page.click('#mapPaneToggle');
  await page.fill('#mapPiInput', PREFIXES);
  await page.click('#mapPiSubmit');
  // Either outcome means the round trip finished.
  await page.waitForFunction(() => {
    const r = document.getElementById('mapPiResults');
    const e = document.getElementById('mapPiError');
    return (r && r.textContent.trim().length > 0) || (e && e.textContent.trim().length > 0);
  }, null, { timeout: 10000 });
}

async function main() {
  const requireChromium = process.env.CHROMIUM_REQUIRE === '1';
  let browser;
  try {
    browser = await chromium.launch({ headless: true });
  } catch (err) {
    if (requireChromium) {
      console.error(`HARD FAIL — Chromium unavailable: ${err.message}`);
      process.exit(1);
    }
    console.warn(`SKIP — Chromium unavailable: ${err.message}`);
    process.exit(0);
  }

  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  const page = await ctx.newPage();

  // (1) The side pane exists and starts collapsed.
  await page.goto(`${BASE}/#/map`, { waitUntil: 'domcontentloaded' });
  try {
    await page.waitForSelector('#mapSidePane', { timeout: 15000 });
    const cls = await page.getAttribute('#mapSidePane', 'class');
    if (cls && /\bexpanded\b/.test(cls)) fail(`(1) the side pane starts expanded (class="${cls}")`);
    else pass('(1) the side pane is present and collapsed by default');
  } catch {
    fail('(1) #mapSidePane never appeared within 15s');
  }

  // (2) The toggle expands it.
  try {
    await page.click('#mapPaneToggle');
    await page.waitForFunction(() => {
      const el = document.getElementById('mapSidePane');
      return !!el && /\bexpanded\b/.test(el.className);
    }, null, { timeout: 5000 });
    pass('(2) clicking the toggle expands the pane');
  } catch {
    const cls = await page.getAttribute('#mapSidePane', 'class').catch(() => '(missing)');
    fail(`(2) the pane did not gain .expanded (class="${cls}")`);
  }

  // (3) Submitting prefixes completes a round trip.
  try {
    await openPaneAndSubmit(page);
    pass('(3) submitting prefixes renders results or an error');
  } catch {
    fail('(3) neither results nor an error appeared within 10s of submitting');
  }

  // (4) and (5) need at least one candidate from the fixture.
  const candidates = await page.evaluate(() =>
    document.querySelectorAll('#mapPiResults button[data-idx]').length);

  if (candidates === 0) {
    skip(`(4) Show on Map: the fixture returned no candidates for "${PREFIXES}"`);
    skip('(5) switching candidates: needs at least two candidates');
  } else {
    // (4) Show on Map draws something.
    const before = await countRoutePaths(page);
    await page.click('#mapPiResults button[data-idx="0"]');
    try {
      await page.waitForFunction((b) =>
        document.querySelectorAll('.leaflet-overlay-pane svg path').length > b, before, { timeout: 5000 });
      pass('(4) Show on Map draws the route');
    } catch {
      fail(`(4) no polyline appeared after Show on Map (paths before ${before}, after ${await countRoutePaths(page)})`);
    }

    if (candidates < 2) {
      skip(`(5) switching candidates: the fixture returned only ${candidates} candidate`);
    } else {
      // (5) Switching candidates replaces the route instead of stacking it.
      // map.js clears routeLayer before drawing, so the count after
      // 0-then-1 must equal the count for 1 on its own.
      const after01 = await (async () => {
        await page.click('#mapPiResults button[data-idx="1"]');
        await page.waitForTimeout(500);
        return countRoutePaths(page);
      })();

      await openPaneAndSubmit(page);
      const baseline = await countRoutePaths(page);
      await page.click('#mapPiResults button[data-idx="1"]');
      await page.waitForTimeout(500);
      const after1 = await countRoutePaths(page);

      if (after01 === after1) {
        pass(`(5) switching candidates replaces the route (${after01} paths either way, baseline ${baseline})`);
      } else {
        fail(`(5) the prior route was left behind: 0-then-1 drew ${after01} paths, 1 alone drew ${after1}`);
      }
    }
  }

  // (6) The legacy trace URL still redirects.
  await page.goto(`${BASE}/#/traces/abc123`, { waitUntil: 'domcontentloaded' });
  try {
    await page.waitForFunction(() => location.hash.indexOf('#/tools/trace/abc123') === 0, null, { timeout: 5000 });
    pass('(6) /#/traces/<hash> redirects to /#/tools/trace/<hash>');
  } catch {
    fail(`(6) no redirect; the URL is ${JSON.stringify(page.url())}`);
  }

  // (7) The tools landing lists both tools.
  await page.goto(`${BASE}/#/tools`, { waitUntil: 'domcontentloaded' });
  try {
    await page.waitForSelector('.tools-landing', { timeout: 8000 });
    const links = await page.evaluate(() => ({
      pi: !!document.querySelector('a[href="#/tools/path-inspector"]'),
      trace: !!document.querySelector('a[href*="#/tools/trace"]'),
    }));
    if (links.pi && links.trace) pass('(7) the tools landing links to both tools');
    else fail(`(7) the tools landing is missing a link (path-inspector: ${links.pi}, trace: ${links.trace})`);
  } catch {
    fail('(7) .tools-landing never rendered within 8s');
  }

  await browser.close();
  console.log(`\ntest-path-inspector-e2e: ${passes} passed, ${failures} failed, ${skips} skipped`);
  process.exit(failures ? 1 : 0);
}

main().catch((e) => { console.error(e); process.exit(1); });

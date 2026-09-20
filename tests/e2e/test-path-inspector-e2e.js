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
// Prefixes are taken from the dataset under test rather than hardcoded: a
// fixed pair like "2c,a1" resolves to nothing on the CI fixture, and (4) and
// (5) below then skip, which is the "green but covers nothing" outcome #2037
// is about.
let PREFIXES = null;

async function pickPrefixes(page) {
  // /api/paths/inspect beam-searches the NEIGHBOUR GRAPH, so prefixes only
  // yield candidates when the nodes behind them are actually connected in it.
  // Taking them from a packet's recorded path is not enough: those hops need
  // not form an edge chain the search can walk. So walk the graph itself,
  // through the same API the product uses.
  //
  // Against a populated instance this works: POST /api/paths/inspect with the
  // three prefixes picked here returns 10 candidates, where packet-path
  // prefixes returned none. Against the CI fixture (110 edges over 200 nodes)
  // it still returns none, so (4) and (5) below skip there. Two things were
  // ruled out: the prefixes are valid (the endpoint accepts them and answers
  // 200), and the graph is not empty. What has NOT been established is why
  // the beam search finds nothing in it, and the most likely remaining
  // explanation is that the fixture's graph does not contain a chain the
  // search will score above its thresholds. Seeding one is fixture work, not
  // a change to this suite. Until then the two cases are exercised only
  // against an instance with a real graph, and they say so out loud rather
  // than reporting a pass.
  const get = async (path) => {
    const r = await page.request.get(BASE + path);
    return r.ok() ? r.json() : null;
  };
  // A neighbour entry can carry a null pubkey: the graph records the hop by
  // prefix, and the node behind it need not be known. Those are useless here,
  // since the chain has to be followed one hop further.
  const resolved = (r) => ((r && r.neighbors) || []).filter(x => typeof x.pubkey === 'string' && x.pubkey.length >= 2);

  const seed = await get('/api/nodes?role=repeater&limit=25');
  const nodes = ((seed && seed.nodes) || []).filter(n => typeof n.public_key === 'string' && n.public_key.length >= 2);
  for (const n of nodes) {
    const pk = n.public_key;
    const aN = resolved(await get(`/api/nodes/${pk}/neighbors`));
    if (!aN.length) continue;
    // Prefer a neighbour that itself has a neighbour, so the chain is three
    // hops long and (5) below has two candidates to switch between.
    for (const hop of aN) {
      const bN = resolved(await get(`/api/nodes/${hop.pubkey}/neighbors`)).filter(x => x.pubkey !== pk);
      if (bN.length) {
        return [pk, hop.pubkey, bN[0].pubkey].map(k => k.slice(0, 2).toLowerCase()).join(',');
      }
    }
    return [pk, aN[0].pubkey].map(k => k.slice(0, 2).toLowerCase()).join(',');
  }
  return null;
}

let passes = 0, failures = 0, skips = 0;
function pass(msg) { console.log(`  ✓ ${msg}`); passes++; }
function fail(msg) { console.error(`  ✗ ${msg}`); failures++; }
function skip(msg) { console.log(`  ○ SKIP ${msg}`); skips++; }

// Count the polylines Leaflet has drawn. preferCanvas is on for markers, but
// drawPacketRoute's polylines land in the SVG overlay, so they are countable.
function countRoutePaths(page) {
  return page.evaluate(() => document.querySelectorAll('#leaflet-map .leaflet-overlay-pane svg path, .leaflet-overlay-pane svg path').length);
}

// The toggle ships in the page template, but its click handler is attached by
// initMapSidePane(), which public/map.js calls at the END of loadNodes()
// (map.js:1795). So the button exists for seconds before it does anything, and
// a single click on sight is a race that silently no-ops. Measured at ~3s
// against a populated instance.
//
// Clicking for real each attempt rather than dispatching in-page on purpose: a
// click that cannot land (an overlay eating it, as in #2049) has to fail here,
// which an element.click() from evaluate would hide.
async function expandPane(page) {
  await page.waitForSelector('#mapPaneToggle', { timeout: 15000 });
  for (let i = 0; i < 20; i++) {
    const expanded = await page.evaluate(() => {
      const el = document.getElementById('mapSidePane');
      return !!el && /\bexpanded\b/.test(el.className);
    });
    if (expanded) return;
    try {
      await page.click('#mapPaneToggle', { timeout: 1500 });
    } catch { /* not clickable yet; the next round re-checks */ }
    await page.waitForTimeout(500);
  }
  throw new Error('the pane never expanded after 20 clicks over ~20s');
}

async function openPaneAndSubmit(page) {
  await page.goto(`${BASE}/#/map`, { waitUntil: 'domcontentloaded' });
  await expandPane(page);
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

  PREFIXES = await pickPrefixes(page);
  if (!PREFIXES) {
    // Not a skip. Every dataset this runs against, fixture included, has a
    // neighbour graph; none would mean the graph or the nodes API changed
    // shape, and the inspector cases below would then be silently untested.
    console.error('  ✗ (0) no connected pair found in the neighbour graph, so the inspector cannot be exercised');
    await browser.close();
    process.exit(1);
  }
  console.log(`  · prefixes taken from the dataset: ${PREFIXES}`);

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
    await expandPane(page);
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

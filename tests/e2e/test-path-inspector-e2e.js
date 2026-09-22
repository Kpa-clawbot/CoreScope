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
 *   - /#/traces/<hash> still redirects to /#/tools/trace/<hash>
 *   - /#/tools lists both tools
 *
 * NOT covered here, deliberately: "Show on Map draws a route" and "switching
 * candidates replaces it rather than stacking". Both need
 * /api/paths/inspect to return a candidate, and its beam search finds none in
 * the CI fixture's neighbour graph. They were written, they worked against a
 * populated instance, and they skipped in CI — a green suite hiding two
 * untested assertions, which is the exact shape #2037 is about. Removed
 * rather than shipped as skips; the fixture work they need is #2060.
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
// Prefixes are taken from the dataset under test rather than hardcoded, so
// the submit below exercises a real lookup instead of a constant that resolves
// to nothing.
let PREFIXES = null;

async function pickPrefixes(page) {
  // /api/paths/inspect beam-searches the NEIGHBOUR GRAPH, so prefixes only
  // yield candidates when the nodes behind them are actually connected in it.
  // Taking them from a packet's recorded path is not enough: those hops need
  // not form an edge chain the search can walk. So walk the graph itself,
  // through the same API the product uses.
  //
  // Against a populated instance this yields candidates: POST
  // /api/paths/inspect with the three prefixes picked here returned 10, where
  // prefixes taken from a packet's recorded path returned none. Against the CI
  // fixture (110 edges over 200 nodes) it still returns none, which is why the
  // two drawing assertions are not in this file — see the header and #2060.
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

let passes = 0, failures = 0;
function pass(msg) { console.log(`  ✓ ${msg}`); passes++; }
function fail(msg) { console.error(`  ✗ ${msg}`); failures++; }

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
    // A failure, not a skip. Every dataset this runs against, fixture
    // included, has a neighbour graph; none would mean the graph or the nodes
    // API changed shape.
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

  // (4) The legacy trace URL still redirects.
  await page.goto(`${BASE}/#/traces/abc123`, { waitUntil: 'domcontentloaded' });
  try {
    await page.waitForFunction(() => location.hash.indexOf('#/tools/trace/abc123') === 0, null, { timeout: 5000 });
    pass('(4) /#/traces/<hash> redirects to /#/tools/trace/<hash>');
  } catch {
    fail(`(4) no redirect; the URL is ${JSON.stringify(page.url())}`);
  }

  // (5) The tools landing lists both tools.
  await page.goto(`${BASE}/#/tools`, { waitUntil: 'domcontentloaded' });
  try {
    await page.waitForSelector('.tools-landing', { timeout: 8000 });
    const links = await page.evaluate(() => ({
      pi: !!document.querySelector('a[href="#/tools/path-inspector"]'),
      trace: !!document.querySelector('a[href*="#/tools/trace"]'),
    }));
    if (links.pi && links.trace) pass('(5) the tools landing links to both tools');
    else fail(`(5) the tools landing is missing a link (path-inspector: ${links.pi}, trace: ${links.trace})`);
  } catch {
    fail('(5) .tools-landing never rendered within 8s');
  }

  await browser.close();
  console.log(`\ntest-path-inspector-e2e: ${passes} passed, ${failures} failed`);
  process.exit(failures ? 1 : 0);
}

main().catch((e) => { console.error(e); process.exit(1); });

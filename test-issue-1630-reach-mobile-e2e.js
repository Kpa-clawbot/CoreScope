/**
 * E2E (#1630): Reach page mobile layout.
 *
 * On narrow viewports (393×800 and 360×740 — common iPhone/Android sizes)
 * the /#/nodes/{pubkey}/reach page must:
 *  - Shrink the map height (#nqMap) to ≤ 320px so stats/table are visible
 *    above the fold instead of being pushed below a 420px-tall map.
 *  - Lay the 6-column link table out so its scrollWidth ≤ clientWidth (no
 *    horizontal scroll inside the table).
 *
 * Desktop guard (≥768px / 1440×900): map height must remain the original
 * 420px and the table must still render 6 visible columns — no regression.
 *
 * Run: BASE_URL=http://localhost:13581 node test-issue-1630-reach-mobile-e2e.js
 */
'use strict';
const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';
const SUBPIXEL_TOL = 1; // browsers round subpixels; tolerate 1px noise.

let passed = 0, failed = 0;
async function step(name, fn) {
  try { await fn(); passed++; console.log('  \u2713 ' + name); }
  catch (e) { failed++; console.error('  \u2717 ' + name + ': ' + e.message); }
}
function assert(c, m) { if (!c) throw new Error(m || 'assertion failed'); }

async function pickRepeaterWithReach(page) {
  // Find a repeater whose 30-day reach has >= 2 links (table renders rows).
  const list = await page.request.get(BASE + '/api/nodes?role=repeater&limit=80');
  if (!list.ok()) throw new Error('GET /api/nodes failed: ' + list.status());
  const nodes = (await list.json()).nodes || [];
  for (const n of nodes) {
    const r = await page.request.get(BASE + '/api/nodes/' + n.public_key + '/reach?days=30');
    if (!r.ok()) continue;
    const j = await r.json();
    if (Array.isArray(j.links) && j.links.length >= 2) return n.public_key;
  }
  throw new Error('no repeater with reach links found in fixture');
}

async function loadReachPage(page, pubkey) {
  await page.goto(BASE + '/#/nodes/' + pubkey + '/reach');
  await page.waitForSelector('.nq-head', { timeout: 20000 });
  // The fixture only has reach data within a 30-day window, but DEFAULT_DAYS
  // is 7. Click the 30d button so the table actually renders rows.
  const btn30 = await page.$('button[data-days="30"]');
  if (btn30) {
    await btn30.click();
  }
  // Wait for the row body to appear and have at least one row.
  await page.waitForFunction(() => {
    const tb = document.getElementById('nqRows');
    return tb && tb.children.length > 0;
  }, { timeout: 15000 });
  // Let leaflet paint (map height is set by CSS, not waiting on tiles).
  await page.waitForSelector('#nqMap', { timeout: 5000 });
}

async function measure(page) {
  return page.evaluate(() => {
    const m = document.getElementById('nqMap');
    const t = document.querySelector('.nq-table');
    return {
      mapH: m ? Math.round(m.getBoundingClientRect().height) : -1,
      tableSw: t ? t.scrollWidth : -1,
      tableCw: t ? t.clientWidth : -1,
      htmlSw: document.documentElement.scrollWidth,
      htmlCw: document.documentElement.clientWidth,
      visibleThCount: t ? [...t.querySelectorAll('thead th')].filter(th => {
        const cs = getComputedStyle(th);
        return cs.display !== 'none' && cs.visibility !== 'hidden';
      }).length : 0,
    };
  });
}

async function run() {
  const launchOpts = { args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'] };
  if (process.env.CHROMIUM_PATH) launchOpts.executablePath = process.env.CHROMIUM_PATH;
  let browser;
  try {
    browser = await chromium.launch(launchOpts);
  } catch (e) {
    if (process.env.CHROMIUM_REQUIRE === '1') {
      console.error('test-issue-1630: Chromium required but unavailable: ' + e.message);
      process.exit(1);
    }
    console.log('test-issue-1630 SKIP (no chromium): ' + e.message);
    return;
  }

  const page = await browser.newPage();
  const pubkey = await pickRepeaterWithReach(page);
  console.log('  using pubkey ' + pubkey.slice(0, 12) + '...');

  // --- 393×800 phone viewport (iPhone 14 / Pixel-class) ---
  await page.setViewportSize({ width: 393, height: 800 });
  await loadReachPage(page, pubkey);
  const m393 = await measure(page);
  await step('393×800: map height ≤ 320px (currently ' + m393.mapH + ')', () => {
    assert(m393.mapH > 0 && m393.mapH <= 320,
      'map height ' + m393.mapH + 'px exceeds 320px cap on narrow viewport');
  });
  await step('393×800: link table fits without horizontal scroll', () => {
    assert(m393.tableSw - m393.tableCw <= SUBPIXEL_TOL,
      'table scrollWidth ' + m393.tableSw + ' exceeds clientWidth ' + m393.tableCw);
  });
  await step('393×800: low-signal columns collapsed (≤4 visible TH)', () => {
    assert(m393.visibleThCount > 0 && m393.visibleThCount <= 4,
      'narrow viewport visible TH count ' + m393.visibleThCount +
      ' — distance and/or we_hear/they_hear must be hidden/stacked');
  });

  // --- 360×740 (small Android) ---
  await page.setViewportSize({ width: 360, height: 740 });
  // Re-trigger paint by clicking 30d again (no-op if already 30) — ensures
  // measurements reflect the new viewport.
  await page.evaluate(() => window.dispatchEvent(new Event('resize')));
  await page.waitForTimeout(200);
  const m360 = await measure(page);
  await step('360×740: map height ≤ 320px (currently ' + m360.mapH + ')', () => {
    assert(m360.mapH > 0 && m360.mapH <= 320,
      'map height ' + m360.mapH + 'px exceeds 320px cap on narrow viewport');
  });
  await step('360×740: link table fits without horizontal scroll', () => {
    assert(m360.tableSw - m360.tableCw <= SUBPIXEL_TOL,
      'table scrollWidth ' + m360.tableSw + ' exceeds clientWidth ' + m360.tableCw);
  });

  // --- 1440×900 desktop guard ---
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.evaluate(() => window.dispatchEvent(new Event('resize')));
  await page.waitForTimeout(200);
  const mDesk = await measure(page);
  await step('1440×900: desktop map height unchanged (~420px)', () => {
    assert(mDesk.mapH >= 400 && mDesk.mapH <= 440,
      'desktop map height ' + mDesk.mapH + 'px regressed from 420px baseline');
  });
  await step('1440×900: desktop shows all 6 columns', () => {
    assert(mDesk.visibleThCount === 6,
      'desktop visible TH count ' + mDesk.visibleThCount + ' != 6');
  });

  await browser.close();
  console.log(passed + ' passed, ' + failed + ' failed');
  if (failed > 0) process.exit(1);
}

run().catch(e => { console.error(e); process.exit(1); });

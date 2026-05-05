/**
 * E2E (#1058): Analytics chart containers — fluid + auto-stacking via
 * container queries.
 *
 * Boots Chromium with a minimal HTML harness that links public/style.css
 * and renders the .analytics-charts grid at 768/1080/1440 viewports.
 *
 * Asserts:
 *  - No horizontal overflow of the chart grid (scrollWidth <= clientWidth).
 *  - Cards STACK (single column) when the .analytics-charts container is
 *    narrower than 800px.
 *  - Cards are SIDE-BY-SIDE (≥2 columns) when the container is at least
 *    1200px wide.
 *  - The .analytics-charts element opts in to container queries via
 *    `container-type: inline-size`.
 *
 * Pure file:// harness — does not require the Go server.
 *
 * Usage: node test-analytics-fluid-charts.js
 */
'use strict';
const { chromium } = require('playwright');
const fs = require('fs');
const path = require('path');
const os = require('os');

const CSS_PATH = path.join(__dirname, 'public', 'style.css');
const cssHref = 'file://' + CSS_PATH;

// Minimal harness: a sized wrapper that defines the available width
// for the .analytics-charts container, plus a handful of chart cards
// matching the production markup.
function harnessHTML(wrapperWidth) {
  const card = (full) =>
    `<div class="analytics-chart-card${full ? ' full' : ''}">` +
    `<h4>Card</h4>` +
    `<div class="analytics-chart-desc">Desc</div>` +
    `<svg viewBox="0 0 800 200" style="width:100%;max-height:160px"><rect width="800" height="200" fill="#888"/></svg>` +
    `</div>`;
  return `<!doctype html><html><head>
  <meta charset="utf-8">
  <link rel="stylesheet" href="${cssHref}">
  <style>
    /* Sized wrapper simulates the page's content column width — the
       .analytics-charts inside MUST stay fluid relative to this. */
    #wrap { width: ${wrapperWidth}px; box-sizing: border-box; padding: 0; margin: 0; }
    body { margin: 0; }
  </style>
  </head><body>
  <div id="wrap">
    <div class="analytics-charts" id="grid">
      ${card(false)}${card(false)}${card(false)}${card(false)}
    </div>
  </div>
  </body></html>`;
}

let passed = 0, failed = 0;
async function step(name, fn) {
  try { await fn(); passed++; console.log('  \u2713 ' + name); }
  catch (e) { failed++; console.error('  \u2717 ' + name + ': ' + e.message); }
}
function assert(c, m) { if (!c) throw new Error(m || 'assertion failed'); }

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || '/usr/bin/chromium',
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });
  const ctx = await browser.newContext();
  const page = await ctx.newPage();
  page.on('pageerror', (e) => console.error('[pageerror]', e.message));

  console.log('\n=== #1058 Analytics fluid charts E2E ===');

  async function load(wrapperWidth, viewportWidth) {
    await page.setViewportSize({ width: viewportWidth, height: 900 });
    const tmp = path.join(os.tmpdir(),
      `1058-harness-${wrapperWidth}-${viewportWidth}.html`);
    fs.writeFileSync(tmp, harnessHTML(wrapperWidth));
    await page.goto('file://' + tmp, { waitUntil: 'domcontentloaded' });
  }

  // Helper: count distinct column-x-positions of chart cards.
  async function colCount() {
    return page.evaluate(() => {
      const cards = Array.from(document.querySelectorAll(
        '.analytics-charts > .analytics-chart-card'));
      const xs = new Set(cards.map(c =>
        Math.round(c.getBoundingClientRect().left)));
      return xs.size;
    });
  }
  async function overflow() {
    return page.evaluate(() => {
      const g = document.getElementById('grid');
      return { scrollW: g.scrollWidth, clientW: g.clientWidth };
    });
  }

  // --- Container-query opt-in -------------------------------------------
  await step('analytics-charts opts in to container queries', async () => {
    await load(1200, 1440);
    const ct = await page.evaluate(() => {
      const g = document.getElementById('grid');
      return getComputedStyle(g).containerType;
    });
    assert(/inline-size|size/.test(ct),
      `expected container-type to be inline-size; got "${ct}"`);
  });

  // --- Viewport 1440: container ≥1200 → side-by-side --------------------
  await step('viewport 1440 / wrapper 1300px → side-by-side (≥2 cols)', async () => {
    await load(1300, 1440);
    const cols = await colCount();
    assert(cols >= 2, `expected ≥2 columns at wrapper 1300px; got ${cols}`);
    const o = await overflow();
    assert(o.scrollW <= o.clientW + 1,
      `horizontal overflow: scrollW=${o.scrollW} clientW=${o.clientW}`);
  });

  // --- Viewport 1080: medium width — must not overflow ------------------
  await step('viewport 1080 / wrapper 1040px → no horizontal overflow', async () => {
    await load(1040, 1080);
    const o = await overflow();
    assert(o.scrollW <= o.clientW + 1,
      `horizontal overflow: scrollW=${o.scrollW} clientW=${o.clientW}`);
  });

  // --- Viewport 768: container <800 → must stack vertically -------------
  await step('viewport 768 / wrapper 760px → cards stack (1 col)', async () => {
    await load(760, 768);
    const cols = await colCount();
    assert(cols === 1, `expected 1 column at wrapper 760px; got ${cols}`);
    const o = await overflow();
    assert(o.scrollW <= o.clientW + 1,
      `horizontal overflow: scrollW=${o.scrollW} clientW=${o.clientW}`);
  });

  // --- THE bug: wide viewport + narrow container — must stack ----------
  // Today's @media (max-width:768px) is keyed off viewport, not container.
  // A narrow wrapper inside a wide viewport (e.g., side pane on a 1440
  // screen) should still stack the charts via container queries.
  await step('viewport 1440 / wrapper 600px → cards stack via container query', async () => {
    await load(600, 1440);
    const cols = await colCount();
    assert(cols === 1,
      `expected 1 column when container <800px regardless of viewport; got ${cols}`);
    const o = await overflow();
    assert(o.scrollW <= o.clientW + 1,
      `horizontal overflow at wide-viewport/narrow-container: scrollW=${o.scrollW} clientW=${o.clientW}`);
  });

  await browser.close();

  console.log(`\n${passed} passed, ${failed} failed`);
  process.exit(failed ? 1 : 0);
})();

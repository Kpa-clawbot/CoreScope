#!/usr/bin/env node
/* Trailing action-column clip — node Neighbours "Map" button, analytics
 * "View on map" button.
 *
 * Symptom (operator report, 2026-08-30): on the node detail page the
 * Neighbours table's "Map" button rendered as a cropped "◎ M" on every row.
 * Unaffected by browser, browser version, zoom level or incognito — because
 * it is a pure layout bug, not a rendering one.
 *
 * Root cause: the action column carries an empty <th>, so it contributes
 * nothing to the table's width distribution, and the global
 *   .data-table td { max-width: 0; overflow: hidden; }
 * zeroes its max-content contribution as well. The auto table algorithm
 * therefore hands that column only whatever space is left over, which is
 * narrower than the button it holds, and `overflow: hidden` crops the
 * remainder without any scrollbar to reveal it. Measured before the fix:
 *
 *   viewport   column   button   rows clipped
 *   1024px     28px     53px     all
 *   1440px     40px     53px     all
 *   1920px     54px     53px     all (padding pushes it over)
 *   420px split panel: 12px column — 47 of 53px cropped
 *
 * Fix: `.data-table th.col-action, .data-table td.col-action` opts the
 * column out of the zeroing (`max-width: none`), sizes it to its content
 * (`width: 1%`) and stops it cropping (`overflow: visible`); nodes.js and
 * analytics.js tag their action <th>/<td> with it.
 *
 * This test does not need a running server: the bug lives entirely in
 * public/style.css plus the markup contract, so it renders the real
 * stylesheet against the real emitted row markup via setContent().
 *
 * Mutation guard: dropping the `.col-action` class from either the <th> or
 * the <td>, or removing `max-width: none` / `width: 1%` from the rule, must
 * make the geometry assertions below fail.
 */
'use strict';

const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert');
const { chromium } = require('playwright');

const CSS = fs.readFileSync(path.join(__dirname, 'public', 'style.css'), 'utf8');
const NODES_JS = fs.readFileSync(path.join(__dirname, 'public', 'nodes.js'), 'utf8');
const ANALYTICS_JS = fs.readFileSync(path.join(__dirname, 'public', 'analytics.js'), 'utf8');

const HEIGHT = 900;
// The bug's window: below ~640px the table finally overflows and the
// container scrolls (so nothing is cropped); above ~2000px the leftover
// space happens to exceed the button. Everything between was broken.
const WIDTHS = [1024, 1280, 1366, 1440, 1600, 1920];

// A neighbour row exactly as renderNeighborRows() emits it (nodes.js).
function neighborRow(name, role, score, count, seen, dist) {
  return '<tr>' +
    `<td data-value="${name.toLowerCase()}" style="font-weight:600"><a href="#/nodes/x">${name}</a></td>` +
    `<td data-value="${role}"><span class="badge" style="background:#dc2626;color:#fff;font-size:10px">${role}</span></td>` +
    `<td data-value="${score}">${score}</td>` +
    `<td data-value="${count}">${count}</td>` +
    `<td data-value="0"><span>${seen}</span></td>` +
    `<td data-value="1">${dist}</td>` +
    '<td><span title="HIGH">●</span></td>' +
    '<td class="col-action"> <button class="btn-link neighbor-show-map" data-pubkey="x"' +
    ' style="font-size:11px;padding:1px 6px;white-space:nowrap">◎ Map</button></td>' +
    '</tr>';
}

const ROWS = [
  ['TREX-SBC Room Observer', 'room', '1.00', '12028', '4m ago', '5.5 mi'],
  ['SFO-SF-POTRO-RE-8C09', 'repeater', '1.00', '5965', '34m ago', '1.1 mi'],
  ['alameda_shoreline_repea', 'repeater', '0.99', '140', '2h ago', '16.0 mi'],
  ['Dublin Ridge', 'repeater', '0.97', '138', '2h ago', '17.7 mi'],
  ['SLHills Repeater', 'repeater', '0.85', '98', '2h ago', '0.8 mi'],
  ['TH-00 San Carlos', 'repeater', '0.80', '86', '3h ago', '9.2 mi'],
].map((r) => neighborRow(...r)).join('');

const TABLE =
  '<table class="data-table neighbor-sort-table" style="font-size:12px"><thead><tr>' +
  '<th scope="col">Neighbor</th><th scope="col">Role</th><th scope="col">Score</th>' +
  '<th scope="col">Obs</th><th scope="col">Last Seen</th><th scope="col">Distance</th>' +
  '<th scope="col">Conf</th><th scope="col" class="col-action"></th>' +
  '</tr></thead><tbody>' + ROWS + '</tbody></table>';

// Both real hosts: the full-screen node view and the ~420px split panel.
const SHELLS = {
  'node-fullscreen': `<main id="app" class="app-fixed"><div class="node-fullscreen">
    <div class="node-full-body"><div class="node-full-card">
      <h4>Neighbors (${ROWS.length})</h4><div id="host">${TABLE}</div>
    </div></div></div></main>`,
  'split-panel': `<main id="app" class="app-fixed"><div class="split-layout" style="display:flex;height:100%">
    <div class="panel-left" style="flex:1"></div>
    <div class="panel-right"><div class="node-detail-section">
      <h4>Neighbors</h4><div id="host">${TABLE}</div>
    </div></div></div></main>`,
};

function page(shell) {
  return `<!DOCTYPE html><html lang="en" data-theme="light"><head><meta charset="utf-8">
<style>${CSS}</style></head><body>${shell}</body></html>`;
}

let passes = 0;
let failures = 0;

function check(fn) {
  try { fn(); passes++; } catch (err) { console.error(`FAIL ${err.message}`); failures++; }
}

// ---- Static guards: the markup contract the CSS rule depends on ----------

check(() => {
  assert.ok(
    /\.data-table th\.col-action,\s*\n\s*\.data-table td\.col-action\s*\{[^}]*max-width:\s*none[^}]*\}/.test(CSS),
    '.col-action rule in public/style.css must set `max-width: none` (opts out of the global `.data-table td { max-width: 0 }` that starves the column)',
  );
  console.log('PASS style.css: .col-action sets max-width: none');
});

check(() => {
  const rule = CSS.match(/\.data-table th\.col-action,\s*\n\s*\.data-table td\.col-action\s*\{([^}]*)\}/);
  assert.ok(rule, '.col-action rule not found in public/style.css');
  assert.ok(/width:\s*1%/.test(rule[1]), '.col-action must set `width: 1%` (shrink-to-content for auto-layout tables)');
  assert.ok(/overflow:\s*visible/.test(rule[1]), '.col-action must set `overflow: visible` so a miscalculation overhangs visibly instead of cropping silently');
  console.log('PASS style.css: .col-action sets width: 1% and overflow: visible');
});

check(() => {
  assert.ok(
    NODES_JS.includes('<th scope="col" class="col-action">') && NODES_JS.includes("'<td class=\"col-action\">' + showOnMap"),
    'public/nodes.js renderNeighborTable/renderNeighborRows must tag BOTH the action <th> and <td> with class="col-action"',
  );
  console.log('PASS nodes.js: neighbour action <th> and <td> carry .col-action');
});

check(() => {
  const ths = (ANALYTICS_JS.match(/<th scope="col" class="col-action">/g) || []).length;
  const tds = (ANALYTICS_JS.match(/<td class="col-action">\$\{mapBtn\}<\/td>/g) || []).length;
  assert.strictEqual(ths, 2, `public/analytics.js must tag 2 action <th> (Top Hops + Top Paths) with .col-action, found ${ths}`);
  assert.strictEqual(tds, 2, `public/analytics.js must tag 2 action <td> with .col-action, found ${tds}`);
  console.log('PASS analytics.js: both map-button columns carry .col-action');
});

// ---- Geometry: nothing is cropped at any desktop width -------------------

async function main() {
  let browser;
  try {
    browser = await chromium.launch({
      headless: true,
      executablePath: process.env.CHROMIUM_PATH || undefined,
      args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
    });
  } catch (err) {
    if (process.env.CHROMIUM_REQUIRE === '1') {
      console.error(`test-neighbor-map-btn-clip-e2e.js: FAIL — Chromium required but unavailable: ${err.message}`);
      process.exit(1);
    }
    console.log(`test-neighbor-map-btn-clip-e2e.js: SKIP geometry (Chromium unavailable: ${err.message.split('\n')[0]})`);
    console.log(`test-neighbor-map-btn-clip-e2e.js: ${passes} passed, ${failures} failed`);
    process.exit(failures > 0 ? 1 : 0);
  }

  const ctx = await browser.newContext();
  const p = await ctx.newPage();
  p.setDefaultTimeout(15000);

  for (const [shellName, shell] of Object.entries(SHELLS)) {
    for (const w of WIDTHS) {
      await p.setViewportSize({ width: w, height: HEIGHT });
      await p.setContent(page(shell), { waitUntil: 'load' });

      const probe = await p.evaluate(() => {
        const rows = Array.from(document.querySelectorAll('#host tbody tr'));
        let worst = 0;
        let clipped = 0;
        let colWidth = 0;
        let btnWidth = 0;
        for (const tr of rows) {
          const td = tr.querySelector('td.col-action');
          const btn = td && td.querySelector('.neighbor-show-map');
          if (!btn) continue;
          colWidth = td.getBoundingClientRect().width;
          btnWidth = btn.getBoundingClientRect().width;
          const over = btn.getBoundingClientRect().right - td.getBoundingClientRect().right;
          if (over > 0.5) { clipped++; if (over > worst) worst = over; }
        }
        return { rows: rows.length, clipped, worst, colWidth, btnWidth };
      });

      const tag = `${shellName} vw=${w}`;
      check(() => {
        assert.strictEqual(
          probe.clipped, 0,
          `${tag}: ${probe.clipped}/${probe.rows} Map buttons cropped by their cell (worst ${probe.worst.toFixed(1)}px; column ${probe.colWidth.toFixed(1)}px vs button ${probe.btnWidth.toFixed(1)}px)`,
        );
        console.log(`PASS ${tag}: column ${probe.colWidth.toFixed(1)}px holds ${probe.btnWidth.toFixed(1)}px button, 0/${probe.rows} cropped`);
      });
    }
  }

  await browser.close();
  console.log(`\ntest-neighbor-map-btn-clip-e2e.js: ${passes} passed, ${failures} failed`);
  if (failures > 0) process.exit(1);
}

main().catch((err) => {
  console.error('test-neighbor-map-btn-clip-e2e.js: ERROR', err);
  process.exit(1);
});

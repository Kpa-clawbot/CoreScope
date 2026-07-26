/* #1865 follow-up (cwichura, PR #1867): identify which observers send
 * /neighbors reports (opt-in firmware, unavailable on non-PSRAM hardware).
 * Production code must expose:
 *   window.neighborsReportBadge(o) -> string
 * Must render a plain, neutral "never" state (no warning icon, no red
 * styling) when the observer has never reported — cwichura explicitly asked
 * that the system not "shame" operators for a feature that's opt-in and
 * hardware-gated.
 */
'use strict';
const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log(`  ✅ ${name}`); }
  catch (e) { failed++; console.log(`  ❌ ${name}: ${e.message}`); }
}

function makeCtx() {
  const ctx = {
    window: {},
    document: { addEventListener: () => {}, querySelector: () => null },
    console, Date, Math, Array, Object, String, Number, Boolean, JSON,
    setInterval: () => 0, clearInterval: () => {},
    encodeURIComponent, decodeURIComponent,
  };
  ctx.registerPage = () => {};
  ctx.RegionFilter = { init: () => {}, onChange: () => () => {}, getSelected: () => null };
  ctx.timeAgo = (iso) => 'TIME_AGO(' + iso + ')';
  ctx.makeColumnsResizable = () => {};
  ctx.debouncedOnWS = (fn) => fn;
  ctx.offWS = () => {};
  ctx.api = () => Promise.resolve({ observers: [] });
  ctx.CLIENT_TTL = {};
  ctx.location = { hash: '' };
  vm.createContext(ctx);
  return ctx;
}

console.log('\n=== #1865 follow-up — Observers "Neighbors" report badge ===');

const ctx = makeCtx();
vm.runInContext(fs.readFileSync('public/observers.js', 'utf8'), ctx);

test('window.neighborsReportBadge exists', () => {
  assert.strictEqual(typeof ctx.window.neighborsReportBadge, 'function');
});

test('never-reported observer renders a neutral dash, not a warning icon', () => {
  const html = ctx.window.neighborsReportBadge({ id: 'x', last_neighbors_report_at: null });
  assert.ok(/text-muted/.test(html), 'expected text-muted styling for the never state');
  assert.ok(!/ph-warning/.test(html), 'must NOT render a warning icon for never-reported observers');
  assert.ok(/opt-in/.test(html), 'title should explain the feature is opt-in');
});

test('reported observer renders the share-network icon and timeAgo', () => {
  const html = ctx.window.neighborsReportBadge({ id: 'x', last_neighbors_report_at: '2026-07-26T10:00:00Z' });
  assert.ok(/ph-share-network/.test(html), 'expected share-network icon');
  assert.ok(/TIME_AGO\(2026-07-26T10:00:00Z\)/.test(html), 'expected timeAgo() to be called with the timestamp');
});

console.log(`\n${passed} passed, ${failed} failed\n`);
if (failed > 0) process.exit(1);

/**
 * Issue #1108 — Hide non-region nodes when a region is selected on Live map.
 *
 * Unit tests for the public helpers added to region-filter.js:
 *   - RegionShowAll.get() / set() with localStorage persistence
 *   - RegionFilter.nodesRegionQueryString() — returns &region= when filter
 *     active AND showAll is OFF; empty string otherwise.
 *
 * These tests load the module via vm sandbox with mocked globals
 * (no DOM, no fetch). Mirrors the pattern from test-area-filter.js.
 */
'use strict';
const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log(`  \u2713 ${name}`); }
  catch (e) { failed++; console.error(`  \u2717 ${name}: ${e.message}`); }
}

function buildCtx(initialStorage) {
  const storage = Object.assign(Object.create(null), initialStorage || {});
  const localStorage = {
    getItem: (k) => (k in storage ? storage[k] : null),
    setItem: (k, v) => { storage[k] = String(v); },
    removeItem: (k) => { delete storage[k]; },
  };
  const ctx = {
    window: {},
    document: { addEventListener() {} },
    localStorage,
    fetch: async () => ({ json: async () => ({}) }),
    console,
    setTimeout, clearTimeout,
  };
  ctx.window = ctx;
  vm.createContext(ctx);
  const src = fs.readFileSync(__dirname + '/public/region-filter.js', 'utf8');
  vm.runInContext(src, ctx);
  return ctx;
}

console.log('#1108 RegionShowAll + nodesRegionQueryString unit tests');

test('RegionShowAll exposed on window', () => {
  const ctx = buildCtx();
  assert.ok(ctx.window.RegionShowAll, 'expected window.RegionShowAll');
  assert.strictEqual(typeof ctx.window.RegionShowAll.get, 'function');
  assert.strictEqual(typeof ctx.window.RegionShowAll.set, 'function');
});

test('RegionShowAll.get() defaults to false', () => {
  const ctx = buildCtx();
  assert.strictEqual(ctx.window.RegionShowAll.get(), false);
});

test('RegionShowAll.set(true) persists; rebuild loads it back', () => {
  const ctx = buildCtx();
  ctx.window.RegionShowAll.set(true);
  assert.strictEqual(ctx.window.RegionShowAll.get(), true);
  const ctx2 = buildCtx({ 'mc-region-show-all-nodes': 'true' });
  assert.strictEqual(ctx2.window.RegionShowAll.get(), true);
});

test('RegionShowAll.set(false) clears persisted value', () => {
  const ctx = buildCtx({ 'mc-region-show-all-nodes': 'true' });
  assert.strictEqual(ctx.window.RegionShowAll.get(), true);
  ctx.window.RegionShowAll.set(false);
  assert.strictEqual(ctx.window.RegionShowAll.get(), false);
});

test('nodesRegionQueryString returns &region= when filter set + showAll off', () => {
  const ctx = buildCtx({ 'meshcore-region-filter': '["SJC"]' });
  ctx.window.RegionShowAll.set(false);
  assert.strictEqual(ctx.window.RegionFilter.nodesRegionQueryString(), '&region=SJC');
});

test('nodesRegionQueryString empty when showAll on (legacy show-everything behavior)', () => {
  const ctx = buildCtx({ 'meshcore-region-filter': '["SJC"]' });
  ctx.window.RegionShowAll.set(true);
  assert.strictEqual(ctx.window.RegionFilter.nodesRegionQueryString(), '');
});

test('nodesRegionQueryString empty when no region selected', () => {
  const ctx = buildCtx();
  ctx.window.RegionShowAll.set(false);
  assert.strictEqual(ctx.window.RegionFilter.nodesRegionQueryString(), '');
});

console.log(`\nResults: ${passed} passed, ${failed} failed`);
process.exit(failed > 0 ? 1 : 0);

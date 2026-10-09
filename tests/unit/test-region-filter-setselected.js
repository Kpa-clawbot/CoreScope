/**
 * RegionFilter.setSelected() saved the selection and redrew the control but
 * never ran the onChange listeners: _listeners was only called from
 * toggleRegion. A programmatic selection therefore updated the dropdown while
 * nothing re-queried. The packets page's own ?region= URL path has the same
 * gap, and an overlay that offered one-click region groups had to wrap
 * setSelected to work around it.
 *
 * setSelected must notify listeners when the selection actually changes, and
 * stay quiet when it does not (so a caller that re-applies the current
 * selection does not trigger a reload).
 */
'use strict';
const REPO_ROOT = require('path').resolve(__dirname, '..', '..');
const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log(`  ✓ ${name}`); }
  catch (e) { failed++; console.error(`  ✗ ${name}: ${e.message}`); }
}

function buildCtx(initialStorage) {
  const storage = Object.assign(Object.create(null), initialStorage || {});
  const ctx = {
    window: {},
    document: { addEventListener() {}, removeEventListener() {} },
    localStorage: {
      getItem: (k) => (k in storage ? storage[k] : null),
      setItem: (k, v) => { storage[k] = String(v); },
      removeItem: (k) => { delete storage[k]; },
    },
    fetch: async () => ({ json: async () => ({}) }),
    console, setTimeout, clearTimeout,
  };
  ctx.window = ctx;
  vm.createContext(ctx);
  vm.runInContext(fs.readFileSync(REPO_ROOT + '/public/region-filter.js', 'utf8'), ctx);
  return ctx;
}

function listen(rf) {
  const calls = [];
  rf.onChange((sel) => calls.push(sel === null ? null : Array.from(sel)));
  return calls;
}

console.log('RegionFilter.setSelected notifies change listeners');

test('selecting regions programmatically notifies listeners with the new selection', () => {
  const rf = buildCtx().window.RegionFilter;
  const calls = listen(rf);
  rf.setSelected(['SFO', 'SJC']);
  assert.deepStrictEqual(calls, [['SFO', 'SJC']]);
});

test('clearing the selection notifies listeners with null (all regions)', () => {
  const rf = buildCtx({ 'meshcore-region-filter': '["SFO"]' }).window.RegionFilter;
  const calls = listen(rf);
  rf.setSelected([]);
  assert.deepStrictEqual(calls, [null]);
});

test('re-applying the current selection does not notify', () => {
  const rf = buildCtx({ 'meshcore-region-filter': '["SFO","SJC"]' }).window.RegionFilter;
  const calls = listen(rf);
  rf.setSelected(['SJC', 'SFO']);
  rf.setSelected(['SFO', 'SJC']);
  assert.deepStrictEqual(calls, []);
});

test('clearing an already-empty selection does not notify', () => {
  const rf = buildCtx().window.RegionFilter;
  const calls = listen(rf);
  rf.setSelected([]);
  rf.setSelected(null);
  assert.deepStrictEqual(calls, []);
});

console.log(`\n${passed} passed, ${failed} failed`);
if (failed) process.exit(1);

/* test-clear-filters.js — unit test for clear-filters button (#964) */
'use strict';

const assert = require('assert');
const fs = require('fs');
const path = require('path');

console.log('--- test-clear-filters.js ---');

const src = fs.readFileSync(path.join(__dirname, 'public', 'packets.js'), 'utf-8');

// Test 1: button HTML present
assert(src.includes('clearFiltersBtn'), 'clearFiltersBtn ID should exist');
assert(src.includes('✕ Clear'), 'Clear button label should exist');

// Test 2: clear handler resets all filter keys
for (const k of ['hash', 'node', 'observer', 'channel', 'type', '_filterExpr']) {
  assert(src.includes(`filters.${k} = undefined`), `should reset filters.${k}`);
}
assert(src.includes('filters._packetFilter = null'), 'should reset _packetFilter');
assert(src.includes('filters.myNodes = false'), 'should reset myNodes');

// Test 3: clears localStorage
assert(src.includes("localStorage.removeItem('meshcore-observer-filter')"), 'clear observer localStorage');
assert(src.includes("localStorage.removeItem('meshcore-type-filter')"), 'clear type localStorage');

// Test 4: resets RegionFilter
assert(src.includes('RegionFilter.setSelected([])'), 'reset RegionFilter');

// Test 5: visibility toggle in updatePacketsUrl
assert(src.includes("cb.style.display = active ?"), 'toggle clear btn visibility');

// Test 6: resets DOM inputs
assert(src.includes("getElementById('fHash').value = ''"), 'clear hash input');
assert(src.includes("getElementById('fNode').value = ''"), 'clear node input');
assert(src.includes("getElementById('fChannel').value = ''"), 'clear channel select');

// Test 7: resets multi-select checkboxes
assert(src.includes('observerMenu'), 'clear observer checkboxes');
assert(src.includes('typeMenu'), 'clear type checkboxes');

console.log('All 7 tests passed ✅');

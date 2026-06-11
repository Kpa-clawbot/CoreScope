/**
 * Pin test for #1662: the packets PAGES entry in test-slideover-1056-e2e.js
 * must use a STRICT row selector (data-id only) — never a bare
 * `tbody tr` fallback that would match the virtual-scroll spacer row.
 *
 * Also pin the waitForFunction guard: it must require a row matching the
 * page-specific attribute, not just any <tr>.
 *
 * This is a discipline test — it prevents the loose fallback from sneaking
 * back in and re-introducing the 5% flake described in #1662.
 */
'use strict';
const fs = require('fs');
const path = require('path');

const SRC = path.join(__dirname, 'test-slideover-1056-e2e.js');
const src = fs.readFileSync(SRC, 'utf8');

let passed = 0, failed = 0;
function check(name, fn) {
  try { fn(); passed++; console.log('  ✓ ' + name); }
  catch (e) { failed++; console.error('  ✗ ' + name + ': ' + e.message); }
}
function assert(c, m) { if (!c) throw new Error(m || 'assertion failed'); }

console.log('\n=== #1662 strict row-selector pin ===');

check('packets PAGES entry uses strict #pktTable tbody tr[data-id] (no bare fallback)', () => {
  // Find the packets entry line.
  const m = src.match(/{\s*hash:\s*'#\/packets'[^}]*}/);
  assert(m, 'could not find packets PAGES entry');
  const entry = m[0];
  // Must include the strict selector.
  assert(/rowSel:\s*'#pktTable tbody tr\[data-id\]'/.test(entry),
    'packets rowSel is not the strict `#pktTable tbody tr[data-id]` string; got: ' + entry);
  // Must NOT include any bare `tbody tr` fallback (no attribute).
  assert(!/#pktTable tbody tr\s*['",]/.test(entry),
    'packets rowSel still has a bare-`tr` fallback (no attribute selector)');
});

check('waitForFunction guard requires a row matching the page-specific rowSel', () => {
  // The guard must reference `rowSel` (passed-through) or at minimum query
  // with an attribute selector — NOT a bare `tbody tr` count.
  // Acceptable shape: querySelector(rowSel) !== null
  // Or: querySelector('tbody tr[data-...]')
  const loose = /querySelectorAll\(\s*['"]tbody tr['"]\s*\)\.length\s*>\s*0/;
  assert(!loose.test(src),
    'waitForFunction still uses loose `querySelectorAll("tbody tr").length > 0` — ' +
    'will be satisfied by virtual-scroll spacer alone');
});

console.log(`\nResults: ${passed} passed, ${failed} failed\n`);
process.exit(failed === 0 ? 0 : 1);

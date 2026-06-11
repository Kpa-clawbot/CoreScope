/**
 * Regression test for #1662 — flake on test-slideover-1056-e2e.js packets@800.
 *
 * The slide-over E2E was racing the packets virtual-scroll spacer row:
 *   - `rowSel` for the packets page fell back to `#pktTable tbody tr` (any
 *     `<tr>`), which matched the empty spacer placeholder that has no
 *     `data-id`.
 *   - The `waitForFunction` gate accepted any `tbody tr` length > 0, so the
 *     spacer satisfied the wait before real data rows existed.
 *
 * The fix is test-only: keep `rowSel` strict to `tr[data-id]` for packets and
 * derive the wait predicate from the same strict selector. This test
 * statically asserts the file stays strict; it is RED against the old
 * test-slideover-1056-e2e.js and GREEN after the selector tightening.
 *
 * Static-only — no browser, no network. Safe to run in the unit-test job.
 */
'use strict';

const fs = require('fs');
const path = require('path');

const TARGET = path.join(__dirname, 'test-slideover-1056-e2e.js');
const src = fs.readFileSync(TARGET, 'utf8');

let failed = 0;
function check(name, cond, detail) {
  if (cond) {
    console.log('  ✓ ' + name);
  } else {
    failed++;
    console.error('  ✗ ' + name + (detail ? ': ' + detail : ''));
  }
}

console.log('\n=== #1662 regression: strict rowSel + strict wait predicate ===');

// 1. Loose `#pktTable tbody tr` fallback (with no attribute predicate) must
//    not appear anywhere as a comma-fallback inside a rowSel string.
const looseFallback = /,\s*#pktTable\s+tbody\s+tr\b(?!\s*\[)/;
check(
  'packets rowSel has no loose `, #pktTable tbody tr` fallback',
  !looseFallback.test(src),
  'matched: ' + (src.match(looseFallback) || [''])[0]
);

// 2. The packets PAGES entry must use the strict `tr[data-id]` selector.
const packetsEntry = src.match(/\{\s*hash:\s*'#\/packets'[^}]*\}/);
check('packets PAGES entry exists', !!packetsEntry);
if (packetsEntry) {
  check(
    "packets rowSel includes a strict `tr[data-*]` selector",
    /tr\[data-(id|hash)\]/.test(packetsEntry[0]),
    packetsEntry[0]
  );
  check(
    "packets rowSel is strict (no bare `tbody tr` token)",
    !/,\s*#pktTable\s+tbody\s+tr\b(?!\s*\[)/.test(packetsEntry[0]),
    packetsEntry[0]
  );
}

// 3. The first-row wait predicate must require a row matching the page's
//    strict per-page rowSel — not just any `tbody tr`. The smoking-gun in
//    #1662 was `t.querySelectorAll('tbody tr').length > 0`, which the spacer
//    row satisfied. Reject that exact pattern.
const looseWait = /querySelectorAll\(\s*['"`]tbody tr['"`]\s*\)\.length\s*>\s*0/;
check(
  'wait predicate does not gate on bare `tbody tr` count',
  !looseWait.test(src),
  'matched: ' + (src.match(looseWait) || [''])[0]
);

// 4. Wait predicate must reference the strict rowSel — easiest signal is
//    that it queries with a `data-*` attribute selector inside the wait
//    helper. The simplest enforceable shape: `querySelector('… [data-…])`
//    appears inside a waitForFunction body, AND the loose form is gone.
const waitFn = src.match(/waitForFunction\(\s*\([^)]*\)\s*=>\s*\{[\s\S]*?\}\s*,\s*p\.\w+/);
check('waitForFunction predicate found', !!waitFn);
if (waitFn) {
  check(
    'wait predicate uses a strict `data-*` row selector',
    /data-(id|hash|value|action)/.test(waitFn[0]),
    waitFn[0].slice(0, 200)
  );
}

if (failed > 0) {
  console.error(`\n${failed} check(s) failed — #1662 regression: spacer-row race can re-occur.`);
  process.exit(1);
}
console.log('\nAll #1662 strict-rowSel checks passed.');

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

// 4. Wait predicate must reference a strict `data-*` row selector. The
//    smoking gun was `querySelectorAll('tbody tr')` in the predicate body;
//    a strict gate uses `querySelector('tbody tr[data-…]')` via the per-page
//    `waitSel`. We extract the FIRST `waitForFunction` predicate body by
//    walking balanced braces (an earlier lazy regex matched past the body
//    into the next page.evaluate block and validated the WRONG code).
function extractFirstWaitPredicateBody(s) {
  const anchor = s.indexOf('waitForFunction(');
  if (anchor < 0) return null;
  // Find the predicate's opening `{` after `=>`.
  const arrow = s.indexOf('=>', anchor);
  if (arrow < 0) return null;
  const open = s.indexOf('{', arrow);
  if (open < 0) return null;
  // Walk balanced braces, ignoring braces inside strings/template literals.
  let depth = 0;
  let i = open;
  let inStr = null; // quote char or null
  for (; i < s.length; i++) {
    const c = s[i];
    if (inStr) {
      if (c === '\\') { i++; continue; }
      if (c === inStr) inStr = null;
      continue;
    }
    if (c === "'" || c === '"' || c === '`') { inStr = c; continue; }
    if (c === '{') depth++;
    else if (c === '}') { depth--; if (depth === 0) return s.slice(open + 1, i); }
  }
  return null;
}
const waitBody = extractFirstWaitPredicateBody(src);
check('waitForFunction predicate body extracted', !!waitBody);
if (waitBody) {
  // The predicate must call `querySelector(...)` with a strict `tbody
  // tr[data-…]` selector (either inline or via the `waitSel` param the
  // page-loop now passes). Accept either form:
  //   - literal: querySelector('tbody tr[data-id]') / [data-value] / [data-action="…"]
  //   - param:   querySelector(waitSel) where waitSel is destructured/used
  const strictLiteral =
    /querySelector\(\s*['"`]tbody\s+tr\[data-[a-z]+(?:="[^"]+")?\]['"`]\s*\)/.test(waitBody);
  const strictParam = /querySelector\(\s*waitSel\s*\)/.test(waitBody);
  check(
    'wait predicate uses a strict `tbody tr[data-*]` selector (literal or via waitSel param)',
    strictLiteral || strictParam,
    waitBody.slice(0, 240)
  );
  // Belt-and-suspenders: also reject the smoking-gun pattern inside the
  // predicate body itself.
  check(
    'wait predicate body does NOT contain bare `querySelectorAll("tbody tr")`',
    !/querySelectorAll\(\s*['"`]tbody tr['"`]\s*\)/.test(waitBody),
    waitBody.slice(0, 240)
  );
}

// 5. The CLICK step's row finder must include a `data-id` branch so packets
//    (whose rows carry `data-id`, not `data-action`/`data-value`) actually
//    click a real row, not the spacer (#1662 round-1 fix for M2). Without
//    this branch the picker falls through to `children.length>0`, which can
//    still match the spacer if it ever renders a child cell — and is weaker
//    than the strict wait predicate above.
const clickerDataIdBranch = /\.find\(\s*r\s*=>\s*r\.hasAttribute\(\s*['"`]data-id['"`]\s*\)\s*\)/;
check(
  'click step row picker has a `data-id`-aware branch (matches wait strictness)',
  clickerDataIdBranch.test(src),
  'expected `.find(r => r.hasAttribute("data-id"))` in click body'
);

if (failed > 0) {
  console.error(`\n${failed} check(s) failed — #1662 regression: spacer-row race can re-occur.`);
  process.exit(1);
}
console.log('\nAll #1662 strict-rowSel checks passed.');

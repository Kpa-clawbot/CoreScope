/**
 * #1364 — regression(map): #1362 pill max-width:4ch over-clamps multi-digit
 * counts → `R…` instead of `R60`.
 *
 * The defense-in-depth `max-width: 4ch` added in #1362 ellipsizes pill
 * content because the 4ch box includes left/right padding (1px 3px),
 * leaving ~2.5ch for text — enough for `R6` but not `R60`.
 *
 * Fix (Option A from issue): drop `max-width` entirely. JS already caps
 * at "999+" so CSS guard was overcaution. Keep `overflow:hidden` +
 * `text-overflow:ellipsis` as graceful-degrade if JS ever fails.
 */
'use strict';

const fs = require('fs');
const path = require('path');

let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}

const cssSrc = fs.readFileSync(path.join(__dirname, 'public', 'style.css'), 'utf8');
const pillRuleRe = /\.mc-cluster\s+\.mc-pill\s*\{([\s\S]*?)\}/;
const pillMatch = cssSrc.match(pillRuleRe);

console.log('\n=== #1364: .mc-pill no longer clamps multi-digit counts ===');

assert(pillMatch, '.mc-cluster .mc-pill rule found in style.css');

if (pillMatch) {
  const body = pillMatch[1];

  // Primary regression guard: NO max-width: 4ch (or any max-width that would
  // clamp `R999+`). Issue acceptance criterion: "assert .mc-pill CSS does
  // NOT contain max-width: 4ch".
  assert(!/max-width\s*:\s*4ch/.test(body),
    '.mc-pill does NOT declare `max-width: 4ch` (regression guard for #1364)');

  // Graceful degradation: keep belt-only overflow guards in case JS cap
  // is bypassed by a hypothetical regression.
  assert(/overflow\s*:\s*hidden/.test(body),
    '.mc-pill keeps `overflow: hidden` as graceful-degrade');
  assert(/text-overflow\s*:\s*ellipsis/.test(body),
    '.mc-pill keeps `text-overflow: ellipsis` as graceful-degrade');
}

console.log('\n=== Summary ===');
console.log('  Passed: ' + passed);
console.log('  Failed: ' + failed);
console.log('\n#1364 ' + (failed === 0 ? 'PASS' : 'FAIL'));
process.exit(failed === 0 ? 0 : 1);

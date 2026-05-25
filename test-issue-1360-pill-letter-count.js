/**
 * #1360 — regression(map): #1357 cluster role pills lost the count number.
 *
 * Pill body must contain BOTH the role letter (WCAG carrier from #1356)
 * AND the per-role count (the data sighted operators need at a glance).
 *
 * Pure-string assertions over public/map.js (mirrors #1356 test pattern).
 */
'use strict';

const fs = require('fs');
const path = require('path');

let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}

const mapSrc = fs.readFileSync(path.join(__dirname, 'public', 'map.js'), 'utf8');

console.log('\n=== #1360: pill body emits letter + count (not letter alone) ===');

// A. Source must concatenate letter and n (the count) into the pill body.
//    Acceptable shapes: `letter + n`, `letter + String(n)`, `(letter + n)`.
const concatRe = /letter\s*\+\s*(?:String\()?\s*n\b/;
assert(concatRe.test(mapSrc),
  'map.js concatenates letter + n (or letter + String(n)) for pill body');

// B. The pill body must NOT be bare `letter` followed immediately by '</span>'.
//    i.e. reject `... + letter + '</span>'` with nothing in between.
const bareLetterRe = /\+\s*letter\s*\+\s*['"]<\/span>/;
assert(!bareLetterRe.test(mapSrc),
  'pill body is no longer just letter (no `+ letter + "</span>"` pattern)');

// C. Simulate makeClusterIcon by exercising __meshcoreMapInternals if loadable
//    in Node — fallback: pattern-check the rendered HTML template.
//    map.js is browser-oriented (Leaflet IIFE) so we string-test the template.
//    Build a synthetic expected pill body: a letter from R/C/M/S/O + digits.
//    The assertion below validates the rendered shape via regex over the
//    template's emitted output pattern.
const pillTemplateRe = /<span class="mc-pill[\s\S]{0,400}letter\s*\+\s*(?:String\()?\s*n/;
assert(pillTemplateRe.test(mapSrc),
  'pill HTML template body interpolates letter + n inside the span');

// D. Letter is still the first character of the pill body (preserves #1356
//    WCAG carrier ordering — assistive scanning sees the role letter first).
//    The concatenation must be `letter + n`, not `n + letter`.
const reverseRe = /\bn\s*\+\s*letter\b/;
assert(!reverseRe.test(mapSrc),
  'letter precedes count in concatenation (letter + n, not n + letter)');

// E. Acceptance criterion from the issue: pill body matches /^[RCMSO]\d+$/
//    for non-zero counts. Verify ROLE_LETTERS maps to the expected set.
const roleLettersRe = /ROLE_LETTERS\s*=\s*\{([\s\S]*?)\}/;
const rlMatch = mapSrc.match(roleLettersRe);
assert(rlMatch, 'ROLE_LETTERS map is defined in map.js');
if (rlMatch) {
  const letters = (rlMatch[1].match(/'[A-Z]'/g) || []).map(function (s) { return s[1]; });
  const expected = ['R', 'C', 'M', 'S', 'O'];
  const haveAll = expected.every(function (l) { return letters.indexOf(l) !== -1; });
  assert(haveAll,
    'ROLE_LETTERS includes R, C, M, S, O so pill body matches /^[RCMSO]\\d+$/');
}

// === #1360 follow-up: 4+ digit count overflow guard ===
console.log('\n=== #1360 follow-up: pill width bounded for 4+ digit counts ===');

// F. JS cap: makeClusterIcon must clamp counts > 999 to "999+" so pill body
//    becomes e.g. "R999+" instead of "R1234" / "R10000".
const jsCapRe = /n\s*>\s*999[\s\S]{0,80}['"]999\+['"]/;
assert(jsCapRe.test(mapSrc),
  'makeClusterIcon caps counts > 999 to "999+" (n > 999 → "999+")');

// G. CSS guard: .mc-pill rule must include max-width AND text-overflow:ellipsis
//    as defense-in-depth in case a render slips past the JS cap.
const cssSrc = fs.readFileSync(path.join(__dirname, 'public', 'style.css'), 'utf8');
const pillRuleRe = /\.mc-cluster\s+\.mc-pill\s*\{([\s\S]*?)\}/;
const pillMatch = cssSrc.match(pillRuleRe);
assert(pillMatch, '.mc-cluster .mc-pill rule found in style.css');
if (pillMatch) {
  const body = pillMatch[1];
  // #1364: dropped `max-width` — it over-clamped multi-digit counts.
  // Graceful-degrade ellipsis assertion stays.
  assert(/text-overflow\s*:\s*ellipsis/.test(body),
    '.mc-pill declares text-overflow: ellipsis (graceful clip)');
}

console.log('\n=== Summary ===');
console.log('  Passed: ' + passed);
console.log('  Failed: ' + failed);
console.log('\n#1360 ' + (failed === 0 ? 'PASS' : 'FAIL'));
process.exit(failed === 0 ? 0 : 1);

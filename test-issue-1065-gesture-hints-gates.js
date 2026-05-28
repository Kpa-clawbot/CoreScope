#!/usr/bin/env node
/* Issue #1065 follow-up — gesture hints must:
 *   1. Define a hasTouchCapability() helper that probes ontouchstart,
 *      maxTouchPoints, and (pointer: coarse).
 *   2. Gate every HINTS[*].relevant() body on hasTouchCapability() at the
 *      very top (no hint should fire on mouse-only viewports).
 *   3. Ship a .gesture-hint parent CSS rule that includes
 *      `width: fit-content` AND `max-width: 360px` so the pill shrinks to
 *      its content instead of stretching full-bleed and being pushed
 *      off-screen by translateX(-50%) on narrow viewports.
 *
 * Pure source-file assertions — no browser required.
 */
'use strict';

const fs = require('fs');
const path = require('path');

const JS_PATH = path.join(__dirname, 'public', 'gesture-hints.js');
const CSS_PATH = path.join(__dirname, 'public', 'style.css');

let failures = 0, passes = 0;
const fail = (m) => { failures++; console.error('  FAIL: ' + m); };
const pass = (m) => { passes++; console.log('  PASS: ' + m); };

const js = fs.readFileSync(JS_PATH, 'utf8');
const css = fs.readFileSync(CSS_PATH, 'utf8');

// (1) helper exists and probes the three signals
if (/function\s+hasTouchCapability\s*\(/.test(js)) pass('hasTouchCapability() defined');
else fail('hasTouchCapability() not defined in gesture-hints.js');

if (/ontouchstart/.test(js)) pass('hasTouchCapability probes ontouchstart');
else fail('hasTouchCapability missing ontouchstart probe');

if (/maxTouchPoints/.test(js)) pass('hasTouchCapability probes maxTouchPoints');
else fail('hasTouchCapability missing maxTouchPoints probe');

if (/pointer:\s*coarse/.test(js)) pass('hasTouchCapability probes (pointer: coarse)');
else fail('hasTouchCapability missing (pointer: coarse) probe');

// (2) every relevant() body must start with the touch gate
// Find each `relevant: function () { ... }` block and check.
const relevantRe = /relevant:\s*function\s*\(\s*\)\s*\{([\s\S]*?)\n\s{6}\}/g;
let m, count = 0, gated = 0;
while ((m = relevantRe.exec(js)) !== null) {
  count++;
  const body = m[1];
  // First non-comment statement must be hasTouchCapability gate
  if (/^\s*if\s*\(\s*!\s*hasTouchCapability\s*\(\s*\)\s*\)\s*return\s+false\s*;/m.test(body)) {
    gated++;
  }
}
if (count >= 4) pass(`found ${count} relevant() predicates`);
else fail(`expected ≥4 relevant() predicates, found ${count}`);
if (gated === count && count > 0) pass(`all ${gated}/${count} relevant() bodies start with !hasTouchCapability() return false`);
else fail(`only ${gated}/${count} relevant() bodies gate on hasTouchCapability()`);

// (3) .gesture-hint parent rule has width: fit-content + max-width: 360px
// Locate the rule block starting `.gesture-hint {` (NOT .gesture-hint-...).
const ruleRe = /\n\.gesture-hint\s*\{([\s\S]*?)\}/;
const ruleMatch = ruleRe.exec(css);
if (!ruleMatch) {
  fail('.gesture-hint parent CSS rule not found in style.css');
} else {
  pass('.gesture-hint parent CSS rule present');
  const body = ruleMatch[1];
  if (/\bwidth:\s*fit-content\b/.test(body)) pass('.gesture-hint declares width: fit-content');
  else fail('.gesture-hint missing width: fit-content (pill must shrink to content)');
  if (/\bmax-width:\s*360px\b/.test(body)) pass('.gesture-hint declares max-width: 360px');
  else fail('.gesture-hint missing max-width: 360px');
}

// (4) defensive: no em-dash or stray "*/" inside .gesture-hint rule body
if (ruleMatch) {
  const body = ruleMatch[1];
  if (/[\u2014\u2013]/.test(body)) fail('em-dash / en-dash inside .gesture-hint rule body (CSS-parse-fragile)');
  else pass('no em-dash inside .gesture-hint rule body');
}

console.log(`\ntest-issue-1065-gesture-hints-gates.js: ${passes} passed, ${failures} failed`);
process.exit(failures > 0 ? 1 : 0);

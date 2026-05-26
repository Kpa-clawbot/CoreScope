/**
 * #1375 — regression(analytics): Scopes tab fetches `/api/api/scope-stats`
 * (duplicate prefix) → 404 → SPA HTML → JSON.parse error.
 *
 * The `api()` helper already prepends `/api`. Other callers in
 * public/analytics.js correctly pass `/scope-stats` style relative paths;
 * the Scopes loader was the lone offender passing `/api/scope-stats`,
 * producing the doubled prefix at runtime.
 *
 * Fix: drop the leading `/api` from the Scopes-tab call so the helper
 * builds `/api/scope-stats?window=…`.
 *
 * Originally landed on the PR #915 branch (commit 2fd22cee) but that
 * branch never merged, so the bug resurfaced in subsequent rebases.
 */
'use strict';

const fs = require('fs');
const path = require('path');

let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}

const src = fs.readFileSync(
  path.join(__dirname, 'public', 'analytics.js'), 'utf8');

console.log('\n=== #1375: Scopes tab scope-stats fetch path ===');

// Regression guard: the buggy doubled-prefix form must never reappear.
const badRe = /api\(\s*['"]\/api\/scope-stats/g;
const badMatches = src.match(badRe) || [];
assert(badMatches.length === 0,
  "ZERO `api('/api/scope-stats'` occurrences in analytics.js " +
  '(regression guard for doubled /api prefix)');

// Positive: the corrected, helper-relative form is present exactly once.
const goodRe = /api\(\s*['"]\/scope-stats/g;
const goodMatches = src.match(goodRe) || [];
assert(goodMatches.length === 1,
  "Exactly one `api('/scope-stats'` call exists (the fixed loader) — " +
  'found ' + goodMatches.length);

console.log('\n=== Summary ===');
console.log('  Passed: ' + passed);
console.log('  Failed: ' + failed);
console.log('\n#1375 ' + (failed === 0 ? 'PASS' : 'FAIL'));
process.exit(failed === 0 ? 0 : 1);

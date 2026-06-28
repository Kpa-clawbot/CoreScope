/* test-issue-1753-copy-url-slash.js — regression test for issue #1753.
 *
 * Bug: Copy URL / Copy short URL buttons on the node detail page built URLs as
 *   location.origin + '#/nodes/...'  ->  https://analyzer.00id.net#/nodes/...
 * That's a malformed URL (missing the '/' between authority and fragment).
 * Some mobile browsers reject it.
 *
 * Strategy: behavioral source-level check. The three URL-construction sites
 * in public/nodes.js are simple string concatenations of `location.origin`
 * with a hardcoded literal. The "real" assertion is on the literal itself —
 * it MUST start with '/#/' (not '#/'). The test extracts every
 *   location.origin + '<literal>'
 * occurrence in public/nodes.js and asserts each literal begins with '/#/'.
 * Reverting the fix (dropping the leading '/') re-introduces the failure.
 */
'use strict';
const fs = require('fs');
const path = require('path');
const assert = require('assert');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log('  ✅ ' + name); }
  catch (e) { failed++; console.log('  ❌ ' + name + ': ' + e.message); }
}

const src = fs.readFileSync(path.join(__dirname, 'public/nodes.js'), 'utf8');

// Match: location.origin + '<literal>'  (single or double quotes)
const re = /location\.origin\s*\+\s*(['"])([^'"]*)\1/g;
const sites = [];
let m;
while ((m = re.exec(src)) !== null) {
  const lineNo = src.slice(0, m.index).split('\n').length;
  sites.push({ literal: m[2], lineNo });
}

console.log('issue-1753 copy-URL slash regression');

test('found at least 3 location.origin + literal sites in public/nodes.js', () => {
  assert.ok(sites.length >= 3,
    'expected >=3 sites, found ' + sites.length + ': ' + JSON.stringify(sites));
});

for (const s of sites) {
  test('public/nodes.js:' + s.lineNo + ' literal starts with "/#/" (got ' + JSON.stringify(s.literal) + ')', () => {
    assert.ok(
      s.literal.startsWith('/#/') || s.literal.startsWith('/'),
      'location.origin + ' + JSON.stringify(s.literal) +
      ' produces malformed URL (missing leading "/"). See issue #1753.'
    );
    // Specifically forbid '#/' as the literal start — that's the bug.
    assert.ok(
      !s.literal.startsWith('#/'),
      'literal begins with "#/" — concatenating with location.origin yields ' +
      '"<origin>#/..." which is malformed. Fix: prepend "/". See issue #1753.'
    );
  });
}

console.log('\n  ' + passed + ' passed, ' + failed + ' failed');
process.exit(failed > 0 ? 1 : 0);

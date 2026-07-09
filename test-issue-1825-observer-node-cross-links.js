/* test-issue-1825-observer-node-cross-links.js — Issue #1825 regression test.
 *
 * Asserts cross-navigation links exist between the observer detail page and
 * the node detail page for the same pubkey:
 *
 *  - public/observer-detail.js renders an anchor `href="#/nodes/${encodeURIComponent(currentId)}"`
 *    inside the .page-header (near the `<h2 id="obsTitle">`).
 *  - public/nodes.js renders an anchor `href="#/observers/${encodeURIComponent(n.public_key)}"`
 *    in the same button row as the analytics/reach anchors on the full node
 *    detail page.
 *
 * Static-source test (grep the file text). No server, no DOM.
 */
'use strict';

const fs = require('fs');
const path = require('path');
const assert = require('assert');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log(`  \u2713 ${name}`); }
  catch (e) { failed++; console.log(`  \u2717 ${name}\n    ${e.message}`); }
}

console.log('\u2500\u2500 Observer <-> Node cross-links (#1825) \u2500\u2500');

const obsSrc = fs.readFileSync(path.join(__dirname, 'public', 'observer-detail.js'), 'utf8');
const nodesSrc = fs.readFileSync(path.join(__dirname, 'public', 'nodes.js'), 'utf8');

// Note: #1836 introduced case-normalization on these hrefs (currentId →
// currentId.toLowerCase(), n.public_key → n.public_key.toUpperCase()). The
// regexes below accept an optional `.toLowerCase()`/`.toUpperCase()` call so
// this test continues to guard the presence + placement of the cross-links
// regardless of the case-normalization detail (which #1836 covers directly).

test('observer-detail.js links to #/nodes/${encodeURIComponent(currentId[.toLowerCase()])}', () => {
  assert.ok(
    /href="#\/nodes\/\$\{encodeURIComponent\(currentId(?:\.toLowerCase\(\))?\)\}"/.test(obsSrc),
    'expected anchor href="#/nodes/${encodeURIComponent(currentId[.toLowerCase()])}" in observer-detail.js'
  );
});

test('observer-detail.js cross-link sits inside the .page-header block', () => {
  const m = obsSrc.match(/class="page-header"[\s\S]*?<\/div>/);
  assert.ok(m, '.page-header block not found in observer-detail.js');
  assert.ok(
    /href="#\/nodes\/\$\{encodeURIComponent\(currentId(?:\.toLowerCase\(\))?\)\}"/.test(m[0]),
    'observer -> node cross-link is not inside the .page-header block'
  );
});

test('nodes.js links to #/observers/${encodeURIComponent(n.public_key[.toUpperCase()])}', () => {
  assert.ok(
    /href="#\/observers\/\$\{encodeURIComponent\(n\.public_key(?:\.toUpperCase\(\))?\)\}"/.test(nodesSrc),
    'expected anchor href="#/observers/${encodeURIComponent(n.public_key[.toUpperCase()])}" in nodes.js'
  );
});

test('nodes.js Observer link is a sibling of the analytics/reach anchors', () => {
  // Find the button row that contains both analytics and reach anchors and
  // assert it now also contains the observers anchor.
  const rowRe = /<div[^>]*display:flex[^>]*flex-wrap:wrap[^>]*>[\s\S]*?<\/div>/g;
  let found = false;
  let m;
  while ((m = rowRe.exec(nodesSrc)) !== null) {
    const row = m[0];
    if (/href="#\/nodes\/\$\{encodeURIComponent\(n\.public_key\)\}\/analytics"/.test(row) &&
        /href="#\/nodes\/\$\{encodeURIComponent\(n\.public_key\)\}\/reach"/.test(row)) {
      if (/href="#\/observers\/\$\{encodeURIComponent\(n\.public_key(?:\.toUpperCase\(\))?\)\}"/.test(row)) {
        found = true;
        break;
      }
    }
  }
  assert.ok(found, 'observer anchor not found in the same button row as analytics/reach anchors');
});

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed ? 1 : 0);

/* test-issue-1836-crossnav-case-normalization.js — Issue #1836 regression test.
 *
 * The cross-navigation links introduced in PR #1826 return 404 because pubkeys
 * are stored lowercase in the `nodes` table and uppercase in the `observers`
 * table, and the backend WHERE clauses are case-sensitive. The frontend link
 * builders must normalize the case of the pubkey/id when linking across.
 *
 *  - public/observer-detail.js: observer → node link must lowercase currentId.
 *  - public/nodes.js: node → observer link must uppercase n.public_key.
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

console.log('\u2500\u2500 Cross-nav case normalization (#1836) \u2500\u2500');

const obsSrc = fs.readFileSync(path.join(__dirname, 'public', 'observer-detail.js'), 'utf8');
const nodesSrc = fs.readFileSync(path.join(__dirname, 'public', 'nodes.js'), 'utf8');

test('observer-detail.js observer→node href lowercases currentId', () => {
  assert.ok(
    /href="#\/nodes\/\$\{encodeURIComponent\(currentId\.toLowerCase\(\)\)\}"/.test(obsSrc),
    'expected href="#/nodes/${encodeURIComponent(currentId.toLowerCase())}" in observer-detail.js'
  );
});

test('nodes.js node→observer href uppercases n.public_key', () => {
  assert.ok(
    /href="#\/observers\/\$\{encodeURIComponent\(n\.public_key\.toUpperCase\(\)\)\}"/.test(nodesSrc),
    'expected href="#/observers/${encodeURIComponent(n.public_key.toUpperCase())}" in nodes.js'
  );
});

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed ? 1 : 0);

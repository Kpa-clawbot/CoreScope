/* Issue #1409 — channels.js must NOT unconditionally force-enable
 * 'channels-show-encrypted' in localStorage on every init.
 *
 * The bug: channels.js set localStorage.setItem('channels-show-encrypted', 'true')
 * unconditionally on init, which made it impossible for an operator to ever
 * hide the 246 encrypted-placeholder channels.
 *
 * Test strategy: source-grep. The file must not contain a
 * setItem('channels-show-encrypted', 'true') call anywhere — there is no
 * legitimate place to force this on; the only writer should be a future
 * user-toggle handler that writes BOTH 'true' and 'false' under a condition.
 */
'use strict';
const fs = require('fs');
const path = require('path');
const assert = require('assert');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log('  \u2705 ' + name); }
  catch (e) { failed++; console.log('  \u274c ' + name + ': ' + e.message); }
}

const src = fs.readFileSync(path.join(__dirname, 'public/channels.js'), 'utf8');

console.log('Issue #1409 — no force-enable of channels-show-encrypted');

test('channels.js does NOT unconditionally setItem(channels-show-encrypted, true)', function () {
  // Match any whitespace/quote variant of:
  //   localStorage.setItem('channels-show-encrypted', 'true')
  // or with double quotes. A user-toggle handler would set a VARIABLE,
  // not the literal string 'true', so this is a safe gate.
  var re = /localStorage\s*\.\s*setItem\s*\(\s*['"]channels-show-encrypted['"]\s*,\s*['"]true['"]\s*\)/;
  var m = src.match(re);
  assert.strictEqual(m, null,
    'Found forbidden literal force-set of channels-show-encrypted=true in public/channels.js. ' +
    'A user-toggle handler should pass a boolean variable, not the literal string "true".');
});

test('channels.js still reads channels-show-encrypted (toggle gate preserved)', function () {
  // We are NOT removing the read path; the reader is still needed so a
  // future user toggle works. This sanity-check ensures the fix did not
  // also delete the reader.
  assert.ok(/getItem\(\s*['"]channels-show-encrypted['"]\s*\)/.test(src),
    'Expected getItem(channels-show-encrypted) to still be present');
});

console.log('\n' + passed + ' passed, ' + failed + ' failed');
process.exit(failed > 0 ? 1 : 0);

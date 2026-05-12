/* test-observers-headings.js — Issue #1039 regression test.
 * Asserts observer table thead column count matches tbody row column count.
 */
'use strict';

const fs = require('fs');
const path = require('path');
const assert = require('assert');

const src = fs.readFileSync(path.join(__dirname, 'public', 'observers.js'), 'utf8');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log(`  ✓ ${name}`); }
  catch (e) { failed++; console.log(`  ✗ ${name}\n    ${e.message}`); }
}

function extractBlock(s, openRe, closeRe) {
  const m = s.match(openRe);
  if (!m) throw new Error('open marker not found');
  const start = m.index + m[0].length;
  const rest = s.slice(start);
  const cm = rest.match(closeRe);
  if (!cm) throw new Error('close marker not found');
  return rest.slice(0, cm.index);
}

console.log('── Observers table headings (#1039) ──');

test('thead column count equals tbody row column count', () => {
  const thead = extractBlock(src, /<thead><tr>/, /<\/tr><\/thead>/);
  const thCount = (thead.match(/<th\b/g) || []).length;

  // tbody row template lives inside a backtick-template `<tr ...>...</tr>`.
  // Grab from the first `<tr ` after `tbody>` up to the first `</tr>`.
  const tbodyStart = src.indexOf('<tbody>');
  assert.ok(tbodyStart > 0, '<tbody> not found in observers.js');
  const after = src.slice(tbodyStart);
  const trOpen = after.search(/`<tr\b/);
  assert.ok(trOpen > 0, 'row template `<tr` not found');
  const rowStart = trOpen;
  const rowEnd = after.indexOf('</tr>', rowStart);
  assert.ok(rowEnd > rowStart, '</tr> not found in row template');
  const row = after.slice(rowStart, rowEnd);
  const tdCount = (row.match(/<td\b/g) || []).length;

  assert.strictEqual(
    tdCount, thCount,
    `Observer table column mismatch: ${thCount} <th> headings vs ${tdCount} <td> cells per row. ` +
    `Headings drift after "Last Packet" — see issue #1039.`
  );
});

test('expected headings present and ordered', () => {
  const thead = extractBlock(src, /<thead><tr>/, /<\/tr><\/thead>/);
  const labels = [];
  const re = /<th[^>]*>([^<]+)<\/th>/g;
  let m;
  while ((m = re.exec(thead)) !== null) labels.push(m[1].trim());
  const expected = ['Status', 'Name', 'Region', 'Last Status', 'Last Packet',
                    'Packet Health', 'Total Packets', 'Packets/Hour', 'Clock Offset', 'Uptime'];
  assert.deepStrictEqual(labels, expected,
    `Headings out of sync.\nGot:      ${JSON.stringify(labels)}\nExpected: ${JSON.stringify(expected)}`);
});

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed === 0 ? 0 : 1);

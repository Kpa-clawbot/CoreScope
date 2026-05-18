/* test-observers-headings.js — Issue #1039 regression test.
 * Asserts observer table thead column count matches tbody row column count.
 *
 * The thead mixes literal `<th>` tags with `sortTh('Label','col',prio)` helper
 * calls (each call renders exactly one `<th>`). The parser below understands
 * both forms so it stays accurate as columns move between literal and helper.
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

// Headings in source order. Each match is either a sortTh('Label',...) call or
// a literal <th ...>Label</th> tag — both produce one column header.
function theadLabels(thead) {
  const labels = [];
  const re = /sortTh\('([^']+)'|<th\b[^>]*>([^<]+)<\/th>/g;
  let m;
  while ((m = re.exec(thead)) !== null) {
    labels.push((m[1] !== undefined ? m[1] : m[2]).trim());
  }
  return labels;
}

console.log('── Observers table headings (#1039) ──');

test('thead column count equals tbody row column count', () => {
  const thead = extractBlock(src, /<thead><tr>/, /<\/tr><\/thead>/);
  const thCount = theadLabels(thead).length;

  // tbody row template is built via .map() and starts with return `<tr ...`.
  // Search the whole source for this pattern (it appears before <tbody> in the
  // full-render template literal).
  const trOpen = src.search(/return\s*`<tr\b/);
  assert.ok(trOpen > 0, 'row template `<tr` not found');
  const after = src.slice(trOpen);
  const rowStart = after.search(/`<tr\b/);
  assert.ok(rowStart >= 0, '`<tr` not found after return');
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
  const labels = theadLabels(thead);
  const expected = ['Status', 'Name', 'SF', 'Packet Health', 'Region', 'Last Status',
                    'Clock Offset', 'Uptime', 'Total Packets', 'Packets/Hour', 'Last Packet'];
  assert.deepStrictEqual(labels, expected,
    `Headings out of sync.\nGot:      ${JSON.stringify(labels)}\nExpected: ${JSON.stringify(expected)}`);
});

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed === 0 ? 0 : 1);

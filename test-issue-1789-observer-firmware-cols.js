/* test-issue-1789-observer-firmware-cols.js — Issue #1789 regression test.
 *
 * Asserts the observers table renders Firmware and Client (client_version)
 * columns from data already on the wire from /api/observers. Both headers
 * must carry data-priority="4" (matches Clock Offset/Uptime so
 * TableResponsive hides them first on mobile) and data-sort-key, and both
 * cells must carry a data-value (raw string for sorting) and class="mono".
 *
 * For the firmware cell, a long "... Build: ..." suffix on the displayed
 * text must be truncated, with the full string preserved in a title=
 * attribute.
 *
 * Static-source test (like test-observers-headings.js) — no server needed.
 */
'use strict';

const fs = require('fs');
const path = require('path');
const assert = require('assert');

const src = fs.readFileSync(path.join(__dirname, 'public', 'observers.js'), 'utf8');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log(`  \u2713 ${name}`); }
  catch (e) { failed++; console.log(`  \u2717 ${name}\n    ${e.message}`); }
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

console.log('\u2500\u2500 Observers firmware/client columns (#1789) \u2500\u2500');

test('thead contains Firmware header with data-priority=4 and data-sort-key=firmware', () => {
  const thead = extractBlock(src, /<thead><tr>/, /<\/tr><\/thead>/);
  assert.ok(
    /<th[^>]*data-priority="4"[^>]*data-sort-key="firmware"[^>]*>\s*Firmware\s*<\/th>/.test(thead) ||
    /<th[^>]*data-sort-key="firmware"[^>]*data-priority="4"[^>]*>\s*Firmware\s*<\/th>/.test(thead),
    'Firmware <th> with data-priority="4" and data-sort-key="firmware" not found in thead'
  );
});

test('thead contains Client header with data-priority=4 and data-sort-key=client_version', () => {
  const thead = extractBlock(src, /<thead><tr>/, /<\/tr><\/thead>/);
  assert.ok(
    /<th[^>]*data-priority="4"[^>]*data-sort-key="client_version"[^>]*>\s*Client\s*<\/th>/.test(thead) ||
    /<th[^>]*data-sort-key="client_version"[^>]*data-priority="4"[^>]*>\s*Client\s*<\/th>/.test(thead),
    'Client <th> with data-priority="4" and data-sort-key="client_version" not found in thead'
  );
});

test('tbody row template contains a firmware <td> with data-value and class="mono"', () => {
  const tbodyStart = src.indexOf('<tbody>');
  assert.ok(tbodyStart > 0, '<tbody> not found in observers.js');
  const after = src.slice(tbodyStart);
  const trOpen = after.search(/`<tr\b/);
  const rowEnd = after.indexOf('</tr>', trOpen);
  const row = after.slice(trOpen, rowEnd);
  assert.ok(
    /<td[^>]*class="mono"[^>]*data-value="\$\{[^}]*o\.firmware[^}]*\}"|<td[^>]*data-value="\$\{[^}]*o\.firmware[^}]*\}"[^>]*class="mono"/.test(row),
    'firmware <td> with data-value reading o.firmware and class="mono" not found in row template'
  );
});

test('tbody row template contains a client_version <td> with data-value and class="mono"', () => {
  const tbodyStart = src.indexOf('<tbody>');
  const after = src.slice(tbodyStart);
  const trOpen = after.search(/`<tr\b/);
  const rowEnd = after.indexOf('</tr>', trOpen);
  const row = after.slice(trOpen, rowEnd);
  assert.ok(
    /<td[^>]*class="mono"[^>]*data-value="\$\{[^}]*o\.client_version[^}]*\}"|<td[^>]*data-value="\$\{[^}]*o\.client_version[^}]*\}"[^>]*class="mono"/.test(row),
    'client_version <td> with data-value reading o.client_version and class="mono" not found in row template'
  );
});

test('firmware display truncates "Build:" suffix and preserves full string in title=', () => {
  // Look for any helper or inline expression that strips Build:... when
  // rendering, AND a title= attribute carrying the unmodified firmware string.
  assert.ok(
    /Build:/.test(src),
    'firmware truncation marker "Build:" not referenced anywhere in observers.js'
  );
  assert.ok(
    /title="\$\{[^}]*escapeHtml\([^)]*o\.firmware[^)]*\)[^}]*\}"/.test(src) ||
    /title="\$\{[^}]*o\.firmware[^}]*\}"/.test(src),
    'firmware cell does not carry a title= with full o.firmware string'
  );
});

test('thead+tbody column counts stay balanced after adding new columns', () => {
  const thead = extractBlock(src, /<thead><tr>/, /<\/tr><\/thead>/);
  const thCount = (thead.match(/<th\b/g) || []).length;
  const tbodyStart = src.indexOf('<tbody>');
  const after = src.slice(tbodyStart);
  const trOpen = after.search(/`<tr\b/);
  const rowEnd = after.indexOf('</tr>', trOpen);
  const row = after.slice(trOpen, rowEnd);
  const tdCount = (row.match(/<td\b/g) || []).length;
  assert.strictEqual(tdCount, thCount,
    `Observer table column mismatch: ${thCount} <th> vs ${tdCount} <td>`);
});

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed === 0 ? 0 : 1);

/**
 * Issue #1473 — MeshCore reserves prefixes whose first byte is 0x00 or 0xFF.
 * Firmware citation (examples/simple_repeater/main.cpp:64):
 *   while (... && (pub_key[0] == 0x00 || pub_key[0] == 0xFF)) { ... }
 *
 * This test pins:
 *   - isReservedPrefix() semantics (case-insensitive, multi-byte aware)
 *   - markReservedCells() applies .prefix-reserved + disables click on matrix
 *
 * Reporter: @halo779 (community).
 */
'use strict';

const fs = require('fs');
const path = require('path');
const vm = require('vm');
const assert = require('assert');

// Load prefix-reserved.js as a CommonJS module under vm.
const src = fs.readFileSync(path.join(__dirname, 'public', 'prefix-reserved.js'), 'utf8');
const sandbox = { module: { exports: {} }, exports: {}, window: {} };
vm.createContext(sandbox);
vm.runInContext(src, sandbox);
const PR = (sandbox.module.exports && sandbox.module.exports.isReservedPrefix)
  ? sandbox.module.exports
  : sandbox.window.PrefixReserved;

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log('  \u2713 ' + name); }
  catch (e) { failed++; console.error('  \u2717 ' + name + ': ' + e.message); }
}

console.log('\n=== #1473: isReservedPrefix() ===');
test('"00" is reserved',           () => assert.strictEqual(PR.isReservedPrefix('00'), true));
test('"FF" is reserved',           () => assert.strictEqual(PR.isReservedPrefix('FF'), true));
test('lowercase "ff" is reserved', () => assert.strictEqual(PR.isReservedPrefix('ff'), true));
test('lowercase "00" is reserved', () => assert.strictEqual(PR.isReservedPrefix('00'), true));
test('"01" is NOT reserved',       () => assert.strictEqual(PR.isReservedPrefix('01'), false));
test('"FE" is NOT reserved',       () => assert.strictEqual(PR.isReservedPrefix('FE'), false));
test('"A3" is NOT reserved',       () => assert.strictEqual(PR.isReservedPrefix('A3'), false));
test('2-byte "0042" reserved (first byte = 00)', () => assert.strictEqual(PR.isReservedPrefix('0042'), true));
test('2-byte "FF01" reserved (first byte = FF)', () => assert.strictEqual(PR.isReservedPrefix('FF01'), true));
test('2-byte "01FF" NOT reserved (first byte = 01)', () => assert.strictEqual(PR.isReservedPrefix('01FF'), false));
test('3-byte "001234" reserved', () => assert.strictEqual(PR.isReservedPrefix('001234'), true));
test('3-byte "FFAABB" reserved', () => assert.strictEqual(PR.isReservedPrefix('FFAABB'), true));
test('empty string NOT reserved', () => assert.strictEqual(PR.isReservedPrefix(''), false));

console.log('\n=== #1473: markReservedCells() on a mock 1-byte matrix ===');
function mkCell(hex) {
  const classes = new Set(['hash-cell']);
  const attrs = { 'data-hex': hex };
  return {
    getAttribute: k => attrs[k],
    setAttribute: (k, v) => { attrs[k] = String(v); },
    classList: {
      add:      c => classes.add(c),
      remove:   c => classes.delete(c),
      contains: c => classes.has(c),
    },
    _attrs: attrs,
    _classes: classes,
  };
}

const cells = [];
for (let i = 0; i < 256; i++) {
  const hex = i.toString(16).toUpperCase().padStart(2, '0');
  cells.push(mkCell(hex));
}
cells[0].classList.add('hash-active');
cells[255].classList.add('hash-active');
cells[10].classList.add('hash-active');

const root = { querySelectorAll: () => cells };
const marked = PR.markReservedCells(root);

test('markReservedCells returned 2 (00 and FF)', () => assert.strictEqual(marked, 2));
test('cell 00 has .prefix-reserved',  () => assert.strictEqual(cells[0]._classes.has('prefix-reserved'),   true));
test('cell FF has .prefix-reserved',  () => assert.strictEqual(cells[255]._classes.has('prefix-reserved'), true));
test('cell 01 does NOT have .prefix-reserved', () => assert.strictEqual(cells[1]._classes.has('prefix-reserved'), false));
test('cell A3 does NOT have .prefix-reserved', () => assert.strictEqual(cells[0xA3]._classes.has('prefix-reserved'), false));
test('cell FE does NOT have .prefix-reserved', () => assert.strictEqual(cells[0xFE]._classes.has('prefix-reserved'), false));
test('cell 00 had .hash-active removed (no click handler attached)', () => assert.strictEqual(cells[0]._classes.has('hash-active'),   false));
test('cell FF had .hash-active removed',                              () => assert.strictEqual(cells[255]._classes.has('hash-active'), false));
test('cell 10 still has .hash-active (untouched)',                    () => assert.strictEqual(cells[10]._classes.has('hash-active'),  true));
test('cell 00 has aria-disabled=true',                                () => assert.strictEqual(cells[0]._attrs['aria-disabled'],   'true'));
test('cell FF has aria-disabled=true',                                () => assert.strictEqual(cells[255]._attrs['aria-disabled'], 'true'));
test('cell 00 title cites MeshCore protocol', () => assert.ok(/reserved by the MeshCore protocol/i.test(cells[0]._attrs.title)));
test('cell FF title cites MeshCore protocol', () => assert.ok(/reserved by the MeshCore protocol/i.test(cells[255]._attrs.title)));

console.log('\n=== #1473: prefix-reserved.js loaded by index.html ===');
const indexHtml = fs.readFileSync(path.join(__dirname, 'public', 'index.html'), 'utf8');
test('index.html includes prefix-reserved.js script', () => assert.ok(/prefix-reserved\.js/.test(indexHtml)));
const styleCss = fs.readFileSync(path.join(__dirname, 'public', 'style.css'), 'utf8');
test('style.css defines .prefix-reserved', () => assert.ok(/\.prefix-reserved\b/.test(styleCss)));
test('style.css disables pointer events on reserved cell',
  () => assert.ok(/\.prefix-reserved[\s\S]{0,400}pointer-events:\s*none/i.test(styleCss)));

console.log('\n' + passed + ' passed, ' + failed + ' failed');
if (failed) process.exit(1);

/* test-live-multibyte-filter.js
 * feat(live): packet-level multibyte classifier used by the "Multibyte only"
 * live filter. Pure function — no DOM. Mirrors app.js hash-size math:
 *   hashSize = (pathByte >> 6) + 1, pathByte at offset 5 (transport) else 1.
 */
'use strict';
const fs = require('fs');
const path = require('path');
const vm = require('vm');
const assert = require('assert');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log('  ✅ ' + name); }
  catch (e) { failed++; console.log('  ❌ ' + name + ': ' + e.message); }
}

// Load hop-filter.js into a sandbox exposing module.exports.
function load() {
  const ctx = {
    window: { addEventListener() {}, dispatchEvent() {} },
    localStorage: { getItem: () => null, setItem() {}, removeItem() {} },
    console, Math, Number, String, JSON, parseInt, isNaN,
    module: { exports: {} }, exports: {},
  };
  ctx.window.localStorage = ctx.localStorage;
  vm.createContext(ctx);
  const src = fs.readFileSync(path.join(__dirname, 'public/hop-filter.js'), 'utf8');
  vm.runInContext(src, ctx, { filename: 'hop-filter.js' });
  return ctx.module.exports.packetHashSize;
}

const packetHashSize = load();

console.log('\n=== packetHashSize (multibyte classifier) ===');

test('non-transport single-byte: byte1 top bits 00 -> size 1', () => {
  // byte0=0x10 (header), byte1=0x00 (hashSize-1=0, hashCount=0)
  assert.strictEqual(packetHashSize('1000', 1), 1);
});

test('non-transport multibyte: byte1 0x40 -> size 2', () => {
  // byte1=0x40 = 0b01000000 -> (0x40>>6)+1 = 2
  assert.strictEqual(packetHashSize('1040', 1), 2);
});

test('non-transport 3-byte: byte1 0x80 -> size 3', () => {
  // byte1=0x80 = 0b10000000 -> (0x80>>6)+1 = 3
  assert.strictEqual(packetHashSize('1080', 1), 3);
});

test('transport route uses offset 5 (route_type 0)', () => {
  // bytes: 00 | 4 transport-code bytes | path-len byte 0x40 | ...
  //         0    1  2  3  4               5
  assert.strictEqual(packetHashSize('00aabbccdd40', 0), 2);
});

test('transport route_type 3 also uses offset 5', () => {
  assert.strictEqual(packetHashSize('03aabbccdd00', 3), 1);
});

test('missing raw_hex -> 0 (unresolvable)', () => {
  assert.strictEqual(packetHashSize('', 1), 0);
  assert.strictEqual(packetHashSize(null, 1), 0);
  assert.strictEqual(packetHashSize(undefined, 1), 0);
});

test('too short for offset -> 0', () => {
  // non-transport needs >= 2 bytes; only 1 byte present
  assert.strictEqual(packetHashSize('10', 1), 0);
  // transport needs >= 6 bytes; only 5 present
  assert.strictEqual(packetHashSize('00aabbccdd', 0), 0);
});

test('garbage hex byte -> 0', () => {
  assert.strictEqual(packetHashSize('10zz', 1), 0);
});

test('whitespace in raw_hex is tolerated', () => {
  assert.strictEqual(packetHashSize('10 40', 1), 2);
});

console.log('\n--- ' + passed + ' passed, ' + failed + ' failed ---\n');
process.exit(failed > 0 ? 1 : 0);

/**
 * Issue #1473 — Prefix generator (#/analytics?tab=prefix-tool) must NOT
 * suggest any prefix whose FIRST byte is 0x00 or 0xFF, since the MeshCore
 * firmware keygen routine re-rolls such identities by convention (HEAD
 * 8ede7641, examples/simple_repeater/main.cpp:83).
 *
 * Reporter: @halo779 (community).
 */
'use strict';

const fs = require('fs');
const path = require('path');
const vm = require('vm');
const assert = require('assert');

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

console.log('\n=== #1473: filterReserved() drops 00 and FF from 1-byte space ===');
const all1byte = [];
for (let i = 0; i < 256; i++) all1byte.push(i.toString(16).toUpperCase().padStart(2, '0'));
const filtered = PR.filterReserved(all1byte);

test('filtered length is 254 (256 - 2 reserved)', () => assert.strictEqual(filtered.length, 254));
test('"00" not in filtered output', () => assert.strictEqual(filtered.indexOf('00'), -1));
test('"FF" not in filtered output', () => assert.strictEqual(filtered.indexOf('FF'), -1));
test('"01" still present', () => assert.ok(filtered.indexOf('01') >= 0));
test('"FE" still present', () => assert.ok(filtered.indexOf('FE') >= 0));
test('"A3" still present', () => assert.ok(filtered.indexOf('A3') >= 0));

console.log('\n=== #1473: reservedCount() per byte length ===');
test('1-byte reserved count = 2',      () => assert.strictEqual(PR.reservedCount(1), 2));
test('2-byte reserved count = 512',    () => assert.strictEqual(PR.reservedCount(2), 512));
test('3-byte reserved count = 131072', () => assert.strictEqual(PR.reservedCount(3), 131072));

console.log('\n=== #1473: simulated generator never returns a reserved prefix ===');
// Mirrors the production generator loop: random sampling + reserved filter.
// We pre-seed the RNG to return the reserved hex values up front; the loop
// MUST iterate past them and end on a non-reserved value.
function generateOne(bytes, usedSet, rng) {
  const totalSpace = Math.pow(256, bytes);
  const hexLen = bytes * 2;
  let attempts = 0, prefix;
  do {
    prefix = Math.floor(rng() * totalSpace).toString(16).toUpperCase().padStart(hexLen, '0');
  } while ((usedSet.has(prefix) || PR.isReservedPrefix(prefix)) && ++attempts < 2000);
  return prefix;
}

// Sequence that biases towards reserved first, then a valid prefix.
const seq = [0, 255, 0, 255, 0, 0x42, 0x77, 0x10];
let rngIdx = 0;
const biasedRng = () => seq[rngIdx++ % seq.length] / 256;
for (let k = 0; k < 20; k++) {
  const out = generateOne(1, new Set(), biasedRng);
  test('1-byte gen run #' + k + ' (' + out + ') is not reserved',
    () => assert.strictEqual(PR.isReservedPrefix(out), false));
}

// Enumeration-style generator (fallback path in production) — first available
// non-reserved.
function enumerateFirstFree(bytes, usedSet) {
  const totalSpace = Math.pow(256, bytes);
  const hexLen = bytes * 2;
  for (let i = 0; i < totalSpace; i++) {
    const p = i.toString(16).toUpperCase().padStart(hexLen, '0');
    if (!usedSet.has(p) && !PR.isReservedPrefix(p)) return p;
  }
  return null;
}
test('enumerate-first-free (1-byte, empty used) returns "01" (skips 00)',
  () => assert.strictEqual(enumerateFirstFree(1, new Set()), '01'));

// Used = everything except 00, FE, FF → must return FE, NOT 00 or FF.
const usedAllBut3 = new Set();
for (let i = 0; i < 256; i++) {
  if (i !== 0x00 && i !== 0xFE && i !== 0xFF) {
    usedAllBut3.add(i.toString(16).toUpperCase().padStart(2, '0'));
  }
}
test('with only {00, FE, FF} free, generator returns "FE" (NOT 00 or FF)',
  () => assert.strictEqual(enumerateFirstFree(1, usedAllBut3), 'FE'));

console.log('\n=== #1473: analytics.js wires reserved filter into the generator ===');
const analyticsSrc = fs.readFileSync(path.join(__dirname, 'public', 'analytics.js'), 'utf8');
test('analytics.js references PrefixReserved (generator wiring)',
  () => assert.ok(/PrefixReserved/.test(analyticsSrc)));
test('analytics.js mentions the reserved-excluded note in the generator card',
  () => assert.ok(/0x00 and 0xFF[\s\S]{0,200}excluded/i.test(analyticsSrc)));
test('analytics.js loads prefix-reserved before analytics in index.html',
  () => {
    const html = fs.readFileSync(path.join(__dirname, 'public', 'index.html'), 'utf8');
    const pri = html.indexOf('prefix-reserved.js');
    const ani = html.indexOf('analytics.js');
    assert.ok(pri > 0 && ani > 0 && pri < ani, 'prefix-reserved.js must load before analytics.js');
  });

console.log('\n' + passed + ' passed, ' + failed + ' failed');
if (failed) process.exit(1);

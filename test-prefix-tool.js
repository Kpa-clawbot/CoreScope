/* Unit tests for prefix tool logic (analytics.js _prefixToolExports) */
'use strict';
const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log(`  ✅ ${name}`); }
  catch (e) { failed++; console.log(`  ❌ ${name}: ${e.message}`); }
}

// Load analytics.js in a VM sandbox with minimal stubs
const code = fs.readFileSync(__dirname + '/public/analytics.js', 'utf8');
const sandbox = {
  window: {},
  document: { addEventListener() {} },
  location: { hash: '' },
  setTimeout: () => {},
  requestAnimationFrame: () => {},
  console,
  Map, Set, Array, Object, Number, Math, Date, JSON,
  encodeURIComponent,
  URLSearchParams,
  parseInt, parseFloat, isNaN, isFinite,
  RegExp, Error, TypeError, RangeError,
  Promise: { resolve: () => ({ then: () => ({}) }) },
};
sandbox.window = sandbox;
sandbox.self = sandbox;

try {
  vm.runInNewContext(code, sandbox, { filename: 'analytics.js', timeout: 5000 });
} catch (e) {
  // IIFE may throw due to missing DOM — that's fine, we just need the exports
}

const ex = sandbox.window._prefixToolExports;
if (!ex) {
  console.log('❌ _prefixToolExports not found on window');
  process.exit(1);
}

const { buildPrefixIndex, computePrefixStats, recommendPrefixSize,
        validatePrefixInput, checkPrefix, generatePrefix,
        renderSeverityBadge, PREFIX_SPACE_SIZES } = ex;

console.log('\n--- buildPrefixIndex ---');

test('builds 3-tier index from nodes', () => {
  const nodes = [
    { public_key: 'A1B2C3D4E5F6' },
    { public_key: 'A1B2FFFFFF00' },
    { public_key: 'FF00112233AA' },
  ];
  const idx = buildPrefixIndex(nodes);
  assert.strictEqual(idx[1].size, 2); // A1, FF
  assert.strictEqual(idx[2].size, 2); // A1B2, FF00
  assert.strictEqual(idx[3].size, 3); // A1B2C3, A1B2FF, FF0011
  assert.strictEqual(idx[1].get('A1').length, 2);
  assert.strictEqual(idx[2].get('A1B2').length, 2);
  assert.strictEqual(idx[1].get('FF').length, 1);
});

test('handles empty node list', () => {
  const idx = buildPrefixIndex([]);
  assert.strictEqual(idx[1].size, 0);
  assert.strictEqual(idx[2].size, 0);
  assert.strictEqual(idx[3].size, 0);
});

console.log('\n--- computePrefixStats ---');

test('detects collisions', () => {
  const nodes = [
    { public_key: 'A1B2C3D4E5F6' },
    { public_key: 'A1B2FFFFFF00' },
  ];
  const idx = buildPrefixIndex(nodes);
  const stats = computePrefixStats(idx);
  assert.strictEqual(stats[1].collidingPrefixes, 1); // A1 collides
  assert.strictEqual(stats[2].collidingPrefixes, 1); // A1B2 collides
  assert.strictEqual(stats[3].collidingPrefixes, 0); // no 3-byte collision
});

test('no collisions when all unique', () => {
  const nodes = [
    { public_key: 'A1B2C3D4E5F6' },
    { public_key: 'B1B2C3D4E5F6' },
  ];
  const idx = buildPrefixIndex(nodes);
  const stats = computePrefixStats(idx);
  assert.strictEqual(stats[1].collidingPrefixes, 0);
});

console.log('\n--- recommendPrefixSize ---');

test('recommends 1-byte for small networks (<20)', () => {
  const r = recommendPrefixSize(5);
  assert.strictEqual(r.rec, '1-byte');
});

test('recommends 2-byte for medium networks (20-499)', () => {
  const r = recommendPrefixSize(100);
  assert.strictEqual(r.rec, '2-byte');
});

test('recommends 3-byte for large networks (>=500)', () => {
  const r = recommendPrefixSize(500);
  assert.strictEqual(r.rec, '3-byte');
});

test('recommends 3-byte for very large networks', () => {
  const r = recommendPrefixSize(5000);
  assert.strictEqual(r.rec, '3-byte');
});

test('boundary: 19 nodes = 1-byte', () => {
  assert.strictEqual(recommendPrefixSize(19).rec, '1-byte');
});

test('boundary: 20 nodes = 2-byte', () => {
  assert.strictEqual(recommendPrefixSize(20).rec, '2-byte');
});

test('boundary: 499 nodes = 2-byte', () => {
  assert.strictEqual(recommendPrefixSize(499).rec, '2-byte');
});

console.log('\n--- validatePrefixInput ---');

test('empty input', () => {
  const r = validatePrefixInput('');
  assert.strictEqual(r.valid, false);
  assert.strictEqual(r.isEmpty, true);
});

test('valid 1-byte prefix', () => {
  const r = validatePrefixInput('A1');
  assert.strictEqual(r.valid, true);
  assert.strictEqual(r.tiers.length, 1);
  assert.strictEqual(r.tiers[0].b, 1);
  assert.strictEqual(r.tiers[0].prefix, 'A1');
});

test('valid 2-byte prefix', () => {
  const r = validatePrefixInput('a1b2');
  assert.strictEqual(r.valid, true);
  assert.strictEqual(r.tiers[0].prefix, 'A1B2');
  assert.strictEqual(r.isFullKey, false);
});

test('valid 3-byte prefix', () => {
  const r = validatePrefixInput('A1B2C3');
  assert.strictEqual(r.valid, true);
  assert.strictEqual(r.tiers[0].b, 3);
});

test('full public key (64 chars) derives 3 tiers', () => {
  const pk = 'A1B2C3D4' + '0'.repeat(56);
  const r = validatePrefixInput(pk);
  assert.strictEqual(r.valid, true);
  assert.strictEqual(r.isFullKey, true);
  assert.strictEqual(r.tiers.length, 3);
  assert.strictEqual(r.tiers[0].prefix, 'A1');
  assert.strictEqual(r.tiers[1].prefix, 'A1B2');
  assert.strictEqual(r.tiers[2].prefix, 'A1B2C3');
});

test('rejects non-hex', () => {
  const r = validatePrefixInput('ZZZZ');
  assert.strictEqual(r.valid, false);
  assert(r.error.includes('hex'));
});

test('rejects odd-length input', () => {
  const r = validatePrefixInput('A1B');
  assert.strictEqual(r.valid, false);
  assert(r.error.includes('2, 4, or 6'));
});

console.log('\n--- checkPrefix ---');

test('detects collision on 1-byte', () => {
  const nodes = [{ public_key: 'A1B2C3D4E5F6' }, { public_key: 'A1FFFFFF0000' }];
  const idx = buildPrefixIndex(nodes);
  const r = checkPrefix('A1', idx, nodes);
  assert.strictEqual(r.valid, true);
  assert.strictEqual(r.results[0].count, 2);
});

test('no collision for unused prefix', () => {
  const nodes = [{ public_key: 'A1B2C3D4E5F6' }];
  const idx = buildPrefixIndex(nodes);
  const r = checkPrefix('FF', idx, nodes);
  assert.strictEqual(r.results[0].count, 0);
});

test('full key excludes self from colliders', () => {
  const pk = 'A1B2C3D4E5F60000';
  const nodes = [{ public_key: pk }, { public_key: 'A1B2FFFFFF000000' }];
  const idx = buildPrefixIndex(nodes);
  const r = checkPrefix(pk, idx, nodes);
  assert.strictEqual(r.isFullKey, true);
  // 1-byte tier: A1 has both nodes, but self excluded = 1 collider
  assert.strictEqual(r.results[0].count, 1);
});

console.log('\n--- generatePrefix ---');

test('generates a collision-free 1-byte prefix', () => {
  const nodes = [];
  // Fill all but one 1-byte prefix
  for (let i = 0; i < 255; i++) {
    nodes.push({ public_key: i.toString(16).toUpperCase().padStart(2, '0') + '0000000000' });
  }
  const idx = buildPrefixIndex(nodes);
  const prefix = generatePrefix(1, idx, () => 0.5);
  assert.strictEqual(prefix, 'FF'); // only FF is free
  assert(!idx[1].has(prefix));
});

test('returns null when no prefix available', () => {
  const nodes = [];
  for (let i = 0; i < 256; i++) {
    nodes.push({ public_key: i.toString(16).toUpperCase().padStart(2, '0') + '0000000000' });
  }
  const idx = buildPrefixIndex(nodes);
  const prefix = generatePrefix(1, idx);
  assert.strictEqual(prefix, null);
});

test('generates 2-byte prefix not in index', () => {
  const nodes = [{ public_key: 'A1B2C3D4E5F6' }];
  const idx = buildPrefixIndex(nodes);
  const prefix = generatePrefix(2, idx, () => 0.5);
  assert.strictEqual(typeof prefix, 'string');
  assert.strictEqual(prefix.length, 4);
  assert(!idx[2].has(prefix));
});

test('uses deterministic random function', () => {
  const nodes = [{ public_key: 'A1B2C3D4E5F6' }];
  const idx = buildPrefixIndex(nodes);
  const p1 = generatePrefix(2, idx, () => 0.1);
  const p2 = generatePrefix(2, idx, () => 0.1);
  assert.strictEqual(p1, p2);
});

console.log('\n--- renderSeverityBadge ---');

test('unique badge for 0', () => {
  assert(renderSeverityBadge(0).includes('Unique'));
});

test('warning badge for 1-2', () => {
  assert(renderSeverityBadge(1).includes('1 collision'));
  assert(renderSeverityBadge(2).includes('2 collisions'));
});

test('red badge for 3+', () => {
  assert(renderSeverityBadge(5).includes('5 collisions'));
  assert(renderSeverityBadge(5).includes('status-red'));
});

// --- Summary ---
console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed > 0 ? 1 : 0);

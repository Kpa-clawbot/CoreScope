/* Unit tests for compare.js flood/direct packet filter — #928 */
'use strict';
const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log(`  ✅ ${name}`); }
  catch (e) { failed++; console.log(`  ❌ ${name}: ${e.message}`); }
}

// Build minimal sandbox and load compare.js
function makeSandbox() {
  const ctx = {
    window: { addEventListener: () => {}, dispatchEvent: () => {} },
    document: {
      readyState: 'complete',
      createElement: () => ({ id: '', textContent: '', innerHTML: '', addEventListener: () => {} }),
      head: { appendChild: () => {} },
      getElementById: () => null,
      querySelectorAll: () => [],
      addEventListener: () => {},
    },
    console,
    setTimeout, clearTimeout,
    location: { hash: '#/compare', href: '' },
    history: { replaceState: () => {} },
    URLSearchParams,
    Map, Set, Date, Promise,
    escapeHtml: (s) => s,
    api: () => Promise.resolve({ observers: [] }),
    CLIENT_TTL: { observers: 0 },
    registerPage: () => {},
    timeAgo: () => '',
    payloadTypeColor: () => '',
  };
  ctx.window.comparePacketSets = undefined;
  ctx.self = ctx.window;
  return ctx;
}

const ctx = makeSandbox();
const sandbox = vm.createContext(ctx);
const compareSrc = fs.readFileSync(__dirname + '/public/compare.js', 'utf8');
vm.runInContext(compareSrc, sandbox);

// --- Tests ---

console.log('\ncompare.js flood/direct filter tests:');

test('filterPacketsByRoute is exposed on window', () => {
  assert.strictEqual(typeof sandbox.window.filterPacketsByRoute, 'function',
    'filterPacketsByRoute should be exposed on window');
});

const packets = [
  { hash: 'a1', route_type: 0 }, // TransportFlood
  { hash: 'a2', route_type: 1 }, // Flood
  { hash: 'a3', route_type: 2 }, // Direct
  { hash: 'a4', route_type: 3 }, // TransportDirect
  { hash: 'a5', route_type: null }, // unknown
];

test('mode "all" returns all packets', () => {
  const result = sandbox.window.filterPacketsByRoute(packets, 'all');
  assert.strictEqual(result.length, 5);
});

test('mode "flood" returns only route_type 0 and 1', () => {
  const result = sandbox.window.filterPacketsByRoute(packets, 'flood');
  assert.strictEqual(result.length, 2);
  assert.deepStrictEqual(result.map(p => p.hash), ['a1', 'a2']);
});

test('mode "direct" returns only route_type 2 and 3', () => {
  const result = sandbox.window.filterPacketsByRoute(packets, 'direct');
  assert.strictEqual(result.length, 2);
  assert.deepStrictEqual(result.map(p => p.hash), ['a3', 'a4']);
});

test('mode "flood" excludes null route_type', () => {
  const result = sandbox.window.filterPacketsByRoute(packets, 'flood');
  assert.ok(!result.some(p => p.route_type === null));
});

test('empty array returns empty', () => {
  const result = sandbox.window.filterPacketsByRoute([], 'flood');
  assert.strictEqual(result.length, 0);
});

// --- Summary ---
console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed > 0 ? 1 : 0);

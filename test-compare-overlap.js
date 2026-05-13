/* Unit tests for compare.js asymmetric overlap stats — Fixes #671 */
'use strict';
const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log(`  ✅ ${name}`); }
  catch (e) { failed++; console.log(`  ❌ ${name}: ${e.message}`); }
}

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
  ctx.self = ctx.window;
  return ctx;
}

const ctx = makeSandbox();
const sandbox = vm.createContext(ctx);
const compareSrc = fs.readFileSync(__dirname + '/public/compare.js', 'utf8');
vm.runInContext(compareSrc, sandbox);

console.log('\ncompare.js asymmetric overlap stats (#671):');

test('computeOverlapStats is exposed on window', () => {
  assert.strictEqual(typeof sandbox.window.computeOverlapStats, 'function',
    'computeOverlapStats should be exposed on window');
});

test('basic asymmetric overlap — A sees 8/10 of B\'s, B sees 8/12 of A\'s', () => {
  // A: 12 unique packets total (10 shared with B + 2 unique)
  // B: 10 unique packets total (10 shared with A... wait: 8 shared + 2 unique to B)
  // Let's do: A has packets 1..10 + extras 11,12; B has packets 1..8 + extras 13,14
  // shared = {1..8} = 8
  // onlyA = {9,10,11,12} = 4
  // onlyB = {13,14} = 2
  // A total = 12, B total = 10
  // A sees 8/10 = 80% of B's packets
  // B sees 8/12 = 66.7% of A's packets
  const setA = new Set(['1','2','3','4','5','6','7','8','9','10','11','12']);
  const setB = new Set(['1','2','3','4','5','6','7','8','13','14']);
  const cmp = sandbox.window.comparePacketSets(setA, setB);
  const stats = sandbox.window.computeOverlapStats(cmp);
  assert.strictEqual(stats.totalA, 12, 'totalA');
  assert.strictEqual(stats.totalB, 10, 'totalB');
  assert.strictEqual(stats.shared, 8, 'shared');
  assert.strictEqual(stats.onlyA, 4, 'onlyA');
  assert.strictEqual(stats.onlyB, 2, 'onlyB');
  assert.strictEqual(stats.aSeesOfB, 80.0, 'A sees 80% of B\'s');
  assert.strictEqual(stats.bSeesOfA, Math.round(8/12*1000)/10, 'B sees 66.7% of A\'s');
});

test('zero packets — no division by zero', () => {
  const cmp = sandbox.window.comparePacketSets(new Set(), new Set());
  const stats = sandbox.window.computeOverlapStats(cmp);
  assert.strictEqual(stats.aSeesOfB, 0);
  assert.strictEqual(stats.bSeesOfA, 0);
  assert.strictEqual(stats.shared, 0);
});

test('one observer empty — other gets 0% mutual coverage', () => {
  const cmp = sandbox.window.comparePacketSets(new Set(['x','y']), new Set());
  const stats = sandbox.window.computeOverlapStats(cmp);
  assert.strictEqual(stats.totalA, 2);
  assert.strictEqual(stats.totalB, 0);
  assert.strictEqual(stats.aSeesOfB, 0, 'no B packets to see');
  assert.strictEqual(stats.bSeesOfA, 0, 'B saw 0 of A\'s');
});

test('perfect overlap — 100% both ways', () => {
  const s = new Set(['a','b','c']);
  const cmp = sandbox.window.comparePacketSets(s, new Set(s));
  const stats = sandbox.window.computeOverlapStats(cmp);
  assert.strictEqual(stats.aSeesOfB, 100);
  assert.strictEqual(stats.bSeesOfA, 100);
  assert.strictEqual(stats.shared, 3);
});

test('disjoint observers — 0% both ways', () => {
  const cmp = sandbox.window.comparePacketSets(new Set(['a','b']), new Set(['c','d']));
  const stats = sandbox.window.computeOverlapStats(cmp);
  assert.strictEqual(stats.aSeesOfB, 0);
  assert.strictEqual(stats.bSeesOfA, 0);
  assert.strictEqual(stats.shared, 0);
});

console.log(`\n  ${passed} passed, ${failed} failed`);
process.exit(failed > 0 ? 1 : 0);

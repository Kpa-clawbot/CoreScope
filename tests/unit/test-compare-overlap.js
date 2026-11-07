/* Unit tests for compare.js asymmetric overlap stats — Fixes #671 */
'use strict';
const REPO_ROOT = require('path').resolve(__dirname, '..', '..');
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
const compareSrc = fs.readFileSync(REPO_ROOT + '/public/compare.js', 'utf8');
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

async function testReceptionRequests() {
  const elements = new Map();
  function element(id) {
    if (!elements.has(id)) elements.set(id, {
      innerHTML: '', value: '', addEventListener() {},
      classList: { toggle() {} }, querySelectorAll() { return []; },
    });
    return elements.get(id);
  }
  let hooks;
  const requests = [];
  // The top-level row represents the longest path, not necessarily the
  // selected observer: the shared reception may even display B's TX.
  const txOnlyA = { hash: 'a-tx-b-rx', direction: 'rx', observer_id: 'B', payload_type: 4, route_type: 1 };
  const sharedRx = { hash: 'shared-rx', direction: 'tx', observer_id: 'B', payload_type: 4, route_type: 1 };
  const context = makeSandbox();
  context.location.hash = '#/compare?a=A&b=B';
  context.document.getElementById = element;
  context.document.querySelector = () => null;
  context.registerPage = (name, pageHooks) => { hooks = pageHooks; };
  context.api = async (path) => {
    if (path === '/observers') return { observers: [{ id: 'A' }, { id: 'B' }] };
    const query = new URLSearchParams(path.split('?')[1]);
    requests.push(query);
    return { packets: query.get('observer') === 'A' && query.get('excludeTx') === 'true'
      ? [sharedRx] : [txOnlyA, sharedRx] };
  };
  vm.runInNewContext(compareSrc, context);
  hooks.init(element('app'));
  await new Promise(resolve => setImmediate(resolve));
  test('comparison requests reception membership in exactly two bulk calls (#667)', () => {
    assert.strictEqual(requests.length, 2);
    assert.deepStrictEqual(requests.map(q => q.get('observer')).sort(), ['A', 'B']);
    for (const query of requests) assert.strictEqual(query.get('excludeTx'), 'true');
    const summary = element('compareDetail').innerHTML.replace(/<[^>]*>/g, '');
    assert.match(summary, /A saw 1 unique packets and B saw 2 unique packets/);
    assert.match(summary, /1 \(50\.0%\) were seen by both observers/);
    assert.doesNotMatch(summary, /Perfect overlap/);
  });
}

testReceptionRequests().then(() => {
  console.log(`\n  ${passed} passed, ${failed} failed`);
  process.exit(failed > 0 ? 1 : 0);
}).catch(error => { console.error(error); process.exit(1); });

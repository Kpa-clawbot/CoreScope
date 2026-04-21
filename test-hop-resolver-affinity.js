/**
 * Unit tests for HopResolver affinity-aware hop resolution.
 */
'use strict';
const fs = require('fs');
const vm = require('vm');

// Load hop-resolver.js in a sandboxed context
const code = fs.readFileSync(__dirname + '/public/hop-resolver.js', 'utf8');
const sandbox = { window: {}, console, Math, Object, Array, Number, Date, Map, Set, parseInt, parseFloat, encodeURIComponent };
vm.createContext(sandbox);
vm.runInContext(code, sandbox);
const HopResolver = sandbox.window.HopResolver;

let passed = 0;
let failed = 0;

function assert(condition, msg) {
  if (condition) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}

// ── Test nodes ──
// Two nodes share the same 1-byte prefix "ab"
const nodeA = { public_key: 'ab1111', name: 'NodeA', lat: 37.0, lon: -122.0 };
const nodeB = { public_key: 'ab2222', name: 'NodeB', lat: 38.0, lon: -123.0 };
const nodeC = { public_key: 'cd3333', name: 'NodeC', lat: 37.5, lon: -122.5 };

console.log('\n=== HopResolver Affinity Tests ===\n');

// Test 1: Affinity prefers neighbor candidate over geo-closest
console.log('Test 1: Affinity prefers neighbor over geo-closest');
HopResolver.init([nodeA, nodeB, nodeC]);
HopResolver.setAffinity({
  edges: [
    { source: 'cd3333', target: 'ab2222', score: 0.8 }
    // NodeC is a neighbor of NodeB but NOT NodeA
  ]
});

// Resolve hop "ab" after NodeC was resolved — should pick NodeB (neighbor) not NodeA (geo-closer)
// Origin at NodeC's position so forward pass runs with NodeC as anchor
const result1 = HopResolver.resolve(['cd33', 'ab'], nodeC.lat, nodeC.lon, null, null, null);
assert(result1['ab'].name === 'NodeB', 'Should pick NodeB (affinity neighbor of NodeC) — got: ' + result1['ab'].name);

// Test 2: Without affinity, falls back to geo-closest
console.log('\nTest 2: Cold start (no affinity) falls back to geo-closest');
HopResolver.init([nodeA, nodeB, nodeC]);
HopResolver.setAffinity({}); // No edges

// With anchor at NodeC's position, NodeA is closer to NodeC than NodeB
const result2 = HopResolver.resolve(['cd33', 'ab'], nodeC.lat, nodeC.lon, null, null, null);
// NodeA (37, -122) is closer to NodeC (37.5, -122.5) than NodeB (38, -123)
assert(result2['ab'].name === 'NodeA', 'Should pick NodeA (geo-closest) — got: ' + result2['ab'].name);

// Test 3: setAffinity with null/undefined doesn't crash
console.log('\nTest 3: setAffinity with null/undefined is safe');
HopResolver.setAffinity(null);
HopResolver.setAffinity(undefined);
HopResolver.setAffinity({});
assert(true, 'No crash on null/undefined/empty affinity');

// Test 4: getAffinity returns correct scores
console.log('\nTest 4: getAffinity returns correct scores');
HopResolver.setAffinity({
  edges: [
    { source: 'aaa', target: 'bbb', score: 0.95 },
    { source: 'ccc', target: 'ddd', weight: 5 }
  ]
});
assert(HopResolver.getAffinity('aaa', 'bbb') === 0.95, 'aaa→bbb = 0.95');
assert(HopResolver.getAffinity('bbb', 'aaa') === 0.95, 'bbb→aaa = 0.95 (bidirectional)');
assert(HopResolver.getAffinity('ccc', 'ddd') === 5, 'ccc→ddd = 5 (weight fallback)');
assert(HopResolver.getAffinity('aaa', 'zzz') === 0, 'unknown pair = 0');
assert(HopResolver.getAffinity(null, 'bbb') === 0, 'null pubkey = 0');

// Test 5: Affinity with multiple neighbors — highest score wins
console.log('\nTest 5: Highest affinity score wins among neighbors');
HopResolver.init([nodeA, nodeB, nodeC]);
HopResolver.setAffinity({
  edges: [
    { source: 'cd3333', target: 'ab1111', score: 0.3 },
    { source: 'cd3333', target: 'ab2222', score: 0.9 }
  ]
});
const result5 = HopResolver.resolve(['cd33', 'ab'], nodeC.lat, nodeC.lon, null, null, null);
assert(result5['ab'].name === 'NodeB', 'Should pick NodeB (highest affinity 0.9) — got: ' + result5['ab'].name);

// Test 6: Unambiguous hops are not affected by affinity
console.log('\nTest 6: Unambiguous hops unaffected by affinity');
const nodeD = { public_key: 'ee4444', name: 'NodeD', lat: 36.0, lon: -121.0 };
HopResolver.init([nodeA, nodeB, nodeC, nodeD]);
HopResolver.setAffinity({ edges: [] });
const result6 = HopResolver.resolve(['ee44'], null, null, null, null, null);
assert(result6['ee44'].name === 'NodeD', 'Unique prefix resolves directly — got: ' + result6['ee44'].name);
assert(!result6['ee44'].ambiguous, 'Should not be marked ambiguous');

// Test 7: lat=0 / lon=0 candidates are NOT excluded (equator/prime-meridian bug fix)
console.log('\nTest 7: lat=0 / lon=0 candidates are included in geo scoring');
const nodeEquator = { public_key: 'ab5555', name: 'EquatorNode', lat: 0, lon: 10 };
const nodeFar = { public_key: 'ab6666', name: 'FarNode', lat: 60, lon: 60 };
const anchorNearEq = { public_key: 'cd7777', name: 'AnchorEq', lat: 1, lon: 11 };
HopResolver.init([nodeEquator, nodeFar, anchorNearEq]);
HopResolver.setAffinity({});
// Anchor near equator — EquatorNode (0,10) should be geo-closest
const result7 = HopResolver.resolve(['cd77', 'ab'], 1.0, 11.0, null, null, null);
assert(result7['ab'].name === 'EquatorNode',
  'lat=0 candidate should be included and win by geo — got: ' + result7['ab'].name);

// Test 8: lon=0 candidate is also included
console.log('\nTest 8: lon=0 candidate is included in geo scoring');
const nodePrime = { public_key: 'ab8888', name: 'PrimeMeridian', lat: 10, lon: 0 };
const anchorNearPM = { public_key: 'cd9999', name: 'AnchorPM', lat: 11, lon: 1 };
HopResolver.init([nodePrime, nodeFar, anchorNearPM]);
HopResolver.setAffinity({});
const result8 = HopResolver.resolve(['cd99', 'ab'], 11.0, 1.0, null, null, null);
assert(result8['ab'].name === 'PrimeMeridian',
  'lon=0 candidate should be included and win by geo — got: ' + result8['ab'].name);

console.log('\n' + (passed + failed) + ' tests, ' + passed + ' passed, ' + failed + ' failed\n');
process.exit(failed > 0 ? 1 : 0);

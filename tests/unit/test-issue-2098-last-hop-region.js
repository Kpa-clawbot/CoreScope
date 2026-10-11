/**
 * #2098 — the observer's region says where the LAST hop is, nothing more.
 *
 * Only the final hop of a path was heard by the observer directly, so only that
 * hop has to be within radio range of it. Every earlier hop can be anywhere the
 * mesh reaches. HopResolver used to apply the observer's IATA region to every
 * hop, which narrowed earlier hops on a false premise; it stayed invisible on
 * deployments whose codes were missing from /api/iata-coords.
 *
 * These tests pin: the region filter runs on the directly heard hop only, a
 * caller can name the directly heard hops when it resolves a set of hops
 * gathered from several packets, and packets.js keeps the two apart in its
 * cache.
 */
'use strict';
const REPO_ROOT = require('path').resolve(__dirname, '..', '..');
const fs = require('fs');
const vm = require('vm');

function load(file) {
  const sandbox = {
    window: {}, console, Math, Object, Array, Number, Date, Map, Set, JSON, String, Boolean,
    parseInt, parseFloat, isNaN,
  };
  vm.createContext(sandbox);
  vm.runInContext(fs.readFileSync(REPO_ROOT + '/public/' + file, 'utf8'), sandbox);
  return sandbox;
}

let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  PASS  ' + msg); }
  else { failed++; console.log('  FAIL  ' + msg); }
}

const HopResolver = load('hop-resolver.js').window.HopResolver;

// The observer sits at Ostend (OST). "ef" has one candidate nearby and one in
// Bavaria, about 620 km away. "ab" is the same shape.
const OBSERVER_ID = 'A943DC76B109';
const NODES = [
  { public_key: 'ef0069c0aa', name: 'EF-NEAR', role: 'repeater', lat: 51.08, lon: 3.78 },
  { public_key: 'efbf0eeacc', name: 'EF-FAR', role: 'repeater', lat: 48.35, lon: 11.79 },
  { public_key: 'ab11111111', name: 'AB-NEAR', role: 'repeater', lat: 51.15, lon: 3.70 },
  { public_key: 'ab22222222', name: 'AB-FAR', role: 'repeater', lat: 48.40, lon: 11.70 },
];
HopResolver.init(NODES, {
  observers: [{ id: OBSERVER_ID, iata: 'OST' }],
  iataCoords: { OST: { lat: 51.1998, lon: 2.874673 } },
});

const flagged = e => (e && e.conflicts || []).filter(c => c.regional).length;

console.log('\n=== #2098: a path resolves its last hop against the region, not the rest ===');
{
  // Path order: "ef" first, "ab" heard by the observer.
  const r = HopResolver.resolve(['ef', 'ab'], null, null, null, null, OBSERVER_ID);
  assert(flagged(r['ab']) === 1, 'the last hop is narrowed to the candidate in region (' + flagged(r['ab']) + ' flagged)');
  assert(flagged(r['ef']) === 0, 'an earlier hop gets no regional flag (' + flagged(r['ef']) + ' flagged)');
  assert((r['ef'].conflicts || []).length === 2, 'an earlier hop keeps every candidate, the distant one included');
  assert(r['ef'].conflicts.every(c => c.filterMethod === 'none' && c.distKm === undefined),
    'an earlier hop carries no distance to the observer region');
  assert(r['ef'].globalFallback === true, 'an earlier hop with an observer reports globalFallback, so its badge still counts every candidate');
}

console.log('\n=== #2098: a caller names the directly heard hops for a set from many packets ===');
{
  const r = HopResolver.resolve(['ef', 'ab'], null, null, null, null, OBSERVER_ID, { directHops: ['ef'] });
  assert(flagged(r['ef']) === 1, 'a hop named as direct is narrowed to the region');
  assert(flagged(r['ab']) === 0, 'a hop not named as direct is not, even in last position');
  const none = HopResolver.resolve(['ef', 'ab'], null, null, null, null, OBSERVER_ID, { directHops: [] });
  assert(flagged(none['ef']) === 0 && flagged(none['ab']) === 0, 'an empty directHops list filters no hop');
}

console.log('\n=== #2098: a single hop is its own last hop ===');
{
  const r = HopResolver.resolve(['ab'], null, null, null, null, OBSERVER_ID);
  assert(flagged(r['ab']) === 1 && r['ab'].name === 'AB-NEAR', 'a one-hop path is narrowed to the region');
}

console.log('\n=== #2098: packets.js keeps direct and relayed occurrences apart ===');
{
  const src = fs.readFileSync(REPO_ROOT + '/public/packets.js', 'utf8');
  assert(/function hopCacheKey\(h, observerId, direct\)/.test(src),
    'the hop cache key carries whether the hop was heard directly');
  assert(/directHops/.test(src), 'packets.js tells the resolver which hops were heard directly');
  assert(/iataCoords: coordData\.coords/.test(src), 'the region table is still passed to the resolver');
}

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed ? 1 : 0);

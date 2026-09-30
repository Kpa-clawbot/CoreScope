/**
 * #2097 — an ambiguous path hop was shown as a single certain name.
 *
 * Every node on the network shares its 1-byte path prefix with at least one
 * other: measured, 254 prefixes cover all 2043 nodes. HopResolver already marks
 * such a hop `ambiguous` with a populated `conflicts` list, and HopDisplay
 * already renders a warning badge for it. Neither was reaching the packets page,
 * because resolve() takes six parameters and packets.js passed one.
 *
 * Without the observer, `packetIata` is null, so nodeInRegion() never runs, no
 * candidate is flagged `regional`, `globalFallback` stays false, and
 * HopDisplay's badgeCount computes to 0. The whole chain is silent while the
 * data says the name is a guess.
 *
 * These tests pin the chain end to end: resolver output, then the markup
 * HopDisplay produces from it.
 */
'use strict';
const REPO_ROOT = require('path').resolve(__dirname, '..', '..');
const fs = require('fs');
const vm = require('vm');

function load(file, extra) {
  const sandbox = Object.assign({
    window: {}, document: { addEventListener() {}, getElementById: () => null, querySelector: () => null, createElement: () => ({ style: {}, classList: { add() {}, remove() {} }, appendChild() {}, setAttribute() {} }), body: { appendChild() {}, removeChild() {} } },
    console, Math, Object, Array, Number, Date, Map, Set, JSON, String, Boolean,
    parseInt, parseFloat, isNaN, encodeURIComponent, decodeURIComponent,
    setTimeout, clearTimeout,
  }, extra || {});
  vm.createContext(sandbox);
  vm.runInContext(fs.readFileSync(REPO_ROOT + '/public/' + file, 'utf8'), sandbox);
  return sandbox;
}

const rs = load('hop-resolver.js');
const HopResolver = rs.window.HopResolver;
const ds = load('hop-display.js');
const HopDisplay = ds.window.HopDisplay;

let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  PASS  ' + msg); }
  else { failed++; console.log('  FAIL  ' + msg); }
}

// Three nodes share the 1-byte prefix "ef", as nine real ones do today. Two sit
// near the observer, one is 130 km away.
const near1 = { public_key: 'ef0069c0aa', name: 'NEAR-ONE', role: 'repeater', lat: 51.08, lon: 3.78 };
const near2 = { public_key: 'ef86f6a5bb', name: 'NEAR-TWO', role: 'repeater', lat: 51.15, lon: 3.70 };
const far   = { public_key: 'efbf0eeacc', name: 'FAR-AWAY', role: 'repeater', lat: 50.87, lon: 5.52 };
const other = { public_key: 'e7aaaaaadd', name: 'NEXT-HOP', role: 'repeater', lat: 51.10, lon: 3.80 };
const NODES = [near1, near2, far, other];

// The observer that heard the packet, and its IATA centre.
const OBSERVER_ID = 'A943DC76B109';
const IATA = 'OST';

console.log('\n=== #2097: the resolver marks an ambiguous hop ===');

HopResolver.init(NODES, {
  observers: [{ id: OBSERVER_ID, iata: IATA }],
  iataCoords: { [IATA]: { lat: 51.23, lon: 2.92 } },
});

{
  const r = HopResolver.resolve(['ef']);
  const e = r['ef'];
  assert(e && e.ambiguous === true, 'three candidates for "ef" are reported as ambiguous');
  assert(e && Array.isArray(e.conflicts) && e.conflicts.length === 3, 'all three candidates are listed in conflicts');
}

console.log('\n=== #2097: HopDisplay renders a badge only when the count survives ===');

{
  // This is the regression itself: resolve() without the observer produces
  // conflicts that carry no regional flag, and HopDisplay then computes
  // badgeCount = 0 and renders nothing.
  const noCtx = HopResolver.resolve(['ef'])['ef'];
  const withCtx = HopResolver.resolve(['ef'], null, null, null, null, OBSERVER_ID)['ef'];

  const flaggedNo = (noCtx.conflicts || []).filter(c => c.regional).length;
  const flaggedYes = (withCtx.conflicts || []).filter(c => c.regional).length;
  assert(flaggedYes > flaggedNo,
    `observer context flags regional candidates (${flaggedNo} without, ${flaggedYes} with)`);

  const html = HopDisplay.renderHop('ef', withCtx, {});
  assert(/hop-conflict-btn/.test(html),
    'a hop with several regional candidates renders the conflict badge');
  assert(/ph-warning/.test(html), 'the badge carries a warning icon, not colour alone');
  assert(/data-conflict=/.test(html), 'the badge carries the candidate list for the popover');
}

{
  // A hop with exactly one candidate must stay clean: the badge has to mean
  // something, and 2-byte hops are unique 98.5% of the time.
  const one = HopResolver.resolve(['e7aa'], null, null, null, null, OBSERVER_ID)['e7aa'];
  const html = HopDisplay.renderHop('e7aa', one, {});
  assert(!/hop-conflict-btn/.test(html), 'an unambiguous hop renders no badge');
  assert(/NEXT-HOP/.test(html), 'an unambiguous hop still shows its name');
}

{
  // Geography must actually narrow the field: the node 130 km outside the
  // observer's region should not be the one presented as the answer.
  const withCtx = HopResolver.resolve(['ef'], null, null, null, null, OBSERVER_ID)['ef'];
  assert(withCtx.name !== 'FAR-AWAY',
    'the candidate outside the observer region is not the one displayed (got ' + withCtx.name + ')');
}


console.log('\n=== #2097: packets.js never resolves a hop without its observer ===');

{
  // Structural guard. The resolver and the display could always report the
  // ambiguity; the regression was one caller dropping five of six arguments,
  // and nothing failed when it did.
  const src = fs.readFileSync(REPO_ROOT + '/public/packets.js', 'utf8');

  const bare = src.match(/HopResolver\.resolve\(\s*[A-Za-z_$][\w$]*\s*\)/g) || [];
  assert(bare.length === 0,
    'no HopResolver.resolve() call passes the hops alone'
    + (bare.length ? ': ' + bare.join(', ') : ''));

  assert(/function hopCacheKey\(/.test(src),
    'the per-observer cache key is computed in one place');
  assert(/async function resolveHopsForPackets\(/.test(src),
    'multi-packet call sites group their hops by observer');

  // renderHop reads the per-observer key; something has to write it.
  assert(/hopNameCache\[hopCacheKey\(h, observerId\)\] = entry/.test(src),
    'resolveHops writes the per-observer cache key that renderHop reads');
}

console.log('\n=== #2097: a badge belongs to the pill on its left ===');

{
  // The badge is a sibling after the pill, so in "A [8] -> B [6]" a reader
  // cannot tell which name the 8 qualifies. Pill and badge must render as one
  // unbreakable group, with the arrow clearly outside it.
  const withCtx = HopResolver.resolve(['ef'], null, null, null, null, OBSERVER_ID)['ef'];
  const html = HopDisplay.renderHop('ef', withCtx, {});
  assert(/class="hop-group"/.test(html), 'pill and badge are wrapped in one group');
  const m = html.match(/<span class="hop-group">([\s\S]*)<\/span>$/);
  assert(m && /hop-link/.test(m[1]) && /hop-conflict-btn/.test(m[1]),
    'the group contains both the pill and its badge');
}

{
  // An unambiguous hop needs no wrapper: the group exists to tie a badge to its
  // pill, and wrapping every hop would change layout for nothing.
  const one = HopResolver.resolve(['e7aa'], null, null, null, null, OBSERVER_ID)['e7aa'];
  const html = HopDisplay.renderHop('e7aa', one, {});
  assert(!/hop-group/.test(html), 'a hop with no badge is not wrapped');
}

{
  // The packets list wants the names without a badge on every hop.
  const withCtx = HopResolver.resolve(['ef'], null, null, null, null, OBSERVER_ID)['ef'];
  const plain = HopDisplay.renderHop('ef', withCtx, { badge: false });
  assert(!/hop-conflict-btn/.test(plain), 'badge:false suppresses the badge');
  assert(/hop-ambiguous/.test(plain),
    'the hop keeps its ambiguous class, so CSS can still mark it');
  assert(/NEAR-|FAR-/.test(plain), 'the name is still rendered');
}

console.log('\n=== #2097: the list summarises, the detail pane does not ===');

{
  const src = fs.readFileSync(REPO_ROOT + '/public/packets.js', 'utf8');
  assert(/function renderPath\(hops, observerId, opts\)/.test(src),
    'renderPath takes an options object');
  assert(/summary: true/.test(src),
    'at least one call site asks for the summarised form');
  assert(/renderPath\(pathHops, effectivePkt\.observer_id\)/.test(src),
    'the detail pane calls renderPath without summary, so it keeps per-hop badges');
  assert(/hop-path-warn/.test(src),
    'the summarised form emits a single per-path indicator');
}

console.log('\n=== #2097: the observer position anchors the pick ===');

{
  // Measured on the live deployment: 0 of 42 observers have their IATA code in
  // /api/iata-coords, so nodeInRegion() always returns null and the 300 km
  // filter narrows nothing. 27 of 42 do carry their own lat/lon, and resolve()
  // already accepts them as the backward anchor for pickByAffinity.
  //
  // FAR-AWAY is listed first on purpose: with no anchor the resolver has no
  // context and keeps candidate order, which is the shape of the live bug.
  const far   = { public_key: 'efbf0eeacc', name: 'FAR-AWAY', role: 'repeater', lat: 50.87, lon: 5.52 };
  const near1 = { public_key: 'ef0069c0aa', name: 'NEAR-ONE', role: 'repeater', lat: 51.08, lon: 3.78 };
  const near2 = { public_key: 'ef86f6a5bb', name: 'NEAR-TWO', role: 'repeater', lat: 51.15, lon: 3.70 };
  HopResolver.init([far, near1, near2], {
    observers: [{ id: OBSERVER_ID, iata: IATA }],
    iataCoords: {},   // deliberately empty: this is the live state
  });

  const OBS_LAT = 51.21, OBS_LON = 3.44;   // the observer that heard it

  const without = HopResolver.resolve(['ef'], null, null, null, null, OBSERVER_ID)['ef'];
  const withPos = HopResolver.resolve(['ef'], null, null, OBS_LAT, OBS_LON, OBSERVER_ID)['ef'];

  assert(without.name === 'FAR-AWAY',
    'without an anchor the resolver keeps candidate order (got ' + without.name + ')');
  assert(withPos.name !== 'FAR-AWAY',
    'the observer position rules out the distant candidate (got ' + withPos.name + ')');
  assert(withPos.ambiguous === true,
    'it is still reported as ambiguous: a better pick is not a certain one');
  assert((withPos.conflicts || []).length === 3,
    'every candidate is still listed for the reader');
}

{
  const src = fs.readFileSync(REPO_ROOT + '/public/packets.js', 'utf8');
  assert(/HopResolver\.resolve\(unknown, null, null, obsLat, obsLon, observerId\)/.test(src),
    'resolveHops passes the observer position as the anchor');
  const withId = src.match(/HopResolver\.resolve\([^)]*observer_id[^)]*\)/g) || [];
  assert(withId.every(c => !/null, null, null, null/.test(c)),
    'no call passes an observer id but drops its position: ' + withId.join(' | '));
  assert(/function observerPosition\(/.test(src) && /observerPosition\(observerId\)/.test(src),
    'one helper looks the observer up, used by every resolve path');
}

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed ? 1 : 0);

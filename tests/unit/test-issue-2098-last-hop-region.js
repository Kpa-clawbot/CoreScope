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
// Bavaria, about 620 km away. "ab" is the same shape. Both "cd" candidates are
// in Bavaria.
const OBSERVER_ID = 'A943DC76B109';
const NODES = [
  { public_key: 'cd11111111', name: 'CD-FAR-1', role: 'repeater', lat: 48.30, lon: 11.60 },
  { public_key: 'cd22222222', name: 'CD-FAR-2', role: 'repeater', lat: 48.45, lon: 11.90 },
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
  assert(r['ef'].globalFallback !== true, 'an earlier hop is not a global fallback: no region check ran on it');
  assert(r['ef'].regionSkipped === true, 'an earlier hop says the region was skipped, so its badge still counts every candidate');
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

console.log('\n=== #2098: the last hop with no candidate in region is a global fallback ===');
{
  const r = HopResolver.resolve(['ab', 'cd'], null, null, null, null, OBSERVER_ID);
  assert(r['cd'].globalFallback === true, 'a directly heard hop with no candidate in region is a global fallback');
  assert(r['cd'].regionSkipped !== true, 'a directly heard hop was checked against the region');
}

// HopDisplay, real file: what a reader sees for each kind of hop.
function loadDisplay() {
  const appended = [];
  const sandbox = {
    window: { scrollX: 0, scrollY: 0, innerWidth: 1200 }, console, Math, Object, Array, Number, JSON, String,
    document: {
      querySelector: () => null, addEventListener: () => {},
      createElement: () => ({ style: {}, innerHTML: '' }),
      body: { appendChild: el => appended.push(el) },
    },
  };
  vm.createContext(sandbox);
  vm.runInContext(fs.readFileSync(REPO_ROOT + '/public/hop-display.js', 'utf8'), sandbox);
  return { HopDisplay: sandbox.window.HopDisplay, appended };
}

console.log('\n=== #2098: HopDisplay styles only a checked hop as a global fallback ===');
{
  const { HopDisplay, appended } = loadDisplay();
  const r = HopResolver.resolve(['ef', 'cd'], null, null, null, null, OBSERVER_ID);
  const relayed = HopDisplay.renderHop('ef', r['ef']);
  const fallback = HopDisplay.renderHop('cd', r['cd']);
  assert(/hop-conflict-btn[\s\S]*>2<\/button>/.test(relayed), 'an earlier hop still gets a badge counting its 2 candidates');
  assert(!/hop-global-fallback/.test(relayed), 'an earlier hop is not styled as a global fallback');
  assert(/hop-global-fallback/.test(fallback), 'a directly heard hop outside the region is styled as a global fallback');

  const popoverHeader = (entry, h) => {
    appended.length = 0;
    HopDisplay._showFromBtn({
      dataset: { conflict: JSON.stringify({ h, conflicts: entry.conflicts, globalFallback: entry.globalFallback || false }) },
      getBoundingClientRect: () => ({ bottom: 0, left: 0 }),
    });
    return appended.length ? appended[0].innerHTML : '';
  };
  assert(!/global fallback/.test(popoverHeader(r['ef'], 'ef')), 'the popover of an earlier hop does not say "global fallback"');
  assert(/global fallback/.test(popoverHeader(r['cd'], 'cd')), 'the popover of a directly heard hop outside the region does');
}

// packets.js, real file, with the real resolver and display.
function loadPackets() {
  const ctx = {
    window: { addEventListener: () => {}, removeEventListener: () => {}, dispatchEvent: () => {}, innerWidth: 1200, PacketFilter: null, scrollX: 0, scrollY: 0 },
    document: {
      readyState: 'complete',
      createElement: () => ({ id: '', textContent: '', innerHTML: '', className: '', style: {},
        appendChild: () => {}, setAttribute: () => {}, addEventListener: () => {},
        querySelectorAll: () => [], querySelector: () => null,
        classList: { add: () => {}, remove: () => {}, contains: () => false } }),
      head: { appendChild: () => {} }, getElementById: () => null,
      addEventListener: () => {}, removeEventListener: () => {},
      querySelectorAll: () => [], querySelector: () => null, body: { appendChild: () => {} },
    },
    console, Date, Infinity, Math, Array, Object, String, Number, JSON, RegExp,
    Error, TypeError, RangeError, parseInt, parseFloat, isNaN, isFinite,
    encodeURIComponent, decodeURIComponent,
    setTimeout: () => {}, clearTimeout: () => {}, setInterval: () => {}, clearInterval: () => {},
    fetch: () => Promise.resolve({ ok: true, json: () => Promise.resolve({}) }),
    performance: { now: () => Date.now() },
    localStorage: (() => { const st = {}; return {
      getItem: k => st[k] || null, setItem: (k, v) => { st[k] = String(v); }, removeItem: k => { delete st[k]; },
    }; })(),
    location: { hash: '' }, history: { replaceState: () => {} },
    CustomEvent: class CustomEvent {}, Map, Set, Promise, URLSearchParams,
    addEventListener: () => {}, removeEventListener: () => {}, dispatchEvent: () => {},
    requestAnimationFrame: () => {},
    registerPage: () => {},
  };
  vm.createContext(ctx);
  const loadFile = f => {
    vm.runInContext(fs.readFileSync(REPO_ROOT + '/public/' + f, 'utf8'), ctx, { filename: f });
    for (const k of Object.keys(ctx.window)) ctx[k] = ctx.window[k];
  };
  ['payload-labels.js', 'roles.js', 'app.js', 'packet-helpers.js', 'hop-resolver.js', 'hop-display.js', 'packets.js'].forEach(loadFile);
  ctx.fetchAllNodes = () => Promise.resolve({ nodes: NODES });
  ctx.api = path => Promise.resolve(
    path === '/observers' ? { observers: [{ id: OBSERVER_ID, iata: 'OST' }] }
    : path === '/iata-coords' ? { coords: { OST: { lat: 51.1998, lon: 2.874673 } } }
    : {});
  return ctx;
}

// Splits a rendered path into its hops. A badge is the hop's conflict button.
const hopParts = html => html.split('<span class="arrow">→</span>');
const hasBadge = part => /hop-conflict-btn/.test(part);

(async () => {
  console.log('\n=== #2098: packets.js resolves a prefix apart as last hop and as earlier hop ===');
  {
    const t = loadPackets()._packetsTestAPI;
    // Same observer, "ab" heard directly in one packet and relayed in the other.
    await t.resolveHopsForPackets([
      { observer_id: OBSERVER_ID, path_json: '["ef","ab"]' },
      { observer_id: OBSERVER_ID, path_json: '["ab","ef"]' },
    ]);
    const [ef1, ab1] = hopParts(t.renderPath(['ef', 'ab'], OBSERVER_ID));
    const [ab2, ef2] = hopParts(t.renderPath(['ab', 'ef'], OBSERVER_ID));
    assert(/AB-NEAR/.test(ab1) && !hasBadge(ab1), 'last hop "ab": the candidate in region, no badge');
    assert(hasBadge(ab2), 'earlier hop "ab": a badge, every candidate kept');
    assert(/EF-NEAR/.test(ef2) && !hasBadge(ef2), 'last hop "ef": the candidate in region, no badge');
    assert(hasBadge(ef1), 'earlier hop "ef": a badge, every candidate kept');
  }

  console.log('\n=== #2098: the detail pane keeps its region-anchored hops out of the list cache ===');
  {
    const t = loadPackets()._packetsTestAPI;
    const panel = { innerHTML: '', querySelectorAll: () => [], querySelector: () => null, addEventListener: () => {} };
    await t.renderDetail(panel, {
      packet: { id: 1, hash: 'h1', observer_id: OBSERVER_ID, route_type: 1, payload_type: 5,
        raw_hex: '1502EFAB' + 'AA'.repeat(24), path_json: '["ef","ab"]',
        decoded_json: JSON.stringify({ type: 'CHAN', channel: '#test', sender: 'A', text: 'A: hi' }) },
      observations: [],
    });
    // The detail infers its hops by position and renders them itself; the
    // list's per-prefix cache is not written (it used to be, by prefix).
    const row = (panel.innerHTML.match(/<dt>Path<\/dt><dd>([\s\S]*?)<\/dd>/) || [])[1] || '';
    assert(/AB-NEAR/.test(hopParts(row)[1] || ''), 'with the observer, the detail resolved the last hop in region');
    // Without an observer (the bare key) it must not borrow that narrowed entry.
    const bare = t.renderPath(['ab'], undefined);
    assert(!/AB-NEAR/.test(bare) || hasBadge(bare), 'without an observer, "ab" is not shown as narrowed to one region (' + bare.replace(/<[^>]+>/g, '') + ')');
  }

  console.log('\n=== #2098: the detail pane resolves the shown observation for its own observer ===');
  {
    // The packet row comes from another observer (no region); the detail shows
    // the first observation, heard by the OST observer, whose last hop is "ab".
    const t = loadPackets()._packetsTestAPI;
    const panel = { innerHTML: '', querySelectorAll: () => [], querySelector: () => null, addEventListener: () => {} };
    await t.renderDetail(panel, {
      packet: { id: 2, hash: 'h2', observer_id: 'OTHEROBSERVER', route_type: 1, payload_type: 5,
        raw_hex: '1503EFABCD' + 'AA'.repeat(24), path_json: '["ef","ab","cd"]',
        decoded_json: JSON.stringify({ type: 'CHAN', channel: '#test', sender: 'A', text: 'A: hi' }) },
      observations: [{ id: 21, observer_id: OBSERVER_ID, path_json: '["ef","ab"]', raw_hex: '1502EFAB' + 'AA'.repeat(24) }],
    });
    const m = panel.innerHTML.match(/<dt>Path<\/dt><dd>([\s\S]*?)<\/dd>/);
    const shown = m ? hopParts(m[1]) : [];
    const last = shown[shown.length - 1] || '';
    assert(shown.length === 2, 'the Path row shows the observation\'s 2 hops (' + shown.length + ')');
    assert(/AB-NEAR/.test(last) && !hasBadge(last), 'its last hop is narrowed to the region of the observer that heard it (' + last.replace(/<[^>]+>/g, '').trim() + ')');
  }

  console.log(`\n${passed} passed, ${failed} failed`);
  process.exit(failed ? 1 : 0);
})();

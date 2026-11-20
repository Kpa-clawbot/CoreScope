/**
 * Map route deep-link path params — #/map?packet=<hash> plus
 *   &observer=<pubkey or 8+ char prefix>  isolate that observer's shortest path
 *   &paths=all                            open the path picker's "All" view
 *
 * Covers the real helpers in public/route-view-utils.js
 * (resolveDeepLinkObservation, pathGroupKey) and the source wiring in
 * map.js / route-view.js that hands the result to the path picker.
 */
'use strict';
const REPO_ROOT = require('path').resolve(__dirname, '..', '..');

const fs = require('fs');
const path = require('path');
const vm = require('vm');

let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}

const read = f => fs.readFileSync(path.join(REPO_ROOT, 'public', f), 'utf8');
const warnings = [];
const ctx = { window: {}, URLSearchParams, console: { log() {}, warn: m => warnings.push(m), error() {} } };
vm.createContext(ctx);
vm.runInContext(read('route-view-utils.js'), ctx);
const U = ctx.window.MC_ROUTE_UTILS;

const OBS_A = 'a1b2c3d4' + '0'.repeat(56);
const OBS_B = 'b1b2c3d4' + '1'.repeat(56);
const observations = [
  { id: 10, observer_id: OBS_B, timestamp: '2026-11-01T10:00:00Z', path_json: '["aa","bb"]' },
  { id: 11, observer_id: OBS_A, timestamp: '2026-11-01T10:00:05Z', path_json: '["cc","dd","ee"]' },
  { id: 12, observer_id: OBS_A.toUpperCase(), timestamp: '2026-11-01T10:00:02Z', path_json: '["ff"]' },
  { id: 13, observer_id: OBS_A, timestamp: '2026-11-01T10:00:02Z', path_json: '["99"]' },
];
const pick = params => U.resolveDeepLinkObservation(observations, params);

console.log('\n=== observer: pick that observer\'s shortest path ===');
{
  const r = pick({ observer: 'a1b2c3d4' });
  assert(r.chosen && r.chosen.id === 12, '8-char prefix picks the shortest path, ties to the earliest then the lower id (got ' + (r.chosen && r.chosen.id) + ')');
  assert(r.observationId === '12', 'observationId is the chosen observation (back link lands on it)');
  assert(r.pathView && r.pathView.mode === 'path' && r.pathView.path.join(',') === 'ff',
    'pathView isolates the chosen observation\'s raw hops');
  assert(pick({ observer: OBS_A }).chosen.id === 12, 'full public key matches');
  assert(pick({ observer: 'A1B2C3D4' }).chosen.id === 12, 'match is case-insensitive');
  assert(pick({ observer: '  a1b2c3d4 ' }).chosen.id === 12, 'surrounding whitespace is ignored');
}

console.log('\n=== observer: shortest path wins over earliest ===');
{
  // The analyzer.comchan.net report: one observer heard the packet twice in
  // the same second, the lower id over the longer path.
  const OBS_C = '3aac26fc' + '2'.repeat(56);
  const same = [
    { id: 19837252, observer_id: OBS_C.toUpperCase(), timestamp: '2026-11-20T18:19:40.000Z', path_json: '["CD2806","93702B","AE5842","2E20A9"]' },
    { id: 19837258, observer_id: OBS_C.toUpperCase(), timestamp: '2026-11-20T18:19:40.000Z', path_json: '["CD2806","93702B","AE5842"]' },
  ];
  const r = U.resolveDeepLinkObservation(same, { observer: OBS_C });
  assert(r.chosen.id === 19837258, 'same timestamp: the 3-hop path wins over the lower-id 4-hop one (got ' + r.chosen.id + ')');
  assert(r.pathView.path.length === 3, 'path view isolates the 3-hop path');
  const later = [
    { id: 1, observer_id: OBS_C, timestamp: '2026-11-20T18:19:40Z', path_json: '["aa","bb","cc"]' },
    { id: 2, observer_id: OBS_C, timestamp: '2026-11-20T18:19:45Z', path_json: '["aa"]' },
  ];
  assert(U.resolveDeepLinkObservation(later, { observer: '3aac26fc' }).chosen.id === 2, 'a later but shorter path wins');
  const empty = [
    { id: 1, observer_id: OBS_C, timestamp: '2026-11-20T18:19:40Z', path_json: '[]' },
    { id: 2, observer_id: OBS_C, timestamp: '2026-11-20T18:19:45Z', path_json: '["aa","bb"]' },
  ];
  assert(U.resolveDeepLinkObservation(empty, { observer: '3aac26fc' }).chosen.id === 2,
    'an empty path (nothing to draw) loses to an observed one');
  const bad = [
    { id: 1, observer_id: OBS_C, timestamp: '2026-11-20T18:19:40Z', path_json: 'not json' },
    { id: 2, observer_id: OBS_C, timestamp: '2026-11-20T18:19:45Z', path_json: '["aa"]' },
  ];
  assert(U.resolveDeepLinkObservation(bad, { observer: '3aac26fc' }).chosen.id === 2, 'unparseable path_json loses');
}

console.log('\n=== observer: invalid or unmatched keys fall back ===');
{
  warnings.length = 0;
  const short = pick({ observer: 'a1b2c3' });
  assert(short.chosen.id === 10 && short.pathView === null, 'prefix shorter than 8 chars falls back to the first observation');
  assert(pick({ observer: 'a1b2c3d4zz' }).pathView === null, 'non-hex key is rejected');
  const none = pick({ observer: 'ffffffff' });
  assert(none.chosen.id === 10 && none.pathView === null && none.observationId === null,
    'unmatched observer falls back to the first observation with no path view');
  assert(warnings.some(w => /ffffffff/.test(w)), 'unmatched observer logs a console warning');
  assert(pick({ observer: 'ffffffff', paths: 'all' }).pathView.mode === 'all',
    'unmatched observer still honours paths=all');
}

console.log('\n=== precedence ===');
{
  const r = pick({ obsId: '11', observer: 'b1b2c3d4' });
  assert(r.chosen.id === 11 && r.pathView === null, 'obs wins over observer');
  assert(pick({ obsId: '11', paths: 'all' }).pathView.mode === 'all', 'obs combines with paths=all');
  {
    // The All button writes paths=all next to an existing observer=, so a
    // reload of that URL must open the All view, with the observer's
    // observation still the one chosen.
    const both = pick({ observer: 'b1b2c3d4', paths: 'all' });
    assert(both.pathView.mode === 'all', 'paths=all wins the view over observer');
    assert(both.chosen.id === 10 && both.observationId === '10', 'observer still chooses the observation with paths=all');
  }
  {
    const fell = pick({ obsId: '999', observer: 'b1b2c3d4' });
    assert(fell.chosen.id === 10 && fell.pathView.mode === 'path', 'an unmatched obs falls through to observer');
    assert(fell.observationId === '10', 'observationId is the observer\'s observation, not the stale obs (got ' + fell.observationId + ')');
    const stale = pick({ obsId: '999' });
    assert(stale.chosen.id === 10 && stale.observationId === null,
      'an unmatched obs alone falls back to the first observation with no observationId (got ' + stale.observationId + ')');
    assert(pick({ obsId: 11 }).observationId === '11', 'a matched obs is returned as a string id');
  }
  assert(pick({}).chosen.id === 10 && pick({}).pathView === null, 'no params: first observation, default view');
  assert(pick({ paths: 'ALL' }).pathView === null, 'paths only accepts "all"');
  assert(U.resolveDeepLinkObservation([], { observer: 'a1b2c3d4' }).chosen === null, 'no observations: nothing chosen');
}

console.log('\n=== withPathsAllParam: the All button writes the deep link ===');
{
  const W = U.withPathsAllParam;
  assert(W('#/map?packet=ABC', true) === '#/map?packet=ABC&paths=all', 'adds paths=all');
  assert(W('#/map?packet=ABC&observer=a1b2c3d4', true) === '#/map?packet=ABC&observer=a1b2c3d4&paths=all',
    'keeps observer and the other params');
  assert(W('#/map?packet=ABC&paths=all', true) === '#/map?packet=ABC&paths=all', 'already there: unchanged');
  assert(W('#/map?packet=ABC&paths=all&observer=a1b2c3d4', false) === '#/map?packet=ABC&observer=a1b2c3d4',
    'removes paths=all, keeps the rest');
  assert(W('#/map?packet=ABC', false) === '#/map?packet=ABC', 'nothing to remove: unchanged');
  assert(W('#/map?route=1', true) === '#/map?route=1', 'no packet= (sessionStorage route): untouched, paths=all would not reload');
  assert(W('#/map', true) === '#/map', 'plain map: untouched');
  assert(W('', true) === '', 'empty hash: untouched');
  assert(W('#/map?packet=A%2FB&obs=7', true) === '#/map?packet=A%2FB&obs=7&paths=all', 'encoded values survive');
}

console.log('\n=== pathGroupKey matches the path picker grouping ===');
{
  assert(U.pathGroupKey(['aa', 'bb']) === 'aa→bb', 'hops joined with the picker arrow');
  const drop1 = hops => hops.filter(h => h.length > 2);
  assert(U.pathGroupKey(['aa', 'bbcc'], drop1) === 'bbcc', 'filter applied before joining');
  assert(U.pathGroupKey(['aa', 'bb'], drop1) !== U.pathGroupKey(['cc', 'dd'], drop1),
    'fully filtered paths stay distinct');
  assert(U.pathGroupKey(null) === U.pathGroupKey([]), 'missing hops treated as empty');
}

console.log('\n=== wiring ===');
{
  const mapSrc = read('map.js');
  const rvSrc = read('route-view.js');
  assert(/resolveDeepLinkObservation\(observations, \{\s*obsId: obsId, observer: params\.get\('observer'\), paths: params\.get\('paths'\)/.test(mapSrc),
    'deep link resolves the observation from obs / observer / paths');
  assert(/initialPathView: pick\.pathView/.test(mapSrc), 'deep link hands the path view to the multi-path draw');
  assert(/initialPathView: opts\.initialPathView \|\| null/.test(mapSrc), 'multi-path draw forwards it to the renderer');
  assert(/var k = pathGroupKey\(rawHops, filterHops\);/.test(rvSrc), 'picker rows are keyed by pathGroupKey');
  assert(/if \(clearBtn\) clearBtn\.click\(\);/.test(rvSrc), 'paths=all goes through the All button handler');
  assert(/pathRows\[ri\]\.dataset\.pathKey === key\) \{ pathRows\[ri\]\.click\(\);/.test(rvSrc),
    'observer view goes through the path row click handler');
  assert(/sidebar\._applyPathView\(opts\.initialPathView\)/.test(rvSrc), 'renderer applies the initial path view');
  const syncCalls = rvSrc.match(/syncPathsAllParam\((true|false)\)/g) || [];
  assert(syncCalls.filter(c => c === 'syncPathsAllParam(true)').length === 2,
    'the All button and the map badge both write paths=all (' + syncCalls.join(', ') + ')');
  assert(syncCalls.includes('syncPathsAllParam(false)'), 'isolating one path removes paths=all');
  assert(/history\.replaceState\(null, '', next\)/.test(rvSrc),
    'the hash is rewritten with replaceState, so the router does not re-run the deep link');
}

console.log('\n' + passed + ' passed, ' + failed + ' failed');
if (failed) process.exit(1);

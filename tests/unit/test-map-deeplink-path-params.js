/**
 * Map route deep-link path params — #/map?packet=<hash> plus
 *   &observer=<pubkey or 8+ char prefix>  isolate that observer's shortest path
 *   &paths=all                            open the path picker's "All" view
 *   &paths=<hop>-<hop>-...                isolate that path (a picker row click)
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
  // One observer heard the packet twice in the same second, the lower id
  // over the longer path. All values are made up.
  const OBS_C = 'c1c2c3d4' + '2'.repeat(56);
  const same = [
    { id: 101, observer_id: OBS_C.toUpperCase(), timestamp: '2026-01-02T03:04:05.000Z', path_json: '["111111","222222","333333","444444"]' },
    { id: 102, observer_id: OBS_C.toUpperCase(), timestamp: '2026-01-02T03:04:05.000Z', path_json: '["111111","222222","333333"]' },
  ];
  const r = U.resolveDeepLinkObservation(same, { observer: OBS_C });
  assert(r.chosen.id === 102, 'same timestamp: the 3-hop path wins over the lower-id 4-hop one (got ' + r.chosen.id + ')');
  assert(r.pathView.path.length === 3, 'path view isolates the 3-hop path');
  const later = [
    { id: 1, observer_id: OBS_C, timestamp: '2026-01-02T03:04:00Z', path_json: '["aa","bb","cc"]' },
    { id: 2, observer_id: OBS_C, timestamp: '2026-01-02T03:04:05Z', path_json: '["aa"]' },
  ];
  assert(U.resolveDeepLinkObservation(later, { observer: 'c1c2c3d4' }).chosen.id === 2, 'a later but shorter path wins');
  const empty = [
    { id: 1, observer_id: OBS_C, timestamp: '2026-01-02T03:04:00Z', path_json: '[]' },
    { id: 2, observer_id: OBS_C, timestamp: '2026-01-02T03:04:05Z', path_json: '["aa","bb"]' },
  ];
  assert(U.resolveDeepLinkObservation(empty, { observer: 'c1c2c3d4' }).chosen.id === 2,
    'an empty path (nothing to draw) loses to an observed one');
  const bad = [
    { id: 1, observer_id: OBS_C, timestamp: '2026-01-02T03:04:00Z', path_json: 'not json' },
    { id: 2, observer_id: OBS_C, timestamp: '2026-01-02T03:04:05Z', path_json: '["aa"]' },
  ];
  assert(U.resolveDeepLinkObservation(bad, { observer: 'c1c2c3d4' }).chosen.id === 2, 'unparseable path_json loses');
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

console.log('\n=== paths=<hops>: the clicked row ===');
{
  const r = pick({ observer: 'a1b2c3d4', paths: 'aa-bb' });
  assert(r.pathView.mode === 'path' && r.pathView.path.join(',') === 'aa,bb', 'a named path wins the view over observer');
  assert(r.chosen.id === 12 && r.observationId === '12', 'observer still chooses the observation (route spine, back link)');
  const byId = pick({ obsId: '11', paths: 'aa-bb' });
  assert(byId.chosen.id === 11 && byId.pathView.path.join(',') === 'aa,bb', 'obs combines with a named path');
  const alone = pick({ paths: 'ff' });
  assert(alone.chosen.id === 10 && alone.observationId === null && alone.pathView.path.join(',') === 'ff',
    'a named path alone sets only the view');
  assert(pick({ paths: 'AA-BB' }).pathView.path.join(',') === 'aa,bb', 'case-insensitive, returns the observed spelling');
  warnings.length = 0;
  assert(pick({ observer: 'a1b2c3d4', paths: '77-88' }).pathView.path.join(',') === 'ff',
    'a path nobody observed falls back to the observer view');
  assert(warnings.some(w => /77-88/.test(w)), 'a path nobody observed logs a console warning');
  assert(pick({ paths: 'aa' }).pathView === null, 'part of a path is not a match');
  const odd = [{ id: 1, path_json: 'not json' }, { id: 2, path_json: '{}' }, { id: 3, path_json: '["aa"]' }];
  assert(U.resolveDeepLinkObservation(odd, { paths: 'aa' }).pathView.path.join() === 'aa', 'unreadable paths are skipped');
  assert(U.pathsParamValue(['aa', 'bb']) === 'aa-bb', 'pathsParamValue joins raw hops');
}

console.log('\n=== withPathsParam: the picker writes the deep link ===');
{
  const W = U.withPathsParam;
  assert(W('#/map?packet=ABC', 'all') === '#/map?packet=ABC&paths=all', 'All adds paths=all');
  assert(W('#/map?packet=ABC&observer=a1b2c3d4', 'all') === '#/map?packet=ABC&observer=a1b2c3d4&paths=all',
    'keeps observer and the other params');
  assert(W('#/map?packet=ABC&paths=all', 'all') === '#/map?packet=ABC&paths=all', 'already there: unchanged');
  assert(W('#/map?packet=ABC&paths=all&observer=a1b2c3d4', 'AA-BB') === '#/map?packet=ABC&paths=AA-BB&observer=a1b2c3d4',
    'a row replaces paths=all, keeps the rest, needs no escaping');
  assert(W('#/map?packet=ABC&paths=AA-BB', 'all') === '#/map?packet=ABC&paths=all', 'All replaces a named path');
  assert(W('#/map?route=1', 'all') === '#/map?route=1', 'no packet= (sessionStorage route): untouched, would not reload');
  assert(W('#/map', 'all') === '#/map', 'plain map: untouched');
  assert(W('', 'all') === '', 'empty hash: untouched');
  assert(W('#/map?packet=A%2FB&obs=7', 'all') === '#/map?packet=A%2FB&obs=7&paths=all', 'encoded values survive');
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
  assert(/pathRows\[ri\]\.dataset\.pathKey === key\) \{ selectRow\(pathRows\[ri\]\);/.test(rvSrc),
    'a deep-linked path selects its row without rewriting the link');
  assert(/sidebar\._applyPathView\(opts\.initialPathView\)/.test(rvSrc), 'renderer applies the initial path view');
  assert((rvSrc.match(/syncPathView\(\{ mode: 'all' \}\)/g) || []).length === 2,
    'the All button and the map badge both write paths=all');
  assert(/syncPathView\(\{ mode: 'path', path: /.test(rvSrc), 'a row click writes its path');
  assert(/history\.replaceState\(null, '', next\)/.test(rvSrc),
    'the hash is rewritten with replaceState, so the router does not re-run the deep link');
}

console.log('\n' + passed + ' passed, ' + failed + ' failed');
if (failed) process.exit(1);

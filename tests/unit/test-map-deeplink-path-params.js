/**
 * Map route deep-link path params — #/map?packet=<hash> plus
 *   &observer=<pubkey or 8+ char prefix>  isolate that observer's earliest path
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
const ctx = { window: {}, console: { log() {}, warn: m => warnings.push(m), error() {} } };
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

console.log('\n=== observer: pick that observer\'s earliest observation ===');
{
  const r = pick({ observer: 'a1b2c3d4' });
  assert(r.chosen && r.chosen.id === 12, '8-char prefix picks the earliest, ties to the lower id (got ' + (r.chosen && r.chosen.id) + ')');
  assert(r.observationId === '12', 'observationId is the chosen observation (back link lands on it)');
  assert(r.pathView && r.pathView.mode === 'path' && r.pathView.path.join(',') === 'ff',
    'pathView isolates the chosen observation\'s raw hops');
  assert(pick({ observer: OBS_A }).chosen.id === 12, 'full public key matches');
  assert(pick({ observer: 'A1B2C3D4' }).chosen.id === 12, 'match is case-insensitive');
  assert(pick({ observer: '  a1b2c3d4 ' }).chosen.id === 12, 'surrounding whitespace is ignored');
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
  assert(pick({ observer: 'b1b2c3d4', paths: 'all' }).pathView.mode === 'path', 'observer wins over paths=all');
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
}

console.log('\n' + passed + ' passed, ' + failed + ' failed');
if (failed) process.exit(1);

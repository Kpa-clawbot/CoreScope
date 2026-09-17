/**
 * "My Repeaters" — a favorites monitoring dashboard analytics tab.
 *
 * An at-a-glance monitor over the repeaters the operator has starred
 * (meshcore-favorites). Frontend-only aggregation over /api/nodes,
 * /api/nodes/clock-skew and /api/analytics/neighbor-graph; no server change.
 *
 * Two layers:
 *  - structural pins (file-grep) for tab WIRING that needs a DOM/app to run;
 *  - BEHAVIORAL tests that execute renderMyRepeatersTab against stub globals
 *    and assert on the rendered HTML — including regression guards for the
 *    review fixes (escaping, status a11y text, fetch-once caching).
 */
'use strict';
const REPO_ROOT = require('path').resolve(__dirname, '..', '..');

const fs = require('fs');
const path = require('path');

let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}

const a = fs.readFileSync(path.join(REPO_ROOT, 'public', 'analytics.js'), 'utf8');
const app = fs.readFileSync(path.join(REPO_ROOT, 'public', 'app.js'), 'utf8');

console.log('\n=== tab wiring + dropdown shortcut (structural) ===');
assert(/data-tab="my-repeaters"[^>]*>\s*My Repeaters\s*</.test(a), 'tab bar has a "My Repeaters" button');
assert(/case 'my-repeaters':\s*await renderMyRepeatersTab\(el\)/.test(a), 'renderTab dispatches my-repeaters');
assert(/AREA_FILTER_TABS[\s\S]{0,200}'my-repeaters'/.test(a), 'my-repeaters participates in region/area filtering');
assert(/href="#\/analytics\?tab=my-repeaters"/.test(app) && /Monitor my repeaters/.test(app),
  'favorites dropdown links to the My Repeaters dashboard');
// Round-3 nits: no dead fav-dd-monitor class; monitor row has a fav-dd-meta
// cell so its columns line up with the favorite rows.
assert(!/fav-dd-monitor/.test(app), 'dead fav-dd-monitor class removed');
assert(/Monitor my repeaters<\/span>\s*'\s*\+\s*'<span class="fav-dd-meta">/.test(app),
  'monitor link carries a fav-dd-meta cell for column alignment');

// --- Execute the real render against stub globals. ---
const start = a.indexOf('function _mrSummaryCard');
const end = a.indexOf('// === MY REPEATERS BLOCK END');
if (start < 0 || end < 0) { console.error('  ✗ could not locate the My Repeaters block'); process.exit(1); }
const block = a.slice(start, end);

const esc = s => s ? String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;') : '';
let favorites = [];
const getFavorites = () => favorites.slice();
let _regionQS = '';
const RegionFilter = { regionQueryString: () => _regionQS };
const AreaFilter = { areaQueryString: () => '' };
let nodesData = [];
let fetchAllNodesCalls = 0;
const fetchAllNodes = async () => { fetchAllNodesCalls++; return { nodes: nodesData }; };
let clockData = [], graphData = { nodes: [], edges: [] };
const api = async (p) => (p.indexOf('/nodes/clock-skew') === 0 ? clockData : graphData);
const CLIENT_TTL = { nodeList: 0, analyticsRF: 0 };
const getHealthThresholds = () => ({ degradedMs: 3600000, silentMs: 86400000 });
const renderSkewBadge = (sev) => 'CLOCKBADGE:' + sev;
const currentSkewValue = (cs) => (cs.recentMedianSkewSec != null ? cs.recentMedianSkewSec : cs.medianSkewSec);
const favStar = (pk) => '<button class="fav-star" data-fav="' + pk + '"></button>';
const timeAgo = () => '5m ago';
let lastBindCb = null;
const bindFavStars = (el, cb) => { lastBindCb = cb; };
const window = { ROLE_COLORS: { repeater: '#3b82f6', room: '#a855f7' }, aaBadgeStyle: null };

const M = new Function(
  'esc', 'getFavorites', 'RegionFilter', 'AreaFilter', 'fetchAllNodes', 'api', 'CLIENT_TTL',
  'getHealthThresholds', 'renderSkewBadge', 'currentSkewValue', 'favStar', 'timeAgo', 'bindFavStars', 'window',
  block + '\nreturn { renderMyRepeatersTab, _mrStatusCell, _mrSummaryCard };')(
  esc, getFavorites, RegionFilter, AreaFilter, fetchAllNodes, api, CLIENT_TTL,
  getHealthThresholds, renderSkewBadge, currentSkewValue, favStar, timeAgo, bindFavStars, window);

function makeEl() { return { _h: '', set innerHTML(v) { this._h = v; }, get innerHTML() { return this._h; } }; }
const tick = () => new Promise(r => setTimeout(r, 0));

(async () => {
  const nowIso = new Date().toISOString();
  const oldIso = new Date(Date.now() - 5 * 86400000).toISOString();

  console.log('\n=== empty state ===');
  favorites = [];
  let el = makeEl();
  await M.renderMyRepeatersTab(el);
  assert(/No favorite repeaters yet/.test(el.innerHTML), 'empty state when there are no favorites');

  console.log('\n=== populated render (behavioral) ===');
  favorites = ['AA', 'BB', 'CC'];
  nodesData = [
    { public_key: 'AA', name: 'Active Rptr', role: 'repeater', last_seen: nowIso, last_heard: nowIso, traffic_share_score: 0.42, bridge_score: 0.1, relay_count_1h: 7, relay_count_24h: 120, last_relayed: nowIso, battery_mv: 4010 },
    { public_key: 'BB', name: 'Silent Rptr', role: 'repeater', last_seen: oldIso, last_heard: oldIso, traffic_share_score: 0.05, bridge_score: 0.9, relay_count_1h: 0, relay_count_24h: 3 },
    { public_key: 'CC', name: 'My Phone', role: 'client', last_seen: nowIso },
  ];
  clockData = [{ pubkey: 'AA', severity: 'ok', recentMedianSkewSec: 2 }];
  graphData = { edges: [
    { source: 'AA', target: 'BB', score: 0.66, weight: 88, avg_snr: 7.5, bidirectional: true, ambiguous: true },
    { source: 'AA', target: 'CC', score: 0.9, weight: 5 },
  ] };
  el = makeEl();
  fetchAllNodesCalls = 0;
  await M.renderMyRepeatersTab(el);
  let html = el.innerHTML;
  assert(/Active Rptr/.test(html) && /Silent Rptr/.test(html), 'both favorite repeaters listed');
  assert(!/My Phone/.test(html), 'non-repeater favorite (client) excluded');
  assert(html.indexOf('Silent Rptr') < html.indexOf('Active Rptr'), 'silent repeater sorted before active');
  assert(/CLOCKBADGE:ok/.test(html), 'clock badge rendered from clock-skew data');
  assert(/42\.0%/.test(html) && /90\.0%/.test(html), 'traffic/bridge rendered as percentages');
  assert(/4\.01 V/.test(html), 'battery rendered in volts');
  assert(/&harr;/.test(html) && /88/.test(html), 'favorite↔favorite affinity edge shown with weight');
  assert(!/href="#\/nodes\/CC/.test(html), 'affinity edge to non-favorite-repeater CC excluded');

  console.log('\n=== bot fixes (behavioral regression guards) ===');
  // Status a11y: cell carries TEXT, not colour alone.
  assert(/>Active<\/span>/.test(html) && /<span style="font-size:11px">Silent<\/span>/.test(html),
    'status column shows a text label, not colour alone');
  // esc(String(value)) regression: a 0-valued summary card renders "0", not "".
  assert(/font-weight:700[^>]*>0<\/div>/.test(html),
    'summary cards render the number 0 (esc(String(0)) not swallowed to empty)');
  // Caching: un-star must re-paint WITHOUT another fetch.
  const callsAfterRender = fetchAllNodesCalls;
  assert(callsAfterRender === 1, 'initial render fetched the node list exactly once');
  favorites = ['AA']; // simulate un-starring BB
  assert(typeof lastBindCb === 'function', 'a re-paint callback was bound for live un-star');
  lastBindCb();
  await tick();
  html = el.innerHTML;
  assert(fetchAllNodesCalls === callsAfterRender, 'un-star re-paints from cache — NO extra fetch');
  assert(!/Silent Rptr/.test(html) && /Active Rptr/.test(html), 'un-starred repeater dropped on re-paint');

  console.log('\n=== round-2 fixes (regression guards) ===');
  // Re-render the full set for the affinity/layout assertions.
  favorites = ['AA', 'BB', 'CC'];
  el = makeEl();
  await M.renderMyRepeatersTab(el);
  const h2 = el.innerHTML;
  // Affinity arrow is always ↔ (undirected); never →.
  assert(/&harr;/.test(h2) && !/&rarr;/.test(h2), 'affinity link always renders ↔, never →');
  // Ambiguous marker is a ph-question icon with aria-label, not a "(?)" string.
  assert(/ph-question/.test(h2) && /aria-label="Path attribution ambiguous"/.test(h2) && !/\(\?\)/.test(h2),
    'ambiguous affinity edge uses a ph-question icon, not "(?)"');
  // Summary cards use a responsive grid (no flex-wrap-and-expand).
  assert(/grid-template-columns:repeat\(auto-fit/.test(h2), 'summary cards use an auto-fit grid');
  // Status numbers carry no status colour (Tufte: rely on the label).
  assert(!/var\(--status-green-text\)/.test(h2), 'summary numbers drop the status colour');
  // _mrSummaryCard helper: value-colour is opt-in, label always escaped.
  assert(!/font-weight:700;color:/.test(M._mrSummaryCard(5, 'Active')) && /font-weight:700;color:red/.test(M._mrSummaryCard(5, 'X', 'red')),
    '_mrSummaryCard colours the number only when a colour is explicitly passed');
  // _mrStatusCell falls back to an explicit "Unknown", not "Silent".
  assert(/Unknown/.test(M._mrStatusCell('weird')) && /Active/.test(M._mrStatusCell('active')),
    '_mrStatusCell maps unknown status to "Unknown"');
  // Dead CSS class removed from the favourite star call.
  assert(!/fav-mr-star/.test(h2), 'dead fav-mr-star class removed');

  console.log('\n=== un-star last favorite → empty state (from cache) ===');
  favorites = ['AA'];
  el = makeEl();
  await M.renderMyRepeatersTab(el);
  const callsBeforeUnstar = fetchAllNodesCalls;
  favorites = [];
  lastBindCb();
  await tick();
  assert(/No favorite repeaters yet/.test(el.innerHTML) && fetchAllNodesCalls === callsBeforeUnstar,
    'removing the last favorite shows the empty state without refetching');

  console.log('\n=== region/area filter hides a favorite → notice (MAJOR-1) ===');
  _regionQS = '&region=somewhere';      // filter active → scoped fetch
  favorites = ['AA', 'ZZ'];             // ZZ is outside the scope (not fetched)
  nodesData = [
    { public_key: 'AA', name: 'In-Scope Rptr', role: 'repeater', last_seen: nowIso, last_heard: nowIso, traffic_share_score: 0.1, bridge_score: 0.2, relay_count_24h: 5 },
  ];
  graphData = { edges: [] };
  el = makeEl();
  await M.renderMyRepeatersTab(el);
  assert(/1 favorite not shown with the active region\/area filter/.test(el.innerHTML),
    'hidden favorite is surfaced with a causally-neutral "not shown" notice');
  assert(!/outside the active/.test(el.innerHTML),
    'notice no longer claims the favorite is "outside" the filter (could be deleted/client)');
  assert(/In-Scope Rptr/.test(el.innerHTML), 'in-scope favorite still rendered alongside the notice');
  _regionQS = '';                       // no filter → no notice
  el = makeEl();
  await M.renderMyRepeatersTab(el);
  assert(!/not shown with the active region/.test(el.innerHTML),
    'no filter active → no hidden-favorite notice');

  console.log('\n────────────────────────────────────────');
  console.log(`  ${passed} passed, ${failed} failed`);
  process.exit(failed ? 1 : 0);
})();

/* MediaMesh (radio2) on Analytics and the Live feed.
 *
 * Analytics: an Overview card, the radio2 part of each payload type (Payload
 * Type Mix and Relay Airtime Share), and a MediaMesh tab with radio2 packets
 * per hour, the MediaMesh repeaters and the observers that hear radio2.
 * Live: a fuchsia badge on feed rows carried on radio2, and "MediaMesh only".
 * A mesh without MediaMesh data renders exactly as before.
 */
'use strict';
const fs = require('fs');
const assert = require('assert');
const vm = require('vm');

const analytics = fs.readFileSync('public/analytics.js', 'utf8');
const live = fs.readFileSync('public/live.js', 'utf8');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log(`  ✅ ${name}`); }
  catch (e) { failed++; console.log(`  ❌ ${name}: ${e.message}`); }
}

// Pull named function declarations out of a source file.
function extract(src, names) {
  return names.map((n) => {
    const start = src.indexOf('function ' + n + '(');
    assert.ok(start >= 0, n + ' not found');
    let depth = 0, i = src.indexOf('{', start);
    for (; i < src.length; i++) {
      if (src[i] === '{') depth++;
      else if (src[i] === '}' && --depth === 0) break;
    }
    return src.slice(start, i + 1);
  }).join('\n');
}

const ctx = {
  window: {},
  esc: (s) => (s ? String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;') : ''),
  sf: (v, d) => (v != null ? v.toFixed(d) : '–'),
  timeAgo: () => '5m ago',
  packetsPerHourCard: (pph) => ({ html: '<h3>Packets / Hour</h3><svg data-n="' + pph.length + '"></svg>' }),
};
ctx.escapeHtml = ctx.esc;
vm.createContext(ctx);
vm.runInContext(extract(analytics, ['renderPayloadPie', 'mediaMeshTypeShares', 'mediaMeshStatCard',
  'mediaMeshRadioText', 'renderMediaMeshTabHtml', 'renderRelayAirtimeDumbbell']) +
  '\nthis.api = { renderPayloadPie, mediaMeshTypeShares, mediaMeshStatCard, renderMediaMeshTabHtml, renderRelayAirtimeDumbbell };', ctx);
const A = ctx.api;

const MM = {
  total_transmissions: 1000, mediamesh_transmissions: 120, share_pct: 12,
  by_type: [{ name: 'GRP_TXT', count: 500, mediamesh: 100 }, { name: 'ADVERT', count: 100, mediamesh: 0 }, { name: 'GRP_DATA', count: 40, mediamesh: 20 }],
  per_hour: [{ hour: '2026-10-30T14', count: 50 }, { hour: '2026-10-30T15', count: 70 }],
  observers: [{ id: 'abc', name: 'First Hill <obs>', iata: 'SEA', mediamesh_observations: 120, total_observations: 900, share_pct: 13.3, last_heard: '2026-10-30T15:00:00Z' }],
  repeaters: [
    { public_key: 'aa11', name: 'Peak', role: 'repeater', mode: 'rxtx', radio: '911.15,250,7,5', source: 'status', last_seen: '2026-10-30T15:00:00Z' },
    { public_key: 'bb22', name: 'Valley', role: 'repeater', mode: 'rx', source: 'neighbor', last_seen: '2026-10-30T14:00:00Z' },
  ],
};
const TYPES = [{ name: 'GRP_TXT', count: 500 }, { name: 'ADVERT', count: 100 }, { name: 'GRP_DATA', count: 40 }];

console.log('\n=== Overview card ===');

test('shows radio2 transmissions, share and repeaters', () => {
  const h = A.mediaMeshStatCard(MM);
  assert.ok(h.includes('>120<'), h);
  assert.ok(h.includes('12.0% of transmissions'), h);
  assert.ok(h.includes('2 repeaters'), h);
  assert.ok(h.includes('href="#/analytics?tab=mediamesh"'));
});

test('absent on a mesh without MediaMesh', () => {
  assert.strictEqual(A.mediaMeshStatCard(null), '');
  assert.strictEqual(A.mediaMeshStatCard({ mediamesh_transmissions: 0, repeaters: [] }), '');
});

console.log('\n=== Payload Type Mix ===');

test('the radio2 part of each type is a fuchsia segment and a percentage', () => {
  const h = A.renderPayloadPie(TYPES, MM);
  assert.strictEqual((h.match(/mm-bar-segment/g) || []).length, 2, 'GRP_TXT and GRP_DATA only');
  assert.ok(h.includes('20% radio2'), 'GRP_TXT 100 of 500');
  assert.ok(h.includes('50% radio2'), 'GRP_DATA 20 of 40');
  assert.ok(h.includes('part carried on MediaMesh'), 'legend');
});

test('unchanged without MediaMesh data', () => {
  const before = A.renderPayloadPie(TYPES);
  assert.ok(!before.includes('mm-'));
  assert.strictEqual(A.renderPayloadPie(TYPES, { by_type: [] }), before);
});

console.log('\n=== Relay Airtime Share ===');

const AIR = { total_score: 1000, rows: [
  { payload_type: 'GRP_TXT', count: 10, count_pct: 50, airtime_pct: 60, score: 600, mediamesh_count: 3, mediamesh_airtime_pct: 18 },
  { payload_type: 'ADVERT', count: 10, count_pct: 50, airtime_pct: 40, score: 400 },
] };

test('rows with radio2 traffic get a hollow ring and an r2 value', () => {
  const h = A.renderRelayAirtimeDumbbell(AIR);
  assert.strictEqual((h.match(/dumbbell-dot-mediamesh/g) || []).length, 1);
  assert.ok(h.includes('left:18.000%'));
  assert.ok(h.includes('r2 18.0%'));
  assert.ok(h.includes('airtime on MediaMesh'), 'legend entry');
});

test('no ring, value or legend entry without radio2 traffic', () => {
  const h = A.renderRelayAirtimeDumbbell({ total_score: 1000, rows: [AIR.rows[1]] });
  assert.ok(!h.includes('dumbbell-dot-mediamesh') && !h.includes('r2 ') && !h.includes('airtime on MediaMesh'));
});

console.log('\n=== MediaMesh tab ===');

test('per hour, repeaters with mode/setting/last seen, observers; names escaped', () => {
  const h = A.renderMediaMeshTabHtml(MM, '');
  assert.ok(h.includes('radio2 packets /'), 'per-hour chart title');
  assert.ok(h.includes('data-n="2"'), 'both hours charted');
  assert.ok(h.includes('Peak') && h.includes('listens and sends') && h.includes('reported by the node'));
  assert.ok(h.includes('Valley') && h.includes('listens') && h.includes('reported by a neighbour'));
  assert.ok(h.includes('911.15,250,7,5') || h.includes('911.15 MHz'), 'radio2 setting');
  assert.ok(h.includes('First Hill &lt;obs&gt;') && !h.includes('<obs>'), 'observer name escaped');
  assert.ok(h.includes('13.3%') && h.includes('5m ago'));
});

test('empty state explains what is needed', () => {
  const h = A.renderMediaMeshTabHtml({ mediamesh_transmissions: 0, repeaters: [], observers: [], per_hour: [] }, '');
  assert.ok(h.includes('No MediaMesh traffic or repeaters'));
});

test('tab button, switch case and bulk load are wired', () => {
  assert.ok(analytics.includes('data-tab="mediamesh">MediaMesh</button>'));
  assert.ok(analytics.includes("case 'mediamesh': await renderMediaMeshTab(el); break;"));
  assert.ok(/api\('\/analytics\/mediamesh' \+ sepMM[^)]*\)\.catch\(\(\) => null\)/.test(analytics), 'Overview data never fails the page');
});

console.log('\n=== Live ===');

const lctx = {};
vm.createContext(lctx);
vm.runInContext(extract(live, ['mediaMeshFeedBadgeHtml', 'groupIsMediaMesh']) + '\nthis.api = { mediaMeshFeedBadgeHtml, groupIsMediaMesh };', lctx);

test('feed rows on radio2 get the MediaMesh badge, others none', () => {
  assert.ok(lctx.api.mediaMeshFeedBadgeHtml({ mediamesh: true }).includes('badge-mediamesh'));
  assert.strictEqual(lctx.api.mediaMeshFeedBadgeHtml({}), '');
  assert.strictEqual(lctx.api.mediaMeshFeedBadgeHtml({ mediamesh: 'true' }), '');
});

test('a group is MediaMesh when any observation was on radio2', () => {
  assert.ok(lctx.api.groupIsMediaMesh([{}, { mediamesh: true }]));
  assert.ok(!lctx.api.groupIsMediaMesh([{}, {}]));
  assert.ok(!lctx.api.groupIsMediaMesh(null));
});

test('"MediaMesh only" toggle is wired, remembered and filters the view', () => {
  assert.ok(live.includes('id="liveMediaMeshToggle"'));
  assert.ok(live.includes("localStorage.getItem('live-mediamesh-only') === 'true'"));
  assert.ok(live.includes('if (mediaMeshOnly && !groupIsMediaMesh(packets)) return;'), 'renderer');
  assert.ok(live.includes('if (mediaMeshOnly && !groupIsMediaMesh(group.packets)) continue;'), 'feed rebuild');
});

test('the badge is added when a later observation is on radio2', () => {
  assert.ok(live.includes("pkt.mediamesh === true && !entry.element.querySelector('.badge-mediamesh')"));
  assert.ok(/mediamesh: packets\.some\(function \(p\) \{ return p && p\.mediamesh === true; \}\)/.test(live), 'consolidated row');
});

test('colours come from --mediamesh-path-color', () => {
  const css = fs.readFileSync('public/style.css', 'utf8') + fs.readFileSync('public/live.css', 'utf8');
  for (const cls of ['.mm-bar-segment', '.dumbbell-dot-mediamesh', '.badge-mediamesh', '.stat-card-mediamesh']) {
    const i = css.indexOf(cls + ' {');
    assert.ok(i >= 0, cls);
    assert.ok(css.slice(i, css.indexOf('}', i)).includes('var(--mediamesh-path-color)'), cls);
  }
});

console.log(`\n${passed} passed, ${failed} failed`);
if (failed) process.exit(1);

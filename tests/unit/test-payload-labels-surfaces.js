/* Every page that names a payload type reads it from public/payload-labels.js.
 *
 * #1804 moved packets / live / packet-filter onto the canonical map, but
 * node-analytics, observer-detail, compare, nodes and app.js kept their own
 * hand-rolled copies. Those copies lacked GRP_DATA (6), MULTIPART (10) and
 * RAW_CUSTOM (15), so the Node Analytics "Packet Types" doughnut showed
 * "Type 6" for the most common packet on some meshes.
 *
 * Usage: node tests/unit/test-payload-labels-surfaces.js
 */
'use strict';
const fs = require('fs');
const path = require('path');
const vm = require('vm');

const ROOT = path.join(__dirname, '..', '..');
const read = (rel) => fs.readFileSync(path.join(ROOT, rel), 'utf8');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log('  ✓ ' + name); }
  catch (e) { failed++; console.error('  ✗ ' + name + ' — ' + e.message); }
}
async function testAsync(name, fn) {
  try { await fn(); passed++; console.log('  ✓ ' + name); }
  catch (e) { failed++; console.error('  ✗ ' + name + ' — ' + e.message); }
}
function assert(c, m) { if (!c) throw new Error(m || 'assertion failed'); }

const plCtx = { window: {}, console };
vm.createContext(plCtx);
vm.runInContext(read('public/payload-labels.js'), plCtx);
const PL = plCtx.window.PayloadLabels;

console.log('=== no hand-rolled payload label maps outside payload-labels.js ===');

// A hand-rolled map pairs a numeric payload id with a short label or a
// firmware enum name, e.g. `6: 'Group Data'`, `{0:'Request',...}` or
// `12: 'RAW_CUSTOM'` (map.js had RAW_CUSTOM on 12; the firmware uses 0x0F).
const escapeRe = (s) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
const names = Object.values(PL.enums).flatMap((e) => [e.short, e.enumName]).map(escapeRe);
const MAP_ENTRY = new RegExp('\\b\\d{1,2}\\s*:\\s*[\'"](' + names.join('|') + ')[\'"]');

const publicJs = fs.readdirSync(path.join(ROOT, 'public'))
  .filter((f) => f.endsWith('.js') && f !== 'payload-labels.js');

for (const f of publicJs) {
  const src = read('public/' + f);
  test(f + ' has no hand-rolled payload label map', () => {
    const lines = src.split('\n');
    const hits = lines
      .map((line, i) => ({ line, n: i + 1 }))
      .filter(({ line }) => MAP_ENTRY.test(line) && !/^\s*(\/\/|\*)/.test(line));
    assert(hits.length === 0,
      'use window.PayloadLabels instead — line ' + hits.map((h) => h.n).join(', '));
  });
}

console.log('=== PayloadLabels.api.displayName ===');

test('known types return the canonical short label', () => {
  for (const e of Object.values(PL.enums)) {
    assert(PL.api.displayName(e.enumId) === e.short,
      e.enumName + ': expected ' + e.short + ', got ' + PL.api.displayName(e.enumId));
  }
});
test('GRP_DATA (6) reads "Group Data"', () => {
  assert(PL.api.displayName(6) === 'Group Data', 'got ' + PL.api.displayName(6));
});
test('ids the firmware does not define fall back to "Type N"', () => {
  assert(PL.api.displayName(13) === 'Type 13', 'got ' + PL.api.displayName(13));
  assert(PL.api.displayName('12') === 'Type 12', 'string ids work too');
});
test('string ids (object keys) resolve like numbers', () => {
  assert(PL.api.displayName('6') === 'Group Data', 'got ' + PL.api.displayName('6'));
});
test('displayName lives on .api only (root aliases are legacy)', () => {
  assert(PL.displayName === undefined, 'new helpers must not grow the legacy root');
});

console.log('=== node-analytics.js Packet Types chart ===');

async function renderNodeAnalytics(breakdown) {
  const charts = [];
  const calls = [];
  const els = {};
  const mkEl = () => ({ style: {}, textContent: '', innerHTML: '', insertAdjacentText() {}, addEventListener() {} });
  const ctx = {
    console, Math, JSON, Date, Number, String, Array, Object, Promise, isNaN, setTimeout,
    getComputedStyle: () => ({ getPropertyValue: () => '' }),
    Chart: Object.assign(function Chart(el, cfg) { charts.push(cfg); this.destroy = () => {}; }, { defaults: {} }),
    CLIENT_TTL: {}, escapeHtml: (s) => String(s), timeAgo: () => '',
    viewGeneration: () => 0,
    api: (url) => new Promise((resolve) => calls.push({ url, resolve })),
    NodeHopAnalytics: { load() {}, destroy() {} },
  };
  let page = null;
  ctx.registerPage = (name, p) => { if (name === 'node-analytics') page = p; };
  ctx.window = ctx;
  ctx.document = { documentElement: {}, getElementById: (id) => els[id] || (els[id] = mkEl()) };
  vm.createContext(ctx);
  vm.runInContext(read('public/payload-labels.js'), ctx);
  vm.runInContext(read('public/node-analytics.js'), ctx);
  const app = { innerHTML: '', querySelectorAll: () => [] };
  const pk = 'ab'.repeat(32);
  page.init(app, pk + '/analytics');
  calls.find((c) => c.url.startsWith('/nodes/')).resolve({
    node: { public_key: pk, name: 'Relay One', role: 'repeater' },
    computedStats: { totalTransmissions: 100, availabilityPct: 50, signalGrade: 'A', avgPacketsPerDay: 10,
      uniqueObservers: 2, relayPct: 5, longestSilenceMs: 0 },
    timeRange: null, peerInteractions: [], activityTimeline: [], snrTrend: [], packetTypeBreakdown: breakdown,
    observerCoverage: [], hopDistribution: [], uptimeHeatmap: [],
  });
  await new Promise((r) => setTimeout(r, 10));
  return charts.find((c) => c.type === 'doughnut');
}

(async () => {
  await testAsync('doughnut labels GRP_DATA, MULTIPART and RAW_CUSTOM by name', async () => {
    const chart = await renderNodeAnalytics([
      { payload_type: 6, count: 257 }, { payload_type: 10, count: 3 },
      { payload_type: 15, count: 2 }, { payload_type: 5, count: 80 },
    ]);
    assert(chart, 'Packet Types doughnut was not built');
    const labels = chart.data.labels.join(', ');
    assert(labels === 'Group Data, Multipart, Raw Custom, Channel Msg', 'labels: ' + labels);
  });
  await testAsync('doughnut keeps "Type N" for an id the firmware does not define', async () => {
    const chart = await renderNodeAnalytics([{ payload_type: 13, count: 1 }]);
    assert(chart && chart.data.labels[0] === 'Type 13', 'labels: ' + (chart && chart.data.labels));
  });

  console.log('=== ' + passed + ' passed, ' + failed + ' failed ===');
  process.exit(failed === 0 ? 0 : 1);
})();

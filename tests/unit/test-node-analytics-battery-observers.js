/**
 * Node Analytics battery card: readings from observers that poll a node
 * directly name the observer that took them — in the "Reported by" line, as
 * one chart line per observer, and in the readings table. Loads the real
 * public/node-analytics.js in a vm sandbox.
 *
 * Usage: node tests/unit/test-node-analytics-battery-observers.js
 */
'use strict';
const fs = require('fs');
const path = require('path');
const vm = require('vm');

const SRC = fs.readFileSync(path.join(__dirname, '..', '..', 'public/node-analytics.js'), 'utf8');
let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}
const flush = () => new Promise((r) => setTimeout(r, 10));
const PK = 'ab'.repeat(32);
const OBS_A = '7e'.repeat(32);
const OBS_B = '3b'.repeat(32);

function analyticsData() {
  return {
    node: { public_key: PK, name: 'West Lomond', role: 'repeater' },
    computedStats: { totalTransmissions: 100, availabilityPct: 50, signalGrade: 'A', avgPacketsPerDay: 10,
      uniqueObservers: 2, relayPct: 5, longestSilenceMs: 0 },
    timeRange: null, peerInteractions: [], activityTimeline: [], snrTrend: [], packetTypeBreakdown: [],
    observerCoverage: [], hopDistribution: [], uptimeHeatmap: [],
  };
}

function load() {
  const els = {};
  const calls = [];
  const charts = [];
  const mkEl = () => ({ style: {}, textContent: '', innerHTML: '' });
  const ctx = {
    console, Math, JSON, Date, Number, String, Array, Object, Map, Promise, isNaN, setTimeout, encodeURIComponent,
    getComputedStyle: () => ({ getPropertyValue: () => '' }),
    Chart: Object.assign(function Chart(el, cfg) { charts.push(cfg); this.destroy = () => {}; }, { defaults: {} }),
    CLIENT_TTL: {}, escapeHtml: (s) => String(s).replace(/</g, '&lt;'), timeAgo: () => '5 min ago',
    viewGeneration: () => 0,
    api: (url) => new Promise((resolve, reject) => calls.push({ url, resolve, reject })),
    NodeHopAnalytics: { load() {}, destroy() {} },
  };
  let page = null;
  ctx.registerPage = (name, p) => { if (name === 'node-analytics') page = p; };
  ctx.window = ctx;
  ctx.document = { documentElement: {}, getElementById: (id) => els[id] || (els[id] = mkEl()) };
  vm.createContext(ctx);
  vm.runInContext(SRC, ctx);
  const app = { innerHTML: '', querySelectorAll: () => [] };
  const call = (prefix) => calls.filter((c) => c.url.startsWith(prefix));
  return { page, app, els, charts, call };
}

const sample = (ts, mv, obs, name, snr, hops) => ({
  timestamp: ts, battery_mv: mv, source: 'neighbor', observer_id: obs, observer_name: name, snr, reply_hops: hops,
});
const A = { source: 'neighbor', observer_id: OBS_A, observer_name: 'Corstorphine OBS', latest_mv: 3850,
  latest_ts: '2026-10-23T18:00:00Z', samples: 2, median_snr: 7.25, reply_hops: 0 };
const B = { source: 'neighbor', observer_id: OBS_B, observer_name: 'Pentland <OBS>', latest_mv: 3200,
  latest_ts: '2026-10-23T19:00:00Z', samples: 1, median_snr: -4, reply_hops: 2 };

(async () => {
  console.log('Node Analytics: neighbour battery readings');
  const t = load();
  t.page.init(t.app, PK + '/analytics');
  t.call('/nodes/')[0].resolve(analyticsData());
  await flush();
  t.call('/nodes/' + PK + '/battery')[0].resolve({
    status: 'ok', latest_mv: 3850, latest_ts: A.latest_ts,
    thresholds: { low_mv: 3300, critical_mv: 3000 },
    samples: [
      sample('2026-10-23T17:00:00Z', 3900, OBS_A, 'Corstorphine OBS', 7.5, 0),
      sample('2026-10-23T18:00:00Z', 3850, OBS_A, 'Corstorphine OBS', 7.0, 0),
      sample('2026-10-23T19:00:00Z', 3200, OBS_B, 'Pentland <OBS>', -4, 2),
    ],
    sources: [B, A], selected: A,
  });
  await flush();

  const by = t.els.batteryReportedBy;
  assert(by.style.display === '', 'the Reported by line is shown');
  assert(by.innerHTML.includes('Corstorphine OBS') && by.innerHTML.includes('#/observers/' + OBS_A.toUpperCase()),
    'it names and links the selected observer');
  assert(by.innerHTML.includes('direct') && by.innerHTML.includes('+7.3 dB'), 'with the route and median SNR');
  assert(by.innerHTML.includes('also reported by') && by.innerHTML.includes('Pentland &lt;OBS>'),
    'and lists the other observers, escaped');

  const cfg = t.charts[t.charts.length - 1];
  const labels = cfg.data.datasets.map((d) => d.label);
  assert(labels.includes('Corstorphine OBS') && labels.includes('Pentland <OBS>'), 'one chart line per observer');
  const a = cfg.data.datasets.find((d) => d.label === 'Corstorphine OBS');
  assert(a.data.length === 2 && a.data[1].y === 3850, 'each line holds that observer\'s readings');
  assert(labels.includes('Low threshold') && labels.includes('Critical'), 'threshold lines stay');

  const badge = t.els.batteryStatusBadge.innerHTML;
  assert(badge.includes('<svg') && badge.includes('3850 mV'), 'the status badge renders its icon as markup, not text');

  const table = t.els.batteryReadings.innerHTML;
  assert(t.els.batteryReadings.style.display === '', 'the readings table is shown');
  assert(table.indexOf('Pentland') < table.indexOf('Corstorphine'), 'newest reading first');
  assert(table.includes('via 2 hops') && table.includes('-4.0 dB'), 'rows carry route and SNR');

  console.log('Node Analytics: a single reading');
  {
    const w = load();
    w.page.init(w.app, PK + '/analytics');
    w.call('/nodes/')[0].resolve(analyticsData());
    await flush();
    w.call('/nodes/' + PK + '/battery')[0].resolve({
      status: 'ok', latest_mv: 3994, thresholds: { low_mv: 3300, critical_mv: 3000 },
      samples: [sample('2026-10-23T23:56:10Z', 3994, OBS_A, 'Corstorphine OBS', -5, 0)], sources: [A], selected: A,
    });
    await flush();
    const x = w.charts[w.charts.length - 1].options.scales.x;
    assert(x.max - x.min > 0, 'the time axis has a span, so the lone point is drawn');
  }

  console.log('Node Analytics: own status reports only');
  const u = load();
  u.page.init(u.app, PK + '/analytics');
  u.call('/nodes/')[0].resolve(analyticsData());
  await flush();
  const self = { source: 'self', latest_mv: 4100, latest_ts: '2026-10-23T18:00:00Z', samples: 1 };
  u.call('/nodes/' + PK + '/battery')[0].resolve({
    status: 'ok', latest_mv: 4100, thresholds: { low_mv: 3300, critical_mv: 3000 },
    samples: [{ timestamp: '2026-10-23T18:00:00Z', battery_mv: 4100, source: 'self' }],
    sources: [self], selected: self,
  });
  await flush();
  assert(u.els.batteryReportedBy.innerHTML.includes('Own status report') && !u.els.batteryReportedBy.innerHTML.includes('<a '),
    'a self report says so, with no observer link');
  assert(!u.els.batteryReportedBy.innerHTML.includes('also reported by'), 'no other sources listed');

  console.log('\n' + passed + ' passed, ' + failed + ' failed');
  if (failed) process.exit(1);
})();

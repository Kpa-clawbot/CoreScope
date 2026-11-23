/* budgets.js — performance budgets for the #2428 map-provider work, evaluated
 * by report.js against results.json from run.js.
 *
 * Rule fields:
 *   id, description
 *   metric     summary key in results.json (scenarios[x].summary[metric])
 *   stat       which summary statistic to compare (default 'median')
 *   kind       'relative' — candidate MEDIAN vs baseline MEDIAN (needs a
 *                           candidate). Over +maxPct is a regression only
 *                           when the candidate median is also above the
 *                           baseline p95 ('fail', red); over +maxPct but
 *                           within the baseline's own run-to-run spread is
 *                           'noise' (amber, not a failure). Measured p95/median
 *                           on the reference machine is 1.05-1.25 for tile and
 *                           long-task times, i.e. wider than 10%.
 *              'absolute' — fail if the value exceeds max (checked on the
 *                           candidate, or on the baseline when it is alone)
 *   maxPct / max
 *   floor      relative rules only: when the baseline value is below floor,
 *              compare against floor instead (a 0 ms baseline would make any
 *              long task an infinite % regression)
 *   applies(scenario) -> boolean   scenario = results.json scenario record
 */
'use strict';

const isRaster = (sc) => (sc.format || 'raster') !== 'vector';
const has = (sc, action) => (sc.actions || []).indexOf(action) >= 0;
const cold = (sc) => has(sc, 'tiles') && !has(sc, 'switch') && !has(sc, 'panzoom') && !has(sc, 'vcr');

const REGRESSION_PCT = 10; // report.js flags any lower-is-better metric that grows more than this

const RULES = [
  {
    id: 'raster-all-tiles', description: 'Raster: time to all visible tiles ≤ +10% vs baseline',
    metric: 'allTilesMs', kind: 'relative', maxPct: 10, floor: 0,
    applies: (sc) => isRaster(sc) && cold(sc),
  },
  {
    id: 'raster-markers', description: 'Raster: time to first marker ≤ +10% vs baseline',
    metric: 'firstMarkerMs', kind: 'relative', maxPct: 10, floor: 0,
    applies: (sc) => isRaster(sc) && cold(sc),
  },
  {
    id: 'raster-long-tasks', description: 'Raster: long-task total ≤ +10% vs baseline (floor 50 ms)',
    metric: 'longTaskTotalMs', kind: 'relative', maxPct: 10, floor: 50,
    applies: (sc) => isRaster(sc) && cold(sc),
  },
  {
    // The kernel alone (256 px tile). hillshadeTileMsMedian (createTile to
    // tileload) also contains the elevation fetch and decode, so it is
    // reported but not budgeted.
    id: 'hillshade-tile', description: 'Hillshade: per-tile shading kernel ≤ 5 ms median',
    metric: 'hillshadeKernel256MsMedian', kind: 'absolute', max: 5,
    applies: () => true, // only scenarios that ran the kernel timing carry the metric
  },
  {
    id: 'vector-no-fallback', description: 'Vector scenarios: MapLibre style loads (raster fallback not used)',
    metric: 'vectorFallbackUsed', stat: 'max', kind: 'absolute', max: 0,
    applies: (sc) => !isRaster(sc),
  },
  {
    id: 'switch-heap-growth', description: 'Heap growth after 50 provider switches ≤ 5% (forced GC)',
    metric: 'heapGrowthPct', kind: 'absolute', max: 5,
    applies: (sc) => has(sc, 'switch'),
  },
  {
    id: 'raster-no-maplibre', description: 'Raster-only scenarios never request MapLibre',
    metric: 'maplibreRequests', stat: 'max', kind: 'absolute', max: 0,
    applies: (sc) => isRaster(sc),
  },
];

/**
 * Evaluate one rule for one scenario. Returns
 * { status: 'pass'|'noise'|'fail'|'n/a', value, base, baseP95, deltaPct, note }.
 */
function evaluate(rule, scBase, scCand) {
  const stat = rule.stat || 'median';
  const pick = (rec) => (rec && rec.status === 'ok' && rec.summary && rec.summary[rule.metric]) ? rec.summary[rule.metric][stat] : null;
  const subject = scCand || scBase;
  if (!subject || !rule.applies(subject.scenario || {})) return null;
  if (rule.kind === 'absolute') {
    const v = pick(subject);
    if (v == null) return { status: 'n/a', value: null, note: 'metric not recorded' };
    return { status: v <= rule.max ? 'pass' : 'fail', value: v, note: `≤ ${rule.max}` };
  }
  if (!scCand) return { status: 'n/a', value: pick(scBase), note: 'no candidate' };
  const b = pick(scBase), c = pick(scCand);
  if (b == null || c == null) return { status: 'n/a', value: c, base: b, note: 'metric missing on one side' };
  const ref = Math.max(b, rule.floor || 0);
  const deltaPct = ref > 0 ? (c - ref) / ref * 100 : (c > 0 ? Infinity : 0);
  const bs = scBase.summary[rule.metric];
  const baseP95 = bs ? Math.max(bs.p95, rule.floor || 0) : null;
  const status = deltaPct <= rule.maxPct ? 'pass' : (baseP95 != null && c <= baseP95 ? 'noise' : 'fail');
  return { status: status, value: c, base: b, baseP95: bs ? bs.p95 : null, deltaPct: b > 0 ? (c - b) / b * 100 : null, note: `≤ +${rule.maxPct}% (or ≤ baseline p95)` };
}

module.exports = { RULES, REGRESSION_PCT, evaluate };

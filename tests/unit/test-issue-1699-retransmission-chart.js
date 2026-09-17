/**
 * #1699: Topology tab, retransmission pressure chart.
 *
 * Loads public/analytics.js into a stub browser context and exercises the
 * pure render helpers it exposes for testing:
 *   - _analyticsRetransmissionBucketFor(window) picks a bucket per window
 *   - _analyticsRenderRetransmissionChart(data) renders the SVG + captions
 *
 * Usage: node test-issue-1699-retransmission-chart.js
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

const ctx = {
  console, Math, JSON, Date, Number, String, Array, Object, Set, Map, RegExp, URLSearchParams,
  setTimeout, clearTimeout, requestAnimationFrame: () => 0,
  getComputedStyle: () => ({ getPropertyValue: () => '' }),
  localStorage: { getItem: () => null, setItem() {}, removeItem() {} },
  registerPage: () => {},
  api: async () => ({}),
  CLIENT_TTL: {},
  RegionFilter: { regionQueryString: () => '' },
  AreaFilter: { areaQueryString: () => '' },
};
ctx.window = ctx;
ctx.document = {
  documentElement: {},
  createElement: () => ({ style: {}, addEventListener() {} }),
  addEventListener() {}, removeEventListener() {},
  querySelector: () => null, querySelectorAll: () => [],
  getElementById: () => null,
};
vm.createContext(ctx);
vm.runInContext(fs.readFileSync(path.join(REPO_ROOT, 'public/analytics.js'), 'utf8'), ctx);

const bucketFor = ctx._analyticsRetransmissionBucketFor;
const render = ctx._analyticsRenderRetransmissionChart;

console.log('\n=== #1699 retransmission chart: exports ===');
assert(typeof bucketFor === 'function', '_analyticsRetransmissionBucketFor exposed');
assert(typeof render === 'function', '_analyticsRenderRetransmissionChart exposed');
if (typeof bucketFor !== 'function' || typeof render !== 'function') {
  console.log(`\n${passed} passed, ${failed} failed`);
  process.exit(1);
}

console.log('\n=== bucket per window ===');
assert(bucketFor('') === '1h', 'all data -> 1h (matches the server default shape)');
assert(bucketFor('1h') === '5m', '1h window -> 5m');
assert(bucketFor('24h') === '1h', '24h window -> 1h');
assert(bucketFor('7d') === '1h', '7d window -> 1h');
assert(bucketFor('30d') === '6h', '30d window -> 6h');
assert(bucketFor('bogus') === '1h', 'unknown window -> 1h');

console.log('\n=== empty data ===');
const empty = render({ bucket_seconds: 3600, summary: { packets: 0 }, buckets: [] });
assert(/No flood packets/.test(empty), 'empty buckets render a no-data message');
assert(!/<svg/.test(empty), 'empty buckets render no SVG');
assert(/No flood packets/.test(render(null)), 'null data renders the no-data message');

console.log('\n=== chart ===');
const H = 3600;
const data = {
  bucket_seconds: H,
  summary: { packets: 400, avg_repeaters: 7.5, observers: 20, one_byte_packets: 100, no_repeater_packets: 3 },
  buckets: [
    { start: '2026-09-13T10:00:00Z', packets: 100, repeater_sum: 700, avg_repeaters: 7, observers: 18 },
    { start: '2026-09-13T11:00:00Z', packets: 100, repeater_sum: 800, avg_repeaters: 8, observers: 20 },
    // 3h gap: the line must break here, not interpolate across it.
    { start: '2026-09-13T14:00:00Z', packets: 100, repeater_sum: 600, avg_repeaters: 6, observers: 12 },
    { start: '2026-09-13T15:00:00Z', packets: 100, repeater_sum: 900, avg_repeaters: 9, observers: 19 },
  ],
};
const html = render(data);
assert(/<svg/.test(html), 'renders an SVG');
const avgLines = html.match(/class="rtx-avg-line"/g) || [];
assert(avgLines.length === 2, 'avg line split into 2 segments at the gap (got ' + avgLines.length + ')');
const obsLines = html.match(/class="rtx-obs-line"/g) || [];
assert(obsLines.length === 2, 'observer line split into 2 segments at the gap (got ' + obsLines.length + ')');
assert(/proxy/i.test(html), 'labelled as a proxy');
assert(/not a measured collision rate/i.test(html), 'says it is not a measured collision rate');
assert(/observer/i.test(html) && /coverage/i.test(html), 'caption names observer coverage bias');
assert(/lower bound/i.test(html), 'caption names the 1-byte prefix lower bound');
assert(/counts once per flood/i.test(html), 'caption says a prefix counts once per flood');
assert(/more than 5 minutes/i.test(html) && /new flood/i.test(html), 'caption explains the 5 minute flood event split');
assert(!/where they were first seen/i.test(html), 'caption no longer buckets by first_seen');
assert(/25%/.test(html), '1-byte share shown as 25% (100 of 400)');
assert(/7\.5/.test(html), 'summary average shown');
assert(/area filter/i.test(html), 'caption says the area filter does not apply');
assert(!/#[0-9a-fA-F]{3,8}\b/.test(html), 'no hardcoded hex colours in the chart markup');
assert(/var\(--accent\)/.test(html), 'uses CSS variables for colours');

console.log('\n=== y axis and points ===');
const avgPts = (html.match(/class="rtx-avg-line" points="([^"]*)"/g) || [])
  .map(s => s.replace(/.*points="([^"]*)"/, '$1').trim().split(/\s+/).length);
assert(avgPts.join(',') === '2,2', 'each segment carries its 2 points (got ' + avgPts.join(',') + ')');
assert(/>9</.test(html) || /9\.0/.test(html) || />10</.test(html), 'y axis reaches the max average');

function barsInPlot(markup) {
  const bars = [...markup.matchAll(/class="rtx-packets-bar" x="([\d.-]+)" y="[\d.-]+" width="([\d.-]+)"/g)];
  // viewBox 0..800 with 40px left and 16px right padding.
  return bars.length > 0 && bars.every(m => Number(m[1]) >= 40 && Number(m[1]) + Number(m[2]) <= 784);
}
assert(barsInPlot(html), 'packet bars stay inside the plot area');

console.log('\n=== single bucket ===');
const one = render({ bucket_seconds: H, summary: { packets: 5, avg_repeaters: 3, observers: 2, one_byte_packets: 0 },
  buckets: [{ start: '2026-09-13T10:00:00Z', packets: 5, repeater_sum: 15, avg_repeaters: 3, observers: 2 }] });
assert(/<circle[^>]*class="rtx-avg-dot"/.test(one), 'a lone bucket is drawn as a dot');
assert(barsInPlot(one), 'a lone bucket bar stays inside the plot area');

console.log('\n=== untrusted server strings ===');
const evil = render({ bucket_seconds: H, summary: { packets: 1, avg_repeaters: 1, observers: 1, one_byte_packets: 0 },
  buckets: [{ start: '<img src=x onerror=alert(1)>', packets: 1, repeater_sum: 1, avg_repeaters: 1, observers: 1 }] });
assert(!/<img/.test(evil), 'an unparseable bucket start is dropped, never echoed');
const tipHtml = render(data);
assert(/<title>2026-09-13T10:00:00Z\nAvg distinct repeaters: 7\.0\nFlood packets: 100\nObservers: 18<\/title>/.test(tipHtml),
  'per-bucket tooltip carries start, average, packets and observers');

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed ? 1 : 0);

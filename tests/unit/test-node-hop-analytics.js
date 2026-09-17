'use strict';
const REPO_ROOT = require('path').resolve(__dirname, '..', '..');
// Issue #1812: hop count at this node, node analytics page. Loads the browser
// IIFE in a vm sandbox (pattern from test-node-reach-coverage.js) and exercises
// the filter, histogram, box statistics and section render.
const assert = require('assert');
const fs = require('fs');
const path = require('path');
const vm = require('vm');

const code = fs.readFileSync(path.join(REPO_ROOT, 'public', 'node-hop-analytics.js'), 'utf8');
const sandbox = { window: {}, console };
vm.createContext(sandbox);
vm.runInContext(code, sandbox);
const H = sandbox.window.NodeHopAnalytics;

let passed = 0;
function test(name, fn) {
  fn();
  passed++;
  console.log('  ok ' + name);
}

const packets = [
  { hash: 'a', hops: 0, tags: ['flood', 'unscoped'] },
  { hash: 'b', hops: 2, tags: ['flood', 'scoped'] },
  { hash: 'c', hops: 5, tags: ['flood', 'unscoped', 'advert'] },
  { hash: 'd', hops: 1, tags: ['flood', 'scoped', 'advert'] },
];

test('filters follow the firmware limits: flood.max all, .unscoped and .advert by tag', () => {
  assert.deepStrictEqual(Array.from(H.filterHops(packets, 'flood')), [0, 2, 5, 1]);
  assert.deepStrictEqual(Array.from(H.filterHops(packets, 'flood_unscoped')), [0, 5]);
  assert.deepStrictEqual(Array.from(H.filterHops(packets, 'flood_adverts')), [5, 1]);
  assert.deepStrictEqual(Array.from(H.filterHops(packets, 'bogus')), [0, 2, 5, 1], 'unknown filter falls back to flood');
});

test('histogram has one bucket per hop count from 0 to max', () => {
  assert.deepStrictEqual(Array.from(H.hopHistogram([0, 1, 1, 3])), [1, 2, 0, 1]);
  assert.deepStrictEqual(Array.from(H.hopHistogram([])), []);
});

test('box stats: interpolated quartiles, 1.5 IQR whiskers, outliers counted', () => {
  // 8 lies between the 1.5 IQR fence (6) and a 3 IQR fence (9), so the
  // fence factor decides whether it is an outlier.
  const s = H.hopBoxStats([8, 0, 1, 3, 1]);
  assert.strictEqual(s.n, 5);
  assert.strictEqual(s.min, 0);
  assert.strictEqual(s.q1, 1);
  assert.strictEqual(s.median, 1);
  assert.strictEqual(s.q3, 3);
  assert.strictEqual(s.max, 8);
  assert.strictEqual(s.whiskerLow, 0);
  assert.strictEqual(s.whiskerHigh, 3, 'upper fence is q3 + 1.5*IQR = 6, so the whisker stops at 3');
  assert.strictEqual(s.outliers, 1);

  const even = H.hopBoxStats([4, 1, 3, 2]);
  assert.strictEqual(even.q1, 1.75);
  assert.strictEqual(even.median, 2.5);
  assert.strictEqual(even.q3, 3.25);
  assert.strictEqual(H.hopBoxStats([]), null);
});

test('section render: firmware-named chips, active filter, summary and ambiguous note', () => {
  const html = H.renderHopSection({ packets, ambiguous: 2 }, 'flood_adverts');
  assert.ok(html.includes('>flood.max<'), 'flood.max chip');
  assert.ok(html.includes('>flood.max.unscoped<'), 'flood.max.unscoped chip');
  assert.ok(html.includes('>flood.max.advert<'), 'flood.max.advert chip');
  assert.ok(/data-hop-filter="flood_adverts"[^>]*aria-pressed="true"/.test(html), 'selected chip is pressed');
  assert.ok(/data-hop-filter="flood"[^>]*aria-pressed="false"/.test(html), 'other chips are not pressed');
  assert.ok(html.includes('2 packets'), 'packet count for the advert filter');
  assert.ok(html.includes('median 3'), 'median of [5, 1]');
  assert.ok(html.includes('max 5'), 'max of [5, 1]');
  assert.ok(html.includes('id="hopCountChart"'), 'chart canvas');
  assert.ok(html.includes('2 packets left out'), 'ambiguous packets are reported');
});

test('section render: empty filter shows a message instead of a chart', () => {
  const html = H.renderHopSection({ packets: [packets[1]], ambiguous: 0 }, 'flood_unscoped');
  assert.ok(!html.includes('id="hopCountChart"'), 'no canvas without data');
  assert.ok(html.includes('No forwarded'), 'empty message');
  assert.ok(!html.includes('left out'), 'no ambiguous note when zero');
});

console.log('node-hop-analytics: ' + passed + ' tests passed');

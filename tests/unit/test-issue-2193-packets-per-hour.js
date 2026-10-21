/**
 * #2193: the Packets / Hour chart on long-retention instances.
 *
 * Drives window._analyticsPacketsPerHourCard(pph) from public/analytics.js:
 * months of hours roll up to daily bars with few labels, missing hours count
 * as zero, and the chart starts where the data does, not at a thin run of
 * old packets a short store keeps because they were heard again.
 *
 * Usage: node tests/unit/test-issue-2193-packets-per-hour.js
 */
'use strict';
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
  registerPage: () => {}, api: async () => ({}), CLIENT_TTL: {},
  RegionFilter: { regionQueryString: () => '' }, AreaFilter: { areaQueryString: () => '' },
};
ctx.window = ctx;
ctx.document = {
  documentElement: {}, createElement: () => ({ style: {}, addEventListener() {} }),
  addEventListener() {}, removeEventListener() {}, querySelector: () => null, querySelectorAll: () => [], getElementById: () => null,
};
vm.createContext(ctx);
vm.runInContext(fs.readFileSync(path.join(__dirname, '..', '..', 'public/analytics.js'), 'utf8'), ctx);
const card = ctx._analyticsPacketsPerHourCard;
if (typeof card !== 'function') { console.error('  ✗ _analyticsPacketsPerHourCard not exposed'); process.exit(1); }

// n hours ending 2026-10-12T17; count(k) gives each hour's count (0 = left
// out, the way the server sends empty hours).
const END = Date.UTC(2026, 9, 12, 17);
function hours(n, count) {
  const out = [];
  for (let k = 0; k < n; k++) {
    const c = count ? count(k, n - 1 - k) : 20 + (k % 24);
    if (c) out.push({ hour: new Date(END - (n - 1 - k) * 3600e3).toISOString().slice(0, 13), count: c });
  }
  return out;
}
const bars = (html) => (html.match(/<rect /g) || []).length;
const labels = (html) => (html.match(/<text [^>]*font-size="9"/g) || []).length;

let c = card(hours(2932));
assert(/Packets \/ Day/.test(c.html) && bars(c.html) === 123, '122 days: 123 daily bars, titled Packets / Day (got ' + bars(c.html) + ')');
assert(labels(c.html) >= 4 && labels(c.html) <= 12, 'with 4 to 12 labels (got ' + labels(c.html) + ')');

c = card(hours(168));
assert(/Packets \/ Hour/.test(c.html) && bars(c.html) === 168, '7 days: 168 hourly bars (got ' + bars(c.html) + ')');

c = card(hours(48, (k) => (k >= 10 && k < 15 ? 0 : 5)));
assert(bars(c.html) === 48, 'five missing hours drawn as zero: 48 bars (got ' + bars(c.html) + ')');

// A 7-day store: weeks of a few old packets a day, then 8 days of data.
c = card(hours(44 * 24, (k, back) => (back < 8 * 24 ? 300 + (k % 140) : k % 24 === 9 ? 1 + (k % 30) : 0)));
assert(c.from === Date.UTC(2026, 9, 4, 18), 'starts on the hour the data does, not at the first straggler (from ' + new Date(c.from || 0).toISOString() + ')');

c = card(hours(60 * 24, (k) => (k >= 240 && k < 360 ? 0 : 50)));
assert(c.from === Date.UTC(2026, 7, 13), 'an outage after the data starts is not taken for its start (from ' + new Date(c.from || 0).toISOString() + ')');

assert(/No packets/.test(card([]).html), 'no data says so');

// The crop is for "All data", where a short store's stragglers sit weeks
// before the rest. In a window the user picked, the server has already cut
// to it, so a quieter early stretch is real and stays: 90 days, the first
// 30 at 1 packet an hour, then 100 an hour.
const quietStart = hours(90 * 24, (k) => (k < 30 * 24 ? 1 : 100));
c = card(quietStart, { window: '30d' });
assert(bars(c.html) === 91 && !/since/.test(c.html), 'in a picked window nothing is cropped: 91 daily bars (got ' + bars(c.html) + ')');

// Under "All data" a crop is said on the card, so it is never invisible.
c = card(hours(44 * 24, (k, back) => (back < 8 * 24 ? 300 + (k % 140) : k % 24 === 9 ? 1 + (k % 30) : 0)), { window: '' });
assert(/since 10-04/.test(c.html), 'a cropped chart says where it starts (since 10-04)');
c = card(hours(2932), { window: '' });
assert(!/since/.test(c.html), 'and an uncropped one says nothing');

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed > 0 ? 1 : 0);

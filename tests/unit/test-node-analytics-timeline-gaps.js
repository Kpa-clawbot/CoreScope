/**
 * Node analytics activity timeline: the server sends only the hours that
 * hold packets, so the chart must place every hour of the covered window on
 * the axis, silent hours included. Without that an 11 h outage collapsed into
 * two neighbouring bars. Loads the real public/node-analytics.js in a vm
 * sandbox.
 */
'use strict';
const fs = require('fs');
const path = require('path');
const vm = require('vm');
const assert = require('assert');

const REPO_ROOT = path.resolve(__dirname, '..', '..');
let passed = 0, failed = 0;
function t(name, fn) {
  try { fn(); passed++; console.log('  ✓ ' + name); }
  catch (e) { failed++; console.error('  ✗ ' + name + ': ' + e.message); }
}

const ctx = { window: {}, registerPage: () => {}, Date, Math, Number, String, isNaN, console };
vm.createContext(ctx);
vm.runInContext(fs.readFileSync(path.join(REPO_ROOT, 'public', 'node-analytics.js'), 'utf8'), ctx);
const fill = ctx.window.NodeAnalyticsTimeline && ctx.window.NodeAnalyticsTimeline.fill;

console.log('node-analytics.js activity timeline gaps');

t('exposes NodeAnalyticsTimeline.fill', () => {
  assert.strictEqual(typeof fill, 'function');
});

t('silent hours between two active hours become zero buckets', () => {
  const out = fill(
    [{ bucket: '2026-11-17T18:00:00Z', count: 6, certain: 5 }, { bucket: '2026-11-17T21:00:00Z', count: 2, certain: 0 }],
    { from: '2026-11-17T18:00:00Z', to: '2026-11-17T21:30:00Z' });
  assert.deepStrictEqual(JSON.parse(JSON.stringify(out)), [
    { bucket: '2026-11-17T18:00:00Z', count: 6, certain: 5 },
    { bucket: '2026-11-17T19:00:00Z', count: 0, certain: 0 },
    { bucket: '2026-11-17T20:00:00Z', count: 0, certain: 0 },
    { bucket: '2026-11-17T21:00:00Z', count: 2, certain: 0 },
  ]);
});

t('runs from the hour of the window start to the hour of its end', () => {
  const out = fill([{ bucket: '2026-11-18T02:00:00Z', count: 3 }],
    { from: '2026-11-18T00:40:00Z', to: '2026-11-18T03:05:00Z' });
  assert.deepStrictEqual(Array.from(out, b => b.bucket), [
    '2026-11-18T00:00:00Z', '2026-11-18T01:00:00Z', '2026-11-18T02:00:00Z', '2026-11-18T03:00:00Z']);
  assert.deepStrictEqual(Array.from(out, b => b.count), [0, 0, 3, 0]);
});

t('a partial window starts at dataFrom, not at the requested range start', () => {
  const out = fill([{ bucket: '2026-11-18T05:00:00Z', count: 1 }],
    { from: '2026-11-11T06:00:00Z', dataFrom: '2026-11-18T04:10:00Z', to: '2026-11-18T05:20:00Z', partial: true });
  assert.deepStrictEqual(Array.from(out, b => b.bucket), ['2026-11-18T04:00:00Z', '2026-11-18T05:00:00Z']);
});

t('a node with no packets gets a full row of zeros', () => {
  const out = fill([], { from: '2026-11-18T00:00:00Z', to: '2026-11-18T23:59:00Z' });
  assert.strictEqual(out.length, 24);
  assert.ok(out.every(b => b.count === 0));
});

t('filled buckets feed timelineSeries as zero certain and zero uncertain', () => {
  const tl = fill([{ bucket: '2026-11-17T18:00:00Z', count: 3, certain: 1 }],
    { from: '2026-11-17T17:00:00Z', to: '2026-11-17T18:10:00Z' });
  const s = ctx.window.NodeAnalyticsTimeline.series(tl);
  assert.deepStrictEqual(Array.from(s.certain), [0, 1]);
  assert.deepStrictEqual(Array.from(s.uncertain), [0, 2]);
});

t('missing or malformed time range: buckets unchanged', () => {
  const tl = [{ bucket: '2026-11-17T18:00:00Z', count: 6 }];
  assert.strictEqual(fill(tl, null), tl);
  assert.strictEqual(fill(tl, { from: 'garbage', to: '2026-11-17T20:00:00Z' }), tl);
  assert.strictEqual(fill(tl, { from: '2026-11-17T20:00:00Z', to: '2026-11-17T18:00:00Z' }), tl);
});

t('an end exactly on the hour includes that hour', () => {
  const out = fill([], { from: '2026-11-17T17:00:00Z', to: '2026-11-17T19:00:00Z' });
  assert.deepStrictEqual(Array.from(out, b => b.bucket), ['2026-11-17T17:00:00Z', '2026-11-17T18:00:00Z', '2026-11-17T19:00:00Z']);
});

t('7 days stay hourly', () => {
  const out = fill([], { from: '2026-11-12T10:00:00Z', to: '2026-11-19T10:00:00Z' });
  assert.strictEqual(out.length, 169);
  assert.ok(out.every(b => !b.day));
});

t('more than 8 days covered: one bucket per UTC day, counts and certain summed', () => {
  const out = fill([
    { bucket: '2026-11-18T01:00:00Z', count: 3, certain: 1 },
    { bucket: '2026-11-18T22:00:00Z', count: 2, certain: 2 },
    { bucket: '2026-11-19T09:00:00Z', count: 1 },
  ], { from: '2026-11-05T10:00:00Z', to: '2026-11-19T10:00:00Z' });
  assert.strictEqual(out.length, 15);
  assert.strictEqual(out[0].bucket, '2026-11-05T00:00:00Z');
  assert.deepStrictEqual(JSON.parse(JSON.stringify(out.slice(-2))), [
    { bucket: '2026-11-18T00:00:00Z', count: 5, certain: 3, day: true },
    { bucket: '2026-11-19T00:00:00Z', count: 1, certain: 1, day: true },
  ]);
  assert.ok(out.every(b => b.day === true));
  assert.ok(out.slice(0, -2).every(b => b.count === 0));
});

t('a 365-day window gives 366 day buckets', () => {
  const out = fill([], { from: '2025-11-19T10:00:00Z', to: '2026-11-19T10:00:00Z' });
  assert.strictEqual(out.length, 366);
});

t('a legacy space-format bucket lands on its hour, not beside it', () => {
  const out = fill([{ bucket: '2026-11-17 18:00:00Z', count: 2 }],
    { from: '2026-11-17T17:00:00Z', to: '2026-11-17T19:00:00Z' });
  assert.deepStrictEqual(Array.from(out, b => b.bucket), ['2026-11-17T17:00:00Z', '2026-11-17T18:00:00Z', '2026-11-17T19:00:00Z']);
  assert.deepStrictEqual(Array.from(out, b => b.count), [0, 2, 0]);
});

t('a bucket outside the window is kept, not dropped', () => {
  const out = fill([{ bucket: '2026-11-17T10:00:00Z', count: 4 }, { bucket: '2026-11-17T18:00:00Z', count: 1 }],
    { from: '2026-11-17T17:00:00Z', to: '2026-11-17T18:10:00Z' });
  assert.deepStrictEqual(Array.from(out, b => b.bucket), ['2026-11-17T10:00:00Z', '2026-11-17T17:00:00Z', '2026-11-17T18:00:00Z']);
  assert.deepStrictEqual(Array.from(out, b => b.count), [4, 0, 1]);
});

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed ? 1 : 0);

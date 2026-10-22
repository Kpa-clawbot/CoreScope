/**
 * #2260 — node analytics timeline: each bucket splits into packets that can
 * only be this node (certain) and packets attributed through a hop prefix it
 * shares (uncertain). Loads the real public/node-analytics.js in a vm sandbox.
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
const series = ctx.window.NodeAnalyticsTimeline && ctx.window.NodeAnalyticsTimeline.series;

console.log('node-analytics.js certain timeline (#2260)');

t('exposes NodeAnalyticsTimeline.series', () => {
  assert.strictEqual(typeof series, 'function');
});

t('splits each bucket into certain and uncertain', () => {
  const s = series([{ count: 80, certain: 80 }, { count: 3, certain: 0 }, { count: 10, certain: 4 }]);
  assert.deepStrictEqual(Array.from(s.certain), [80, 0, 4]);
  assert.deepStrictEqual(Array.from(s.uncertain), [0, 3, 6]);
});

t('the silent repeater of #2260: after the outage only uncertain bars remain', () => {
  const s = series([{ count: 94, certain: 94 }, { count: 2, certain: 0 }, { count: 1, certain: 0 }]);
  assert.deepStrictEqual(Array.from(s.certain), [94, 0, 0]);
  assert.strictEqual(s.uncertain.reduce((a, b) => a + b, 0), 3);
});

t('a server without the certain field counts everything as certain', () => {
  const s = series([{ count: 5 }, { count: 0 }]);
  assert.deepStrictEqual(Array.from(s.certain), [5, 0]);
  assert.deepStrictEqual(Array.from(s.uncertain), [0, 0]);
});

t('certain never exceeds count, strings are cast', () => {
  const s = series([{ count: '4', certain: '9' }]);
  assert.deepStrictEqual(Array.from(s.certain), [4]);
  assert.deepStrictEqual(Array.from(s.uncertain), [0]);
});

t('missing timeline yields empty series', () => {
  const s = series(undefined);
  assert.strictEqual(s.certain.length, 0);
  assert.strictEqual(s.uncertain.length, 0);
});

console.log(`\n${passed} passed, ${failed} failed`);
if (failed) process.exit(1);

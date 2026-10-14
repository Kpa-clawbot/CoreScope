/**
 * #661 — rolling 1 h TX airtime per observer: the shared badge helper used
 * by the RF Health tiles (analytics.js) and the observer list (observers.js).
 * Loads the real public/observer-airtime.js in a vm sandbox.
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

const ctx = { window: {}, Number, Math, String };
vm.createContext(ctx);
vm.runInContext(fs.readFileSync(path.join(REPO_ROOT, 'public', 'observer-airtime.js'), 'utf8'), ctx);
const A = ctx.window.ObserverAirtime;

console.log('observer-airtime.js');

t('exposes window.ObserverAirtime', () => {
  assert.ok(A && typeof A.format === 'function' && typeof A.isOver === 'function' && typeof A.badge === 'function');
});

t('format: null and undefined read as unknown', () => {
  assert.strictEqual(A.format(null), '—');
  assert.strictEqual(A.format(undefined), '—');
});

t('format: one decimal, tiny non-zero shares as <0.1%', () => {
  assert.strictEqual(A.format(0), '0.0%');
  assert.strictEqual(A.format(0.03), '<0.1%');
  assert.strictEqual(A.format(5.18), '5.2%');
  assert.strictEqual(A.format(100), '100.0%');
});

t('isOver: at or above the threshold warns, unknown never does', () => {
  assert.strictEqual(A.isOver(10, 10), true);
  assert.strictEqual(A.isOver(9.9, 10), false);
  assert.strictEqual(A.isOver(null, 10), false);
});

t('isOver: a share that displays as the threshold warns', () => {
  assert.strictEqual(A.format(9.96), '10.0%');
  assert.strictEqual(A.isOver(9.96, 10), true);
  assert.strictEqual(A.isOver(9.94, 10), false);
});

t('isOver: a missing threshold falls back to 10%', () => {
  assert.strictEqual(A.isOver(10, undefined), true);
  assert.strictEqual(A.isOver(9, 0), false);
});

t('badge: over threshold carries airtime-over and names the limit', () => {
  const html = A.badge(12.5, 10);
  assert.ok(/class="airtime-pct airtime-over"/.test(html), html);
  assert.ok(html.includes('12.5%'), html);
  assert.ok(html.includes('10%'), html);
});

t('badge: under threshold has no warning class', () => {
  const html = A.badge(2, 10);
  assert.ok(!html.includes('airtime-over'), html);
  assert.ok(html.includes('2.0%'), html);
});

t('badge: unknown renders a muted dash', () => {
  const html = A.badge(null, 10);
  assert.ok(html.includes('text-muted') && html.includes('—'), html);
});

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed ? 1 : 0);

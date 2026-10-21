/**
 * #748 — node analytics coverage notice: shown when the server's in-memory
 * window starts after the requested range. Loads the real
 * public/node-analytics.js in a vm sandbox.
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
const notice = ctx.window.NodeAnalyticsCoverage && ctx.window.NodeAnalyticsCoverage.notice;

console.log('node-analytics.js coverage notice');

t('exposes NodeAnalyticsCoverage.notice', () => {
  assert.strictEqual(typeof notice, 'function');
});

t('memory limit: names the start date, the window and the requested days', () => {
  const html = notice({ from: '2025-10-14T12:00:00Z', to: '2026-10-14T12:00:00Z', days: 365, dataFrom: '2026-09-30T09:12:00Z', dataFromReason: 'memory', partial: true });
  assert.ok(html.includes('data-testid="analytics-coverage-notice"'), html);
  assert.ok(html.includes('Data from 2026-09-30'), html);
  assert.ok(html.includes('about 14 days'), html);
  assert.ok(html.includes('not the full 365 days'), html);
});

t('database younger than the range: says so, no memory claim', () => {
  const html = notice({ to: '2026-10-14T12:00:00Z', days: 365, dataFrom: '2026-07-01T00:00:00Z', dataFromReason: 'database', partial: true });
  assert.ok(html.includes('Data from 2026-07-01: the database holds no older packets'), html);
  assert.ok(!html.includes('in memory'), html);
  assert.ok(html.includes('not the full 365 days'), html);
});

t('background load still running: says so', () => {
  const html = notice({ to: '2026-10-14T12:00:00Z', days: 30, dataFrom: '2026-10-13T12:00:00Z', dataFromReason: 'loading', partial: true });
  assert.ok(html.includes('the server is still loading older packets'), html);
});

t('window too short for rates: says so instead of the rates claim', () => {
  const html = notice({ to: '2026-10-14T12:00:00Z', days: 7, dataFrom: '2026-10-14T06:00:00Z', dataFromReason: 'memory', partial: true },
    { availabilityPct: null, avgPacketsPerDay: null });
  assert.ok(html.includes('too short for availability and packets/day'), html);
  assert.ok(!html.includes('not the full'), html);
});

t('rateText: null reads as a dash, numbers keep their suffix', () => {
  const rateText = ctx.window.NodeAnalyticsCoverage.rateText;
  assert.strictEqual(rateText(null, '%'), '—');
  assert.strictEqual(rateText(undefined), '—');
  assert.strictEqual(rateText(0, '%'), '0%');
  assert.strictEqual(rateText(12.5), '12.5');
});

t('full coverage: no notice', () => {
  assert.strictEqual(notice({ from: '2026-10-07T12:00:00Z', to: '2026-10-14T12:00:00Z', days: 7 }), '');
});

t('missing or malformed time range: no notice', () => {
  assert.strictEqual(notice(null), '');
  assert.strictEqual(notice({ partial: true, dataFrom: 'garbage', to: '2026-10-14T12:00:00Z', days: 30 }), '');
});

t('one day of data reads singular', () => {
  const html = notice({ to: '2026-10-14T12:00:00Z', days: 7, dataFrom: '2026-10-13T13:00:00Z', partial: true });
  assert.ok(html.includes('about 1 day of packets'), html);
});

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed ? 1 : 0);

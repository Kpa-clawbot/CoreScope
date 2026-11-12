/**
 * Node analytics "Heard by observers" card: one row per fixed observer that
 * heard the node directly. Loads the real public/node-analytics.js in a vm
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

const escapeHtml = s => String(s).replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const ctx = { window: {}, registerPage: () => {}, escapeHtml, Date, Math, Number, String, isNaN, console };
vm.createContext(ctx);
vm.runInContext(fs.readFileSync(path.join(REPO_ROOT, 'public', 'node-analytics.js'), 'utf8'), ctx);
const HB = ctx.window.NodeAnalyticsHeardBy;
const PK = 'c7d1000000000000000000000000000000000000000000000000000000000001';

function row(i, median) {
  return { observer_id: 'obs' + i, observer_name: 'Obs ' + i, count: 10 + i, medianSnr: median, p10Snr: median == null ? null : median - 3, p90Snr: median == null ? null : median + 2, recentMedianSnr: null, earlierMedianSnr: null };
}
const countRows = html => (html.match(/data-testid="heard-by-row"/g) || []).length;

console.log('node-analytics.js heard by observers');

t('exposes NodeAnalyticsHeardBy', () => {
  assert.strictEqual(typeof HB.html, 'function');
});

t('stripPct maps -20..+20 dB to 0..100 and clamps', () => {
  assert.strictEqual(HB.stripPct(-20), 0);
  assert.strictEqual(HB.stripPct(0), 50);
  assert.strictEqual(HB.stripPct(20), 100);
  assert.strictEqual(HB.stripPct(35), 100);
  assert.strictEqual(HB.stripPct(-40), 0);
});

t('changeCell: 3 dB or more is coloured, less is "=", missing is empty', () => {
  assert.deepStrictEqual(Object.assign({}, HB.changeCell(2, 6)), { text: '-4.0', cls: 'status-warn' });
  assert.deepStrictEqual(Object.assign({}, HB.changeCell(8, 4)), { text: '+4.0', cls: 'status-ok' });
  assert.deepStrictEqual(Object.assign({}, HB.changeCell(5, 6)), { text: '=', cls: '' });
  assert.deepStrictEqual(Object.assign({}, HB.changeCell(null, 6)), { text: '', cls: '' });
});

t('dotClass bands: >= 0 good, >= -10 fair, below weak', () => {
  assert.strictEqual(HB.dotClass(0), 'na-heard-good');
  assert.strictEqual(HB.dotClass(-10), 'na-heard-fair');
  assert.strictEqual(HB.dotClass(-10.25), 'na-heard-weak');
  assert.strictEqual(HB.dotClass(null), '');
});

const twelve = { heardBy: Array.from({ length: 12 }, (_, i) => row(i, 10 - i)) };

t('shows 10 rows and a "Show all 12" control', () => {
  const html = HB.html(twelve, { pubkey: PK });
  assert.strictEqual(countRows(html), 10);
  assert.ok(html.includes('Show all 12'));
});

t('showAll renders every row and no control', () => {
  const html = HB.html(twelve, { pubkey: PK, showAll: true });
  assert.strictEqual(countRows(html), 12);
  assert.ok(!html.includes('Show all'));
});

t('observer names link to the observer page', () => {
  const html = HB.html({ heardBy: [row(1, 4)] }, { pubkey: PK });
  assert.ok(html.includes('href="#/observers/obs1"'));
  assert.ok(html.includes('Obs 1'));
});

t('a row without a median shows its count and no strip', () => {
  const html = HB.html({ heardBy: [row(7, null)] }, { pubkey: PK });
  assert.strictEqual(countRows(html), 1);
  assert.ok(html.includes('>17<'));
  assert.ok(!html.includes('na-heard-dot'));
});

t('empty list explains itself and links to the node page table', () => {
  const html = HB.html({ heardBy: [] }, { pubkey: PK });
  assert.ok(html.includes('No observer heard this node directly in this window'));
  assert.ok(html.includes('An empty list is missing evidence, not proof of missing coverage.'));
  assert.ok(html.includes('href="#/nodes/' + PK + '?section=node-observers"'));
});

t('mobile receptions: collapsed group with summary and Reach link', () => {
  const html = HB.html({ heardBy: [row(1, 4)], mobileReceptions: { receptions: 42, receivers: 3, medianSnr: -1.5, maxDistanceKm: 12.34 } }, { pubkey: PK });
  assert.ok(/<details[^>]*>\s*<summary>Mobile receptions \(42\)<\/summary>/.test(html));
  assert.ok(!/<details[^>]*open/.test(html));
  assert.ok(html.includes('3 receivers'));
  assert.ok(html.includes('-1.5 dB'));
  assert.ok(html.includes('12.3 km'));
  assert.ok(html.includes('href="#/nodes/' + PK + '/reach"'));
});

t('mobile receptions: no distance without a node position, no group without the field', () => {
  const html = HB.html({ heardBy: [], mobileReceptions: { receptions: 1, receivers: 1, medianSnr: null, maxDistanceKm: null } }, { pubkey: PK });
  assert.ok(!html.includes(' km'));
  assert.ok(!HB.html({ heardBy: [row(1, 4)] }, { pubkey: PK }).includes('Mobile receptions'));
});

t('no hex colours in the card', () => {
  const html = HB.html(Object.assign({ mobileReceptions: { receptions: 1, receivers: 1, medianSnr: 1, maxDistanceKm: 1 } }, twelve), { pubkey: PK });
  assert.ok(!/#[0-9a-f]{3,6}\b/i.test(html.replace(/href="#\/[^"]*"/g, '')));
});

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed ? 1 : 0);

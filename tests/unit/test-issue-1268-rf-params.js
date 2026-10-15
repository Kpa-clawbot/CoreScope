/**
 * #1268 — LoRa RF parameters per node: the value shown in the node detail
 * views.
 * Loads the real public/rf-params.js in a vm sandbox.
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

const ctx = { window: {}, Number, Math, String, Date, Array, Object };
vm.createContext(ctx);
vm.runInContext(fs.readFileSync(path.join(REPO_ROOT, 'public', 'rf-params.js'), 'utf8'), ctx);
const R = ctx.window.RfParams;

const presets = [
  { title: 'EU/UK (Narrow) / Switzerland', freq: 869.618, sf: 8, bw: 62.5, cr: 8 },
  { title: 'USA/Canada (Recommended)', freq: 910.525, sf: 7, bw: 62.5, cr: 5 },
];
const euNarrow = { freq: 869.618, sf: 8, bw: 62.5, cr: 8, last_advert: '2026-10-13T12:00:00Z' };
const euCr5 = { freq: 869.618, sf: 8, bw: 62.5, cr: 5, last_advert: '2026-10-13T12:00:00Z' };

console.log('rf-params.js');

t('exposes window.RfParams', () => {
  assert.ok(R && typeof R.detailHtml === 'function');
});

t('detailHtml: values, preset and advert age', () => {
  const html = R.detailHtml(euNarrow, presets, Date.parse('2026-10-14T12:00:00Z'));
  assert.ok(html.includes('869.618 MHz'), html);
  assert.ok(html.includes('SF8') && html.includes('BW 62.5 kHz') && html.includes('CR 4/8'), html);
  assert.ok(html.includes('EU/UK (Narrow) / Switzerland'), html);
  assert.ok(html.includes('1d ago'), html);
});

t('detailHtml: no preset badge when only the coding rate differs', () => {
  const html = R.detailHtml(euCr5, presets, Date.now());
  assert.ok(html.includes('CR 4/5') && !html.includes('badge-region'), html);
});

t('detailHtml: frequency matches a preset within 1 kHz', () => {
  const html = R.detailHtml({ freq: 869.6185, sf: 8, bw: 62.5, cr: 8, last_advert: euNarrow.last_advert }, presets, Date.now());
  assert.ok(html.includes('EU/UK (Narrow) / Switzerland'), html);
});

t('detailHtml: no presets still shows the values', () => {
  const html = R.detailHtml(euNarrow, null, Date.now());
  assert.ok(html.includes('869.618 MHz') && !html.includes('badge-region'), html);
});

t('detailHtml: unknown node says where the data comes from', () => {
  const html = R.detailHtml(null, presets, Date.now());
  assert.ok(html.includes('text-muted') && html.includes('MeshCore map'), html);
});

t('detailHtml: escapes a preset title', () => {
  const html = R.detailHtml(euNarrow, [{ title: '<b>x</b>', freq: 869.618, sf: 8, bw: 62.5, cr: 8 }], Date.now());
  assert.ok(!html.includes('<b>x</b>') && html.includes('&lt;b&gt;x&lt;/b&gt;'), html);
});

const sf7 = { freq: 869.618, sf: 7, bw: 62.5, cr: 8, last_advert: '2026-10-13T12:00:00Z' };
const dev = { majority: { freq: 869.618, sf: 8, bw: 62.5 }, agree: 12, peers: 14, fields: ['sf'] };

t('exposes deviationBadge', () => {
  assert.ok(typeof R.deviationBadge === 'function');
});

t('deviationBadge: names the differing value and the majority in the title', () => {
  const html = R.deviationBadge(sf7, dev);
  assert.ok(/class="badge-lora-diff"/.test(html), html);
  assert.ok(html.includes('>LoRa SF7<'), html);
  assert.ok(html.includes('12 of 14 nearby nodes on this band') && html.includes('SF8'), html);
});

t('deviationBadge: several fields and frequency formatting', () => {
  const html = R.deviationBadge({ freq: 869.432, sf: 7, bw: 125, cr: 8 }, { majority: dev.majority, agree: 5, peers: 6, fields: ['freq', 'sf', 'bw'] });
  assert.ok(html.includes('>LoRa 869.432 SF7 BW125<'), html);
});

t('deviationBadge: nothing without a deviation or params', () => {
  assert.strictEqual(R.deviationBadge(sf7, null), '');
  assert.strictEqual(R.deviationBadge(null, dev), '');
});

t('detailHtml: a deviation adds the comparison line', () => {
  const html = R.detailHtml(sf7, presets, Date.now(), dev);
  assert.ok(html.includes('data-testid="lora-deviation"'), html);
  assert.ok(html.includes('Differs from 12 of 14 nearby nodes on this band') && html.includes('869.618 MHz · SF8 · BW 62.5 kHz'), html);
});

t('detailHtml: no deviation, no comparison line', () => {
  assert.ok(!R.detailHtml(euNarrow, presets, Date.now(), null).includes('lora-deviation'));
});

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed ? 1 : 0);

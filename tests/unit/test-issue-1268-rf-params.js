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

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed ? 1 : 0);

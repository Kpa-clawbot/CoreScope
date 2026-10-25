/**
 * #2270 / #2271: render helpers for the latest /neighbors report
 * (public/observer-neighbors.js), loaded the way the page loads them.
 *
 * Usage: node tests/unit/test-observer-neighbors.js
 */
'use strict';
const fs = require('fs');
const path = require('path');

const window = {};
new Function('window', fs.readFileSync(path.join(__dirname, '..', '..', 'public', 'observer-neighbors.js'), 'utf8'))(window);
const ON = window.ObserverNeighbors;
let pass = 0, fail = 0;
function test(name, fn) {
  try { fn(); pass++; console.log('  ✅ ' + name); } catch (e) { fail++; console.log('  ❌ ' + name + ': ' + e.message); }
}
function assert(c, m) { if (!c) throw new Error(m || 'assertion failed'); }

const NOW = Date.parse('2026-10-25T13:00:00Z');

test('cell: never reported is a dash', () => {
  assert(/—/.test(ON.cell({}, NOW)));
  assert(/—/.test(ON.cell(null, NOW)));
});
test('cell: age and count', () => {
  const html = ON.cell({ neighbors_report_at: '2026-10-25T10:00:00Z', neighbors_count: 12 }, NOW);
  assert(html.includes('3h ago · 12'), html);
  assert(html.includes('12 neighbors'), html);
});
test('cell: one neighbor is singular, bad count is 0', () => {
  assert(ON.cell({ neighbors_report_at: '2026-10-25T12:59:30Z', neighbors_count: 1 }, NOW).includes('30s ago · 1'));
  assert(ON.cell({ neighbors_report_at: '2026-10-25T12:00:00Z', neighbors_count: 'x' }, NOW).includes('· 0'));
});
test('since: days and unparseable', () => {
  assert(ON.since('2026-10-20T13:00:00Z', NOW) === '5d ago');
  assert(ON.since('garbage', NOW) === '—');
});
test('detailHtml: nothing without a report', () => {
  assert(ON.detailHtml(null) === '' && ON.detailHtml({}) === '');
});
test('detailHtml: named and unnamed neighbors, metadata', () => {
  const html = ON.detailHtml({
    neighbor_count: 3, queried_neighbors: 2, self_scopes: '#tst', self_default_scope: '*',
    reported_at: '2026-10-25T09:59:58Z',
    neighbors: [
      { pubkey: 'aabbccdd' + '0'.repeat(56), name: 'Rpt One', role: 'repeater', snr: 6.5, heard_secs_ago: 90, status: 'responded', scopes: '#tst' },
      { pubkey: 'ff00ff00' + '0'.repeat(56), status: 'timeout' },
    ],
  });
  assert(html.includes('Rpt One') && html.includes('repeater'), 'name/role');
  assert(html.includes('ff00ff00…'), 'unknown node shown by key prefix');
  assert(html.includes('#/nodes/aabbccdd'), 'links to the node');
  assert(html.includes('6.5 dB') && html.includes('1m ago'), 'snr/heard');
  assert(html.includes('(2 of 3 listed)'), 'truncated list noted');
  assert(html.includes('own scopes #tst') && html.includes('default scope *'), 'self');
});
test('detailHtml: empty list says so', () => {
  assert(ON.detailHtml({ neighbor_count: 0, neighbors: [] }).includes('lists no neighbors'));
});

console.log(`\nObserver neighbors: ${pass} passed, ${fail} failed`);
process.exit(fail > 0 ? 1 : 0);

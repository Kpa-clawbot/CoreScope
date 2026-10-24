/**
 * The Battery line on the node page and side panel (public/node-battery.js):
 * voltage, status, who reported it and the route, or nothing at all when the
 * node has no readings.
 *
 * Usage: node tests/unit/test-node-battery-line.js
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
const flush = () => new Promise((r) => setTimeout(r, 10));
const PK = 'ab'.repeat(32);
const OBS = '7e'.repeat(32);

function load(response) {
  const ctx = {
    console, Number, String, Promise, encodeURIComponent,
    escapeHtml: (s) => String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;'),
    timeAgo: () => '5m ago',
    api: () => (response instanceof Error ? Promise.reject(response) : Promise.resolve(response)),
  };
  ctx.window = ctx;
  vm.createContext(ctx);
  vm.runInContext(fs.readFileSync(path.join(__dirname, '..', '..', 'public/node-battery.js'), 'utf8'), ctx);
  return ctx.NodeBattery;
}

function el(tag) {
  const val = { innerHTML: '' };
  return { tagName: tag, hidden: true, isConnected: true, style: {}, val, querySelector: () => val };
}

const neighbour = { source: 'neighbor', observer_id: OBS, observer_name: 'Cadham <Village>', latest_mv: 3994,
  latest_ts: '2026-10-23T23:56:10Z', reply_hops: 0 };

(async () => {
  console.log('Node battery line');
  {
    const nb = load({ status: 'ok', latest_mv: 3994, selected: neighbour, sources: [neighbour] });
    const row = el('TR');
    await nb.mount(row, PK);
    await flush();
    const h = row.val.innerHTML;
    assert(!row.hidden, 'shown when there is a reading');
    assert(h.includes('3.99 V') && h.includes('OK'), 'voltage and status');
    assert(h.includes('#/observers/' + OBS.toUpperCase()) && h.includes('Cadham &lt;Village>'), 'names and links the observer, escaped');
    assert(h.includes('direct') && h.includes('5m ago'), 'route and age');
    assert(h.includes('#/nodes/' + PK + '/analytics'), 'links to the history');
  }
  {
    const nb = load({ status: 'no_battery', latest_mv: 0, selected: neighbour, sources: [neighbour, { source: 'self' }] });
    const dl = el('DIV');
    await nb.mount(dl, PK);
    await flush();
    assert(dl.val.innerHTML.includes('No battery fitted') && !dl.val.innerHTML.includes(' V<'), '0 mV says no battery fitted');
    assert(dl.val.innerHTML.includes('+1 other'), 'counts the other sources');
    assert(dl.style.display === 'contents', 'a <div> in a <dl> keeps the dt/dd grid');
  }
  {
    const nb = load({ status: 'unknown', selected: null, sources: [] });
    const row = el('TR');
    await nb.mount(row, PK);
    assert(row.hidden && row.val.innerHTML === '', 'hidden with no readings');
  }
  {
    const nb = load(new Error('boom'));
    const row = el('TR');
    await nb.mount(row, PK);
    assert(row.hidden, 'hidden when the request fails');
  }
  {
    const nb = load({ status: 'ok', latest_mv: 3994, selected: neighbour, sources: [neighbour] });
    const row = el('TR');
    row.isConnected = false;
    await nb.mount(row, PK);
    assert(row.hidden, 'nothing written once the page moved on');
  }
  console.log('\n' + passed + ' passed, ' + failed + ' failed');
  if (failed) process.exit(1);
})();

/* Favorites dropdown and home health cards must judge a node by its role's
 * thresholds (repeater/room: infra, others: node), like the My Repeaters tab
 * and getNodeStatus already do.
 *
 * Before the fix these three sites compared every node against
 * HEALTH_THRESHOLDS.nodeDegradedMs / nodeSilentMs (1 h / 24 h), so a starred
 * repeater last heard 5 h ago showed "degraded" and one heard 30 h ago showed
 * "silent", although the infra thresholds (24 h / 72 h) call them healthy and
 * degraded.
 */
'use strict';
const vm = require('vm');
const fs = require('fs');
const path = require('path');
const assert = require('assert');

const PUBLIC = path.resolve(__dirname, '..', '..', 'public');
const read = (f) => fs.readFileSync(path.join(PUBLIC, f), 'utf8');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log(`  ✅ ${name}`); }
  catch (e) { failed++; console.log(`  ❌ ${name}: ${e.message}`); }
}

// The thresholds themselves: infra roles are held to looser limits than nodes.
const ctx = { window: {}, console, Date, Infinity, document: { readyState: 'complete', createElement: () => ({ id: '' }), head: { appendChild: () => {} }, getElementById: () => null, addEventListener: () => {} }, fetch: () => Promise.resolve({ json: () => Promise.resolve({}) }) };
vm.createContext(ctx);
vm.runInContext(read('roles.js'), ctx);
// roles.js reads HEALTH_THRESHOLDS as a bare global; window.X does not create one here.
for (const k of Object.keys(ctx.window)) ctx[k] = ctx.window[k];
const { getHealthThresholds } = ctx.window;
const H = 3600 * 1000;

test('a repeater 30 h quiet is inside the infra silent window, a companion is not', () => {
  assert.ok(30 * H < getHealthThresholds('repeater').silentMs);
  assert.ok(30 * H >= getHealthThresholds('companion').silentMs);
});
test('room is held to the infra thresholds too', () => {
  assert.strictEqual(getHealthThresholds('room').silentMs, getHealthThresholds('repeater').silentMs);
});

// The three call sites must go through getHealthThresholds(role).
function region(src, startMarker, endMarker) {
  const a = src.indexOf(startMarker);
  assert.ok(a >= 0, 'marker not found: ' + startMarker);
  const b = src.indexOf(endMarker, a + startMarker.length);
  return src.slice(a, b < 0 ? undefined : b);
}
const NODE_ONLY = /HEALTH_THRESHOLDS\.node(Degraded|Silent)Ms/;

test('app.js favorites dropdown uses role thresholds', () => {
  const body = region(read('app.js'), 'async function renderFavDropdown', 'fav-dd-item');
  assert.ok(/getHealthThresholds\(/.test(body), 'renderFavDropdown does not call getHealthThresholds');
  assert.ok(!NODE_ONLY.test(body), 'renderFavDropdown still uses node-only thresholds');
});
test('home.js "My Nodes" card uses role thresholds', () => {
  const body = region(read('home.js'), 'const stats = h.stats || {};', 'statusText');
  assert.ok(/getHealthThresholds\(node\.role\)/.test(body), 'My Nodes card does not call getHealthThresholds(node.role)');
  assert.ok(!NODE_ONLY.test(body), 'My Nodes card still uses node-only thresholds');
});
test('home.js search-result card uses role thresholds', () => {
  const body = region(read('home.js'), 'const claimed = isMyNode(pubkey);', 'statusMsg = `Last heard');
  assert.ok(/getHealthThresholds\(node\.role\)/.test(body), 'search card does not call getHealthThresholds(node.role)');
  assert.ok(!NODE_ONLY.test(body), 'search card still uses node-only thresholds');
});
test('no node-only threshold comparison is left in home.js', () => {
  assert.ok(!NODE_ONLY.test(read('home.js')));
});
test('search card no longer hardcodes "24+ hours" for every role', () => {
  assert.ok(!/Not heard in 24\+ hours/.test(read('home.js')));
});

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed ? 1 : 0);

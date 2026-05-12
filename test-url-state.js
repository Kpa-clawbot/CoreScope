/* Unit tests for URL state helpers (issue #749) */
'use strict';

const assert = require('assert');
const URLState = require('./public/url-state.js');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log('  ✅ ' + name); }
  catch (e) { failed++; console.log('  ❌ ' + name + ': ' + e.message); }
}

console.log('── URL State Helpers ──');

// ------- parseSort -------
test('parseSort: column only defaults to desc', function () {
  assert.deepStrictEqual(URLState.parseSort('time'), { column: 'time', direction: 'desc' });
});
test('parseSort: column:asc', function () {
  assert.deepStrictEqual(URLState.parseSort('lastSeen:asc'), { column: 'lastSeen', direction: 'asc' });
});
test('parseSort: column:desc', function () {
  assert.deepStrictEqual(URLState.parseSort('time:desc'), { column: 'time', direction: 'desc' });
});
test('parseSort: invalid direction → desc', function () {
  assert.deepStrictEqual(URLState.parseSort('time:weird'), { column: 'time', direction: 'desc' });
});
test('parseSort: empty/null → null', function () {
  assert.strictEqual(URLState.parseSort(''), null);
  assert.strictEqual(URLState.parseSort(null), null);
  assert.strictEqual(URLState.parseSort(undefined), null);
});

// ------- serializeSort -------
test('serializeSort: desc default omitted', function () {
  assert.strictEqual(URLState.serializeSort('time', 'desc'), 'time');
});
test('serializeSort: asc included', function () {
  assert.strictEqual(URLState.serializeSort('lastSeen', 'asc'), 'lastSeen:asc');
});
test('serializeSort: empty column → empty string', function () {
  assert.strictEqual(URLState.serializeSort('', 'desc'), '');
  assert.strictEqual(URLState.serializeSort(null, 'asc'), '');
});

// ------- parseHash -------
test('parseHash: bare route', function () {
  assert.deepStrictEqual(URLState.parseHash('#/packets'), { route: 'packets', params: {} });
});
test('parseHash: route with params', function () {
  var r = URLState.parseHash('#/packets?filter=type%3D%3DADVERT&sort=time');
  assert.strictEqual(r.route, 'packets');
  assert.strictEqual(r.params.filter, 'type==ADVERT');
  assert.strictEqual(r.params.sort, 'time');
});
test('parseHash: route with subpath kept (existing deep links)', function () {
  var r = URLState.parseHash('#/nodes/abc123def?tab=repeaters');
  assert.strictEqual(r.route, 'nodes/abc123def');
  assert.strictEqual(r.params.tab, 'repeaters');
});
test('parseHash: empty hash', function () {
  assert.deepStrictEqual(URLState.parseHash(''), { route: '', params: {} });
  assert.deepStrictEqual(URLState.parseHash('#/'), { route: '', params: {} });
});

// ------- buildHash -------
test('buildHash: bare route', function () {
  assert.strictEqual(URLState.buildHash('packets', {}), '#/packets');
});
test('buildHash: with params, omits empty values', function () {
  var h = URLState.buildHash('packets', { filter: 'type==ADVERT', sort: '', empty: null, blank: undefined });
  assert.strictEqual(h, '#/packets?filter=type%3D%3DADVERT');
});
test('buildHash: encodes special chars', function () {
  var h = URLState.buildHash('analytics', { tab: 'topology', window: '7d' });
  // Order is preserved in object iteration
  assert.ok(h === '#/analytics?tab=topology&window=7d' || h === '#/analytics?window=7d&tab=topology');
});
test('buildHash: leading "#/" is OK on route, normalized', function () {
  assert.strictEqual(URLState.buildHash('#/packets', { sort: 'time' }), '#/packets?sort=time');
});

// ------- updateHashParams -------
test('updateHashParams: round-trip preserves route subpath', function () {
  // Simulate location.hash environment
  var fakeLocation = { hash: '#/nodes/abcdef?tab=repeaters' };
  var newHash = URLState.updateHashParams({ sort: 'lastSeen:asc' }, fakeLocation.hash);
  // Must keep the nodes/abcdef subpath
  var r = URLState.parseHash(newHash);
  assert.strictEqual(r.route, 'nodes/abcdef');
  assert.strictEqual(r.params.tab, 'repeaters');
  assert.strictEqual(r.params.sort, 'lastSeen:asc');
});
test('updateHashParams: setting empty/null removes key', function () {
  var newHash = URLState.updateHashParams({ tab: '' }, '#/nodes?tab=repeaters&search=foo');
  var r = URLState.parseHash(newHash);
  assert.strictEqual(r.params.tab, undefined);
  assert.strictEqual(r.params.search, 'foo');
});

console.log('');
console.log(passed + ' passed, ' + failed + ' failed');
process.exit(failed > 0 ? 1 : 0);

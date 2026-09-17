/* Unit tests for the Scopes tab "flood adverts by node role" section (#1979).
 *
 * The section renders /api/scope-stats advertsByRole: one row per sender role,
 * with the count of flood adverts in each of the three scope states and that
 * count's share of the row. It is descriptive only: it reports what each role
 * sent and makes no claim about why.
 *
 * Loads the real public/analytics.js in a vm sandbox and exercises the helper
 * it exposes, the same way test-frontend-helpers.js tests hashStatCardsHtml.
 */
'use strict';
const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log(`  ✅ ${name}`); }
  catch (e) { failed++; console.log(`  ❌ ${name}: ${e.message}`); }
}

function makeSandbox() {
  const ctx = {
    window: {},
    document: {
      documentElement: {}, addEventListener() {}, removeEventListener() {},
      getElementById() { return null; }, querySelector() { return null; }, querySelectorAll() { return []; },
      createElement() { return { style: {}, addEventListener() {} }; },
    },
    console, Date, Math, Array, Object, String, Number, JSON, RegExp, Error,
    parseInt, parseFloat, isFinite, isNaN, Map, Set, Promise,
    setTimeout: () => {}, clearTimeout: () => {}, setInterval: () => {}, clearInterval: () => {},
    localStorage: { getItem: () => null, setItem() {}, removeItem() {} },
    getComputedStyle: () => ({ getPropertyValue: () => '' }),
    registerPage: () => {}, api: async () => ({}), fetch: async () => ({ json: async () => ({}) }),
    addEventListener() {}, removeEventListener() {},
  };
  ctx.window = ctx;
  vm.createContext(ctx);
  vm.runInContext(fs.readFileSync('public/analytics.js', 'utf8'), ctx);
  return ctx;
}

console.log('\n=== analytics.js: scope adverts by role (#1979) ===');
{
  const ctx = makeSandbox();
  const render = ctx.window._analyticsScopeAdvertsByRoleHtml;

  test('helper is exposed for testing', () => {
    assert.strictEqual(typeof render, 'function', '_analyticsScopeAdvertsByRoleHtml must be exposed');
  });

  const rows = [
    { role: 'repeater', unscoped: 3, unknownScope: 1, named: 0 },
    { role: 'companion', unscoped: 1, unknownScope: 0, named: 1 },
  ];
  const html = typeof render === 'function' ? render(rows) : '';

  test('renders one body row per role, in server order', () => {
    const bodyRows = html.match(/<tr data-role="[^"]*">/g) || [];
    assert.deepStrictEqual(bodyRows, ['<tr data-role="repeater">', '<tr data-role="companion">']);
  });

  // Cell contents of one body row, in column order.
  function rowCells(role) {
    const start = html.indexOf('<tr data-role="' + role + '">');
    assert.ok(start !== -1, 'row for ' + role);
    const row = html.slice(start, html.indexOf('</tr>', start));
    return row.split('<td>').slice(1).map((c) => c.replace(/<\/td>$/, ''));
  }

  test('renders the row total and each state with its share of the row, by column', () => {
    assert.deepStrictEqual(rowCells('repeater'), [
      'repeater',
      '4',
      '3 <span class="text-muted">(75.0%)</span>',
      '1 <span class="text-muted">(25.0%)</span>',
      '0 <span class="text-muted">(0.0%)</span>',
    ]);
    assert.deepStrictEqual(rowCells('companion'), [
      'companion',
      '2',
      '1 <span class="text-muted">(50.0%)</span>',
      '0 <span class="text-muted">(0.0%)</span>',
      '1 <span class="text-muted">(50.0%)</span>',
    ]);
  });

  test('header columns are in the same order as the cells', () => {
    const heads = (html.match(/<th>[^<]*<\/th>/g) || []).map((th) => th.slice(4, -5));
    assert.deepStrictEqual(heads, ['Role', 'Adverts', 'Unscoped', 'Unknown scope', 'Named scope']);
  });

  test('escapes the role text', () => {
    const out = render([{ role: '<img src=x onerror=alert(1)>', unscoped: 1, unknownScope: 0, named: 0 }]);
    assert.ok(!out.includes('<img'), 'raw markup must not be emitted');
    assert.ok(out.includes('&lt;img src=x onerror=alert(1)&gt;'), 'role must be HTML-escaped');
  });

  test('empty or missing breakdown renders an empty state, not a table', () => {
    for (const input of [[], undefined]) {
      const out = render(input);
      assert.ok(!out.includes('<table'), 'no table for ' + JSON.stringify(input));
      assert.ok(out.includes('No flood adverts in this window'), 'empty-state text for ' + JSON.stringify(input));
    }
  });

  test('wording stays descriptive, with no causal claim', () => {
    assert.ok(/does not show why/.test(html), 'caption should state the table does not explain causes');
    assert.ok(!/because/i.test(html), 'no causal "because" wording');
  });
}

console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);

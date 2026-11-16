/* Unit tests for the Scopes tab summary cards (#2364).
 *
 * /api/scope-stats reports `total` (= scoped + unscoped, all routes) and
 * splits unscoped into transport, flood and direct. The Scoped and Unscoped
 * cards show their share of `total`, and the Unscoped card lists the route
 * split. Servers older than #2364 send neither, so the cards fall back to
 * scoped + unscoped as the denominator and leave the split out.
 *
 * Loads the real public/analytics.js in a vm sandbox and exercises the helper
 * it exposes, the same way test-issue-1979-scope-adverts-by-role.js does.
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

// The note under the card with the given label.
function note(html, label) {
  const re = new RegExp('<div class="stat-label">' + label + '</div><div class="stat-note[^"]*"[^>]*>([^<]*)</div>');
  const m = html.match(re);
  return m ? m[1] : null;
}

console.log('\n=== analytics.js: scope summary cards (#2364) ===');
{
  const ctx = makeSandbox();
  const render = ctx.window._analyticsScopeSummaryCardsHtml;

  test('helper is exposed for testing', () => {
    assert.strictEqual(typeof render, 'function', '_analyticsScopeSummaryCardsHtml must be exposed');
  });
  if (typeof render !== 'function') {
    console.log(`\n${passed} passed, ${failed} failed`);
    process.exit(1);
  }

  // unscoped (150) exceeds transportTotal (100): the case #2364 is about.
  const current = {
    total: 200, transportTotal: 100, scoped: 50, unscoped: 150,
    unscopedTransport: 50, unscopedFlood: 70, unscopedDirect: 30, unknownScope: 10,
  };
  const html = render(current);

  test('renders the four cards', () => {
    const labels = (html.match(/<div class="stat-label">[^<]*<\/div>/g) || []).map((s) => s.replace(/<[^>]+>/g, ''));
    assert.deepStrictEqual(labels, ['Transport Total', 'Scoped', 'Unscoped', 'Unknown Scope']);
  });

  test('shares use total, not transportTotal', () => {
    assert.strictEqual(note(html, 'Scoped'), '25.0% of all traffic');
    assert.ok(note(html, 'Unscoped').startsWith('75.0% of all traffic'), note(html, 'Unscoped'));
  });

  test('unscoped note lists the route split', () => {
    assert.strictEqual(note(html, 'Unscoped'), '75.0% of all traffic · 50 transport, 70 flood, 30 direct');
  });

  test('unknown scope is a share of scoped', () => {
    assert.strictEqual(note(html, 'Unknown Scope'), '20.0% of scoped');
  });

  test('transport total card shows transportTotal', () => {
    assert.ok(/<div class="stat-value">100<\/div><div class="stat-label">Transport Total<\/div>/.test(html));
  });

  // A server from before #2364: no total, no route split.
  const old = { transportTotal: 100, scoped: 50, unscoped: 150, unknownScope: 10 };
  const oldHtml = render(old);

  test('older server: falls back to scoped + unscoped', () => {
    assert.strictEqual(note(oldHtml, 'Scoped'), '25.0% of all traffic');
  });

  test('older server: no route split', () => {
    assert.strictEqual(note(oldHtml, 'Unscoped'), '75.0% of all traffic');
  });

  test('partial split (no unscopedDirect) is left out', () => {
    const partial = Object.assign({}, old, { unscopedFlood: 70 });
    assert.strictEqual(note(render(partial), 'Unscoped'), '75.0% of all traffic');
  });

  test('empty window shows a dash instead of NaN', () => {
    const empty = render({ total: 0, transportTotal: 0, scoped: 0, unscoped: 0, unknownScope: 0 });
    assert.strictEqual(note(empty, 'Scoped'), '— of all traffic');
    assert.strictEqual(note(empty, 'Unknown Scope'), '— of scoped');
    assert.ok(!/NaN/.test(empty));
  });

  test('total of 0 is honoured, not replaced by the fallback', () => {
    // total present but 0 must not fall back to scoped + unscoped.
    const z = render({ total: 0, transportTotal: 0, scoped: 5, unscoped: 5, unknownScope: 0 });
    assert.strictEqual(note(z, 'Scoped'), '— of all traffic');
  });
}

console.log(`\n${passed} passed, ${failed} failed`);
if (failed) process.exit(1);

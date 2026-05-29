/* Issue #1478 — frontend renders ⚠️ chip on observers list when clock_naive,
 * and a prominent banner on observer-detail page. Behavioral test via JSDOM-
 * style sandbox: we load the production JS files into a vm context with a
 * minimal DOM, call the exposed helper functions, and assert on the rendered
 * HTML strings.
 *
 * Production code must expose:
 *   window.ObserversNaiveChip.render(o)            -> string (chip HTML or "")
 *   window.ObserverDetailNaiveBanner.render(o)     -> string (banner HTML or "")
 * Both must return non-empty content when `o.clock_naive === true`, with the
 * skew magnitude in the chip's tooltip and the actionable recommendation in
 * the banner body.
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

function makeCtx() {
  const ctx = {
    window: { addEventListener: () => {}, dispatchEvent: () => {} },
    document: {
      readyState: 'complete',
      createElement: () => ({ id: '', textContent: '', innerHTML: '' }),
      head: { appendChild: () => {} },
      getElementById: () => null,
      addEventListener: () => {},
      querySelectorAll: () => [],
      querySelector: () => null,
    },
    console,
    Date,
    Math,
    Array,
    Object,
    String,
    Number,
    Boolean,
    JSON,
    setInterval: () => 0,
    clearInterval: () => {},
    setTimeout: (fn) => { try { fn(); } catch {} return 0; },
    encodeURIComponent,
    decodeURIComponent,
    fetch: () => Promise.resolve({ json: () => Promise.resolve({}) }),
  };
  // common globals used by observers.js
  ctx.registerPage = () => {};
  ctx.RegionFilter = { init: () => {}, onChange: () => () => {}, getSelected: () => null };
  ctx.healthStatus = () => ({ cls: 'health-green', label: 'Online' });
  ctx.timeAgo = () => 'now';
  ctx.uptimeStr = () => '—';
  ctx.packetBadge = () => '';
  ctx.sparkBar = () => '';
  ctx.makeColumnsResizable = () => {};
  ctx.observerSkewSeverity = () => 'ok';
  ctx.renderSkewBadge = () => '';
  ctx.debouncedOnWS = (fn) => fn;
  vm.createContext(ctx);
  return ctx;
}

console.log('\n=== Issue #1478 — Observers list ⚠️ chip ===');

(function () {
  const ctx = makeCtx();
  vm.runInContext(fs.readFileSync('public/observers.js', 'utf8'), ctx);

  test('window.ObserversNaiveChip.render exists', () => {
    assert.ok(ctx.window.ObserversNaiveChip, 'ObserversNaiveChip not exported');
    assert.strictEqual(typeof ctx.window.ObserversNaiveChip.render, 'function');
  });

  test('renders ⚠️ chip when clock_naive=true', () => {
    const html = ctx.window.ObserversNaiveChip.render({
      id: 'x', name: 'X', clock_naive: true, clock_skew_seconds: -28800,
      clock_skew_count_24h: 17,
    });
    assert.ok(html && html.length > 0, 'expected non-empty chip HTML');
    assert.ok(/⚠️|warning/i.test(html), `expected warning glyph in chip: ${html}`);
    assert.ok(/title=/.test(html), `expected title= tooltip in chip: ${html}`);
  });

  test('renders nothing when clock_naive=false', () => {
    const html = ctx.window.ObserversNaiveChip.render({ id: 'y', clock_naive: false });
    assert.strictEqual(html || '', '');
  });

  test('renders nothing when clock_naive missing', () => {
    const html = ctx.window.ObserversNaiveChip.render({ id: 'z' });
    assert.strictEqual(html || '', '');
  });

  test('chip tooltip mentions skew magnitude', () => {
    const html = ctx.window.ObserversNaiveChip.render({
      id: 'x', clock_naive: true, clock_skew_seconds: -28800,
    });
    // -28800s = 8h. Tooltip should mention hours or the duration so an
    // operator can see at a glance "off by 8 hours" without clicking through.
    assert.ok(/8|h/.test(html), `expected magnitude in tooltip: ${html}`);
  });
})();

console.log('\n=== Issue #1478 — Observer detail banner ===');

(function () {
  const ctx = makeCtx();
  // observer-detail.js needs Chart
  ctx.Chart = { defaults: {}, register: () => {} };
  ctx.getComputedStyle = () => ({ getPropertyValue: () => '' });
  vm.runInContext(fs.readFileSync('public/observer-detail.js', 'utf8'), ctx);

  test('window.ObserverDetailNaiveBanner.render exists', () => {
    assert.ok(ctx.window.ObserverDetailNaiveBanner, 'ObserverDetailNaiveBanner not exported');
    assert.strictEqual(typeof ctx.window.ObserverDetailNaiveBanner.render, 'function');
  });

  test('renders banner when clock_naive=true', () => {
    const html = ctx.window.ObserverDetailNaiveBanner.render({
      id: 'x', name: 'X', clock_naive: true, clock_skew_seconds: -28800,
      clock_skew_count_24h: 17, clock_last_naive_at: new Date().toISOString(),
    });
    assert.ok(html && html.length > 0, 'expected non-empty banner HTML');
    // Banner must contain actionable guidance — operator needs to know HOW
    // to fix it (set clock to UTC, or emit zone-aware timestamps).
    assert.ok(/UTC/i.test(html), `expected UTC mention: ${html}`);
    assert.ok(/(Z-suffix|offset-aware|timezone|zone-aware|isoformat)/i.test(html),
      `expected timestamp guidance: ${html}`);
  });

  test('renders nothing when clock_naive=false', () => {
    const html = ctx.window.ObserverDetailNaiveBanner.render({ id: 'y', clock_naive: false });
    assert.strictEqual(html || '', '');
  });
})();

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed === 0 ? 0 : 1);

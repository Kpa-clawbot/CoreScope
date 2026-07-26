/* #1865 follow-up (cwichura, PR #1867): "Direct Neighbors" panel on the
 * observer detail page. Production code must expose:
 *   window.renderDirectNeighbors(neighborsData) -> string
 * Must render a plain, neutral note (no warning icon) when there is no
 * neighbor data -- absence is normal (opt-in firmware, non-PSRAM hardware),
 * never a fault.
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
    console, Date, Math, Array, Object, String, Number, Boolean, JSON,
    setInterval: () => 0, clearInterval: () => {},
    setTimeout: (fn) => { try { fn(); } catch {} return 0; },
    encodeURIComponent, decodeURIComponent,
    fetch: () => Promise.resolve({ json: () => Promise.resolve({}) }),
  };
  ctx.registerPage = () => {};
  ctx.timeAgo = (iso) => 'TIME_AGO(' + iso + ')';
  ctx.escapeHtml = (s) => String(s == null ? '' : s).replace(/[&<>"']/g, (c) => ({
    '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
  })[c]);
  ctx.Chart = function () { return { destroy() {} }; };
  vm.createContext(ctx);
  return ctx;
}

console.log('\n=== #1865 follow-up — Observer detail "Direct Neighbors" panel ===');

const ctx = makeCtx();
vm.runInContext(fs.readFileSync('public/observer-detail.js', 'utf8'), ctx);

test('window.renderDirectNeighbors exists', () => {
  assert.strictEqual(typeof ctx.window.renderDirectNeighbors, 'function');
});

test('empty/never-reported renders a neutral note, not a warning', () => {
  const html = ctx.window.renderDirectNeighbors({ neighbors: [], reportedAt: '' });
  assert.ok(/text-muted/.test(html));
  assert.ok(!/ph-warning/.test(html));
  assert.ok(/opt-in/.test(html));
});

test('missing neighbors field (defensive) also renders the empty note', () => {
  const html = ctx.window.renderDirectNeighbors({});
  assert.ok(/No direct-neighbor data/.test(html));
});

test('a known node renders a link + role icon + escaped name', () => {
  const html = ctx.window.renderDirectNeighbors({
    neighbors: [{ pubkey: 'abc123', name: '<b>Evil</b>', role: 'repeater', scopes: '#dk', status: 'responded' }],
    reportedAt: '2026-07-26T10:00:00Z',
  });
  assert.ok(html.includes('href="#/nodes/abc123"'), 'expected a link to the node detail page');
  assert.ok(html.includes('ph-broadcast'), 'expected the repeater role icon');
  assert.ok(!html.includes('<b>Evil</b>'), 'name must be HTML-escaped');
  assert.ok(html.includes('&lt;b&gt;'), 'expected escaped name markup');
  assert.ok(html.includes('#dk'), 'expected the scope badge');
  assert.ok(html.includes('TIME_AGO(2026-07-26T10:00:00Z)'), 'expected the "as of" timestamp');
});

test('an unresolved pubkey (no name) renders truncated, unlinked', () => {
  const html = ctx.window.renderDirectNeighbors({
    neighbors: [{ pubkey: 'deadbeefdeadbeefdeadbeef', name: null, role: null, scopes: null, status: 'timeout' }],
    reportedAt: '',
  });
  assert.ok(!/href="#\/nodes\//.test(html), 'must not link an unresolved pubkey');
  assert.ok(html.includes('deadbeefdead'), 'expected a truncated pubkey');
  assert.ok(/no reply/.test(html), 'expected the timeout "no reply" label');
});

test('seenViaPackets=true renders "confirmed", not an error/warning treatment', () => {
  const html = ctx.window.renderDirectNeighbors({
    neighbors: [{ pubkey: 'abc123', name: 'Repeater A', role: 'repeater', scopes: '#dk', status: 'responded', seenViaPackets: true }],
    reportedAt: '',
  });
  assert.ok(/confirmed/.test(html));
  assert.ok(!/ph-warning/.test(html));
});

test('seenViaPackets=false surfaces the coverage-gap diagnostic, styled neutrally', () => {
  const html = ctx.window.renderDirectNeighbors({
    neighbors: [{ pubkey: 'abc123', name: 'Repeater A', role: 'repeater', scopes: '#dk', status: 'responded', seenViaPackets: false }],
    reportedAt: '',
  });
  assert.ok(/not seen yet/.test(html));
  assert.ok(/coverage gap/.test(html), 'expected the explanatory tooltip');
  assert.ok(!/ph-warning/.test(html), 'must not use the warning icon treatment');
});

console.log(`\n${passed} passed, ${failed} failed\n`);
if (failed > 0) process.exit(1);

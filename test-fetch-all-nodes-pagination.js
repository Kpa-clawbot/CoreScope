/* Regression test for the /api/nodes per-request row cap (#1606 class).
 *
 * Bug: /api/nodes clamps ?limit to listLimits.nodesMax (default 2000;
 * originally a hard 500 in PR #1540, raised/made configurable in #1589) and
 * orders by last_seen DESC. Every node-list consumer (map.js, live.js,
 * analytics.js, packets.js, area-map.html) issued a single ?limit=N fetch and
 * trusted the response as the full set, so a mesh with more nodes than the cap
 * silently saw only the top rows. A repeater that relays constantly but last
 * self-advertised hours ago fell outside that window and vanished from the
 * map/live view even though it was plainly alive.
 *
 * Fix: a shared app.js `fetchAllNodes()` helper pages through /api/nodes (fixed
 * client page size, well under the server cap) until a short page, deduping by
 * public_key, and caps the result at safetyCap. This test drives the REAL
 * api()+fetchAllNodes against a mocked fetch using a 500-per-page fixture (the
 * client page size) past which a node is hidden, plus the server's unreliable
 * per-page `total`. Pre-fix consumers saw one page; the helper surfaces all.
 */
'use strict';
const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

let passed = 0, failed = 0;
const pending = [];
function test(name, fn) {
  try {
    const out = fn();
    if (out && typeof out.then === 'function') {
      pending.push(out.then(() => { passed++; console.log('  ✅ ' + name); })
        .catch(e => { failed++; console.log('  ❌ ' + name + ': ' + e.message); }));
      return;
    }
    passed++; console.log('  ✅ ' + name);
  } catch (e) {
    failed++; console.log('  ❌ ' + name + ': ' + e.message);
  }
}

function makeSandbox() {
  const ctx = {
    window: {},
    document: {
      readyState: 'complete',
      createElement: () => ({ id: '', textContent: '', innerHTML: '' }),
      head: { appendChild: () => {} },
      getElementById: () => null,
      addEventListener: () => {},
      querySelectorAll: () => [],
      querySelector: () => null,
    },
    console, Date, Infinity, Math, Array, Object, String, Number, JSON, RegExp, Error, TypeError,
    parseInt, parseFloat, isNaN, isFinite, encodeURIComponent, decodeURIComponent,
    setTimeout: () => {}, clearTimeout: () => {}, setInterval: () => {}, clearInterval: () => {},
    performance: { now: () => Date.now() },
    localStorage: (() => { const s = {}; return { getItem: k => s[k] || null, setItem: (k, v) => { s[k] = String(v); }, removeItem: k => { delete s[k]; } }; })(),
    location: { hash: '' },
    CustomEvent: class CustomEvent {},
    Map, Set, Promise, URLSearchParams,
    addEventListener: () => {}, dispatchEvent: () => {},
    fetch: () => Promise.resolve({ ok: true, json: () => Promise.resolve({}) }),
  };
  ctx.window.addEventListener = () => {};
  ctx.window.dispatchEvent = () => {};
  vm.createContext(ctx);
  return ctx;
}

function loadInCtx(ctx, file) {
  vm.runInContext(fs.readFileSync(file, 'utf8'), ctx);
  for (const k of Object.keys(ctx.window)) ctx[k] = ctx.window[k];
}

// A fetch mock that mirrors the real /api/nodes: clamps ?limit to `cap`,
// honors ?offset, and returns the same kind of unreliable `total` the server
// emits (clamped to the page size). `extraDup` injects one duplicate
// public_key straddling a page boundary to exercise dedup.
function makeNodesFetch(total, cap, opts = {}) {
  const calls = [];
  const fixture = [];
  for (let i = 0; i < total; i++) fixture.push({ public_key: 'pk' + i, name: 'N' + i });
  // Optionally repeat the last row of page 1 as the first row of page 2.
  if (opts.dupAtBoundary) fixture[cap] = fixture[cap - 1];
  return {
    calls,
    fetch: (url) => {
      calls.push(url);
      const qs = url.split('?')[1] || '';
      const p = new URLSearchParams(qs);
      const limit = Math.min(parseInt(p.get('limit') || '50', 10), cap);
      const offset = parseInt(p.get('offset') || '0', 10);
      const page = fixture.slice(offset, offset + limit);
      return Promise.resolve({
        ok: true,
        json: () => Promise.resolve({ nodes: page, counts: { repeaters: total }, total: page.length }),
      });
    },
  };
}

console.log('\n=== fetchAllNodes: pagination past the 500-row cap ===');

test('surfaces ALL nodes past the 500 server cap (1200 > 500)', async () => {
  const ctx = makeSandbox();
  loadInCtx(ctx, 'public/app.js');
  const m = makeNodesFetch(1200, 500);
  ctx.fetch = m.fetch;
  const out = await ctx.fetchAllNodes('');
  assert.strictEqual(out.nodes.length, 1200, 'expected all 1200 nodes, got ' + out.nodes.length);
  assert.strictEqual(out.total, 1200, 'total must be the real deduped count, not the clamped per-page total');
  assert.strictEqual(m.calls.length, 3, 'expected 3 pages (500+500+200), got ' + m.calls.length);
});

test('stops on a short page rather than the unreliable server total', async () => {
  const ctx = makeSandbox();
  loadInCtx(ctx, 'public/app.js');
  // Exactly 1000 → pages 500, 500, then a 0-length page stops the loop.
  const m = makeNodesFetch(1000, 500);
  ctx.fetch = m.fetch;
  const out = await ctx.fetchAllNodes('');
  assert.strictEqual(out.nodes.length, 1000);
  assert.strictEqual(m.calls.length, 3, 'expected one extra empty page after two full pages');
});

test('dedups a public_key repeated across a page boundary', async () => {
  const ctx = makeSandbox();
  loadInCtx(ctx, 'public/app.js');
  const m = makeNodesFetch(1200, 500, { dupAtBoundary: true });
  ctx.fetch = m.fetch;
  const out = await ctx.fetchAllNodes('');
  const keys = out.nodes.map(n => n.public_key);
  assert.strictEqual(new Set(keys).size, keys.length, 'result must contain no duplicate public_key');
});

test('passes counts from the first page through', async () => {
  const ctx = makeSandbox();
  loadInCtx(ctx, 'public/app.js');
  const m = makeNodesFetch(600, 500);
  ctx.fetch = m.fetch;
  const out = await ctx.fetchAllNodes('');
  assert.strictEqual(out.counts.repeaters, 600);
});

test('appends extraQuery verbatim and caps at a 500-aligned safetyCap', async () => {
  const ctx = makeSandbox();
  loadInCtx(ctx, 'public/app.js');
  const m = makeNodesFetch(5000, 500);
  ctx.fetch = m.fetch;
  const out = await ctx.fetchAllNodes('&lastHeard=30d&area=BE', { safetyCap: 1000 });
  // safetyCap=1000 → offsets 0 and 500 → exactly 1000 nodes (tightened so an
  // off-by-pageSize regression is caught here, not in production).
  assert.strictEqual(out.nodes.length, 1000, 'expected exactly 1000 nodes, got ' + out.nodes.length);
  assert.ok(m.calls[0].indexOf('&lastHeard=30d&area=BE') !== -1, 'extraQuery must be appended to the request');
  assert.ok(m.calls[0].indexOf('limit=500') !== -1 && m.calls[0].indexOf('offset=0') !== -1, 'first page must request limit/offset');
});

test('safetyCap is a hard node-count ceiling (no pageSize overshoot)', async () => {
  const ctx = makeSandbox();
  loadInCtx(ctx, 'public/app.js');
  const m = makeNodesFetch(5000, 500);
  ctx.fetch = m.fetch;
  // safetyCap=800 is NOT a multiple of pageSize: the loop fetches offsets 0 and
  // 500 (1000 rows) but the result must be sliced back to exactly 800 — this is
  // the live.js LIVE_MAP_MAX_NODES ceiling that previously overshot.
  const out = await ctx.fetchAllNodes('', { safetyCap: 800 });
  assert.strictEqual(out.nodes.length, 800, 'safetyCap must cap returned nodes exactly, got ' + out.nodes.length);
  assert.strictEqual(out.total, 800, 'total must equal the capped count');
});

test('rows missing public_key are NOT collapsed into one', async () => {
  const ctx = makeSandbox();
  loadInCtx(ctx, 'public/app.js');
  // Two distinct rows both lacking public_key must survive as two entries.
  ctx.fetch = () => Promise.resolve({
    ok: true,
    json: () => Promise.resolve({ nodes: [{ name: 'A' }, { name: 'B' }, { public_key: 'pk1', name: 'C' }] }),
  });
  const out = await ctx.fetchAllNodes('');
  assert.strictEqual(out.nodes.length, 3, 'falsy-key rows must not collapse, got ' + out.nodes.length);
});

test('empty result → exactly one request, zero nodes', async () => {
  const ctx = makeSandbox();
  loadInCtx(ctx, 'public/app.js');
  let calls = 0;
  ctx.fetch = () => { calls++; return Promise.resolve({ ok: true, json: () => Promise.resolve({ nodes: [], counts: {}, total: 0 }) }); };
  const out = await ctx.fetchAllNodes('');
  assert.strictEqual(out.nodes.length, 0);
  assert.strictEqual(calls, 1, 'a 0-length first page must stop after one request, got ' + calls);
});

test('handles a bare-array /api/nodes response shape', async () => {
  const ctx = makeSandbox();
  loadInCtx(ctx, 'public/app.js');
  ctx.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([{ public_key: 'a' }, { public_key: 'b' }]) });
  const out = await ctx.fetchAllNodes('');
  assert.strictEqual(out.nodes.length, 2, 'bare-array body must be accepted');
});

// ===== area-map.html inline fetchAllNodesPaged (separate impl, can't import app.js) =====
// area-map.html is embeddable cross-origin, so it carries its own copy of the
// loop. Extract that function from the HTML and exercise the SAME behaviors plus
// its distinct error contract (throws on a non-OK page rather than returning a
// silent partial). Keeping this test next to the helper's prevents the two
// copies from drifting unnoticed.
function loadAreaMapHelper(fetchImpl) {
  const html = fs.readFileSync('public/area-map.html', 'utf8');
  const start = html.indexOf('async function fetchAllNodesPaged');
  assert(start !== -1, 'fetchAllNodesPaged not found in area-map.html');
  let depth = 0, end = -1;
  for (let i = html.indexOf('{', start); i < html.length; i++) {
    if (html[i] === '{') depth++;
    else if (html[i] === '}' && --depth === 0) { end = i + 1; break; }
  }
  const src = html.slice(start, end);
  // Provide fetch + baseUrl as closure params (the function references both).
  return new Function('fetch', 'baseUrl', src + '\nreturn fetchAllNodesPaged;')(fetchImpl, 'http://host');
}

test('area-map inline helper paginates past the cap and dedups', async () => {
  const m = makeNodesFetch(1200, 500, { dupAtBoundary: true });
  const fetchAllNodesPaged = loadAreaMapHelper(m.fetch);
  const list = await fetchAllNodesPaged('');
  const keys = list.map(n => n.public_key);
  assert.strictEqual(new Set(keys).size, keys.length, 'no duplicate public_key');
  assert.ok(list.length >= 1199, 'expected ~1200 nodes past the cap, got ' + list.length);
});

test('area-map inline helper throws on a non-OK page (no silent partial)', async () => {
  const fixture500 = [];
  for (let i = 0; i < 500; i++) fixture500.push({ public_key: 'q' + i });
  const fetchImpl = (url) => {
    const off = parseInt(new URLSearchParams(url.split('?')[1] || '').get('offset') || '0', 10);
    if (off === 0) return Promise.resolve({ ok: true, json: () => Promise.resolve({ nodes: fixture500 }) });
    return Promise.resolve({ ok: false, status: 500, json: () => Promise.resolve({}) });
  };
  const fetchAllNodesPaged = loadAreaMapHelper(fetchImpl);
  let threw = false;
  try { await fetchAllNodesPaged(''); } catch (_) { threw = true; }
  assert.ok(threw, 'a non-OK page must reject, not return a truncated complete-looking list');
});

(async () => {
  await Promise.all(pending);
  console.log(`\n${passed} passed, ${failed} failed`);
  process.exit(failed > 0 ? 1 : 0);
})();

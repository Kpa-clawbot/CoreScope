/* Regression test for the /api/nodes 500-row cap (#1606 class).
 *
 * Bug: v3.8.3 / PR #1540 clamped /api/nodes ?limit to 500 as a DoS guard.
 * Every node-list consumer (map.js, live.js, analytics.js, packets.js,
 * area-map.html) issued a single ?limit=N fetch and trusted the response as
 * the full set, so deployments with >500 nodes silently saw only the top 500
 * by last_seen DESC. A repeater that relays constantly but last self-advertised
 * hours ago fell outside that window and vanished from the map/live view even
 * though it was plainly alive.
 *
 * Fix: a shared app.js `fetchAllNodes()` helper pages through /api/nodes (500
 * per request) until a short page, deduping by public_key. This test drives the
 * REAL api()+fetchAllNodes against a mocked fetch serving a 1200-node fixture
 * with a 500-per-page server cap (mirrors the real clamp + its unreliable
 * `total`). Pre-fix consumers saw 500; the helper must surface all 1200.
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

test('appends extraQuery verbatim and honors safetyCap', async () => {
  const ctx = makeSandbox();
  loadInCtx(ctx, 'public/app.js');
  const m = makeNodesFetch(5000, 500);
  ctx.fetch = m.fetch;
  const out = await ctx.fetchAllNodes('&lastHeard=30d&area=BE', { safetyCap: 1000 });
  // safetyCap=1000 → offsets 0 and 500 only → at most 1000 nodes.
  assert.ok(out.nodes.length <= 1000, 'safetyCap must bound the loop, got ' + out.nodes.length);
  assert.ok(m.calls[0].indexOf('&lastHeard=30d&area=BE') !== -1, 'extraQuery must be appended to the request');
  assert.ok(m.calls[0].indexOf('limit=500') !== -1 && m.calls[0].indexOf('offset=0') !== -1, 'first page must request limit/offset');
});

(async () => {
  await Promise.all(pending);
  console.log(`\n${passed} passed, ${failed} failed`);
  process.exit(failed > 0 ? 1 : 0);
})();

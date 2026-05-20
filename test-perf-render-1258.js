/* Tests for perf.js render performance (#1258).
 *
 * Failure modes we gate against:
 *  1) /api/health awaited sequentially AFTER Promise.all → extra RTT
 *  2) setInterval keeps polling even when document is hidden → wasted work
 *  3) Endpoints table claims "sorted by total time" but renders in map order
 */
'use strict';
const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

let passed = 0, failed = 0;
function test(name, fn) {
  const run = (r) => { if (r && typeof r.then === 'function') return r.then(() => { passed++; console.log(`  ✅ ${name}`); }, e => { failed++; console.log(`  ❌ ${name}: ${e.message}`); }); passed++; console.log(`  ✅ ${name}`); };
  try { const r = fn(); if (r && typeof r.then === 'function') return r.then(() => { passed++; console.log(`  ✅ ${name}`); }, e => { failed++; console.log(`  ❌ ${name}: ${e.message}`); }); else { passed++; console.log(`  ✅ ${name}`); } }
  catch (e) { failed++; console.log(`  ❌ ${name}: ${e.message}`); }
}

function makeSandbox(opts = {}) {
  let capturedHtml = '';
  const pages = {};
  let visState = opts.hidden ? 'hidden' : 'visible';
  const visListeners = [];
  const ctx = {
    window: { addEventListener: () => {}, apiPerf: null },
    document: {
      getElementById: (id) => {
        if (id === 'perfContent') return { set innerHTML(v) { capturedHtml = v; } };
        if (id === 'perfReset' || id === 'perfRefresh') return { addEventListener: () => {} };
        return null;
      },
      addEventListener: (ev, fn) => { if (ev === 'visibilitychange') visListeners.push(fn); },
      removeEventListener: () => {},
      get visibilityState() { return visState; },
      get hidden() { return visState === 'hidden'; },
    },
    console,
    Date, Math, Array, Object, String, Number, JSON, RegExp, Error, TypeError,
    parseInt, parseFloat, isNaN, isFinite,
    setTimeout: (fn, ms) => setTimeout(fn, ms), clearTimeout,
    setInterval: (fn, ms) => { return setInterval(fn, ms); }, clearInterval,
    performance: { now: () => Date.now() },
    Map, Set, Promise,
    registerPage: (name, handler) => { pages[name] = handler; },
    _apiCache: { size: 0 },
    fetch: () => Promise.resolve({ json: () => Promise.resolve({}) }),
  };
  ctx.window.document = ctx.document;
  ctx.globalThis = ctx;
  return { ctx, pages, getHtml: () => capturedHtml,
    setVisibility(v) { visState = v; visListeners.forEach(fn => fn()); } };
}

function loadPerf() {
  const sb = makeSandbox();
  const code = fs.readFileSync('public/perf.js', 'utf8');
  vm.runInNewContext(code, sb.ctx);
  return sb;
}

// ---------- 1) Health fetched in parallel ----------
test('all initial fetches (including /api/health) issued in parallel', async () => {
  const sb = loadPerf();
  const order = [];
  let resolveAll;
  const gate = new Promise(r => { resolveAll = r; });
  sb.ctx.fetch = (url) => {
    order.push(url);
    // Don't resolve until all 5 calls have been issued — proves they're parallel
    return gate.then(() => ({ json: () => Promise.resolve({}) }));
  };
  const p = sb.pages.perf.init({ set innerHTML(v) {} });
  // Microtask flush
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
  // Before any fetch resolves, all 5 URLs must have been started
  assert.ok(order.includes('/api/health'),
    `expected /api/health to be issued in parallel with the others, got: ${order.join(', ')}`);
  resolveAll();
  await p;
});

// ---------- 2) setInterval pauses when tab hidden ----------
test('refresh interval does not fire when document is hidden', async () => {
  const sb = loadPerf();
  let fetchCount = 0;
  sb.ctx.fetch = (url) => {
    fetchCount++;
    return Promise.resolve({ json: () => Promise.resolve({}) });
  };
  // Replace setInterval with fast firing
  let timerFn = null;
  sb.ctx.setInterval = (fn, ms) => { timerFn = fn; return 1; };
  sb.ctx.clearInterval = () => { timerFn = null; };

  await sb.pages.perf.init({ set innerHTML(v) {} });
  await new Promise(r => setTimeout(r, 30));
  const baseline = fetchCount;
  // Hide the tab, then fire the interval — should NOT issue fresh fetches
  sb.setVisibility('hidden');
  if (timerFn) timerFn();
  await new Promise(r => setTimeout(r, 30));
  assert.strictEqual(fetchCount, baseline,
    `refresh should be suppressed while hidden; baseline=${baseline} after=${fetchCount}`);
});

// ---------- 3) Endpoints table actually sorted by total time ----------
test('endpoints table is sorted by total time descending', async () => {
  const sb = loadPerf();
  // Map insertion order is preserved in JS object literals — put SLOW endpoint
  // LAST to ensure the renderer is actively sorting, not relying on input order.
  const perfData = {
    totalRequests: 100, avgMs: 5, uptime: 3600, slowQueries: [],
    endpoints: {
      '/api/fast':   { count: 1,   avgMs: 1,   p50Ms: 1,   p95Ms: 1,   maxMs: 1 },
      '/api/mid':    { count: 10,  avgMs: 10,  p50Ms: 10,  p95Ms: 10,  maxMs: 10 },
      '/api/SLOW':   { count: 100, avgMs: 100, p50Ms: 100, p95Ms: 100, maxMs: 100 },
    },
  };
  sb.ctx.fetch = (url) => {
    if (url === '/api/perf') return Promise.resolve({ json: () => Promise.resolve(perfData) });
    return Promise.resolve({ json: () => Promise.resolve(null) });
  };
  await sb.pages.perf.init({ set innerHTML(v) {} });
  await new Promise(r => setTimeout(r, 30));
  const html = sb.getHtml();
  const iSlow = html.indexOf('/api/SLOW');
  const iMid  = html.indexOf('/api/mid');
  const iFast = html.indexOf('/api/fast');
  assert.ok(iSlow > -1 && iMid > -1 && iFast > -1, 'all three endpoints must render');
  assert.ok(iSlow < iMid && iMid < iFast,
    `expected SLOW < mid < fast in DOM order, got SLOW=${iSlow} mid=${iMid} fast=${iFast}`);
});

setTimeout(() => {
  console.log(`\n${passed} passed, ${failed} failed\n`);
  process.exit(failed ? 1 : 0);
}, 500);

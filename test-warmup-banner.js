/* Unit + integration tests for warmup-banner.js — issue #1660
 *
 * Sub-deliverable (3) E2E: stub /api/healthz returning ready:false → assert
 * banner visible; flip to ready:true → assert banner fades.
 *
 * Uses a vm sandbox with a minimal DOM and a stubbed fetch — same pattern as
 * test-frontend-helpers.js. No Playwright needed for the FE-only contract.
 */
'use strict';
const vm = require('vm');
const fs = require('fs');
const path = require('path');
const assert = require('assert');

let passed = 0, failed = 0;
async function test(name, fn) {
  try {
    await fn();
    passed++;
    console.log('  \u2705 ' + name);
  } catch (e) {
    failed++;
    console.log('  \u274C ' + name + ': ' + e.message);
  }
}

function loadPureModule() {
  const ctx = { window: {}, module: { exports: {} }, console, Date };
  vm.createContext(ctx);
  const src = fs.readFileSync(path.join(__dirname, 'public', 'warmup-banner.js'), 'utf8');
  vm.runInContext(src, ctx);
  return ctx.window.__warmupBanner || ctx.module.exports;
}

/**
 * Build a sandbox with a minimal DOM and a stubbable fetch.
 */
function bootDomSandbox(initialHealthz, initialHeader) {
  let currentHealthz = initialHealthz;
  let currentHeader = initialHeader;
  const nodes = [];
  function makeNode(tag) {
    const node = {
      tagName: tag, id: '', className: '', attrs: {}, children: [], _innerHTML: '',
      get innerHTML() { return this._innerHTML; },
      set innerHTML(v) { this._innerHTML = String(v); },
      classList: {
        _set: new Set(),
        add(c) { this._set.add(c); },
        remove(c) { this._set.delete(c); },
        contains(c) { return this._set.has(c); },
      },
      setAttribute(k, v) { this.attrs[k] = String(v); },
      getAttribute(k) { return this.attrs[k]; },
      appendChild(c) { this.children.push(c); return c; },
      insertBefore(c) { this.children.unshift(c); return c; },
      get firstChild() { return this.children[0] || null; },
    };
    nodes.push(node);
    return node;
  }
  const body = makeNode('body');
  const doc = {
    readyState: 'complete', body: body,
    createElement: (tag) => makeNode(tag),
    addEventListener: () => {},
    getElementById: (id) => nodes.find(n => n.id === id) || null,
  };
  function makeFetch() {
    return function () {
      const headers = {
        get(name) {
          if (String(name).toLowerCase() === 'x-corescope-load-status') return currentHeader;
          return null;
        },
      };
      return Promise.resolve({
        ok: true, headers, json: () => Promise.resolve(currentHealthz),
      });
    };
  }
  const ctx = {
    window: {}, document: doc, console, Date, Math, Number, Object, String, JSON, isFinite,
    setTimeout: () => 0, clearTimeout: () => {},
    setInterval: () => 1, clearInterval: () => {},
    fetch: makeFetch(), Promise,
    module: { exports: {} },
  };
  ctx.window.fetch = ctx.fetch;
  vm.createContext(ctx);
  const src = fs.readFileSync(path.join(__dirname, 'public', 'warmup-banner.js'), 'utf8');
  vm.runInContext(src, ctx);
  const sapi = ctx.window.__warmupBanner;
  return {
    api: sapi, body, nodes,
    setHealthz(h, header) {
      currentHealthz = h;
      if (header !== undefined) currentHeader = header;
    },
    async runPoll() {
      sapi._pollOnce();
      await new Promise((r) => setImmediate(r));
      await new Promise((r) => setImmediate(r));
      await new Promise((r) => setImmediate(r));
    },
    getBanner() { return nodes.find(n => n.id === 'warmup-banner') || null; },
  };
}

(async function main() {
  console.log('warmup-banner.js (#1660):');

  const api = loadPureModule();
  const NOW = 1_700_000_000_000;

  await test('exports getWarmupMessages and shouldShowBanner', () => {
    assert.strictEqual(typeof api.getWarmupMessages, 'function');
    assert.strictEqual(typeof api.shouldShowBanner, 'function');
  });

  await test('loading header alone produces a "historical data" message', () => {
    const msgs = api.getWarmupMessages(null, 'loading', NOW);
    assert.ok(msgs.length >= 1, 'expected at least one message, got 0');
    assert.ok(msgs.some(m => /historical data/i.test(m)),
      'expected "historical data" message, got: ' + JSON.stringify(msgs));
  });

  await test('from_pubkey_backfill.done=false produces a progress message with pct', () => {
    const h = { ready: true,
      from_pubkey_backfill: { done: false, processed: 12400, total: 87500 },
      ingest_liveness: {} };
    const msgs = api.getWarmupMessages(h, 'ready', NOW);
    assert.ok(msgs.some(m => /Backfilling pubkey index/i.test(m)),
      'expected backfill message, got: ' + JSON.stringify(msgs));
    assert.ok(msgs.some(m => m.includes('14%')),
      'expected "14%", got: ' + JSON.stringify(msgs));
  });

  await test('stale ingest source >5min produces a "No packets from" message', () => {
    const h = { ready: true,
      from_pubkey_backfill: { done: true, processed: 1, total: 1 },
      ingest_liveness: {
        'mqtt-eu': { lastReceiptUnix: Math.floor((NOW - 6 * 60 * 1000) / 1000) } } };
    const msgs = api.getWarmupMessages(h, 'ready', NOW);
    assert.ok(msgs.some(m => /No packets from mqtt-eu/.test(m)),
      'expected stale-ingest message, got: ' + JSON.stringify(msgs));
  });

  await test('steady-state ready=true + backfill done + fresh ingest → no banner', () => {
    const h = { ready: true,
      from_pubkey_backfill: { done: true, processed: 1, total: 1 },
      ingest_liveness: {
        'mqtt-eu': { lastReceiptUnix: Math.floor((NOW - 30 * 1000) / 1000) } } };
    const msgs = api.getWarmupMessages(h, 'ready', NOW);
    assert.strictEqual(msgs.length, 0,
      'expected no messages, got: ' + JSON.stringify(msgs));
    assert.strictEqual(api.shouldShowBanner(h, 'ready', NOW), false);
  });

  await test('isSteadyState reflects ready+backfill predicate', () => {
    assert.strictEqual(api.isSteadyState(null), false);
    assert.strictEqual(api.isSteadyState({ ready: false }), false);
    assert.strictEqual(api.isSteadyState({ ready: true,
      from_pubkey_backfill: { done: false } }), false);
    assert.strictEqual(api.isSteadyState({ ready: true,
      from_pubkey_backfill: { done: true } }), true);
    assert.strictEqual(api.isSteadyState({ ready: true }), true);
  });

  await test('E2E: stub /api/healthz ready=false → banner visible', async () => {
    const env = bootDomSandbox({ ready: false,
      from_pubkey_backfill: { done: false, processed: 100, total: 1000 },
      ingest_liveness: {} }, 'loading');
    await env.runPoll();
    const banner = env.getBanner();
    assert.ok(banner, 'expected #warmup-banner to be mounted');
    assert.strictEqual(banner.getAttribute('role'), 'status',
      'banner must be role="status" live region (a11y requirement)');
    assert.strictEqual(banner.classList.contains('warmup-banner--hidden'), false,
      'banner should be visible when ready=false');
    const hasText = banner.children.some(c =>
      c.children && c.children.some(li => /Loading|Backfilling/i.test(li.innerHTML || '')) ||
      /Loading|Backfilling/i.test(c.innerHTML || ''));
    assert.ok(hasText, 'banner should contain warm-up text');
  });

  await test('E2E: flip /api/healthz to ready=true → banner fades (hidden class)', async () => {
    const env = bootDomSandbox({ ready: false,
      from_pubkey_backfill: { done: false, processed: 100, total: 1000 },
      ingest_liveness: {} }, 'loading');
    await env.runPoll();
    env.setHealthz({ ready: true,
      from_pubkey_backfill: { done: true, processed: 1000, total: 1000 },
      ingest_liveness: {} }, 'ready');
    await env.runPoll();
    const banner = env.getBanner();
    assert.ok(banner, 'expected #warmup-banner to remain in DOM');
    assert.strictEqual(banner.classList.contains('warmup-banner--hidden'), true,
      'banner should be hidden after steady state');
  });

  // -------- kb #3: staleness boundary (> vs >=) ---------------------------
  await test('staleness boundary: age == STALE_INGEST_MS is NOT stale (uses >)', () => {
    const STALE = api.STALE_INGEST_MS;
    const h = { ready: true,
      from_pubkey_backfill: { done: true, processed: 1, total: 1 },
      ingest_liveness: {
        'mqtt-eu': { lastReceiptUnix: (NOW - STALE) / 1000 } } };
    const msgs = api.getWarmupMessages(h, 'ready', NOW);
    assert.ok(!msgs.some(m => /No packets from mqtt-eu/.test(m)),
      'at exactly STALE_INGEST_MS the source must NOT be reported stale; got: ' + JSON.stringify(msgs));
  });

  await test('staleness boundary: age == STALE_INGEST_MS + 1ms IS stale', () => {
    const STALE = api.STALE_INGEST_MS;
    const h = { ready: true,
      from_pubkey_backfill: { done: true, processed: 1, total: 1 },
      ingest_liveness: {
        'mqtt-eu': { lastReceiptUnix: (NOW - (STALE + 1)) / 1000 } } };
    const msgs = api.getWarmupMessages(h, 'ready', NOW);
    assert.ok(msgs.some(m => /No packets from mqtt-eu/.test(m)),
      'at STALE_INGEST_MS+1ms the source must be reported stale; got: ' + JSON.stringify(msgs));
  });

  // -------- kb #4: backfill formatter edge cases --------------------------
  await test('backfill total=0 → no NaN%, no divide-by-zero (pct=0)', () => {
    const h = { ready: false,
      from_pubkey_backfill: { done: false, processed: 0, total: 0 },
      ingest_liveness: {} };
    const msgs = api.getWarmupMessages(h, 'loading', NOW);
    const backfillMsg = msgs.find(m => /Backfilling pubkey index/.test(m));
    assert.ok(backfillMsg, 'expected backfill message present, got: ' + JSON.stringify(msgs));
    assert.ok(!/NaN/.test(backfillMsg),
      'backfill msg must not contain NaN; got: ' + backfillMsg);
    assert.ok(/\(0%\)/.test(backfillMsg),
      'total=0 must render as 0%; got: ' + backfillMsg);
  });

  await test('backfill processed>total (race) → clamps at 100%, no overshoot', () => {
    const h = { ready: false,
      from_pubkey_backfill: { done: false, processed: 1500, total: 1000 },
      ingest_liveness: {} };
    const msgs = api.getWarmupMessages(h, 'loading', NOW);
    const backfillMsg = msgs.find(m => /Backfilling pubkey index/.test(m));
    assert.ok(backfillMsg, 'expected backfill message');
    assert.ok(/\(100%\)/.test(backfillMsg),
      'processed>total must clamp at 100%; got: ' + backfillMsg);
    assert.ok(!/15\d%|1\d\d%(?!00)/.test(backfillMsg.replace('100%', '')),
      'no >100% should appear; got: ' + backfillMsg);
  });

  // -------- kb #5: fetch-wrapper double-install ---------------------------
  await test('fetch interceptor: double-install does not nest wrappers', () => {
    let callCount = 0;
    const baseFetch = function () {
      callCount++;
      return Promise.resolve({
        ok: true,
        headers: { get: () => null },
        json: () => Promise.resolve({ ready: true }),
      });
    };
    const ctx = {
      window: {}, document: undefined, console, Date, Math, Number, Object, String, JSON, isFinite,
      setTimeout: () => 0, clearTimeout: () => {},
      setInterval: () => 1, clearInterval: () => {},
      Promise, module: { exports: {} },
    };
    ctx.window.fetch = baseFetch;
    vm.createContext(ctx);
    const src = fs.readFileSync(path.join(__dirname, 'public', 'warmup-banner.js'), 'utf8');
    vm.runInContext(src, ctx);
    const a = ctx.window.__warmupBanner;
    // First install
    a._installFetchInterceptor.call(ctx);
    const f1 = ctx.window.fetch;
    assert.strictEqual(f1.__warmupWrapped, true, 'wrapper must mark window.fetch.__warmupWrapped');
    // Second install — must be a no-op
    a._installFetchInterceptor.call(ctx);
    const f2 = ctx.window.fetch;
    assert.strictEqual(f2, f1, 'second install must not replace the wrapper (no nesting)');
    // Verify underlying call chain didn't grow: one call should invoke baseFetch exactly once
    return f2().then(() => {
      assert.strictEqual(callCount, 1,
        'one fetch() must invoke baseFetch exactly once (nested wrap would call >1 or 0)');
    });
  });



  console.log('');
  console.log('passed=' + passed + ' failed=' + failed);
  process.exit(failed > 0 ? 1 : 0);
})();

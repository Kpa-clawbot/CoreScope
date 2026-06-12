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

  console.log('');
  console.log('passed=' + passed + ' failed=' + failed);
  process.exit(failed > 0 ? 1 : 0);
})();

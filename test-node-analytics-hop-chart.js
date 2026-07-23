/**
 * Tests for the "Relay Hop-Count" chart on the node analytics page
 * (public/node-analytics.js), added for upstream issue #1812.
 *
 * hopQuantile is pure math and gets exact-value tests. The render path
 * (chips, boxplot canvas, Chart.js histogram) is exercised against a
 * minimal fake DOM sufficient to prove it doesn't throw and that filtering
 * by transport chip actually narrows the packet set — not a pixel-level
 * check of the hand-drawn boxplot, which isn't meaningfully assertable
 * headless.
 */
'use strict';

const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

let passed = 0, failed = 0;
async function testAsync(name, fn) {
  try {
    await fn();
    passed++;
    console.log(`  ✅ ${name}`);
  } catch (e) {
    failed++;
    console.log(`  ❌ ${name}: ${e.message}`);
  }
}

// A fake element capable enough for renderHopAnalyticsChips (innerHTML with
// data-hop-transport buttons, re-parsed by querySelectorAll) and for plain
// text/style containers (summary, empty banner) and canvases.
function makeFakeElement(id) {
  const el = {
    id,
    _innerHTML: '',
    textContent: '',
    style: {},
    dataset: {},
    parentElement: { clientWidth: 400 },
    _listeners: [],
    addEventListener(type, fn) { this._listeners.push([type, fn]); },
    get innerHTML() { return this._innerHTML; },
    set innerHTML(html) {
      this._innerHTML = html;
      // Re-derive the button list from the freshly-set markup, matching
      // real DOM behavior closely enough for renderHopAnalyticsChips'
      // querySelectorAll('[data-hop-transport]') call right after.
      this._buttons = [];
      const re = /data-hop-transport="([^"]+)"/g;
      let m;
      while ((m = re.exec(html))) {
        this._buttons.push(makeFakeButton(m[1]));
      }
    },
    querySelectorAll(sel) {
      if (sel === '[data-hop-transport]') return this._buttons || [];
      return [];
    },
    getContext() {
      // No-op 2D context — enough for drawHopBoxplot to run without throwing.
      const noop = () => {};
      return {
        clearRect: noop, beginPath: noop, moveTo: noop, lineTo: noop, stroke: noop,
        fillRect: noop, strokeRect: noop, setTransform: noop,
        set fillStyle(v) {}, set strokeStyle(v) {}, set lineWidth(v) {},
      };
    },
  };
  return el;
}

function makeFakeButton(transportKey) {
  const btn = { dataset: { hopTransport: transportKey }, _listeners: [] };
  btn.addEventListener = (type, fn) => btn._listeners.push([type, fn]);
  btn.click = () => btn._listeners.forEach(([t, fn]) => t === 'click' && fn());
  return btn;
}

function makeSandbox() {
  const elements = {};
  ['hopAnalyticsChips', 'hopAnalyticsSummary', 'hopAnalyticsBoxplot', 'hopAnalyticsHistogram', 'hopAnalyticsEmpty']
    .forEach(id => { elements[id] = makeFakeElement(id); });

  const ctx = {
    window: { addEventListener: () => {}, dispatchEvent: () => {}, devicePixelRatio: 1 },
    document: {
      readyState: 'complete',
      createElement: () => makeFakeElement(''),
      head: { appendChild: () => {} },
      getElementById: (id) => elements[id] || null,
      documentElement: { getAttribute: () => null, setAttribute: () => {} },
      addEventListener: () => {},
      querySelectorAll: () => [],
      querySelector: () => null,
    },
    console, Date, Infinity, Math, Array, Object, String, Number, JSON, RegExp,
    Error, TypeError, parseInt, parseFloat, isNaN, isFinite,
    encodeURIComponent, decodeURIComponent,
    setTimeout: () => {}, clearTimeout: () => {},
    fetch: () => Promise.resolve({ json: () => Promise.resolve({}) }),
    performance: { now: () => Date.now() },
    devicePixelRatio: 1,
    getComputedStyle: () => ({ getPropertyValue: () => '' }),
    localStorage: (() => { const s = {}; return { getItem: k => s[k] || null, setItem: (k, v) => { s[k] = String(v); }, removeItem: k => { delete s[k]; } }; })(),
    location: { hash: '' },
    CustomEvent: class CustomEvent {},
    Map, Promise, URLSearchParams,
    addEventListener: () => {},
    dispatchEvent: () => {},
    requestAnimationFrame: (cb) => setTimeout(cb, 0),
  };
  vm.createContext(ctx);
  ctx.__elements = elements;
  return ctx;
}

function loadInCtx(ctx, file) {
  vm.runInContext(fs.readFileSync(file, 'utf8'), ctx);
  for (const k of Object.keys(ctx.window)) ctx[k] = ctx.window[k];
}

function makeNodeAnalyticsSandbox(apiResponses) {
  const ctx = makeSandbox();
  ctx.registerPage = () => {};
  ctx.escapeHtml = (s) => String(s);
  ctx.timeAgo = () => '—';
  ctx.formatChartAxisLabel = null;
  // Minimal Chart.js stand-in: records construction, no-ops for the rest.
  ctx.Chart = function (canvas, cfg) {
    this.canvas = canvas; this.cfg = cfg;
    this.destroy = () => {};
  };
  ctx.Chart.defaults = { color: '', borderColor: '' };
  ctx.api = (path) => {
    for (const key of Object.keys(apiResponses)) {
      if (path.indexOf(key) === 0) return Promise.resolve(apiResponses[key]);
    }
    return Promise.reject(new Error('unhandled api path: ' + path));
  };
  loadInCtx(ctx, 'public/roles.js');
  try { loadInCtx(ctx, 'public/node-analytics.js'); } catch (e) {
    for (const k of Object.keys(ctx.window)) ctx[k] = ctx.window[k];
  }
  return ctx;
}

(async () => {
  console.log('\n=== node-analytics.js: hopQuantile ===');

  await testAsync('hopQuantile returns exact values at the sorted array boundaries', () => {
    const ctx = makeNodeAnalyticsSandbox({});
    const q = ctx.window._nodeAnalyticsHopQuantile;
    const arr = [1, 2, 3, 4, 5];
    assert.strictEqual(q(arr, 0), 1);
    assert.strictEqual(q(arr, 1), 5);
    assert.strictEqual(q(arr, 0.5), 3);
  });

  await testAsync('hopQuantile interpolates between the two nearest ranks', () => {
    const ctx = makeNodeAnalyticsSandbox({});
    const q = ctx.window._nodeAnalyticsHopQuantile;
    // [1,2,3,4] -- Q1 index = 0.25*3 = 0.75 -> interpolate between arr[0]=1 and arr[1]=2
    assert.strictEqual(q([1, 2, 3, 4], 0.25), 1.75);
  });

  await testAsync('hopQuantile handles a single-element array', () => {
    const ctx = makeNodeAnalyticsSandbox({});
    const q = ctx.window._nodeAnalyticsHopQuantile;
    assert.strictEqual(q([7], 0.25), 7);
    assert.strictEqual(q([7], 0.75), 7);
  });

  console.log('\n=== node-analytics.js: Relay Hop-Count chart ===');

  await testAsync('loadHopAnalyticsChart fetches and stores packets, default filter is "flood"', async () => {
    const packets = [
      { hash: 'a', tsMs: 1, hops: 1, transport: 'flood', scoped: true },
      { hash: 'b', tsMs: 2, hops: 2, transport: 'flood', scoped: true },
      { hash: 'c', tsMs: 3, hops: 0, transport: 'direct', scoped: false },
    ];
    const ctx = makeNodeAnalyticsSandbox({ '/nodes/pk1/hop_analytics': { packets } });
    await ctx.window._nodeAnalyticsLoadHopChart('pk1', 7);
    assert.strictEqual(ctx.window._nodeAnalyticsGetHopFilter(), 'flood');
    assert.deepStrictEqual(ctx.window._nodeAnalyticsGetHopPackets(), packets);
  });

  await testAsync('renderHopAnalyticsChips wires a click that changes the active filter and re-renders', async () => {
    const packets = [
      { hash: 'a', tsMs: 1, hops: 1, transport: 'flood', scoped: true },
      { hash: 'b', tsMs: 2, hops: 3, transport: 'direct', scoped: false },
    ];
    const ctx = makeNodeAnalyticsSandbox({ '/nodes/pk1/hop_analytics': { packets } });
    await ctx.window._nodeAnalyticsLoadHopChart('pk1', 7);

    const chipsEl = ctx.__elements.hopAnalyticsChips;
    const directBtn = (chipsEl._buttons || []).find(b => b.dataset.hopTransport === 'direct');
    assert.ok(directBtn, 'a "direct" chip button should have been rendered');
    directBtn.click();

    assert.strictEqual(ctx.window._nodeAnalyticsGetHopFilter(), 'direct');
    // Summary text should now reflect only the 1 "direct" packet (hops=3).
    assert.ok(ctx.__elements.hopAnalyticsSummary.textContent.indexOf('1 packets') !== -1,
      'summary should reflect the single direct-filtered packet, got: ' + ctx.__elements.hopAnalyticsSummary.textContent);
  });

  await testAsync('renderHopAnalyticsChart shows the empty state when no packet matches the active filter', async () => {
    const packets = [{ hash: 'a', tsMs: 1, hops: 1, transport: 'flood_unscoped', scoped: false }];
    const ctx = makeNodeAnalyticsSandbox({ '/nodes/pk1/hop_analytics': { packets } });
    await ctx.window._nodeAnalyticsLoadHopChart('pk1', 7); // default filter "flood" matches nothing here
    assert.strictEqual(ctx.__elements.hopAnalyticsEmpty.style.display, 'block');
  });

  await testAsync('a fetch failure shows the empty banner with an error message instead of throwing', async () => {
    const ctx = makeNodeAnalyticsSandbox({}); // no matching api() route -> rejects
    await ctx.window._nodeAnalyticsLoadHopChart('unknown-pk', 7);
    assert.strictEqual(ctx.__elements.hopAnalyticsEmpty.style.display, 'block');
    assert.ok(ctx.__elements.hopAnalyticsEmpty.textContent.indexOf('unavailable') !== -1);
  });

  console.log('\n════════════════════════════════════════');
  console.log(`  Node Analytics Hop Chart: ${passed} passed, ${failed} failed`);
  console.log('════════════════════════════════════════');
  process.exit(failed === 0 ? 0 : 1);
})();

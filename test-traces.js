/* Unit tests for traces.js helpers (tested via VM sandbox) */
'use strict';
const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

let passed = 0, failed = 0;
function test(name, fn) {
  try {
    fn();
    passed++;
    console.log(`  ✅ ${name}`);
  } catch (e) {
    failed++;
    console.log(`  ❌ ${name}: ${e.message}`);
  }
}

function makeSandbox() {
  const ctx = {
    window: { addEventListener: () => {}, dispatchEvent: () => {} },
    document: {
      readyState: 'complete',
      createElement: () => ({ id: '', textContent: '', innerHTML: '', addEventListener() {} }),
      head: { appendChild: () => {} },
      getElementById: () => null,
      addEventListener: () => {},
      querySelectorAll: () => [],
      querySelector: () => null,
    },
    console,
    Date, Infinity, Math, Array, Object, String, Number, JSON, RegExp, Error,
    parseInt, parseFloat, isNaN, isFinite,
    encodeURIComponent, decodeURIComponent,
    setTimeout: () => {}, clearTimeout: () => {},
    setInterval: () => {}, clearInterval: () => {},
    fetch: () => Promise.resolve({ json: () => Promise.resolve({}) }),
    performance: { now: () => Date.now() },
    localStorage: (() => {
      const store = {};
      return {
        getItem: k => store[k] || null,
        setItem: (k, v) => { store[k] = String(v); },
        removeItem: k => { delete store[k]; },
      };
    })(),
    location: { hash: '' },
    CustomEvent: class CustomEvent {},
    Map, Set, Promise, URLSearchParams,
    addEventListener: () => {},
    dispatchEvent: () => {},
    requestAnimationFrame: (cb) => setTimeout(cb, 0),
    registerPage: () => {},
    payloadTypeName: () => '',
    payloadTypeColor: () => '',
    escapeHtml: s => s,
  };
  vm.createContext(ctx);
  return ctx;
}

function loadTracesJs(ctx) {
  vm.runInContext(fs.readFileSync('public/traces.js', 'utf8'), ctx);
  for (const k of Object.keys(ctx.window)) ctx[k] = ctx.window[k];
}

// ===== dedupePrefixPaths tests =====
console.log('\n=== traces.js: dedupePrefixPaths ===');
{
  const ctx = makeSandbox();
  loadTracesJs(ctx);
  const { dedupePrefixPaths } = ctx.TracesHelpers;

  test('two strict-prefix observations: only longer kept', () => {
    const a = { hops: ['x', 'y'], observer: 'A' };
    const b = { hops: ['x', 'y', 'z'], observer: 'B' };
    const result = dedupePrefixPaths([a, b]);
    assert.deepStrictEqual(result, [b]);
  });

  test('two identical-length identical-path observations: both kept', () => {
    const a = { hops: ['x', 'y'], observer: 'A' };
    const b = { hops: ['x', 'y'], observer: 'B' };
    const result = dedupePrefixPaths([a, b]);
    assert.deepStrictEqual(result, [a, b]);
  });

  test('two divergent paths: both kept', () => {
    const a = { hops: ['x', 'y'], observer: 'A' };
    const b = { hops: ['x', 'z'], observer: 'B' };
    const result = dedupePrefixPaths([a, b]);
    assert.deepStrictEqual(result, [a, b]);
  });

  test('empty hops array: not dropped (no superseder possible)', () => {
    const a = { hops: [], observer: 'A' };
    const b = { hops: ['x'], observer: 'B' };
    const result = dedupePrefixPaths([a, b]);
    // a has length 0, b has length 1; b.slice(0,0) = [] === [] so a IS a prefix of b
    // a should be dropped
    assert.ok(!result.includes(a), 'empty-hops path should be dropped when superseded');
    assert.ok(result.includes(b));
  });

  test('three-level prefix chain (A⊂B⊂C): only C kept', () => {
    const a = { hops: ['x'], observer: 'A' };
    const b = { hops: ['x', 'y'], observer: 'B' };
    const c = { hops: ['x', 'y', 'z'], observer: 'C' };
    const result = dedupePrefixPaths([a, b, c]);
    assert.deepStrictEqual(result, [c]);
  });

  test('multiple observers on identical full path: all kept', () => {
    const a = { hops: ['x', 'y', 'z'], observer: 'A' };
    const b = { hops: ['x', 'y', 'z'], observer: 'B' };
    const c = { hops: ['x', 'y', 'z'], observer: 'C' };
    const result = dedupePrefixPaths([a, b, c]);
    assert.deepStrictEqual(result, [a, b, c]);
  });
}

// ===== renderPathGraph: per-hop SNR overlay (#1004 Phase 2 of #979) =====
console.log('\n=== traces.js: renderPathGraph hop-SNR overlay ===');
{
  const ctx = makeSandbox();
  loadTracesJs(ctx);
  const { renderPathGraph } = ctx.TracesHelpers;
  assert.strictEqual(typeof renderPathGraph, 'function', 'renderPathGraph must be exported');

  const paths = [{ hops: ['R1', 'R2'], observer: 'OBS' }];
  const decodedTrace = { type: 'TRACE', snrValues: [-3.5, -7.0, -12.25] };
  const decodedNonTrace = { type: 'CHAN', snrValues: [-3.5, -7.0] };

  test('TRACE with snrValues emits <text class="hop-snr"> labels with values', () => {
    const html = renderPathGraph(paths, paths, decodedTrace);
    assert.ok(/class="hop-snr"/.test(html), 'expected <text class="hop-snr"> in output');
    // Each numeric value should appear in a hop-snr label.
    const labels = html.match(/<text[^>]*class="hop-snr"[^>]*>([^<]+)<\/text>/g) || [];
    assert.ok(labels.length >= 1, 'expected at least one hop-snr label');
    const joined = labels.join(' ');
    assert.ok(/-3\.5/.test(joined), 'expected -3.5 in hop-snr label, got: ' + joined);
    assert.ok(/-7(\.0)?/.test(joined), 'expected -7 in hop-snr label, got: ' + joined);
  });

  test('non-TRACE packet: hop-snr labels are ABSENT even when snrValues present', () => {
    const html = renderPathGraph(paths, paths, decodedNonTrace);
    assert.ok(!/class="hop-snr"/.test(html), 'hop-snr must not render for non-TRACE');
  });

  test('TRACE with empty snrValues: no hop-snr labels', () => {
    const html = renderPathGraph(paths, paths, { type: 'TRACE', snrValues: [] });
    assert.ok(!/class="hop-snr"/.test(html), 'hop-snr must not render when snrValues empty');
  });

  test('decoded omitted: no hop-snr labels (back-compat)', () => {
    const html = renderPathGraph(paths, paths);
    assert.ok(!/class="hop-snr"/.test(html), 'hop-snr must not render when decoded omitted');
  });
}

// ===== SUMMARY =====
console.log(`\n${'═'.repeat(40)}`);
console.log(`  traces.js: ${passed} passed, ${failed} failed`);
console.log(`${'═'.repeat(40)}\n`);
if (failed > 0) process.exit(1);

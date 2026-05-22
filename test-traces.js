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

// ===== SUMMARY =====
console.log(`\n${'═'.repeat(40)}`);
console.log(`  traces.js: ${passed} passed, ${failed} failed`);
console.log(`${'═'.repeat(40)}\n`);
if (failed > 0) process.exit(1);

/* test-issue-1633-hide-1byte-hops.js
 * #1633 — customizer toggle that hides 1-byte path hops at every
 * render site. Render-time only; firmware/store untouched.
 *
 * Asserts:
 *  1. Defaults OFF (back-compat: no surprise for existing operators).
 *  2. With hide1ByteHops=true a mixed path renders ONLY multi-byte hops.
 *  3. With hide1ByteHops=false the full path renders.
 *  4. HopDisplay.renderPath emits exactly the multi-byte chips when ON.
 *  5. Map polyline source: positions tagged with 1-byte _hopHex are dropped
 *     from the polyline path when ON; 2/3-byte hops survive; origin /
 *     destination (no _hopHex) are always kept.
 *  6. Analytics route-pattern aggregation: rebuilt path key contains ONLY
 *     multi-byte hop hexes when ON.
 */
'use strict';

const assert = require('assert');
const fs = require('fs');
const path = require('path');
const vm = require('vm');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log('  ✅ ' + name); }
  catch (e) { failed++; console.log('  ❌ ' + name + ': ' + e.message); }
}

// ── DOM-less sandbox so hop-display.js / hop-filter.js load cleanly ──
function makeSandbox() {
  const store = {};
  const ctx = {
    window: {
      addEventListener: () => {},
      dispatchEvent: () => {},
      CustomEvent: function (n, d) { this.type = n; this.detail = (d && d.detail) || null; }
    },
    document: {
      readyState: 'complete',
      createElement: () => ({ id: '', textContent: '', innerHTML: '', dataset: {}, setAttribute(){}, getAttribute(){return null;} }),
      head: { appendChild: () => {} },
      body: { appendChild: () => {} },
      getElementById: () => null,
      addEventListener: () => {},
      querySelectorAll: () => [],
      querySelector: () => null,
    },
    localStorage: {
      getItem: k => (k in store ? store[k] : null),
      setItem: (k, v) => { store[k] = String(v); },
      removeItem: k => { delete store[k]; },
      clear: () => { for (const k of Object.keys(store)) delete store[k]; }
    },
    console, Math, Date, Array, Object, String, Number, JSON, Boolean,
    module: { exports: {} }, exports: {},
  };
  ctx.window.localStorage = ctx.localStorage;
  vm.createContext(ctx);
  return ctx;
}

function load(ctx, file) {
  const src = fs.readFileSync(path.join(__dirname, file), 'utf8');
  vm.runInContext(src, ctx, { filename: file });
}

// === Build a sandbox with hop-filter + hop-display loaded ===
function freshSandbox() {
  const ctx = makeSandbox();
  load(ctx, 'public/hop-filter.js');
  load(ctx, 'public/hop-display.js');
  return ctx;
}

console.log('=== #1633: hide 1-byte path hops everywhere ===');

test('default is OFF (back-compat)', () => {
  const ctx = freshSandbox();
  assert.strictEqual(ctx.window.MC_getHide1ByteHops(), false, 'default must be OFF');
});

test('hopByteLen: "AB"=1, "ABCD"=2, "ABCDEF"=3, ""=0', () => {
  const ctx = freshSandbox();
  const bl = ctx.window.MC_hopByteLen;
  assert.strictEqual(bl('AB'), 1);
  assert.strictEqual(bl('ABCD'), 2);
  assert.strictEqual(bl('ABCDEF'), 3);
  assert.strictEqual(bl(''), 0);
  assert.strictEqual(bl(null), 0);
});

test('isVisibleHop: toggle OFF → every hop visible', () => {
  const ctx = freshSandbox();
  const f = ctx.window.MC_isVisibleHop;
  assert.strictEqual(f('AB', { hide1ByteHops: false }), true);
  assert.strictEqual(f('CDEF', { hide1ByteHops: false }), true);
});

test('isVisibleHop: toggle ON → 1-byte HIDDEN, 2/3-byte SHOWN', () => {
  const ctx = freshSandbox();
  const f = ctx.window.MC_isVisibleHop;
  assert.strictEqual(f('AB',     { hide1ByteHops: true }), false, '1-byte must hide');
  assert.strictEqual(f('CDEF',   { hide1ByteHops: true }), true,  '2-byte must stay');
  assert.strictEqual(f('ABCDEF', { hide1ByteHops: true }), true,  '3-byte must stay');
});

test('filterPathHops: mixed input → only multi-byte kept when ON', () => {
  const ctx = freshSandbox();
  const f = ctx.window.MC_filterPathHops;
  const mixed = ['AB', 'CDEF', '12', 'ABCDEF', '34'];
  // Use Array.from to bridge the vm-realm Array prototype gap.
  assert.deepStrictEqual(
    Array.from(f(mixed, { hide1ByteHops: true })),
    ['CDEF', 'ABCDEF'],
    'must drop every 1-byte entry'
  );
  assert.deepStrictEqual(
    Array.from(f(mixed, { hide1ByteHops: false })),
    mixed,
    'OFF must return input unchanged'
  );
});

test('HopDisplay.renderPath rendered chips match filtered hops when ON', () => {
  const ctx = freshSandbox();
  // Apply the filter at the boundary the way every render site will.
  const hops = ['AB', 'CDEF', '12', 'ABCDEF'];
  const filtered = ctx.window.MC_filterPathHops(hops, { hide1ByteHops: true });
  const html = ctx.window.HopDisplay.renderPath(filtered, {}, { hexMode: true, link: false });
  // Filtered chip set: ONLY 2-byte + 3-byte tokens present.
  assert.ok(html.indexOf('CDEF') !== -1,   'CDEF chip must render');
  assert.ok(html.indexOf('ABCDEF') !== -1, 'ABCDEF chip must render');
  // 1-byte hex tokens must NOT appear in the chip set. Use word
  // boundaries with the chip wrapper to avoid matching substrings of
  // the larger hops.
  assert.strictEqual(html.indexOf('>AB<'), -1, '1-byte AB must NOT render as a chip');
  assert.strictEqual(html.indexOf('>12<'), -1, '1-byte 12 must NOT render as a chip');
});

test('Map polyline source: positions[]._hopHex 1-byte dropped, origin/dest kept', () => {
  const ctx = freshSandbox();
  // Origin & destination have no _hopHex (came from payload, not from path bytes).
  // Intermediate hops carry _hopHex tagged by drawPacketRoute.
  const positions = [
    { name: 'Origin', isOrigin: true },
    { name: 'h1', _hopHex: 'AB' },         // 1-byte → filterable
    { name: 'h2', _hopHex: 'CDEF' },       // 2-byte → keep
    { name: 'h3', _hopHex: '12' },         // 1-byte → filterable
    { name: 'h4', _hopHex: 'ABCDEF' },     // 3-byte → keep
    { name: 'Dest', isDest: true }
  ];
  const opts = { hide1ByteHops: true };
  const kept = positions.filter(p =>
    !p._hopHex || ctx.window.MC_isVisibleHop(p._hopHex, opts)
  );
  assert.deepStrictEqual(
    kept.map(p => p.name),
    ['Origin', 'h2', 'h4', 'Dest'],
    'polyline retains origin/dest + multi-byte hops only'
  );
});

test('Analytics: route-pattern aggregation key drops 1-byte hops when ON', () => {
  const ctx = freshSandbox();
  // Each packet contributes one path. With hide ON, aggregation must key on
  // the FILTERED hop list so 1-byte noise stops inflating distinct counts.
  const packets = [
    { path_json: ['AB', 'CDEF', '12', 'ABCDEF'] },
    { path_json: ['99', 'CDEF', '88', 'ABCDEF'] },   // same multi-byte key
    { path_json: ['CDEF', 'ABCDEF'] }                // identical w/o 1-byte
  ];
  const opts = { hide1ByteHops: true };
  const counts = {};
  for (const p of packets) {
    const key = ctx.window.MC_filterPathHops(p.path_json, opts).join('→');
    counts[key] = (counts[key] || 0) + 1;
  }
  assert.deepStrictEqual(counts, { 'CDEF→ABCDEF': 3 },
    'all three rows collapse to one pattern with 3 hits');
});

console.log('\n' + passed + ' passed, ' + failed + ' failed');
process.exit(failed > 0 ? 1 : 0);

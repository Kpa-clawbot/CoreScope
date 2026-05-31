/**
 * #1498 — Unit tests for the mergeWsAppendedIntoRest() helper.
 *
 * Loads public/channels.js in a sandboxed vm context and exercises the
 * exported helper directly (window._channelsMergeWsAppendedIntoRestForTest).
 * Covers: empty inputs, no overlap, full overlap, partial overlap,
 * null packetHash, ordering preservation, no-alias guarantee, eviction
 * of stale survivors past MAX_WS_SURVIVOR_MS.
 */
'use strict';
const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

// channels.js is large and references many DOM/globals it expects only at
// call time. The merge helper itself touches none of them. We load the
// file inside a tolerant sandbox; if any IIFE init fails on missing DOM,
// we still grab the helper if it was exported before the failure.
const noop = () => {};
const fakeEl = { addEventListener: noop, querySelector: () => fakeEl, classList: { add: noop, remove: noop, toggle: noop, contains: () => false }, appendChild: noop, removeChild: noop, setAttribute: noop, getAttribute: () => null, textContent: '', innerHTML: '', style: {}, dataset: {} };
const doc = {
  readyState: 'complete', createElement: () => ({ ...fakeEl }), head: fakeEl, body: fakeEl,
  getElementById: () => null, querySelector: () => null, querySelectorAll: () => [],
  addEventListener: noop,
};
const win = { addEventListener: noop };
const ctx = {
  window: win, document: doc, console, Date, Math, JSON, Set, Map, Array, Object, Promise, Response: function () {}, Error,
  setTimeout, clearTimeout, setInterval, clearInterval,
  history: { replaceState: noop, pushState: noop },
  location: { hash: '', href: '', pathname: '/' },
  navigator: { userAgent: 'node' },
  // Stub out helpers channels.js references at top-level eval (none, but
  // belt-and-braces against globals referenced inside the IIFE body).
  RegionFilter: { getRegionParam: () => '' },
  api: () => Promise.resolve({ messages: [] }),
  CLIENT_TTL: {},
  ChannelDecrypt: undefined,
  truncate: (s) => s,
  formatHashHex: (h) => String(h),
  channelDisplayName: (c) => c && c.name,
  escapeHtml: (s) => String(s),
  getSenderColor: () => '#000',
  fetch: () => Promise.resolve({ json: () => Promise.resolve({}) }),
};
vm.createContext(ctx);
try {
  vm.runInContext(fs.readFileSync('public/channels.js', 'utf8'), ctx);
} catch (e) {
  // Some downstream code may throw — that's fine as long as the merge
  // helper was exported before the throw.
}

const merge = ctx.window._channelsMergeWsAppendedIntoRestForTest;
if (typeof merge !== 'function') {
  console.error('FATAL: _channelsMergeWsAppendedIntoRestForTest not exported by channels.js');
  process.exit(2);
}

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log('  ✅ ' + name); }
  catch (e) { failed++; console.log('  ❌ ' + name + ': ' + e.message); }
}

console.log('\n=== mergeWsAppendedIntoRest unit tests ===');

test('empty currentMsgs returns a copy of restMsgs', () => {
  const rest = [{ packetHash: 'a' }, { packetHash: 'b' }];
  const out = merge([], rest);
  assert.deepStrictEqual(out, rest);
  assert.notStrictEqual(out, rest, 'must NOT alias the input restMsgs');
});

test('null/undefined currentMsgs returns a copy of restMsgs', () => {
  const rest = [{ packetHash: 'a' }];
  const out = merge(null, rest);
  assert.deepStrictEqual(out, rest);
  assert.notStrictEqual(out, rest);
});

test('non-array restMsgs returns []', () => {
  const a = merge([{ _fromWS: true, packetHash: 'x' }], null);
  const b = merge([], undefined);
  assert.ok(Array.isArray(a) && a.length === 0, 'merge with null rest should return []');
  assert.ok(Array.isArray(b) && b.length === 0, 'merge with undefined rest should return []');
});

test('no overlap: WS survivor appended at end', () => {
  const cur = [{ _fromWS: true, packetHash: 'ws1', _wsAt: Date.now() }];
  const rest = [{ packetHash: 'r1' }, { packetHash: 'r2' }];
  const out = merge(cur, rest);
  assert.strictEqual(out.length, 3);
  assert.strictEqual(out[0].packetHash, 'r1');
  assert.strictEqual(out[1].packetHash, 'r2');
  assert.strictEqual(out[2].packetHash, 'ws1', 'survivor must be at end (newest)');
});

test('full overlap: REST wins, no duplicates', () => {
  const cur = [{ _fromWS: true, packetHash: 'a', _wsAt: Date.now() },
                { _fromWS: true, packetHash: 'b', _wsAt: Date.now() }];
  const rest = [{ packetHash: 'a', src: 'rest' }, { packetHash: 'b', src: 'rest' }];
  const out = merge(cur, rest);
  assert.strictEqual(out.length, 2);
  assert.strictEqual(out[0].src, 'rest');
  assert.strictEqual(out[1].src, 'rest');
});

test('partial overlap: only non-overlapping survivors are preserved', () => {
  const cur = [{ _fromWS: true, packetHash: 'a', _wsAt: Date.now() },
                { _fromWS: true, packetHash: 'b', _wsAt: Date.now() },
                { _fromWS: true, packetHash: 'c', _wsAt: Date.now() }];
  const rest = [{ packetHash: 'b' }, { packetHash: 'd' }];
  const out = merge(cur, rest);
  const hashes = out.map((m) => m.packetHash);
  assert.deepStrictEqual(hashes, ['b', 'd', 'a', 'c'],
    'REST first (preserving its order), then survivors in their original order');
});

test('null packetHash on survivor: preserved (no way to dedup)', () => {
  const cur = [{ _fromWS: true, packetHash: null, _wsAt: Date.now(), tag: 'no-hash' },
                { _fromWS: true, packetHash: undefined, _wsAt: Date.now(), tag: 'undef-hash' }];
  const rest = [{ packetHash: 'r1' }];
  const out = merge(cur, rest);
  assert.strictEqual(out.length, 3);
  assert.strictEqual(out[0].packetHash, 'r1');
  assert.strictEqual(out[1].tag, 'no-hash');
  assert.strictEqual(out[2].tag, 'undef-hash');
});

test('non-_fromWS entries in currentMsgs are NOT carried as survivors', () => {
  const cur = [{ packetHash: 'plain-rest', src: 'old-rest' }];
  const rest = [{ packetHash: 'r-new' }];
  const out = merge(cur, rest);
  assert.strictEqual(out.length, 1);
  assert.strictEqual(out[0].packetHash, 'r-new');
});

test('returns fresh array even with zero survivors (no aliasing)', () => {
  const cur = [];
  const rest = [{ packetHash: 'r1' }];
  const out = merge(cur, rest);
  assert.notStrictEqual(out, rest, 'must not return the same reference as restMsgs');
  // Mutating the result must not mutate restMsgs.
  out.push({ packetHash: 'x' });
  assert.strictEqual(rest.length, 1, 'restMsgs must remain length 1');
});

test('returns fresh array when all survivors are dedup-evicted (no aliasing)', () => {
  const cur = [{ _fromWS: true, packetHash: 'a', _wsAt: Date.now() }];
  const rest = [{ packetHash: 'a' }];
  const out = merge(cur, rest);
  assert.notStrictEqual(out, rest);
  out.push({ packetHash: 'mut' });
  assert.strictEqual(rest.length, 1);
});

test('stale survivor past MAX_WS_SURVIVOR_MS is evicted', () => {
  const stale = Date.now() - (6 * 60 * 1000); // 6 minutes ago
  const fresh = Date.now() - 1000;
  const cur = [
    { _fromWS: true, packetHash: 'stale', _wsAt: stale },
    { _fromWS: true, packetHash: 'fresh', _wsAt: fresh },
  ];
  const rest = [{ packetHash: 'r1' }];
  const out = merge(cur, rest);
  const hashes = out.map((m) => m.packetHash);
  assert.deepStrictEqual(hashes, ['r1', 'fresh'],
    'stale survivor must be evicted, fresh survivor preserved');
});

test('ordering: REST oldest→newest preserved, survivors appended after newest REST', () => {
  const cur = [{ _fromWS: true, packetHash: 'ws', _wsAt: Date.now(), seq: 99 }];
  const rest = [
    { packetHash: 'r1', seq: 1 },
    { packetHash: 'r2', seq: 2 },
    { packetHash: 'r3', seq: 3 },
  ];
  const out = merge(cur, rest);
  assert.deepStrictEqual(out.map((m) => m.seq), [1, 2, 3, 99]);
});

console.log('\n=== ' + passed + ' passed, ' + failed + ' failed ===\n');
process.exit(failed === 0 ? 0 : 1);

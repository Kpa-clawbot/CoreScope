/**
 * Unit tests for the packets channel-picker option merge.
 *
 * Locally-added channels (keys stored in the browser only) are invisible to
 * the server, so /api/channels never lists them. packets.js derives their
 * server-side "enc_<HH>" channel hash client-side and offers them as an
 * extra option group. buildChannelOptions() does that merge; it is pure and
 * exported as window._packetsBuildChannelOptionsForTest.
 *
 * Covers: sorting, server-list passthrough, local append, dedupe by hash
 * value, dedupe by name (server holds the key too), and junk input.
 */
'use strict';
const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

// packets.js expects a browser at call time; the merge helper touches none
// of it. Load in a tolerant sandbox and grab the exported helper — it is
// assigned before any DOM-dependent code runs.
const noop = () => {};
const fakeEl = {
  addEventListener: noop, removeEventListener: noop, appendChild: noop, remove: noop,
  querySelector: () => null, querySelectorAll: () => [], setAttribute: noop,
  getAttribute: () => null, classList: { add: noop, remove: noop, toggle: noop, contains: () => false },
  style: {}, dataset: {}, textContent: '', innerHTML: '',
};
const doc = {
  readyState: 'complete', createElement: () => ({ ...fakeEl }), head: fakeEl, body: fakeEl,
  getElementById: () => null, querySelector: () => null, querySelectorAll: () => [],
  addEventListener: noop, removeEventListener: noop,
};
const win = {
  addEventListener: noop, removeEventListener: noop,
  matchMedia: () => ({ matches: false, addEventListener: noop, removeEventListener: noop }),
  // packets.js fails loud if this module is missing (#1799).
  PayloadLabels: { SHORT_BY_ID: {} },
};
const ctx = {
  window: win, document: doc, console, Date, Math, JSON, Set, Map, Array, Object, Promise, Error,
  setTimeout, clearTimeout, setInterval, clearInterval,
  history: { replaceState: noop, pushState: noop },
  location: { hash: '', href: '', pathname: '/' },
  navigator: { userAgent: 'node' },
  localStorage: { getItem: () => null, setItem: noop, removeItem: noop },
  registerPage: noop,
  api: () => Promise.resolve({}),
  fetch: () => Promise.resolve({ json: () => Promise.resolve({}) }),
};
ctx.self = ctx;
vm.createContext(ctx);
try {
  vm.runInContext(fs.readFileSync('public/packets.js', 'utf8'), ctx);
} catch (e) {
  // Downstream init may throw on the stub DOM — fine, the helper is exported
  // near the top of the module.
}

const build = win._packetsBuildChannelOptionsForTest;
assert.strictEqual(typeof build, 'function', 'buildChannelOptions must be exported for tests');

// buildChannelOptions returns objects created inside the vm realm, whose
// prototypes differ from this realm's — deepStrictEqual would reject them on
// identity alone. Compare structurally.
const plain = (v) => JSON.parse(JSON.stringify(v));

let passed = 0;
function test(name, fn) {
  fn();
  passed++;
  console.log('  ok - ' + name);
}

console.log('packets channel-picker option merge');

test('empty inputs produce empty groups', () => {
  const r = build([], []);
  assert.deepStrictEqual(plain(r), { server: [], local: [] });
  const r2 = build(null, undefined);
  assert.deepStrictEqual(plain(r2), { server: [], local: [] });
});

test('server channels are sorted case-insensitively by label', () => {
  const r = build([{ hash: 'zeta', name: 'zeta' }, { hash: 'Alpha', name: 'Alpha' }, { hash: 'mid', name: 'mid' }], []);
  assert.deepStrictEqual(plain(r.server.map(o => o.value)), ['Alpha', 'mid', 'zeta']);
  assert.strictEqual(r.local.length, 0);
});

test('server channel without a name falls back to its hash', () => {
  const r = build([{ hash: 'enc_A5' }], []);
  assert.deepStrictEqual(plain(r.server), [{ value: 'enc_A5', label: 'enc_A5' }]);
});

test('local channels are appended as their own group', () => {
  const r = build(
    [{ hash: 'public', name: 'public' }],
    [{ value: 'enc_A5', name: '#mytown', label: '#mytown' }]
  );
  assert.deepStrictEqual(plain(r.server), [{ value: 'public', label: 'public' }]);
  assert.deepStrictEqual(plain(r.local), [{ value: 'enc_A5', label: '#mytown' }]);
});

test('local channel is dropped when the server already lists that hash', () => {
  const r = build(
    [{ hash: 'enc_A5', name: 'enc_A5' }],
    [{ value: 'enc_A5', name: '#mytown', label: '#mytown' }]
  );
  assert.strictEqual(r.local.length, 0);
});

test('local channel is dropped when the server decrypts it under the same name', () => {
  const r = build(
    [{ hash: '#MyTown', name: '#MyTown' }],
    [{ value: 'enc_A5', name: '#mytown', label: '#mytown' }]
  );
  assert.strictEqual(r.local.length, 0, 'name match is case-insensitive');
});

test('duplicate local entries collapse to one option', () => {
  const r = build([], [
    { value: 'enc_A5', name: '#a', label: '#a' },
    { value: 'enc_A5', name: '#b', label: '#b' },
  ]);
  assert.deepStrictEqual(plain(r.local), [{ value: 'enc_A5', label: '#a' }]);
});

test('local options are sorted and use their display label', () => {
  const r = build([], [
    { value: 'enc_02', name: 'psk:deadbeef', label: 'Work PSK' },
    { value: 'enc_01', name: '#alpha', label: '#alpha' },
  ]);
  assert.deepStrictEqual(plain(r.local), [
    { value: 'enc_01', label: '#alpha' },
    { value: 'enc_02', label: 'Work PSK' },
  ]);
});

test('valueless / junk entries are skipped', () => {
  const r = build([{ name: '' }, null, {}], [null, { name: '#x' }, { value: '' }]);
  assert.deepStrictEqual(plain(r), { server: [], local: [] });
});

// --- collectLocalChannels(): stored keys → "enc_<HH>" -------------------
// Stubs ChannelDecrypt with the real crypto (node) so the derived value is
// checked against a genuine SHA-256(key)[0], not a fixture.

const crypto = require('crypto');
const collect = win._packetsCollectLocalChannelsForTest;
assert.strictEqual(typeof collect, 'function', 'collectLocalChannels must be exported for tests');

function fakeChannelDecrypt(keys, labels) {
  return {
    getStoredKeys: () => keys,
    getLabel: (name) => (labels && labels[name]) || '',
    hexToBytes: (hex) => Uint8Array.from(Buffer.from(hex, 'hex')),
    computeChannelHash: async (keyBytes) => crypto.createHash('sha256').update(keyBytes).digest()[0],
  };
}

function deriveKeyHex(channelName) {
  return crypto.createHash('sha256').update(channelName).digest().subarray(0, 16).toString('hex');
}

function expectedEncValue(channelName) {
  const key = crypto.createHash('sha256').update(channelName).digest().subarray(0, 16);
  const hashByte = crypto.createHash('sha256').update(key).digest()[0];
  return 'enc_' + hashByte.toString(16).padStart(2, '0').toUpperCase();
}

async function asyncTest(name, fn) {
  await fn();
  passed++;
  console.log('  ok - ' + name);
}

(async () => {
  console.log('\ncollectLocalChannels');

  await asyncTest('no ChannelDecrypt module → no local channels', async () => {
    win.ChannelDecrypt = undefined;
    assert.deepStrictEqual(plain(await collect()), []);
  });

  await asyncTest('derives the server-side enc_<HH> value from the stored key', async () => {
    // '#alpha' and '#zulu' hash to different bytes; both must round-trip.
    const keys = { '#alpha': deriveKeyHex('#alpha'), '#zulu': deriveKeyHex('#zulu') };
    win.ChannelDecrypt = fakeChannelDecrypt(keys);
    const out = plain(await collect());
    assert.strictEqual(out.length, 2);
    assert.deepStrictEqual(out[0], { value: expectedEncValue('#alpha'), name: '#alpha', label: '#alpha' });
    assert.deepStrictEqual(out[1], { value: expectedEncValue('#zulu'), name: '#zulu', label: '#zulu' });
    // Uppercase, zero-padded — matches cmd/ingestor/decoder.go's "%02X".
    for (const o of out) assert.ok(/^enc_[0-9A-F]{2}$/.test(o.value), 'bad value ' + o.value);
  });

  await asyncTest('uses the stored display label when present', async () => {
    win.ChannelDecrypt = fakeChannelDecrypt(
      { 'psk:deadbeef': deriveKeyHex('#work') },
      { 'psk:deadbeef': 'Work PSK' }
    );
    const out = plain(await collect());
    assert.deepStrictEqual(out, [{ value: expectedEncValue('#work'), name: 'psk:deadbeef', label: 'Work PSK' }]);
  });

  await asyncTest('skips malformed keys instead of throwing', async () => {
    win.ChannelDecrypt = fakeChannelDecrypt({
      '#short': 'aabb',                    // not 16 bytes
      '#empty': '',
      '#null': null,
      '#good': deriveKeyHex('#good'),
    });
    const out = plain(await collect());
    assert.deepStrictEqual(out, [{ value: expectedEncValue('#good'), name: '#good', label: '#good' }]);
  });

  await asyncTest('a throwing getStoredKeys degrades to no local channels', async () => {
    win.ChannelDecrypt = { getStoredKeys: () => { throw new Error('quota'); } };
    assert.deepStrictEqual(plain(await collect()), []);
  });

  console.log(`\n${passed} tests passed`);
})().catch((e) => { console.error(e); process.exit(1); });

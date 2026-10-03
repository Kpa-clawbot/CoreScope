/**
 * #2095 — loadChannels() replaced the channel array and discarded every
 * client-only field on it.
 *
 * Two consequences, one root cause. The array assignment at loadChannels()
 * carries nothing across, and mergeUserChannels() only ran from init(), so a
 * region-filter change or the show-encrypted toggle destroyed the My Channels
 * section, every unread badge, and the user's own labels — and evicted the
 * selected channel if it was a PSK row, closing the open conversation.
 *
 * Part 1 tests the pure helper directly. Part 2 drives loadChannels() through
 * its test hook with a stubbed api(), which is what proves the helper is
 * actually wired in rather than merely present.
 *
 * Sandbox pattern copied from test-channels-merge-1498-unit.js.
 */
'use strict';
const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

const noop = () => {};
const fakeEl = {
  addEventListener: noop, removeEventListener: noop, querySelector: () => fakeEl,
  querySelectorAll: () => [], classList: { add: noop, remove: noop, toggle: noop, contains: () => false },
  appendChild: noop, removeChild: noop, setAttribute: noop, getAttribute: () => null,
  textContent: '', innerHTML: '', style: {}, dataset: {}, scrollTop: 0, scrollHeight: 0,
};
const doc = {
  readyState: 'complete', createElement: () => ({ ...fakeEl }), head: fakeEl, body: fakeEl,
  getElementById: () => null, querySelector: () => null, querySelectorAll: () => [],
  addEventListener: noop, removeEventListener: noop,
};

// Stored PSK keys the user added. mergeUserChannels() reads these, so they are
// what makes the My Channels section exist at all.
let storedKeys = {};
let storedLabels = {};

const ctx = {
  window: { addEventListener: noop, removeEventListener: noop },
  document: doc, console, Date, Math, JSON, Set, Map, Array, Object, Promise,
  Response: function () {}, Error, Number, String, Boolean, isNaN, parseInt, parseFloat,
  setTimeout, clearTimeout, setInterval, clearInterval,
  history: { replaceState: noop, pushState: noop },
  location: { hash: '', href: '', pathname: '/' },
  navigator: { userAgent: 'node' },
  localStorage: { getItem: () => null, setItem: noop, removeItem: noop },
  RegionFilter: { getRegionParam: () => '', onChange: () => noop },
  CLIENT_TTL: { channels: 15000 },
  ChannelDecrypt: {
    getStoredKeys: () => storedKeys,
    getLabels: () => storedLabels,
  },
  truncate: (s) => s,
  formatHashHex: (h) => String(h),
  channelDisplayName: (c) => (c && (c.userLabel || c.name)) || '',
  escapeHtml: (s) => String(s),
  getSenderColor: () => '#000',
  registerPage: noop,
  fetch: () => Promise.resolve({ json: () => Promise.resolve({}) }),
};

// The server snapshot loadChannels() will receive. Reassigned per test.
let apiChannels = [];
ctx.api = () => Promise.resolve({ channels: apiChannels.map(c => ({ ...c })) });

vm.createContext(ctx);
try {
  vm.runInContext(fs.readFileSync('public/channels.js', 'utf8'), ctx);
} catch (e) {
  // The IIFE may throw on missing DOM further down; the hooks we need are
  // exported before that point.
}

const merge = ctx.window._channelsMergeClientChannelStateForTest;
const loadChannels = ctx.window._channelsLoadChannelsForTest;
const setState = ctx.window._channelsSetStateForTest;
const getState = ctx.window._channelsGetStateForTest;

for (const [name, fn] of Object.entries({ merge, loadChannels, setState, getState })) {
  if (typeof fn !== 'function') {
    console.error(`FATAL: ${name} not exported by channels.js`);
    process.exit(2);
  }
}

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); console.log(`  PASS  ${name}`); passed++; }
  catch (e) { console.log(`  FAIL  ${name}\n        ${e.message}`); failed++; }
}
async function atest(name, fn) {
  try { await fn(); console.log(`  PASS  ${name}`); passed++; }
  catch (e) { console.log(`  FAIL  ${name}\n        ${e.message}`); failed++; }
}

console.log('\n=== #2095 part 1: mergeClientChannelState() ===');

test('carries unread across the replacement', () => {
  const prev = [{ hash: 'a', unread: 3 }, { hash: 'b', unread: 0 }];
  const fresh = [{ hash: 'a' }, { hash: 'b' }];
  const out = merge(fresh, prev);
  assert.strictEqual(out[0].unread, 3, 'unread on a was dropped');
  assert.strictEqual(out[1].unread, 0);
});

test('keeps WS-fresher activity fields when the snapshot is older', () => {
  // The WS handler already applied a newer message before the REST snapshot
  // landed. Reverting to the snapshot is the flicker in #2095 finding 2.
  const prev = [{ hash: 'a', lastActivityMs: 2000, lastSender: 'ON8AR', lastMessage: 'new', messageCount: 8 }];
  const fresh = [{ hash: 'a', lastActivityMs: 1000, lastSender: 'OLD', lastMessage: 'old', messageCount: 7 }];
  const out = merge(fresh, prev);
  assert.strictEqual(out[0].lastActivityMs, 2000);
  assert.strictEqual(out[0].lastSender, 'ON8AR');
  assert.strictEqual(out[0].lastMessage, 'new');
  assert.strictEqual(out[0].messageCount, 8);
});

test('the server wins when its snapshot is the newer one', () => {
  const prev = [{ hash: 'a', lastActivityMs: 1000, lastSender: 'STALE', lastMessage: 'stale', messageCount: 2 }];
  const fresh = [{ hash: 'a', lastActivityMs: 5000, lastSender: 'FRESH', lastMessage: 'fresh', messageCount: 9 }];
  const out = merge(fresh, prev);
  assert.strictEqual(out[0].lastActivityMs, 5000);
  assert.strictEqual(out[0].lastSender, 'FRESH');
  assert.strictEqual(out[0].messageCount, 9);
});

test('does not resurrect a channel the snapshot left out', () => {
  // A region-filter change legitimately narrows the list. Carrying survivors
  // over would defeat the filter, which is why this helper only enriches rows
  // that are already in the fresh snapshot.
  const prev = [{ hash: 'a', unread: 1 }, { hash: 'gone', unread: 9 }];
  const fresh = [{ hash: 'a' }];
  const out = merge(fresh, prev);
  assert.strictEqual(out.length, 1, 'a filtered-out channel came back');
  assert.strictEqual(out[0].hash, 'a');
});

test('never aliases or mutates its inputs', () => {
  const prev = [{ hash: 'a', unread: 4 }];
  const fresh = [{ hash: 'a' }];
  const out = merge(fresh, prev);
  assert.notStrictEqual(out, fresh, 'returned the input array itself');
  out[0].unread = 99;
  assert.strictEqual(prev[0].unread, 4, 'mutating the result reached prev');
});

test('tolerates empty and missing inputs', () => {
  // Not deepStrictEqual: an array built inside the vm realm has a different
  // Array.prototype, which that assertion compares and rejects.
  assert.strictEqual(merge([], []).length, 0);
  assert.strictEqual(merge(null, null).length, 0);
  assert.strictEqual(merge([{ hash: 'a' }], null).length, 1);
  assert.strictEqual(merge([{ hash: 'a' }], undefined).length, 1);
});

test('ignores rows with no hash rather than matching them together', () => {
  const prev = [{ unread: 5 }, { hash: '', unread: 6 }];
  const fresh = [{ hash: 'a' }, { hash: '' }];
  const out = merge(fresh, prev);
  assert.ok(!out[0].unread, 'a hashless prev row leaked onto a real channel');
});

console.log('\n=== #2095 part 2: loadChannels() keeps client state ===');

(async function () {
  await atest('a refresh keeps the My Channels rows', async () => {
    // The bug: the user has a PSK channel, the server does not know it, and any
    // refresh replaced the array with the server snapshot, so the row vanished.
    storedKeys = { 'MyPSK': 'deadbeef' };
    storedLabels = { 'MyPSK': 'Mijn kanaal' };
    apiChannels = [{ hash: 'public1', name: 'public1', lastActivity: null }];
    setState({ channels: [], messages: [], selectedHash: null });

    await loadChannels(true);

    const after = getState().channels;
    const mine = after.filter(c => c.userAdded === true);
    assert.strictEqual(mine.length, 1, `My Channels lost on refresh (got ${JSON.stringify(after.map(c => c.hash))})`);
    assert.strictEqual(mine[0].hash, 'user:MyPSK');
    assert.strictEqual(mine[0].userLabel, 'Mijn kanaal', 'the user label was dropped');
  });

  await atest('a refresh keeps unread badges', async () => {
    storedKeys = {};
    storedLabels = {};
    apiChannels = [{ hash: 'public1', name: 'public1', lastActivity: null }];
    setState({ channels: [{ hash: 'public1', name: 'public1', unread: 7 }], messages: [], selectedHash: null });

    await loadChannels(true);

    const ch = getState().channels.find(c => c.hash === 'public1');
    assert.ok(ch, 'the channel disappeared');
    assert.strictEqual(ch.unread, 7, 'the unread badge reset to 0 on refresh');
  });

  await atest('a refresh does not close an open PSK conversation', async () => {
    // The worst of the two: reconcileSelectionAfterChannelRefresh() did not
    // find the user:* hash in the server snapshot, so it nulled the selection,
    // emptied messages and rewrote the URL.
    storedKeys = { 'MyPSK': 'deadbeef' };
    storedLabels = {};
    apiChannels = [{ hash: 'public1', name: 'public1', lastActivity: null }];
    setState({
      channels: [{ hash: 'user:MyPSK', name: 'MyPSK', userAdded: true }],
      messages: [{ text: 'hello' }],
      selectedHash: 'user:MyPSK',
    });

    await loadChannels(true);

    const st = getState();
    assert.strictEqual(st.selectedHash, 'user:MyPSK', 'the open PSK channel was deselected');
    assert.strictEqual(st.messages.length, 1, 'the open conversation was emptied');
  });

  await atest('a refresh keeps unread and activity on a PSK-only row', async () => {
    // A user:* row is never in the server snapshot, so mergeClientChannelState
    // has nothing to enrich; mergeUserChannels() rebuilds it from storage and
    // must carry what the tab counted, or a live PSK message's badge and
    // preview reset on every region change.
    storedKeys = { 'MyPSK': 'deadbeef' };
    storedLabels = {};
    apiChannels = [{ hash: 'public1', name: 'public1', lastActivity: null }];
    setState({
      channels: [{
        hash: 'user:MyPSK', name: 'MyPSK', userAdded: true, encrypted: true, unread: 4,
        lastActivityMs: 9000, lastSender: 'ON8AR', lastMessage: 'decrypted text', messageCount: 3,
      }],
      messages: [], selectedHash: null,
    });

    await loadChannels(true);

    const ch = getState().channels.find(c => c.hash === 'user:MyPSK');
    assert.ok(ch, 'the PSK row disappeared');
    assert.strictEqual(ch.unread, 4, 'the PSK unread badge reset on refresh');
    assert.strictEqual(ch.lastActivityMs, 9000);
    assert.strictEqual(ch.lastSender, 'ON8AR');
    assert.strictEqual(ch.lastMessage, 'decrypted text', 'the PSK preview went back to the placeholder');
    assert.strictEqual(ch.messageCount, 3);
  });

  await atest('a refresh does not keep a PSK row whose key was removed', async () => {
    // Storage stays the source of truth for which PSK rows exist.
    storedKeys = {};
    storedLabels = {};
    apiChannels = [{ hash: 'public1', name: 'public1', lastActivity: null }];
    setState({
      channels: [{ hash: 'user:Gone', name: 'Gone', userAdded: true, unread: 2 }],
      messages: [], selectedHash: null,
    });

    await loadChannels(true);

    const hashes = getState().channels.map(c => c.hash);
    assert.ok(!hashes.includes('user:Gone'), `a removed key came back: ${JSON.stringify(hashes)}`);
  });

  await atest('a refresh still drops a channel the server filtered out', async () => {
    // The fix must not defeat the region filter.
    storedKeys = {};
    storedLabels = {};
    apiChannels = [{ hash: 'in-region', name: 'in-region', lastActivity: null }];
    setState({
      channels: [{ hash: 'in-region', name: 'in-region' }, { hash: 'out-of-region', name: 'out-of-region' }],
      messages: [], selectedHash: null,
    });

    await loadChannels(true);

    const hashes = getState().channels.map(c => c.hash);
    assert.ok(!hashes.includes('out-of-region'), `the filter was defeated: ${JSON.stringify(hashes)}`);
  });

  console.log(`\n${passed} passed, ${failed} failed`);
  process.exit(failed ? 1 : 0);
})();

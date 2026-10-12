/* Channels page and approved hashtag channels
 * (docs/specs/2026-10-07-channel-proposals-design.md): loadChannels() merges
 * approvedChannels through CSProposals.mergeApproved, rows carry an
 * "approved" marker, and channel names are escaped in every attribute and
 * CSS selector (approved names may contain " < > \). Sandbox pattern of
 * test-issue-2095-channels-client-state.js, with the real escapeHtml. */
'use strict';
const vm = require('vm');
const fs = require('fs');
const path = require('path');
const assert = require('assert');

const ROOT = path.resolve(__dirname, '..', '..');
const src = (f) => fs.readFileSync(path.join(ROOT, f), 'utf8');
const plain = (v) => JSON.parse(JSON.stringify(v));

function loadEscapeHtml() {
  const m = src('public/app.js').match(/function escapeHtml\(s\) \{[\s\S]*?\n\}/);
  assert(m, 'escapeHtml not found in app.js');
  return vm.runInNewContext('(' + m[0].replace(/^function escapeHtml/, 'function') + ')');
}

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
let apiResponse = { channels: [] };
const ctx = {
  window: { addEventListener: noop, removeEventListener: noop },
  document: doc, console, Response: function () {},
  setTimeout, clearTimeout, setInterval, clearInterval,
  history: { replaceState: noop, pushState: noop },
  location: { hash: '', href: '', pathname: '/' },
  navigator: { userAgent: 'node' },
  localStorage: { getItem: () => null, setItem: noop, removeItem: noop },
  RegionFilter: { getRegionParam: () => '', onChange: () => noop },
  CLIENT_TTL: { channels: 15000 },
  ChannelDecrypt: { getStoredKeys: () => ({}), getLabels: () => ({}) },
  escapeHtml: loadEscapeHtml(),
  getSenderColor: () => 'var(--text)',
  registerPage: noop,
  fetch: () => Promise.resolve({ json: () => Promise.resolve({}) }),
  api: () => Promise.resolve(JSON.parse(JSON.stringify(apiResponse))),
};
vm.createContext(ctx);
vm.runInContext(src('public/channel-proposals.js'), ctx);
try {
  vm.runInContext(src('public/channels.js'), ctx);
} catch (e) {
  // The IIFE may throw on missing DOM further down; the hooks are exported before.
}
const W = ctx.window;
for (const n of ['_channelsLoadChannelsForTest', '_channelsGetStateForTest', '_channelsRenderChannelRowForTest',
  '_channelsRenderChannelRowMobileForTest', '_channelsAttrSelForTest']) {
  if (typeof W[n] !== 'function') { console.error('FATAL: ' + n + ' not exported by channels.js'); process.exit(2); }
}

let passed = 0, failed = 0;
async function test(name, fn) {
  try { await fn(); passed++; console.log('  ok   ' + name); }
  catch (e) { failed++; console.log('  FAIL ' + name + ': ' + e.message); }
}

(async () => {
  console.log('channels.js: approved channels');

  await test('approved names mark rows and add rows without traffic', async () => {
    apiResponse = { channels: [{ hash: '#mesh', name: '#mesh', messageCount: 3, lastActivity: '2026-10-07T10:00:00Z' }], approvedChannels: ['#mesh', '#quiet'] };
    await W._channelsLoadChannelsForTest(true);
    const rows = plain(W._channelsGetStateForTest().channels);
    const mesh = rows.find((c) => c.hash === '#mesh');
    const quiet = rows.find((c) => c.hash === '#quiet');
    assert.strictEqual(rows.length, 2);
    assert.strictEqual(mesh.approved, true);
    assert.strictEqual(mesh.messageCount, 3);
    assert(quiet && quiet.approved === true && quiet.messageCount === 0, JSON.stringify(quiet));
  });

  await test('no approvedChannels (feature off): rows unchanged', async () => {
    apiResponse = { channels: [{ hash: '#mesh', name: '#mesh', messageCount: 3 }] };
    await W._channelsLoadChannelsForTest(true);
    const rows = plain(W._channelsGetStateForTest().channels);
    assert.strictEqual(rows.length, 1);
    assert.strictEqual(rows[0].approved, undefined);
  });

  await test('a revoked channel without traffic is gone after the next load', async () => {
    apiResponse = { channels: [], approvedChannels: ['#quiet'] };
    await W._channelsLoadChannelsForTest(true);
    assert.strictEqual(W._channelsGetStateForTest().channels.length, 1);
    apiResponse = { channels: [], approvedChannels: [] };
    await W._channelsLoadChannelsForTest(true);
    assert.strictEqual(W._channelsGetStateForTest().channels.length, 0);
  });

  await test('desktop and mobile rows escape the name in every attribute and show the marker', async () => {
    const evil = '#a"><img src=x onerror=alert(1)>';
    const ch = { hash: evil, name: evil, approved: true, messageCount: 0, lastActivityMs: 0 };
    for (const html of [W._channelsRenderChannelRowForTest(ch), W._channelsRenderChannelRowMobileForTest(ch)]) {
      assert(html.indexOf('<img') === -1, html);
      assert(html.indexOf('data-hash="#a&quot;&gt;&lt;img src=x onerror=alert(1)&gt;"') !== -1, html);
      assert(html.indexOf('class="ch-approved-badge"') !== -1, html);
    }
    assert.strictEqual(W._channelsRenderChannelRowForTest({ hash: '#x', name: '#x' }).indexOf('ch-approved-badge'), -1);
  });

  await test('attrSel escapes quotes and backslashes for the timestamp selectors', async () => {
    assert.strictEqual(W._channelsAttrSelForTest('#a"b\\c'), '#a\\"b\\\\c');
    assert.strictEqual(W._channelsAttrSelForTest('#plain'), '#plain');
  });

  console.log('\n' + passed + ' passed, ' + failed + ' failed');
  process.exit(failed > 0 ? 1 : 0);
})();

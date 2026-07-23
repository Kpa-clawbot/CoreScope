/**
 * Unit tests for the CoreScope-only "ping" bot reply bubble
 * (public/channels.js renderMessages' botReplyHtml block).
 *
 * The backend (cmd/server/db.go pingBotReply) attaches a synthetic
 * `botReply` field to a channel message whose text is exactly "ping" --
 * this file covers that the frontend renders it distinctly, includes the
 * "not sent to the mesh" caveat, and escapes attacker-controlled fields
 * (observer names flow into botReply.text server-side, so it must not be
 * trusted blindly).
 *
 * Sandbox pattern borrowed from test-channels-merge-1498-unit.js: load
 * channels.js in a tolerant vm context, grab the test-only export.
 */
'use strict';
const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

function escapeHtml(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}

function makeSandbox() {
  const noop = () => {};
  const fakeEl = () => ({
    addEventListener: noop, querySelector: () => null, querySelectorAll: () => [],
    classList: { add: noop, remove: noop, toggle: noop, contains: () => false },
    appendChild: noop, removeChild: noop, setAttribute: noop, getAttribute: () => null,
    textContent: '', innerHTML: '', style: {}, dataset: {}, scrollTop: 0, scrollHeight: 0,
  });
  const chMessagesEl = fakeEl();
  const doc = {
    readyState: 'complete', createElement: fakeEl, head: fakeEl(), body: fakeEl(),
    documentElement: fakeEl(),
    getElementById: (id) => (id === 'chMessages' ? chMessagesEl : null),
    querySelector: () => null, querySelectorAll: () => [],
    addEventListener: noop,
  };
  const win = { addEventListener: noop, matchMedia: () => ({ matches: false, addListener: noop, addEventListener: noop }) };
  const ctx = {
    window: win, document: doc, console, Date, Math, JSON, Set, Map, Array, Object, Promise, Response: function () {}, Error,
    setTimeout, clearTimeout, setInterval, clearInterval,
    history: { replaceState: noop, pushState: noop },
    location: { hash: '', href: '', pathname: '/' },
    navigator: { userAgent: 'node' },
    RegionFilter: { getRegionParam: () => '' },
    api: () => Promise.resolve({ messages: [] }),
    CLIENT_TTL: {},
    ChannelDecrypt: undefined,
    truncate: (s) => s,
    formatHashHex: (h) => String(h),
    channelDisplayName: (c) => c && c.name,
    escapeHtml,
    getSenderColor: () => '#123456',
    fetch: () => Promise.resolve({ json: () => Promise.resolve({}) }),
    btoa: (s) => Buffer.from(String(s), 'binary').toString('base64'),
  };
  vm.createContext(ctx);
  try {
    vm.runInContext(fs.readFileSync('public/channels.js', 'utf8'), ctx);
  } catch (e) {
    // Tolerant: only the render path under test needs to have been
    // exported before any unrelated init code throws.
  }
  return { ctx, chMessagesEl };
}

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log('  ✅ ' + name); }
  catch (e) { failed++; console.log('  ❌ ' + name + ': ' + e.message); }
}

console.log('\n=== channels.js: pingBotReply (shared trigger/format logic) ===');
// This is the client-side twin of pingBotReply in cmd/server/db.go, used
// by the WebSocket live-push path and the client-side PSK-channel decrypt
// path (neither of which round-trips through GetChannelMessages, so
// neither gets the server-computed botReply without this).

test('exact "ping" (any case) triggers a reply', () => {
  const { ctx } = makeSandbox();
  const fn = ctx.window._channelsPingBotReplyForTest;
  assert.ok(fn('ping', 1, 5, 'Obs') !== null);
  assert.ok(fn('PING', 1, 5, 'Obs') !== null);
  assert.ok(fn('  ping  ', 1, 5, 'Obs') !== null, 'surrounding whitespace should be trimmed');
});

test('"/ping" (the slash-command form) also triggers, alongside bare "ping"', () => {
  const { ctx } = makeSandbox();
  const fn = ctx.window._channelsPingBotReplyForTest;
  assert.ok(fn('/ping', 1, 5, 'Obs') !== null);
  assert.ok(fn('/PING', 1, 5, 'Obs') !== null, 'case-insensitive like the bare form');
  assert.strictEqual(fn('/pingx', 1, 5, 'Obs'), null, 'still an exact match, not a prefix match');
});

test('a mention prefix like "@CoreScopeBot ping" is stripped before matching', () => {
  const { ctx } = makeSandbox();
  const fn = ctx.window._channelsPingBotReplyForTest;
  assert.ok(fn('@CoreScopeBot ping', 0, null, null) !== null);
});

test('"pinging" or other substrings do not match (exact trigger only)', () => {
  const { ctx } = makeSandbox();
  const fn = ctx.window._channelsPingBotReplyForTest;
  assert.strictEqual(fn('pinging around', 1, 5, 'Obs'), null);
  assert.strictEqual(fn('not ping', 1, 5, 'Obs'), null);
  assert.strictEqual(fn('', 1, 5, 'Obs'), null);
});

test('reply text includes hops, SNR, and observer when present', () => {
  const { ctx } = makeSandbox();
  const fn = ctx.window._channelsPingBotReplyForTest;
  const r = fn('ping', 3, 8.25, 'Observer One');
  assert.strictEqual(r.sender, 'CoreScopeBot');
  assert.ok(r.text.includes('3 hops'), r.text);
  assert.ok(r.text.includes('SNR 8.3dB') || r.text.includes('SNR 8.2dB'), r.text);
  assert.ok(r.text.includes('heard by Observer One'), r.text);
});

test('hops=0 reports "0 hops (direct)"; missing SNR/observer are omitted cleanly', () => {
  const { ctx } = makeSandbox();
  const fn = ctx.window._channelsPingBotReplyForTest;
  const r = fn('ping', 0, null, null);
  assert.ok(r.text.includes('0 hops (direct)'), r.text);
  assert.ok(!r.text.includes('SNR'), r.text);
  assert.ok(!r.text.includes('heard by'), r.text);
});

test('scope and area are included when present, omitted when not', () => {
  const { ctx } = makeSandbox();
  const fn = ctx.window._channelsPingBotReplyForTest;
  const withBoth = fn('ping', 1, 5, 'Obs', '#dk', 'Aarhus');
  assert.ok(withBoth.text.includes('scope #dk'), withBoth.text);
  assert.ok(withBoth.text.includes('area Aarhus'), withBoth.text);

  const withNeither = fn('ping', 1, 5, 'Obs', null, null);
  assert.ok(!withNeither.text.includes('scope'), withNeither.text);
  assert.ok(!withNeither.text.includes('area'), withNeither.text);
});

console.log('\n=== channels.js: ping-bot reply rendering ===');

test('a message without botReply renders no bot bubble', () => {
  const { ctx, chMessagesEl } = makeSandbox();
  ctx.window._channelsSetStateForTest({ messages: [
    { sender: 'Alice', text: 'just chatting', timestamp: '2026-01-15T10:00:00Z' },
  ] });
  ctx.window._channelsRenderMessagesForTest();
  assert.ok(!chMessagesEl.innerHTML.includes('ch-bot-message'), 'no botReply field should mean no bot bubble');
});

test('a message with botReply renders a distinct bot bubble with the reply text', () => {
  const { ctx, chMessagesEl } = makeSandbox();
  ctx.window._channelsSetStateForTest({ messages: [
    {
      sender: 'Bob', text: 'ping', timestamp: '2026-01-15T10:01:00Z',
      botReply: { sender: 'CoreScopeBot', text: '🏓 pong! 2 hops · SNR 8.2dB · heard by Observer One', hops: 2, snr: 8.2 },
    },
  ] });
  ctx.window._channelsRenderMessagesForTest();
  const html = chMessagesEl.innerHTML;
  assert.ok(html.includes('ch-bot-message'), 'should render the distinct bot-message class');
  assert.ok(html.includes('CoreScopeBot'), 'should show the bot sender name');
  assert.ok(html.includes('2 hops'), 'should include the hop count from the reply text');
  assert.ok(html.includes('SNR 8.2dB'), 'should include the SNR from the reply text');
});

test('"View path" link appears when hops > 0 and a packetHash is available', () => {
  const { ctx, chMessagesEl } = makeSandbox();
  ctx.window._channelsSetStateForTest({ messages: [
    {
      sender: 'Bob', text: 'ping', timestamp: '2026-01-15T10:01:00Z', packetHash: 'abc123',
      botReply: { sender: 'CoreScopeBot', text: '🏓 pong! 2 hops', hops: 2, snr: null },
    },
  ] });
  ctx.window._channelsRenderMessagesForTest();
  const html = chMessagesEl.innerHTML;
  assert.ok(html.includes('View path'), 'should show the View path link');
  assert.ok(html.includes('data-view-path="abc123"'), 'should carry the packet hash for the click handler to look up');
});

test('"View path" link is absent for a direct (0-hop) reply -- nothing to draw', () => {
  const { ctx, chMessagesEl } = makeSandbox();
  ctx.window._channelsSetStateForTest({ messages: [
    {
      sender: 'Bob', text: 'ping', timestamp: '2026-01-15T10:01:00Z', packetHash: 'abc123',
      botReply: { sender: 'CoreScopeBot', text: '🏓 pong! 0 hops (direct)', hops: 0, snr: null },
    },
  ] });
  ctx.window._channelsRenderMessagesForTest();
  assert.ok(!chMessagesEl.innerHTML.includes('View path'), 'a direct reply has no relay path to visualize');
});

test('"View path" link is absent when there is no packetHash to look it up by', () => {
  const { ctx, chMessagesEl } = makeSandbox();
  ctx.window._channelsSetStateForTest({ messages: [
    {
      sender: 'Bob', text: 'ping', timestamp: '2026-01-15T10:01:00Z',
      botReply: { sender: 'CoreScopeBot', text: '🏓 pong! 2 hops', hops: 2, snr: null },
    },
  ] });
  ctx.window._channelsRenderMessagesForTest();
  assert.ok(!chMessagesEl.innerHTML.includes('View path'), 'without a packetHash there is nothing to fetch the path for');
});

test('the "not sent to the mesh" caveat is always present on a bot bubble', () => {
  const { ctx, chMessagesEl } = makeSandbox();
  ctx.window._channelsSetStateForTest({ messages: [
    { sender: 'Bob', text: 'ping', timestamp: '2026-01-15T10:01:00Z', botReply: { sender: 'CoreScopeBot', text: 'pong', hops: 0 } },
  ] });
  ctx.window._channelsRenderMessagesForTest();
  assert.ok(chMessagesEl.innerHTML.includes('Not sent to the mesh'), 'the caveat must be visible so this is never mistaken for a real mesh reply');
});

test('botReply.text and .sender are HTML-escaped (observer names are operator-controlled)', () => {
  const { ctx, chMessagesEl } = makeSandbox();
  ctx.window._channelsSetStateForTest({ messages: [
    {
      sender: 'Bob', text: 'ping', timestamp: '2026-01-15T10:01:00Z',
      botReply: { sender: '<img src=x onerror=alert(1)>', text: 'heard by <script>alert(2)</script>', hops: 0 },
    },
  ] });
  ctx.window._channelsRenderMessagesForTest();
  const html = chMessagesEl.innerHTML;
  assert.ok(!html.includes('<img src=x'), 'botReply.sender must be escaped');
  assert.ok(!html.includes('<script>alert(2)'), 'botReply.text must be escaped');
});

test('the bot bubble renders immediately after its triggering message, not before other messages', () => {
  const { ctx, chMessagesEl } = makeSandbox();
  ctx.window._channelsSetStateForTest({ messages: [
    { sender: 'Bob', text: 'ping', timestamp: '2026-01-15T10:01:00Z', botReply: { sender: 'CoreScopeBot', text: 'pong', hops: 0 } },
    { sender: 'Carol', text: 'after', timestamp: '2026-01-15T10:02:00Z' },
  ] });
  ctx.window._channelsRenderMessagesForTest();
  const html = chMessagesEl.innerHTML;
  const pingIdx = html.indexOf('>ping<');
  const botIdx = html.indexOf('ch-bot-message');
  const afterIdx = html.indexOf('>after<');
  assert.ok(pingIdx > -1 && botIdx > -1 && afterIdx > -1, 'all three pieces should be present');
  assert.ok(pingIdx < botIdx && botIdx < afterIdx, 'order should be: ping message, bot reply, next message');
});

console.log('\n════════════════════════════════════════');
console.log(`  Channels ping-bot reply: ${passed} passed, ${failed} failed`);
console.log('════════════════════════════════════════');
if (failed > 0) process.exit(1);

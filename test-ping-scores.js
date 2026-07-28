/**
 * Tests for public/ping-scores.js — the global "Ping Scores" highscore
 * board + leaderboards page (backed by GET /api/ping-scores,
 * cmd/server/ping_scores.go).
 *
 * Same two-layer pattern as test-packet-path-map.js: string-contract
 * checks over the raw source, plus a functional smoke test using a
 * minimal-but-real DOM mock to exercise the page's init() end-to-end.
 */
'use strict';

const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

const src = fs.readFileSync('public/ping-scores.js', 'utf8');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log('  ✅ ' + name); }
  catch (e) { failed++; console.log('  ❌ ' + name + ': ' + e.message); }
}

console.log('\n=== ping-scores.js: string-contract checks ===');

test('registers the ping-scores page', () => {
  assert.ok(/registerPage\(\s*'ping-scores'/.test(src));
});

test('fetches via the shared api() helper, not a raw fetch', () => {
  assert.ok(/api\(\s*'\/ping-scores'\s*\)/.test(src));
});

test('escapes sender name before interpolating into a record card', () => {
  assert.ok(/escapeHtml\(ping\.sender\)/.test(src));
});

test('escapes leaderboard entry name/pubkey before interpolating', () => {
  assert.ok(/escapeHtml\(e\.name \|\| e\.pubkey/.test(src));
});

test('escapes the fetch-error message before interpolating', () => {
  assert.ok(/escapeHtml\(e\.message\)/.test(src));
});

console.log('\n=== ping-scores.js: functional smoke test ===');

function makeSandbox(apiImpl) {
  function makeElement(tag) {
    const el = {
      tagName: tag, children: [], attributes: {}, style: {}, dataset: {},
      _listeners: {},
      get id() { return this.attributes.id || ''; },
      set id(v) { this.attributes.id = v; },
      set innerHTML(html) {
        this._innerHTML = html;
        this.children = [];
        const re = /id="([^"]+)"/g;
        let m;
        while ((m = re.exec(html))) {
          const child = makeElement('div');
          child.id = m[1];
          this.appendChild(child);
        }
        // Register data-view-path buttons too, since the smoke test needs
        // to click one and verify PacketPathMap.open was called.
        const vpRe = /data-view-path="([^"]*)"/g;
        while ((m = vpRe.exec(html))) {
          const child = makeElement('button');
          child.dataset.viewPath = m[1];
          this.appendChild(child);
        }
      },
      get innerHTML() { return this._innerHTML || ''; },
      appendChild(child) { this.children.push(child); child._parent = this; return child; },
      addEventListener(type, fn) { (this._listeners[type] = this._listeners[type] || []).push(fn); },
      click() { (this._listeners.click || []).forEach(fn => fn()); },
      querySelectorAll(sel) {
        // Only used for '[data-view-path]' in this file.
        const out = [];
        const walk = (el) => {
          if (el.dataset && el.dataset.viewPath !== undefined) out.push(el);
          (el.children || []).forEach(walk);
        };
        walk(this);
        return out;
      },
    };
    return el;
  }

  const container = makeElement('div');
  let registered = null;
  const ctx = {
    window: { PacketPathMap: { open: (h) => { ctx.window._openedHash = h; } } },
    console, Math, String, JSON, Promise, Error, Date, isNaN,
    escapeHtml: (s) => String(s == null ? '' : s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;'),
    api: apiImpl,
    registerPage: (name, mod) => { registered = mod; },
  };
  vm.createContext(ctx);
  vm.runInContext(src, ctx);
  return { ctx, container, getPage: () => registered };
}

(async () => {
  await (async () => {
    try {
      const { container, getPage } = makeSandbox(() => Promise.resolve({ totalPings: 0, generatedAt: new Date().toISOString() }));
      await getPage().init(container);
      assert.ok(container.innerHTML.includes('No pings recorded yet'), 'empty state should show a friendly message, got: ' + container.innerHTML);
      passed++;
      console.log('  ✅ shows a friendly empty state when totalPings is 0');
    } catch (e) { failed++; console.log('  ❌ shows a friendly empty state when totalPings is 0: ' + e.message); }
  })();

  await (async () => {
    try {
      const data = {
        totalPings: 5,
        generatedAt: new Date().toISOString(),
        farthestPing: { hash: 'far0001', sender: 'Alice', timestamp: new Date().toISOString(), farthestKm: 123.4, farthestNodeName: 'RepeaterX', stationCount: 3, deepestHops: 2 },
        mostHopsPing: { hash: 'hop0001', sender: 'Bob', timestamp: new Date().toISOString(), deepestHops: 4, deepestNodeName: 'RepeaterY', stationCount: 2 },
        widestSpreadPing: { hash: 'wide0001', sender: 'Carol', timestamp: new Date().toISOString(), stationCount: 6, deepestHops: 3 },
        fastestSpreadPing: { hash: 'fast0001', sender: 'Dave', timestamp: new Date().toISOString(), spreadSeconds: 2.5, stationCount: 2, deepestHops: 1 },
        mostEfficientPing: { hash: 'eff0001', sender: 'Eve', timestamp: new Date().toISOString(), kmPerSecondAirtime: 50.2, farthestKm: 100, stationCount: 2, deepestHops: 1 },
        thisWeek: {
          farthestPing: { hash: 'wkfar0001', sender: 'Frank', timestamp: new Date().toISOString(), farthestKm: 12.3, farthestNodeName: 'WeeklyRepeater', stationCount: 2, deepestHops: 1 },
        },
        relayLeaderboard: [{ pubkey: 'pkrelay1', name: 'RelayOne', count: 7 }],
        observerLeaderboard: [{ pubkey: 'pkobs1', name: 'ObsOne', count: 3 }],
        senderLeaderboard: [{ name: 'PingMaster', count: 12 }],
        areaLeaderboard: [{ name: 'Area A', count: 9 }],
      };
      const { container, getPage } = makeSandbox(() => Promise.resolve(data));
      await getPage().init(container);
      assert.ok(container.innerHTML.includes('123.4'), 'should show the all-time farthest record km, got: ' + container.innerHTML);
      assert.ok(container.innerHTML.includes('4 hops'), 'should show the most-hops record, got: ' + container.innerHTML);
      assert.ok(container.innerHTML.includes('6 stations'), 'should show the widest-spread record, got: ' + container.innerHTML);
      assert.ok(container.innerHTML.includes('RelayOne'), 'should show the relay leaderboard entry, got: ' + container.innerHTML);
      assert.ok(container.innerHTML.includes('ObsOne'), 'should show the observer leaderboard entry, got: ' + container.innerHTML);
      assert.ok(container.innerHTML.includes('PingMaster'), 'should show the sender leaderboard entry, got: ' + container.innerHTML);
      assert.ok(container.innerHTML.includes('Top Senders (30 days)'), 'should show the Top Senders leaderboard heading with the 30-day window noted, got: ' + container.innerHTML);
      assert.ok(container.innerHTML.includes("This Week's Best"), 'should show the This Week\'s Best section heading, got: ' + container.innerHTML);
      assert.ok(container.innerHTML.includes('All-Time Records'), 'should show the All-Time Records section heading, got: ' + container.innerHTML);
      assert.ok(container.innerHTML.includes('12.3'), 'should show the thisWeek farthest record km distinct from the all-time one, got: ' + container.innerHTML);
      assert.ok(container.innerHTML.includes('WeeklyRepeater'), 'should show the thisWeek record\'s node name, got: ' + container.innerHTML);
      assert.ok(container.innerHTML.includes('Area Activity'), 'should show the Area Activity leaderboard heading, got: ' + container.innerHTML);
      assert.ok(container.innerHTML.includes('Area A'), 'should show the area leaderboard entry, got: ' + container.innerHTML);
      passed++;
      console.log('  ✅ renders all 5 records and all four leaderboards (including Top Senders and Area Activity) from a populated response');
    } catch (e) { failed++; console.log('  ❌ renders all 5 records and all four leaderboards (including Top Senders and Area Activity) from a populated response: ' + e.message); }
  })();

  await (async () => {
    try {
      // areaLeaderboard is omitted entirely (not an empty array) when the
      // deployment has no areas configured -- the whole "Area Activity"
      // card must be skipped, not shown empty/broken, on deployments that
      // never set up areas.
      const data = {
        totalPings: 1,
        generatedAt: new Date().toISOString(),
        relayLeaderboard: [{ pubkey: 'pkrelay1', name: 'RelayOne', count: 7 }],
      };
      const { container, getPage } = makeSandbox(() => Promise.resolve(data));
      await getPage().init(container);
      assert.ok(!container.innerHTML.includes('Area Activity'), 'should NOT show an Area Activity card when areaLeaderboard is absent, got: ' + container.innerHTML);
      passed++;
      console.log('  ✅ Area Activity leaderboard card is skipped entirely when no areas are configured');
    } catch (e) { failed++; console.log('  ❌ Area Activity leaderboard card is skipped entirely when no areas are configured: ' + e.message); }
  })();

  await (async () => {
    try {
      // thisWeek is entirely absent when no ping in the last 7 days
      // resolved to a usable score (cmd/server/ping_scores.go leaves it
      // nil rather than sending an empty object) -- the section must
      // still render its 5 "No record yet" placeholders, not throw. All 5
      // all-time slots are populated here so the only "No record yet"
      // cards left are the 5 thisWeek ones -- isolates what's under test.
      const data = {
        totalPings: 1,
        generatedAt: new Date().toISOString(),
        farthestPing: { hash: 'old0001', sender: 'Grace', timestamp: new Date().toISOString(), farthestKm: 300, stationCount: 2, deepestHops: 1 },
        mostHopsPing: { hash: 'old0002', sender: 'Grace', timestamp: new Date().toISOString(), deepestHops: 3, stationCount: 2 },
        widestSpreadPing: { hash: 'old0003', sender: 'Grace', timestamp: new Date().toISOString(), stationCount: 4, deepestHops: 1 },
        fastestSpreadPing: { hash: 'old0004', sender: 'Grace', timestamp: new Date().toISOString(), spreadSeconds: 3, stationCount: 2, deepestHops: 1 },
        mostEfficientPing: { hash: 'old0005', sender: 'Grace', timestamp: new Date().toISOString(), kmPerSecondAirtime: 10, farthestKm: 20, stationCount: 2, deepestHops: 1 },
      };
      const { container, getPage } = makeSandbox(() => Promise.resolve(data));
      await getPage().init(container);
      assert.ok(container.innerHTML.includes("This Week's Best"), 'should still show the This Week\'s Best heading, got: ' + container.innerHTML);
      const weekSectionCount = (container.innerHTML.match(/No record yet/g) || []).length;
      assert.strictEqual(weekSectionCount, 5, 'expected all 5 thisWeek cards to fall back to "No record yet", got ' + weekSectionCount);
      passed++;
      console.log('  ✅ missing thisWeek renders 5 "No record yet" placeholders instead of throwing');
    } catch (e) { failed++; console.log('  ❌ missing thisWeek renders 5 "No record yet" placeholders instead of throwing: ' + e.message); }
  })();

  await (async () => {
    try {
      // Sender entries never carry a pubkey (see cmd/server/ping_scores.go
      // -- senders are keyed by channel-message display name only) --
      // must render as plain text, not a broken/empty node-detail link.
      const data = {
        totalPings: 1,
        generatedAt: new Date().toISOString(),
        senderLeaderboard: [{ name: 'NoPubkeySender', count: 3 }],
      };
      const { container, getPage } = makeSandbox(() => Promise.resolve(data));
      await getPage().init(container);
      assert.ok(container.innerHTML.includes('NoPubkeySender'), 'should show the sender name, got: ' + container.innerHTML);
      assert.ok(!container.innerHTML.includes('#/nodes/'), 'a pubkey-less sender must not render as a node-detail link, got: ' + container.innerHTML);
      passed++;
      console.log('  ✅ a pubkey-less sender leaderboard entry renders as plain text, not a node link');
    } catch (e) { failed++; console.log('  ❌ a pubkey-less sender leaderboard entry renders as plain text, not a node link: ' + e.message); }
  })();

  await (async () => {
    try {
      const data = {
        totalPings: 1,
        generatedAt: new Date().toISOString(),
        farthestPing: { hash: 'clickme01', sender: 'Alice', timestamp: new Date().toISOString(), farthestKm: 50, stationCount: 2, deepestHops: 1 },
      };
      const { ctx, container, getPage } = makeSandbox(() => Promise.resolve(data));
      await getPage().init(container);
      const btn = container.querySelectorAll('[data-view-path]')[0];
      assert.ok(btn, 'expected a data-view-path button for the farthest record');
      btn.click();
      assert.strictEqual(ctx.window._openedHash, 'clickme01', 'clicking View path should call PacketPathMap.open with the record\'s hash');
      passed++;
      console.log('  ✅ "View path" button opens PacketPathMap with the record\'s hash');
    } catch (e) { failed++; console.log('  ❌ "View path" button opens PacketPathMap with the record\'s hash: ' + e.message); }
  })();

  await (async () => {
    try {
      const { container, getPage } = makeSandbox(() => Promise.reject(new Error('network down')));
      await getPage().init(container);
      assert.ok(container.innerHTML.includes('Failed to load'), 'a failed fetch should show an error status, got: ' + container.innerHTML);
      passed++;
      console.log('  ✅ a failed fetch shows an error status without throwing');
    } catch (e) { failed++; console.log('  ❌ a failed fetch shows an error status without throwing: ' + e.message); }
  })();

  await (async () => {
    try {
      // Missing/nil records (e.g. fastestSpreadPing never won because no
      // ping ever had >=2 stations) must render the honest "No record
      // yet" placeholder, not throw on a null dereference.
      const data = {
        totalPings: 2,
        generatedAt: new Date().toISOString(),
        farthestPing: { hash: 'x', sender: 'A', timestamp: new Date().toISOString(), farthestKm: 10, stationCount: 2, deepestHops: 1 },
      };
      const { container, getPage } = makeSandbox(() => Promise.resolve(data));
      await getPage().init(container);
      assert.ok(container.innerHTML.includes('No record yet'), 'missing records should show "No record yet", got: ' + container.innerHTML);
      passed++;
      console.log('  ✅ missing records (nil) render "No record yet" instead of throwing');
    } catch (e) { failed++; console.log('  ❌ missing records (nil) render "No record yet" instead of throwing: ' + e.message); }
  })();

  console.log('\n' + '='.repeat(48));
  console.log(`  ping-scores.js: ${passed} passed, ${failed} failed`);
  console.log('='.repeat(48));
  if (failed > 0) process.exit(1);
})();

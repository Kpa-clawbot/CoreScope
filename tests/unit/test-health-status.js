/* getHealthStatus (roles.js) turns "last heard" into healthy / degraded /
 * silent by the node's role. The favorites dropdown, both home cards, the
 * analytics network status, My Repeaters and the live node panel all call it.
 *
 * Expected values are written out from the documented defaults (repeater and
 * room: degraded after 24 h, silent after 72 h; every other role: 1 h and
 * 24 h), not computed from HEALTH_THRESHOLDS, so a wrong threshold, a swapped
 * pair or a flipped comparison in roles.js fails here. Which timestamp and
 * role each view passes in is covered by tests/e2e/test-health-status-e2e.js.
 */
'use strict';
const vm = require('vm');
const fs = require('fs');
const path = require('path');
const assert = require('assert');

const PUBLIC = path.resolve(__dirname, '..', '..', 'public');
const ROLES_JS = fs.readFileSync(path.join(PUBLIC, 'roles.js'), 'utf8');

let passed = 0, failed = 0;
async function test(name, fn) {
  try { await fn(); passed++; console.log(`  ✅ ${name}`); }
  catch (e) { failed++; console.log(`  ❌ ${name}: ${e.message}`); }
}

// Load roles.js the way the browser does. `config` is what /api/config/client
// returns, so the healthThresholds override goes through the real code path.
function loadRoles(config) {
  const ctx = {
    window: {}, console, Date, Infinity,
    document: { readyState: 'complete', createElement: () => ({ id: '' }), head: { appendChild: () => {} }, getElementById: () => null, addEventListener: () => {} },
    fetch: () => Promise.resolve({ json: () => Promise.resolve(config || {}) }),
  };
  vm.createContext(ctx);
  vm.runInContext(ROLES_JS, ctx);
  // roles.js reads HEALTH_THRESHOLDS as a bare global; window.X does not create one here.
  for (const k of Object.keys(ctx.window)) ctx[k] = ctx.window[k];
  return ctx.window;
}

const M = 60 * 1000, H = 60 * M;
const NOW = Date.UTC(2026, 9, 15, 12, 0, 0);
const iso = (ageMs) => new Date(NOW - ageMs).toISOString();

const INFRA = ['repeater', 'room'];
const OTHER = ['companion', 'sensor', 'unknown', undefined, null, ''];
const INFRA_CASES = [
  [0, 'healthy'], [30 * M, 'healthy'], [5 * H, 'healthy'], [24 * H - 1, 'healthy'],
  [24 * H, 'degraded'], [30 * H, 'degraded'], [72 * H - 1, 'degraded'],
  [72 * H, 'silent'], [80 * H, 'silent'], [200 * H, 'silent'],
];
const OTHER_CASES = [
  [0, 'healthy'], [30 * M, 'healthy'], [1 * H - 1, 'healthy'],
  [1 * H, 'degraded'], [5 * H, 'degraded'], [24 * H - 1, 'degraded'],
  [24 * H, 'silent'], [30 * H, 'silent'], [80 * H, 'silent'],
];

// Source guard: the age-vs-threshold comparison lives only in roles.js. A view
// that compares on its own can drift from the others, which is how the
// favorites dropdown and home cards ended up role-blind.
const COMPARISONS = [
  /[<>]=?\s*[\w$]+(?:\.[\w$]+)*\.\w*(?:degradedMs|silentMs|DegradedMs|SilentMs)\b/,
  /\.\w*(?:degradedMs|silentMs|DegradedMs|SilentMs)\s*[<>]/,
];
const comparesThresholds = (line) => COMPARISONS.some((re) => re.test(line));

// Execute the actual Live detail renderer, with only its DOM/network boundary
// replaced. This catches a view selecting the wrong timestamp even when the
// shared health helpers themselves remain correct.
async function liveDetail(node, stats = {}, healthFailed = false) {
  const src = fs.readFileSync(path.join(PUBLIC, 'live.js'), 'utf8');
  const start = src.indexOf('  async function showNodeDetail(');
  const end = src.indexOf('  async function loadNodes(', start);
  assert.ok(start >= 0 && end > start, 'Live detail renderer must exist');
  const content = { innerHTML: '' };
  const panel = { classList: { add() {}, remove() {} } };
  const calls = [];
  const helpers = loadRoles();
  const ctx = {
    ...helpers, window: helpers, console,
    document: { getElementById: (id) => id === 'nodeDetailContent' ? content : panel },
    api: async (url) => {
      calls.push(url);
      if (url.endsWith('/health')) {
        if (healthFailed) throw new Error('Unavailable');
        return { stats, observers: [], recentPackets: [] };
      }
      if (url.endsWith('/paths')) return { paths: [] };
      return { node };
    },
    escapeHtml: String,
    formatLiveTimestampHtml: (value) => Number.isNaN(value) ? '—' : String(value),
    activeNodeDetailKey: null,
  };
  vm.createContext(ctx);
  vm.runInContext(src.slice(start, end), ctx);
  await ctx.showNodeDetail(node.public_key);
  assert.strictEqual(calls.length, 3, 'detail must not add network requests');
  return content.innerHTML;
}

(async () => {
  const roles = loadRoles();
  const { getHealthStatus } = roles;

  console.log('\n=== getHealthStatus: role x age ===');
  for (const role of INFRA) {
    await test(`${role}: 24 h / 72 h, ISO and epoch ms agree`, () => {
      for (const [age, want] of INFRA_CASES) {
        assert.strictEqual(getHealthStatus(role, iso(age), NOW), want, `${role} at ${age} ms (ISO)`);
        assert.strictEqual(getHealthStatus(role, NOW - age, NOW), want, `${role} at ${age} ms (epoch)`);
      }
    });
  }
  for (const role of OTHER) {
    await test(`${JSON.stringify(role)}: 1 h / 24 h, ISO and epoch ms agree`, () => {
      for (const [age, want] of OTHER_CASES) {
        assert.strictEqual(getHealthStatus(role, iso(age), NOW), want, `${role} at ${age} ms (ISO)`);
        assert.strictEqual(getHealthStatus(role, NOW - age, NOW), want, `${role} at ${age} ms (epoch)`);
      }
    });
  }

  console.log('\n=== getHealthStatus: inputs ===');
  await test('no usable timestamp is null, not silent, for every role', () => {
    for (const role of [...INFRA, ...OTHER]) {
      for (const heard of [null, undefined, '', 'not a date']) {
        assert.strictEqual(getHealthStatus(role, heard, NOW), null, `${role} / ${JSON.stringify(heard)}`);
      }
    }
  });
  await test('a timestamp ahead of now (clock skew) counts as healthy', () => {
    assert.strictEqual(getHealthStatus('repeater', iso(-5 * M), NOW), 'healthy');
    assert.strictEqual(getHealthStatus('companion', iso(-5 * M), NOW), 'healthy');
  });
  await test('now defaults to the current time', () => {
    assert.strictEqual(getHealthStatus('companion', Date.now() - 30 * M), 'healthy');
    assert.strictEqual(getHealthStatus('companion', Date.now() - 2 * H), 'degraded');
    assert.strictEqual(getHealthStatus('repeater', new Date(Date.now() - 30 * H).toISOString()), 'degraded');
  });

  console.log('\n=== healthThresholds from config.json ===');
  await test('an override from /api/config/client changes the cut-offs', async () => {
    const r = loadRoles({ healthThresholds: { infraDegradedMs: 2 * H, infraSilentMs: 6 * H, nodeSilentMs: 3 * H } });
    await r.MeshConfigReady;
    assert.strictEqual(r.getHealthStatus('repeater', iso(1 * H), NOW), 'healthy');
    assert.strictEqual(r.getHealthStatus('repeater', iso(3 * H), NOW), 'degraded');
    assert.strictEqual(r.getHealthStatus('repeater', iso(7 * H), NOW), 'silent');
    assert.strictEqual(r.getHealthStatus('companion', iso(2 * H), NOW), 'degraded');
    assert.strictEqual(r.getHealthStatus('companion', iso(4 * H), NOW), 'silent');
    // The default instance is unaffected.
    assert.strictEqual(getHealthStatus('repeater', iso(7 * H), NOW), 'healthy');
  });

  console.log('\n=== Live detail: certain activity instead of adverts alone (#2233) ===');
  const recent = new Date(Date.now() - 30 * M).toISOString();
  const old = new Date(Date.now() - 120 * H).toISOString();
  const degraded = new Date(Date.now() - 30 * H).toISOString();
  for (const role of ['repeater', 'room', 'companion']) {
    await test(`${role}: recent certain heard activity wins over an old advert`, async () => {
      const html = await liveDetail({ public_key: 'a'.repeat(64), role, last_seen: old, last_heard: recent });
      assert.match(html, />Online<\/span>/);
      assert.match(html, new RegExp('Last Activity</td><td>' + new Date(recent).getTime() + '</td>'));
    });
  }
  await test('health and node timestamps use the newest certain evidence', async () => {
    for (const [last_heard, lastHeard] of [[old, recent], [recent, old]]) {
      const html = await liveDetail({ public_key: 'a'.repeat(64), role: 'repeater', last_seen: old, last_heard }, { lastHeard });
      assert.match(html, />Online<\/span>/);
      assert.match(html, new RegExp('Last Activity</td><td>' + new Date(recent).getTime() + '</td>'));
    }
  });
  for (const role of ['repeater', 'room']) {
    await test(`${role}: a recent shared-prefix relay is uncertain, not Online`, async () => {
      const html = await liveDetail({ public_key: 'a'.repeat(64), role, last_seen: old, last_relayed: recent });
      assert.match(html, />Uncertain<\/span>/);
      assert.doesNotMatch(html, /class="health-green"/);
      assert.match(html, new RegExp('Last Activity</td><td>' + new Date(old).getTime() + '</td>'));
    });
  }
  await test('companion relay attribution does not replace certain activity', async () => {
    const html = await liveDetail({ public_key: 'a'.repeat(64), role: 'companion', last_seen: old, last_relayed: recent });
    assert.match(html, />Offline<\/span>/);
  });
  await test('degraded, missing and malformed activity remain conservative', async () => {
    for (const [timestamp, label, display] of [[degraded, 'Degraded', new Date(degraded).getTime()], [null, 'Offline', '—'], ['bad date', 'Offline', '—']]) {
      const html = await liveDetail({ public_key: 'a'.repeat(64), role: 'repeater', last_seen: timestamp });
      assert.match(html, new RegExp('>' + label + '</span>'));
      assert.match(html, new RegExp('Last Activity</td><td>' + display + '</td>'));
    }
  });
  await test('node activity still works when the health request fails', async () => {
    const html = await liveDetail({ public_key: 'a'.repeat(64), role: 'room', last_seen: 'bad date', last_heard: recent }, {}, true);
    assert.match(html, />Online<\/span>/);
  });

  console.log('\n=== guard: no threshold comparison outside roles.js ===');
  await test('the guard matches comparisons and nothing else', () => {
    for (const s of ['age < th.degradedMs', 'ageMs<thresholds.silentMs', 'age < HEALTH_THRESHOLDS.nodeDegradedMs',
      'x <= window.HEALTH_THRESHOLDS.infraSilentMs', 'th.silentMs > age', 'th.degradedMs>=age']) {
      assert.ok(comparesThresholds(s), 'should match: ' + s);
    }
    for (const s of ['const silentMs = Date.now() - wsLastMessageAt;', 'if (silentMs >= 0 && silentMs < WS_STALE_MS) {',
      'const threshMs = isInfra ? HEALTH_THRESHOLDS.infraSilentMs : HEALTH_THRESHOLDS.nodeSilentMs;',
      'const s = getHealthStatus(n.role, n.last_heard || n.last_seen, now);']) {
      assert.ok(!comparesThresholds(s), 'should not match: ' + s);
    }
  });
  await test('public/*.js has no threshold comparison outside roles.js', () => {
    const hits = [];
    for (const f of fs.readdirSync(PUBLIC).filter((f) => f.endsWith('.js') && f !== 'roles.js')) {
      fs.readFileSync(path.join(PUBLIC, f), 'utf8').split('\n').forEach((line, i) => {
        if (comparesThresholds(line)) hits.push(`${f}:${i + 1}: ${line.trim().slice(0, 100)}`);
      });
    }
    assert.deepStrictEqual(hits, [], 'use getHealthStatus(role, heard) instead:\n    ' + hits.join('\n    '));
  });

  console.log(`\n${passed} passed, ${failed} failed`);
  process.exit(failed ? 1 : 0);
})();

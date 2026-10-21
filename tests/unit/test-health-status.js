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

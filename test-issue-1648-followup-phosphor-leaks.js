/* Issue #1648 follow-up — v3.8.4 test executor found two emoji surfaces the
 * M6 final-sweep missed because they sit on app-controlled UI labels:
 *
 *   v384-1.2  — /observers .obs-clock-naive-chip ships raw ⚠️ (U+26A0).
 *               Must render the Phosphor `ph-warning` sprite instead.
 *   v384-12.18 — /analytics?tab=channels grouped-table rows for unknown
 *                encrypted channels ship a raw 🔒 (U+1F512) prefix on the
 *                `displayName` text label. Must render the Phosphor
 *                `ph-lock` sprite instead.
 *
 * Both render paths are app-controlled (no user input), so the emoji is
 * pure iconography masquerading as text — same class of bug as #1657. Fix
 * is a sprite swap; same playbook.
 */
'use strict';
const vm = require('vm');
const fs = require('fs');
const path = require('path');
const assert = require('assert');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log('  ✅ ' + name); }
  catch (e) { failed++; console.log('  ❌ ' + name + ': ' + e.message); }
}

// ── Finding 1: ObserversNaiveChip must use ph-warning sprite, no ⚠️ ─────────
console.log('\n=== v384-1.2 — /observers .obs-clock-naive-chip sprite ===');
(function () {
  const ctx = {
    window: { addEventListener: () => {}, dispatchEvent: () => {} },
    document: {
      readyState: 'complete',
      createElement: () => ({ id: '', textContent: '', innerHTML: '' }),
      head: { appendChild: () => {} },
      getElementById: () => null,
      addEventListener: () => {},
      querySelectorAll: () => [],
      querySelector: () => null,
    },
    console, Date, Math, Array, Object, String, Number, Boolean, JSON,
    setInterval: () => 0, clearInterval: () => {},
    setTimeout: (fn) => { try { fn(); } catch {} return 0; },
    encodeURIComponent, decodeURIComponent,
    fetch: () => Promise.resolve({ json: () => Promise.resolve({}) }),
    registerPage: () => {},
    RegionFilter: { init: () => {}, onChange: () => () => {}, getSelected: () => null },
    healthStatus: () => ({ cls: 'health-green', label: 'Online' }),
    timeAgo: () => 'now', uptimeStr: () => '—',
    packetBadge: () => '', sparkBar: () => '',
    makeColumnsResizable: () => {},
    observerSkewSeverity: () => 'ok', renderSkewBadge: () => '',
    debouncedOnWS: (fn) => fn,
  };
  vm.createContext(ctx);
  vm.runInContext(fs.readFileSync(path.join(__dirname, 'public/observers.js'), 'utf8'), ctx);

  const html = ctx.window.ObserversNaiveChip.render({
    id: 'x', clock_naive: true, clock_skew_seconds: -28800,
    clock_skew_count_24h: 17,
  });

  test('chip renders non-empty HTML for clock_naive=true', () => {
    assert.ok(html && html.length > 0, 'expected non-empty chip HTML');
  });

  test('chip contains <use href*="#ph-warning"> Phosphor sprite', () => {
    assert.ok(/<use[^>]+href="[^"]*#ph-warning"/.test(html),
      'expected ph-warning sprite <use href>: ' + html);
  });

  test('chip body has zero ⚠ (U+26A0) codepoints in visible text', () => {
    // Strip tag attributes; the warning glyph must not appear as text
    // content inside the chip body (or inside title/aria-label values).
    const codepoint = '\u26A0';
    assert.ok(!html.includes(codepoint),
      'unexpected U+26A0 (⚠) in chip output: ' + JSON.stringify(html));
  });
})();

// ── Finding 2: analytics decorate channels — encrypted displayName ──────────
console.log('\n=== v384-12.18 — /analytics Channels encrypted group labels ===');
(function () {
  // Match the harness from test-analytics-channels-integration.js so we
  // load analytics.js the same way.
  global.window = global;
  global.document = {
    documentElement: {},
    createElement: () => ({ style: {}, addEventListener() {} }),
    addEventListener() {}, removeEventListener() {},
    querySelector: () => null, querySelectorAll: () => [],
    getElementById: () => null,
  };
  global.localStorage = {
    _s: {},
    getItem(k) { return this._s[k] || null; },
    setItem(k, v) { this._s[k] = String(v); },
    removeItem(k) { delete this._s[k]; },
  };
  global.getComputedStyle = () => ({ getPropertyValue: () => '' });
  global.registerPage = () => {};
  global.api = async () => ({});
  global.fetch = async () => ({ ok: true, json: async () => ({}) });
  global.CLIENT_TTL = {};
  global.RegionFilter = { getRegionParam: () => '' };
  global.Storage = function () {};
  global.timeAgo = () => '';
  global.histogram = () => ({ svg: '' });

  // eslint-disable-next-line no-eval
  eval(fs.readFileSync(path.join(__dirname, 'public/analytics.js'), 'utf8'));

  const decorate = global._analyticsDecorateChannels;
  const tbodyFn = global._analyticsChannelTbodyHtml;
  test('_analyticsDecorateChannels exposed', () => {
    assert.strictEqual(typeof decorate, 'function');
  });
  test('_analyticsChannelTbodyHtml exposed', () => {
    assert.strictEqual(typeof tbodyFn, 'function');
  });

  const out = decorate(
    [
      { hash: 64, name: 'ch64', messages: 300, senders: 0, encrypted: true },
      { hash: 200, name: '',    messages: 5,   senders: 0, encrypted: true },
    ],
    {}, {}
  );

  const unknownEnc = out.find(c => c.hash === 64);
  const emptyEnc   = out.find(c => c.hash === 200);

  test('unknown encrypted displayName has zero 🔒 (U+1F512)', () => {
    assert.ok(!unknownEnc.displayName.includes('\u{1F512}'),
      'unexpected U+1F512 in displayName: ' + JSON.stringify(unknownEnc.displayName));
  });

  test('empty-name encrypted displayName has zero 🔒 (U+1F512)', () => {
    assert.ok(!emptyEnc.displayName.includes('\u{1F512}'),
      'unexpected U+1F512 in displayName: ' + JSON.stringify(emptyEnc.displayName));
  });

  // Rendered table HTML for these rows must show the lock sprite next to
  // the row's name cell — same playbook as #1657 (innerText → innerHTML
  // refactor so the sprite tag renders, not the escaped text).
  const tbody = tbodyFn(out, 'messages', 'desc', { grouped: true });

  test('rendered tbody has zero 🔒 (U+1F512) anywhere', () => {
    assert.ok(!tbody.includes('\u{1F512}'),
      'unexpected U+1F512 in tbody output (sample): ' + tbody.slice(0, 200));
  });

  test('encrypted row name cell renders ph-lock sprite (not 🔒 text)', () => {
    // The encrypted-channel row's NAME column (the first <td> with the
    // <strong> wrapper) must contain a ph-lock sprite — that's where the
    // 🔒 emoji used to leak. Existing usage in the "Decrypted" column
    // (last <td>) is pre-existing and not what we're asserting.
    // Match: <td><strong>...<use href...#ph-lock"...</strong></td>
    const re = /<td><strong>[^<]*<svg[^>]*><use[^>]+href="[^"]*#ph-lock"/;
    assert.ok(re.test(tbody),
      'expected ph-lock sprite inside <strong> name cell; sample: ' +
      tbody.slice(0, 400));
  });
})();

console.log('\n' + (failed ? '✗ ' + failed + ' failed, ' : '') + passed + ' passed');
process.exit(failed ? 1 : 0);

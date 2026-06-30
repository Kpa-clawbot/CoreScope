/* Unit tests for issue #1518: branding.homeUrl validator + nav-brand override.
 *
 * The validator MUST reject any scheme that can execute script when rendered
 * into <a class="nav-brand">[href] (javascript:, data:, vbscript:, file:,
 * about:). It must accept http(s):// absolute URLs and app-relative hash
 * routes (#/...). Empty / whitespace / non-string falls through to the default.
 *
 * The renderBranding output (DOM-string) must expose a data-cv2-field input
 * for "branding.homeUrl" so the customizer Branding tab plumbing matches
 * branding.logoUrl exactly.
 */
'use strict';
const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log('  ✅ ' + name); }
  catch (e) { failed++; console.log('  ❌ ' + name + ': ' + e.message); }
}

function makeSandbox() {
  const storage = {};
  const localStorage = {
    _data: storage,
    getItem(k) { return k in storage ? storage[k] : null; },
    setItem(k, v) { storage[k] = String(v); },
    removeItem(k) { delete storage[k]; },
    clear() { for (const k in storage) delete storage[k]; }
  };
  const ctx = {
    window: {
      addEventListener: () => {},
      dispatchEvent: () => {},
      SITE_CONFIG: {},
      _SITE_CONFIG_ORIGINAL_HOME: null,
    },
    document: {
      readyState: 'loading',
      createElement: () => ({
        id: '', textContent: '', innerHTML: '', className: '',
        setAttribute: () => {}, appendChild: () => {},
        style: {}, addEventListener: () => {},
        querySelectorAll: () => [], querySelector: () => null,
      }),
      head: { appendChild: () => {} },
      getElementById: () => null,
      addEventListener: () => {},
      querySelectorAll: () => [],
      querySelector: () => null,
      documentElement: {
        style: { setProperty: () => {}, removeProperty: () => {}, getPropertyValue: () => '' },
        dataset: { theme: 'dark' },
        getAttribute: () => 'dark',
      },
    },
    console,
    localStorage,
    setTimeout: (fn) => fn(),
    clearTimeout: () => {},
    Date, Math, Array, Object, JSON, String, Number, Boolean,
    parseInt, parseFloat, isNaN, Infinity, NaN, undefined,
    MutationObserver: class { observe() {} },
    HashChangeEvent: class {},
    CustomEvent: class CustomEvent { constructor(type, opts) { this.type = type; this.detail = opts && opts.detail; } },
    getComputedStyle: () => ({ getPropertyValue: () => '' }),
  };
  ctx.window.localStorage = localStorage;
  ctx.self = ctx.window;
  return ctx;
}

function load() {
  const ctx = makeSandbox();
  const labelsCode = fs.readFileSync('public/payload-labels.js', 'utf8');
  const code = fs.readFileSync('public/customize-v2.js', 'utf8');
  vm.createContext(ctx);
  vm.runInContext(labelsCode, ctx, { filename: 'payload-labels.js' });
  vm.runInContext(code, ctx, { filename: 'customize-v2.js' });
  return { ctx, api: ctx.window._customizerV2 };
}

console.log('\n📋 Issue #1518 — branding.homeUrl validator + Branding tab field\n');

// ── isValidHomeUrl: rejects ──
const REJECT = [
  ['javascript: payload', 'javascript:alert(1)'],
  ['JavaScript: mixed case', 'JaVaScRiPt:alert(1)'],
  ['javascript with leading whitespace', '  javascript:alert(1)'],
  ['data: URL', 'data:text/html,<script>alert(1)</script>'],
  ['vbscript:', 'vbscript:msgbox(1)'],
  ['file:', 'file:///etc/passwd'],
  ['about:blank', 'about:blank'],
  ['empty string', ''],
  ['whitespace only', '   \t\n  '],
  ['null', null],
  ['undefined', undefined],
  ['number', 42],
  ['object', { url: 'https://x' }],
  ['protocol-relative //evil.com', '//evil.com'],
  ['no-scheme bare path /foo', '/foo'],
  ['ftp://', 'ftp://example.com/'],
  ['javascript hidden by tab', 'java\tscript:alert(1)'],
];
console.log('isValidHomeUrl rejects:');
REJECT.forEach(([name, input]) => {
  test(name, () => {
    const { api } = load();
    assert.strictEqual(api.isValidHomeUrl(input), false,
      'expected isValidHomeUrl(' + JSON.stringify(input) + ') === false');
  });
});

// ── isValidHomeUrl: accepts ──
const ACCEPT = [
  ['https URL', 'https://example.com'],
  ['https with path', 'https://example.com/path?q=1#frag'],
  ['http URL', 'http://intranet.local/'],
  ['HTTPS mixed case', 'HTTPS://Example.COM/'],
  ['hash root', '#/'],
  ['hash route', '#/home'],
  ['hash deeper', '#/map?packet=abc'],
  ['just #', '#'],
];
console.log('isValidHomeUrl accepts:');
ACCEPT.forEach(([name, input]) => {
  test(name, () => {
    const { api } = load();
    assert.strictEqual(api.isValidHomeUrl(input), true,
      'expected isValidHomeUrl(' + JSON.stringify(input) + ') === true');
  });
});

// ── Branding tab exposes homeUrl field ──
console.log('Branding tab DOM:');
test('renderBranding() output contains data-cv2-field="branding.homeUrl"', () => {
  // The Branding tab markup is built by _renderBranding (private). We
  // assert against the source text directly — same pattern used to verify
  // logoUrl is wired.
  const src = fs.readFileSync('public/customize-v2.js', 'utf8');
  assert.ok(
    /data-cv2-field="branding\.homeUrl"/.test(src),
    'public/customize-v2.js must wire a data-cv2-field="branding.homeUrl" input in the Branding tab (mirror branding.logoUrl)'
  );
});

test('config.example.json declares branding.homeUrl + _comment_', () => {
  const raw = fs.readFileSync('config.example.json', 'utf8');
  const cfg = JSON.parse(raw);
  assert.ok(cfg.branding, 'config.example.json should have a branding section');
  assert.ok('homeUrl' in cfg.branding,
    'config.example.json branding.homeUrl key must be present (set to null by default)');
  // Per AGENTS.md Config Documentation Rule, a comment must describe behavior.
  // Branding shares a single _comment field; it must mention homeUrl.
  assert.ok(
    cfg.branding._comment && /homeUrl/i.test(cfg.branding._comment),
    'config.example.json branding._comment must document homeUrl behavior'
  );
});

test('nav-brand[href] override is wired in applyCSS branding block', () => {
  // Lines ~700-715 in customize-v2.js apply branding.* to the DOM. The fix
  // adds a branch that sets .nav-brand[href] from a validated branding.homeUrl.
  const src = fs.readFileSync('public/customize-v2.js', 'utf8');
  assert.ok(
    /\.nav-brand[\s\S]{0,200}?setAttribute\(\s*['"]href['"]/.test(src) ||
      /querySelector\(\s*['"]\.nav-brand['"]\)[\s\S]{0,400}?\.href\s*=/.test(src) ||
      /\.nav-brand[\s\S]{0,400}?branding\.homeUrl/.test(src),
    'public/customize-v2.js must set the .nav-brand[href] attribute when branding.homeUrl is a valid override'
  );
});

// ── Summary ──
console.log('\n' + (passed + failed) + ' tests: ' + passed + ' passed, ' + failed + ' failed\n');
process.exit(failed > 0 ? 1 : 0);

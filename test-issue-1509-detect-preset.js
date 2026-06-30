/**
 * Regression test for #1509 follow-up: _detectActivePreset() must return
 * 'default' on a vanilla install (no localStorage overrides, no server-side
 * navActiveBg).
 *
 * The original #1509 patch seeded `navActiveBg` in PRESETS.default.theme and
 * .themeDark. Because THEME_COLOR_KEYS derives from THEME_CSS_MAP, the new
 * key joined the per-key equality check in _detectActivePreset(). On a vanilla
 * install effTheme.navActiveBg is undefined (server config doesn't seed it,
 * and the user has no overrides), so the comparison
 *     effTheme.navActiveBg !== 'rgba(74,158,255,0.15)'
 * was true and 'default' was no longer detected as the active preset.
 *
 * Downstream effect: the customize-theme E2E picks the first non-active preset
 * and clicks it. With 'default' silently un-detected, the E2E clicked the
 * 'default' button itself, which triggers a localStorage.removeItem instead
 * of writeOverrides — so the "cs-theme-overrides not written" assertion fired.
 *
 * Fix: an undefined value in the effective theme should match ANY preset value
 * for the same key (the preset's value will become effective via the CSS
 * cascade anyway). This test asserts that semantic.
 *
 * Run: node test-issue-1509-detect-preset.js
 */
'use strict';
const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log(`  ✅ ${name}`); }
  catch (e) { failed++; console.log(`  ❌ ${name}: ${e.message}`); }
}

function makeSandbox(opts) {
  opts = opts || {};
  const storage = {};
  const localStorage = {
    getItem(k) { return k in storage ? storage[k] : null; },
    setItem(k, v) { storage[k] = String(v); },
    removeItem(k) { delete storage[k]; },
    clear() { for (const k in storage) delete storage[k]; }
  };
  const cssProps = {};
  const ctx = {
    window: { addEventListener: () => {}, dispatchEvent: () => {}, SITE_CONFIG: {}, _SITE_CONFIG_ORIGINAL_HOME: null },
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
      body: { style: { setProperty: () => {} } },
      documentElement: {
        style: {
          setProperty: (k, v) => { cssProps[k] = v; },
          removeProperty: (k) => { delete cssProps[k]; },
          getPropertyValue: (k) => cssProps[k] || '',
        },
        dataset: { theme: opts.theme || 'light' },
        getAttribute: () => (opts.theme || 'light'),
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
  return { ctx, cssProps };
}

function loadCustomizer(opts) {
  const { ctx, cssProps } = makeSandbox(opts);
  const labelsCode = fs.readFileSync('public/payload-labels.js', 'utf8');
  const code = fs.readFileSync('public/customize-v2.js', 'utf8');
  vm.createContext(ctx);
  vm.runInContext(labelsCode, ctx, { filename: 'payload-labels.js' });
  vm.runInContext(code, ctx, { filename: 'customize-v2.js' });
  return { api: ctx.window._customizerV2, cssProps, ls: ctx.localStorage };
}

console.log('\n#1509 — _detectActivePreset on vanilla install\n');

test('_detectActivePreset returns "default" with no overrides and no server navActiveBg', () => {
  const { api } = loadCustomizer({ theme: 'light' });
  // Vanilla install: server config has none of the theme keys, including
  // navActiveBg. localStorage has no overrides.
  api.init({});
  assert.strictEqual(typeof api.detectActivePreset, 'function',
    'api.detectActivePreset should be exposed for testability');
  const active = api.detectActivePreset();
  assert.strictEqual(active, 'default',
    'expected "default" preset to be detected as active on vanilla install, got ' + JSON.stringify(active));
});

test('_detectActivePreset still returns "default" in dark mode with no overrides', () => {
  const { api } = loadCustomizer({ theme: 'dark' });
  api.init({});
  const active = api.detectActivePreset();
  assert.strictEqual(active, 'default',
    'expected "default" preset to be detected as active on vanilla install (dark), got ' + JSON.stringify(active));
});

console.log('\n' + passed + '/' + (passed + failed) + ' tests passed');
process.exit(failed > 0 ? 1 : 0);

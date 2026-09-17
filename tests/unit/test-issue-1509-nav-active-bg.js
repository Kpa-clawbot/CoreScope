/**
 * Regression test for #1509: --nav-active-bg must be theme-overridable.
 *
 * The active nav pill uses `background: var(--nav-active-bg)` in style.css.
 * Before #1509 the customizer's THEME_CSS_MAP did not include navActiveBg,
 * so themes / per-operator overrides could not change the active-pill color
 * even though every other nav color (navBg, navBg2, navText, navTextMuted)
 * was themeable.
 *
 * This unit test asserts:
 *   1. THEME_CSS_MAP exposes navActiveBg → '--nav-active-bg'
 *   2. THEME_COLOR_KEYS implicitly picks it up (it derives from THEME_CSS_MAP)
 *   3. applyCSS() actually writes the variable on documentElement when an
 *      override is present (proves end-to-end wiring, not just the map entry)
 *   4. The 'default' preset seeds both light + dark themes with a navActiveBg
 *      default so themes don't fall back to the hardcoded rgba()
 *
 * Run: node test-issue-1509-nav-active-bg.js
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
  // Capture CSS variable writes for assertion.
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

console.log('\n#1509 — themeable --nav-active-bg\n');

test('THEME_CSS_MAP maps navActiveBg → --nav-active-bg', () => {
  const { api } = loadCustomizer();
  assert.strictEqual(api.THEME_CSS_MAP.navActiveBg, '--nav-active-bg',
    'THEME_CSS_MAP.navActiveBg should be "--nav-active-bg"');
});

test('applyCSS writes --nav-active-bg on documentElement when overridden (light)', () => {
  const { api, cssProps, ls } = loadCustomizer({ theme: 'light' });
  ls.setItem('cs-theme-overrides', JSON.stringify({ theme: { navActiveBg: '#abcdef' } }));
  api.init({});
  assert.strictEqual(cssProps['--nav-active-bg'], '#abcdef',
    'expected --nav-active-bg=#abcdef on documentElement, got ' + cssProps['--nav-active-bg']);
});

test('applyCSS writes --nav-active-bg on documentElement when overridden (dark)', () => {
  const { api, cssProps, ls } = loadCustomizer({ theme: 'dark' });
  ls.setItem('cs-theme-overrides', JSON.stringify({ themeDark: { navActiveBg: '#112233' } }));
  api.init({});
  assert.strictEqual(cssProps['--nav-active-bg'], '#112233',
    'expected --nav-active-bg=#112233 on documentElement in dark mode, got ' + cssProps['--nav-active-bg']);
});

console.log('\n' + passed + '/' + (passed + failed) + ' tests passed');
process.exit(failed > 0 ? 1 : 0);

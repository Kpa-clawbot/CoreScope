/**
 * #1496 — "Reset All Customizations" must clear every customizer-touched
 * piece of state, not just `cs-theme-overrides`.
 *
 * Coverage:
 *   - localStorage keys: cs-theme-overrides, meshcore-cb-preset,
 *     channels-show-encrypted, mc-dark-tile-provider
 *   - body attribute: data-cb-preset
 *   - body.style: --mc-role-{role}, --mc-role-{role}-text
 *   - root.style: --mc-role-{role}, --mc-role-{role}-text, --node-{role},
 *     --mc-marker-stroke-{color,width,opacity}, --mc-mb-{confirmed,suspected,unknown},
 *     --mc-rt-ramp-{0..4}, --logo-accent, --logo-accent-hi, theme vars
 *
 * Must NOT touch:
 *   - localStorage: meshcore-theme (theme light/dark),
 *     meshcore-gesture-hints-* (own button), meshcore-favorites,
 *     mc-channels-* selection state
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

function makeSandbox() {
  const storage = {};
  const localStorage = {
    getItem(k) { return k in storage ? storage[k] : null; },
    setItem(k, v) { storage[k] = String(v); },
    removeItem(k) { delete storage[k]; },
    clear() { for (const k in storage) delete storage[k]; },
    _data: storage,
  };

  // Fake CSSStyleDeclaration with property tracking
  function makeStyle() {
    const props = {};
    return {
      _props: props,
      setProperty(name, value /*, priority */) { props[name] = String(value); },
      removeProperty(name) { delete props[name]; },
      getPropertyValue(name) { return props[name] || ''; },
      get cssText() {
        return Object.keys(props).map(k => k + ': ' + props[k]).join('; ');
      },
      set cssText(v) {
        for (const k in props) delete props[k];
        // very loose parser — only used to reset to empty
        if (!v) return;
      },
    };
  }

  const rootStyle = makeStyle();
  const bodyAttrs = {};
  const bodyStyle = makeStyle();

  const docEl = {
    style: rootStyle,
    dataset: { theme: 'dark' },
    getAttribute: (n) => n === 'data-theme' ? 'dark' : null,
    setAttribute: () => {},
    removeAttribute: () => {},
  };
  const body = {
    style: bodyStyle,
    _attrs: bodyAttrs,
    setAttribute(n, v) { bodyAttrs[n] = v; },
    getAttribute(n) { return bodyAttrs[n] || null; },
    removeAttribute(n) { delete bodyAttrs[n]; },
    hasAttribute(n) { return n in bodyAttrs; },
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
        style: makeStyle(), addEventListener: () => {},
        querySelectorAll: () => [], querySelector: () => null,
      }),
      head: { appendChild: () => {} },
      body: body,
      getElementById: () => null,
      addEventListener: () => {},
      querySelectorAll: () => [],
      querySelector: () => null,
      documentElement: docEl,
    },
    console,
    localStorage,
    setTimeout: (fn) => fn(),
    clearTimeout: () => {},
    Date, Math, Array, Object, JSON, String, Number, Boolean,
    parseInt, parseFloat, isNaN, Infinity, NaN, undefined,
    MutationObserver: class { observe() {} },
    HashChangeEvent: class {},
    CustomEvent: class { constructor(type, opts) { this.type = type; this.detail = opts && opts.detail; } },
    getComputedStyle: () => ({ getPropertyValue: () => '' }),
  };
  ctx.window.localStorage = localStorage;
  ctx.self = ctx.window;
  return { ctx, body, rootStyle, bodyStyle, localStorage, bodyAttrs };
}

function loadCustomizer() {
  const env = makeSandbox();
  // Provide a stub MeshCorePresets.clearPreset so resetAll can call it.
  let clearPresetCalls = 0;
  env.ctx.window.MeshCorePresets = {
    clearPreset() {
      clearPresetCalls++;
      env.localStorage.removeItem('meshcore-cb-preset');
      env.body.removeAttribute('data-cb-preset');
      // Mirror real cb-presets.js: strip preset CSS vars from :root
      ['repeater', 'companion', 'room', 'sensor', 'observer'].forEach(function (role) {
        env.rootStyle.removeProperty('--mc-role-' + role);
        env.rootStyle.removeProperty('--mc-role-' + role + '-text');
      });
      ['confirmed', 'suspected', 'unknown'].forEach(function (k) {
        env.rootStyle.removeProperty('--mc-mb-' + k);
      });
      for (var i = 0; i < 5; i++) env.rootStyle.removeProperty('--mc-rt-ramp-' + i);
    },
    get _calls() { return clearPresetCalls; },
    list: [],
    currentPreset: () => null,
  };
  // Tile provider stubs.
  let tpCalls = 0;
  env.ctx.window.MC_setDarkTileProvider = (id) => {
    tpCalls++;
    if (id) env.localStorage.setItem('mc-dark-tile-provider', id);
    return true;
  };
  env.ctx.window.MC_getDarkTileProvider = () => env.localStorage.getItem('mc-dark-tile-provider') || 'carto-dark';

  const code = fs.readFileSync(path.join(__dirname, 'public', 'customize-v2.js'), 'utf8');
  vm.createContext(env.ctx);
  vm.runInContext(code, env.ctx, { filename: 'customize-v2.js' });
  env.api = env.ctx.window._customizerV2;
  return env;
}

console.log('\n📋 Issue #1496 — Reset All Customizations completeness\n');

test('resetAll function is exposed on _customizerV2 API', () => {
  const env = loadCustomizer();
  assert.strictEqual(typeof env.api.resetAll, 'function', 'expected api.resetAll to be a function');
});

test('clears cs-theme-overrides localStorage key', () => {
  const env = loadCustomizer();
  env.localStorage.setItem('cs-theme-overrides', JSON.stringify({ theme: { accent: '#ff0000' } }));
  env.api.resetAll();
  assert.strictEqual(env.localStorage.getItem('cs-theme-overrides'), null);
});

test('clears meshcore-cb-preset localStorage key', () => {
  const env = loadCustomizer();
  env.localStorage.setItem('meshcore-cb-preset', 'deut');
  env.api.resetAll();
  assert.strictEqual(env.localStorage.getItem('meshcore-cb-preset'), null);
});

test('clears channels-show-encrypted localStorage key', () => {
  const env = loadCustomizer();
  env.localStorage.setItem('channels-show-encrypted', 'true');
  env.api.resetAll();
  assert.strictEqual(env.localStorage.getItem('channels-show-encrypted'), null);
});

test('clears mc-dark-tile-provider localStorage key', () => {
  const env = loadCustomizer();
  env.localStorage.setItem('mc-dark-tile-provider', 'voyager-inverted');
  env.api.resetAll();
  assert.strictEqual(env.localStorage.getItem('mc-dark-tile-provider'), null);
});

test('removes data-cb-preset body attribute', () => {
  const env = loadCustomizer();
  env.body.setAttribute('data-cb-preset', 'deut');
  env.api.resetAll();
  assert.strictEqual(env.body.hasAttribute('data-cb-preset'), false);
});

test('clears per-role --mc-role-* writes from body.style', () => {
  const env = loadCustomizer();
  env.bodyStyle.setProperty('--mc-role-repeater', '#abcdef');
  env.bodyStyle.setProperty('--mc-role-companion', '#123456');
  env.bodyStyle.setProperty('--mc-role-repeater-text', '#ffffff');
  env.api.resetAll();
  assert.strictEqual(env.bodyStyle.getPropertyValue('--mc-role-repeater'), '');
  assert.strictEqual(env.bodyStyle.getPropertyValue('--mc-role-companion'), '');
  assert.strictEqual(env.bodyStyle.getPropertyValue('--mc-role-repeater-text'), '');
});

test('clears per-role --mc-role-* and --node-* from root.style', () => {
  const env = loadCustomizer();
  ['repeater','companion','room','sensor','observer'].forEach((r) => {
    env.rootStyle.setProperty('--mc-role-' + r, '#abcdef');
    env.rootStyle.setProperty('--node-' + r, '#abcdef');
  });
  env.api.resetAll();
  ['repeater','companion','room','sensor','observer'].forEach((r) => {
    assert.strictEqual(env.rootStyle.getPropertyValue('--mc-role-' + r), '', '--mc-role-' + r);
    assert.strictEqual(env.rootStyle.getPropertyValue('--node-' + r), '', '--node-' + r);
  });
});

test('clears marker stroke vars from root.style', () => {
  const env = loadCustomizer();
  env.rootStyle.setProperty('--mc-marker-stroke-color', '#ff0000');
  env.rootStyle.setProperty('--mc-marker-stroke-width', '3');
  env.rootStyle.setProperty('--mc-marker-stroke-opacity', '0.5');
  env.api.resetAll();
  assert.strictEqual(env.rootStyle.getPropertyValue('--mc-marker-stroke-color'), '');
  assert.strictEqual(env.rootStyle.getPropertyValue('--mc-marker-stroke-width'), '');
  assert.strictEqual(env.rootStyle.getPropertyValue('--mc-marker-stroke-opacity'), '');
});

test('clears --mc-mb-* and --mc-rt-ramp-* preset vars from root.style', () => {
  const env = loadCustomizer();
  ['confirmed','suspected','unknown'].forEach((k) => env.rootStyle.setProperty('--mc-mb-' + k, '#abcdef'));
  for (let i = 0; i < 5; i++) env.rootStyle.setProperty('--mc-rt-ramp-' + i, '#abcdef');
  env.api.resetAll();
  ['confirmed','suspected','unknown'].forEach((k) => {
    assert.strictEqual(env.rootStyle.getPropertyValue('--mc-mb-' + k), '', '--mc-mb-' + k);
  });
  for (let i = 0; i < 5; i++) {
    assert.strictEqual(env.rootStyle.getPropertyValue('--mc-rt-ramp-' + i), '', '--mc-rt-ramp-' + i);
  }
});

test('does NOT clear theme (meshcore-theme) localStorage key', () => {
  const env = loadCustomizer();
  env.localStorage.setItem('meshcore-theme', 'dark');
  env.api.resetAll();
  assert.strictEqual(env.localStorage.getItem('meshcore-theme'), 'dark');
});

test('does NOT clear meshcore-gesture-hints-* keys', () => {
  const env = loadCustomizer();
  env.localStorage.setItem('meshcore-gesture-hints-row-swipe', '1');
  env.api.resetAll();
  assert.strictEqual(env.localStorage.getItem('meshcore-gesture-hints-row-swipe'), '1');
});

test('does NOT clear meshcore-favorites', () => {
  const env = loadCustomizer();
  env.localStorage.setItem('meshcore-favorites', '["abc"]');
  env.api.resetAll();
  assert.strictEqual(env.localStorage.getItem('meshcore-favorites'), '["abc"]');
});

test('does NOT clear mc-channels-* selection state', () => {
  const env = loadCustomizer();
  env.localStorage.setItem('mc-channels-selection', 'foo');
  env.api.resetAll();
  assert.strictEqual(env.localStorage.getItem('mc-channels-selection'), 'foo');
});

console.log('\n' + passed + ' passed, ' + failed + ' failed\n');
if (failed > 0) process.exit(1);

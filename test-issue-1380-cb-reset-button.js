/**
 * #1380 — Colorblind a11y stretch goal: "Reset to default Wong" button.
 *
 * Deferred from #1361 / PR #1378. This test enforces that the customizer
 * exposes a Reset button which:
 *   - Calls MeshCorePresets.applyPreset('default'), AND
 *   - Removes localStorage["meshcore-cb-preset"] afterwards.
 *
 * Pure-string + vm.createContext assertions, mirrors test-issue-1361.
 */
'use strict';

const fs   = require('fs');
const path = require('path');
const vm   = require('vm');

let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  \u2713 ' + msg); }
  else      { failed++; console.error('  \u2717 ' + msg); }
}

const customSrc  = fs.readFileSync(path.join(__dirname, 'public', 'customize-v2.js'), 'utf8');
const presetsSrc = fs.readFileSync(path.join(__dirname, 'public', 'cb-presets.js'), 'utf8');

console.log('\n=== #1380 Reset A: customize-v2.js renders a Reset-to-Wong button ===');
assert(/data-cv2-cb-reset/.test(customSrc),
  'customize-v2.js exposes a data-cv2-cb-reset hook on the reset button');
assert(/Reset[^<]*Wong/i.test(customSrc),
  'customize-v2.js button copy mentions "Reset" and "Wong"');

console.log('\n=== #1380 Reset B: handler exposed for tests + behavior ===');

function makeSandbox() {
  const stored = {};
  const ls = {
    getItem(k) { return Object.prototype.hasOwnProperty.call(stored, k) ? stored[k] : null; },
    setItem(k, v) { stored[k] = String(v); },
    removeItem(k) { delete stored[k]; },
  };
  const body = {
    _attrs: {},
    setAttribute(k, v) { this._attrs[k] = v; },
    getAttribute(k) { return Object.prototype.hasOwnProperty.call(this._attrs, k) ? this._attrs[k] : null; },
    removeAttribute(k) { delete this._attrs[k]; },
    dataset: {},
  };
  const root = {
    style: {
      _v: {},
      setProperty(k, v) { this._v[k] = v; },
      removeProperty(k) { delete this._v[k]; },
      getPropertyValue(k) { return this._v[k] || ''; },
    },
    dataset: { theme: 'light' },
    getAttribute() { return 'light'; },
    setAttribute() {},
  };
  const handlers = {};
  const sandbox = {
    window: null,
    document: {
      readyState: 'complete',
      body: body,
      documentElement: root,
      head: { appendChild() {} },
      getElementById() { return null; },
      createElement() {
        return {
          id: '', textContent: '', innerHTML: '', className: '',
          setAttribute() {}, appendChild() {}, style: {},
          addEventListener() {}, querySelectorAll() { return []; }, querySelector() { return null; },
        };
      },
      addEventListener(ev, cb) { (handlers[ev] = handlers[ev] || []).push(cb); },
      querySelectorAll() { return []; },
      querySelector() { return null; },
    },
    localStorage: ls,
    console: console,
    setTimeout(fn) { try { fn(); } catch (e) {} },
    clearTimeout() {},
    MutationObserver: class { observe() {} },
    HashChangeEvent: class {},
    CustomEvent: class { constructor(t, o) { this.type = t; this.detail = o && o.detail; } },
    Event: class { constructor(t) { this.type = t; } },
    getComputedStyle() { return { getPropertyValue() { return ''; } }; },
  };
  sandbox.window = {
    addEventListener(ev, cb) { (handlers[ev] = handlers[ev] || []).push(cb); },
    dispatchEvent(ev) { (handlers[ev.type] || []).forEach(function (cb) { try { cb(ev); } catch (_) {} }); return true; },
    localStorage: ls,
    SITE_CONFIG: {},
    location: { hash: '', pathname: '/' },
    CustomEvent: sandbox.CustomEvent,
    Event: sandbox.Event,
    matchMedia() { return { matches: false, addEventListener() {} }; },
  };
  sandbox.self = sandbox.window;
  return { sandbox, body, ls };
}

let envOK = false, env, exposed;
try {
  env = makeSandbox();
  vm.createContext(env.sandbox);
  // Load cb-presets first so MeshCorePresets exists.
  vm.runInContext(presetsSrc, env.sandbox, { filename: 'cb-presets.js' });
  vm.runInContext(customSrc, env.sandbox, { filename: 'customize-v2.js' });
  exposed = env.sandbox.window._customizerV2;
  envOK = !!exposed;
} catch (e) {
  console.error('  ! load failed: ' + e.message);
}

assert(envOK, 'cb-presets.js + customize-v2.js both load in vm sandbox');

// Seed: pretend the user had picked "trit" earlier.
env.ls.setItem('meshcore-cb-preset', 'trit');
env.sandbox.window.MeshCorePresets.applyPreset('trit');
assert(env.ls.getItem('meshcore-cb-preset') === 'trit',
  'seed: localStorage[meshcore-cb-preset] == "trit" before reset');
assert(env.body.getAttribute('data-cb-preset') === 'trit',
  'seed: body[data-cb-preset] == "trit" before reset');

if (envOK && typeof exposed.resetCbPreset === 'function') {
  exposed.resetCbPreset();
  assert(env.ls.getItem('meshcore-cb-preset') === null,
    'resetCbPreset() removes localStorage["meshcore-cb-preset"]');
  // MeshCorePresets.applyPreset('default') sets body[data-cb-preset="default"].
  assert(env.body.getAttribute('data-cb-preset') === 'default',
    'resetCbPreset() applies the default Wong preset (body[data-cb-preset="default"])');
} else {
  assert(false, 'customize-v2.js exposes resetCbPreset() helper');
}

console.log('\n=== #1380 Reset summary ===');
console.log('  passed: ' + passed + '   failed: ' + failed);
if (failed > 0) process.exit(1);

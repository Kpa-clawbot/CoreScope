/**
 * #1361 — Theme customizer: first-class colorblind-mode presets.
 *
 * MVP scope (locked):
 *   - 5 presets: default, deut, prot, trit, achromat
 *   - Each preset overrides --mc-role-* CSS vars + --mc-mb-* status vars
 *   - Achromatopsia uses pure luminance ramp (no hue)
 *   - Persisted to localStorage("meshcore-cb-preset"), survives reload,
 *     syncs across tabs via the `storage` event.
 *   - Customizer UI exposes a radio/dropdown to switch preset.
 *   - WCAG 1.4.3 / 1.4.11 validation helper exists and is correct on
 *     known reference pairs.
 *
 * Pure-string + vm.createContext assertions (mirrors test-issue-1356 / 1360
 * pattern) so this runs in the JS-unit-tests CI step without a browser.
 *
 * Stretch goals (live simulation overlay, "Reset to default Wong" button)
 * are explicitly DEFERRED and intentionally NOT asserted here.
 */
'use strict';

const fs = require('fs');
const path = require('path');
const vm = require('vm');

let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}

const presetsPath = path.join(__dirname, 'public', 'cb-presets.js');
const styleSrc    = fs.readFileSync(path.join(__dirname, 'public', 'style.css'), 'utf8');
const customSrc   = fs.readFileSync(path.join(__dirname, 'public', 'customize-v2.js'), 'utf8');
const appSrc      = fs.readFileSync(path.join(__dirname, 'public', 'app.js'), 'utf8');
const indexSrc    = fs.readFileSync(path.join(__dirname, 'public', 'index.html'), 'utf8');

console.log('\n=== #1361 A: cb-presets.js module exists and is loadable ===');
assert(fs.existsSync(presetsPath), 'public/cb-presets.js exists');
const presetsSrc = fs.existsSync(presetsPath) ? fs.readFileSync(presetsPath, 'utf8') : '';

// Build a minimal browser-ish sandbox so we can run the IIFE module.
function makeSandbox() {
  const root = { style: { _vars: {}, setProperty(k, v) { this._vars[k] = v; }, getPropertyValue(k) { return this._vars[k]; }, removeProperty(k) { delete this._vars[k]; } } };
  const body = { _attrs: {}, setAttribute(k, v) { this._attrs[k] = v; }, getAttribute(k) { return this._attrs[k] || null; }, removeAttribute(k) { delete this._attrs[k]; }, dataset: {} };
  const listeners = {};
  const storage = {
    _data: {},
    getItem(k) { return Object.prototype.hasOwnProperty.call(this._data, k) ? this._data[k] : null; },
    setItem(k, v) { this._data[k] = String(v); },
    removeItem(k) { delete this._data[k]; },
  };
  const sandbox = {
    window: null,
    document: {
      documentElement: root,
      body: body,
      getElementById(id) { return null; },
      createElement() { return { setAttribute() {}, appendChild() {}, style: {} }; },
    },
    localStorage: storage,
    console: console,
    setTimeout: setTimeout,
    clearTimeout: clearTimeout,
    addEventListener(ev, cb) { (listeners[ev] = listeners[ev] || []).push(cb); },
    dispatchEvent(ev) { (listeners[ev.type] || []).forEach(function (cb) { cb(ev); }); return true; },
    CustomEvent: function (type, opts) { this.type = type; this.detail = opts && opts.detail; },
    Event: function (type) { this.type = type; },
  };
  sandbox.window = sandbox;
  sandbox.document.body = body;
  return { sandbox, root, body, storage, listeners };
}

let envOK = false, env;
try {
  env = makeSandbox();
  vm.createContext(env.sandbox);
  vm.runInContext(presetsSrc, env.sandbox);
  envOK = true;
} catch (e) {
  console.error('  ! cb-presets.js failed to load in vm sandbox: ' + e.message);
}

console.log('\n=== #1361 B: MeshCorePresets.list — 5 documented presets ===');
const MCP = envOK && env.sandbox.window && env.sandbox.window.MeshCorePresets;
assert(!!MCP, 'window.MeshCorePresets exists after script load');
assert(MCP && Array.isArray(MCP.list), 'MeshCorePresets.list is an array');
const expectedIds = ['default', 'deut', 'prot', 'trit', 'achromat'];
if (MCP && Array.isArray(MCP.list)) {
  assert(MCP.list.length === 5, 'list contains exactly 5 presets (got ' + MCP.list.length + ')');
  const ids = MCP.list.map(function (p) { return p.id; });
  expectedIds.forEach(function (id) {
    assert(ids.indexOf(id) >= 0, 'list contains preset id="' + id + '"');
  });
  MCP.list.forEach(function (p) {
    assert(typeof p.label === 'string' && p.label.length > 0, 'preset "' + p.id + '" has non-empty label');
    assert(typeof p.description === 'string' && p.description.length > 0, 'preset "' + p.id + '" has 1-line description');
    assert(p.roleColors && typeof p.roleColors === 'object', 'preset "' + p.id + '" has roleColors map');
    ['repeater', 'companion', 'room', 'sensor', 'observer'].forEach(function (role) {
      assert(typeof p.roleColors[role] === 'string' && /^#[0-9a-f]{6}$/i.test(p.roleColors[role]),
        'preset "' + p.id + '" has hex roleColors.' + role);
    });
  });
}

console.log('\n=== #1361 C: applyPreset sets body[data-cb-preset] + CSS vars ===');
assert(MCP && typeof MCP.applyPreset === 'function', 'applyPreset is a function');
if (MCP && typeof MCP.applyPreset === 'function') {
  ['default', 'deut', 'prot', 'trit', 'achromat'].forEach(function (id) {
    MCP.applyPreset(id);
    assert(env.body.getAttribute('data-cb-preset') === id,
      'applyPreset("' + id + '") sets body[data-cb-preset="' + id + '"]');
    // Verify the css var for repeater matches the preset's declared color
    const declared = MCP.list.find(function (p) { return p.id === id; }).roleColors.repeater;
    const got = env.root.style.getPropertyValue('--mc-role-repeater');
    assert(got && got.toLowerCase() === declared.toLowerCase(),
      'applyPreset("' + id + '") sets --mc-role-repeater=' + declared + ' (got ' + got + ')');
  });
}

console.log('\n=== #1361 D: persistence — localStorage("meshcore-cb-preset") ===');
if (MCP) {
  MCP.applyPreset('trit');
  assert(env.storage.getItem('meshcore-cb-preset') === 'trit',
    'applyPreset persists choice to localStorage key "meshcore-cb-preset"');
}

console.log('\n=== #1361 E: re-init from localStorage re-applies preset ===');
// Fresh sandbox with localStorage pre-populated
{
  const env2 = makeSandbox();
  env2.storage.setItem('meshcore-cb-preset', 'achromat');
  vm.createContext(env2.sandbox);
  try {
    vm.runInContext(presetsSrc, env2.sandbox);
    const MCP2 = env2.sandbox.window.MeshCorePresets;
    // Module init OR explicit initFromStorage should re-apply
    if (MCP2 && typeof MCP2.initFromStorage === 'function') MCP2.initFromStorage();
    assert(env2.body.getAttribute('data-cb-preset') === 'achromat',
      're-init from localStorage re-applies "achromat" preset to body data-attr');
  } catch (e) {
    assert(false, 're-init sandbox load failed: ' + e.message);
  }
}

console.log('\n=== #1361 F: cross-tab sync via storage event ===');
if (MCP) {
  // Dispatch a synthetic storage event for our key
  const ev = new env.sandbox.Event('storage');
  ev.key = 'meshcore-cb-preset';
  ev.newValue = 'prot';
  env.sandbox.dispatchEvent(ev);
  assert(env.body.getAttribute('data-cb-preset') === 'prot',
    'storage event with newValue="prot" updates body[data-cb-preset="prot"]');
}

console.log('\n=== #1361 G: style.css has preset blocks for non-default presets ===');
['deut', 'prot', 'trit', 'achromat'].forEach(function (id) {
  const re = new RegExp('body\\[data-cb-preset=["\']' + id + '["\']\\][^{]*\\{[^}]*--mc-role-repeater', 'i');
  assert(re.test(styleSrc),
    'style.css has body[data-cb-preset="' + id + '"] block overriding --mc-role-repeater');
});

console.log('\n=== #1361 H: customize-v2.js has Colorblind preset selector UI ===');
assert(/data-cv2-cb-preset|cust-cb-preset|colorblind|Colorblind/i.test(customSrc),
  'customize-v2.js contains a Colorblind preset selector hook');
assert(/MeshCorePresets|applyPreset|cb-preset/i.test(customSrc),
  'customize-v2.js wires the UI to MeshCorePresets.applyPreset');

console.log('\n=== #1361 I: index.html loads cb-presets.js BEFORE app.js ===');
const cbIdx = indexSrc.indexOf('cb-presets.js');
const appIdx = indexSrc.indexOf('app.js?');
assert(cbIdx > 0, 'index.html includes <script src="cb-presets.js?...">');
assert(cbIdx >= 0 && appIdx >= 0 && cbIdx < appIdx,
  'cb-presets.js script tag precedes app.js (so app.js can init the preset)');

console.log('\n=== #1361 J: app.js initializes preset on DOMContentLoaded ===');
assert(/MeshCorePresets\s*[\.\&]/.test(appSrc) || /window\.MeshCorePresets/.test(appSrc),
  'app.js references window.MeshCorePresets (init wiring)');
assert(/['"]storage['"]/.test(appSrc) && /meshcore-cb-preset/.test(appSrc),
  'app.js handles cross-tab storage event for meshcore-cb-preset');

console.log('\n=== #1361 K: WCAG luminance helper — correctness on reference pairs ===');
assert(MCP && MCP.wcag && typeof MCP.wcag.contrast === 'function',
  'MeshCorePresets.wcag.contrast(fg, bg) is exposed');
if (MCP && MCP.wcag && typeof MCP.wcag.contrast === 'function') {
  const c1 = MCP.wcag.contrast('#000000', '#ffffff');
  assert(Math.abs(c1 - 21) < 0.05, 'contrast(black, white) ≈ 21:1 (got ' + c1.toFixed(2) + ')');
  const c2 = MCP.wcag.contrast('#ffffff', '#ffffff');
  assert(Math.abs(c2 - 1) < 0.001, 'contrast(white, white) === 1:1 (got ' + c2.toFixed(3) + ')');
  // Mid-grey #777 vs white ~ 4.48
  const c3 = MCP.wcag.contrast('#777777', '#ffffff');
  assert(c3 > 4.4 && c3 < 4.7, 'contrast(#777, white) ≈ 4.48 (got ' + c3.toFixed(2) + ')');
}

console.log('\n=== #1361 L: achromat preset is pure luminance (no chroma) ===');
if (MCP) {
  const ach = MCP.list.find(function (p) { return p.id === 'achromat'; });
  if (ach) {
    Object.keys(ach.roleColors).forEach(function (role) {
      const hex = ach.roleColors[role];
      const r = parseInt(hex.slice(1, 3), 16);
      const g = parseInt(hex.slice(3, 5), 16);
      const b = parseInt(hex.slice(5, 7), 16);
      assert(r === g && g === b,
        'achromat preset roleColors.' + role + ' is grey (r==g==b, got ' + hex + ')');
    });
  }
}

console.log('\n=== Summary ===');
console.log('  passed: ' + passed);
console.log('  failed: ' + failed);
if (failed > 0) process.exit(1);

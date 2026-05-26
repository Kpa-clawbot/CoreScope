/**
 * #1407 — cb-preset propagation + WCAG AA for every preset/role.
 *
 * Two bugs:
 *   1. window.ROLE_COLORS is a STATIC literal that's never resynced when
 *      MeshCorePresets.applyPreset() rewrites the --mc-role-* CSS vars.
 *      The hardcoded values are the LEGACY April palette (#dc2626 et al),
 *      not even the current Wong defaults from #1357.
 *   2. The achromat preset pairs dark text (#1a1a1a) with 3 dark grays
 *      whose contrast falls below WCAG 1.4.3 AA (4.5:1): repeater 1.27,
 *      companion 2.55, room 4.43.
 *
 * This test fails on master and passes after the fix lands.
 *
 * Pure node + vm.createContext — runs in the JS-unit-tests CI step
 * without a browser. Mirrors test-issue-1361-cb-presets.js sandbox shape.
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

const rolesSrc   = fs.readFileSync(path.join(__dirname, 'public', 'roles.js'), 'utf8');
const presetsSrc = fs.readFileSync(path.join(__dirname, 'public', 'cb-presets.js'), 'utf8');
const styleSrc   = fs.readFileSync(path.join(__dirname, 'public', 'style.css'), 'utf8');
const mapSrc     = fs.readFileSync(path.join(__dirname, 'public', 'map.js'), 'utf8');

// ─── WCAG helpers (independent of cb-presets, so we validate the impl) ───
function hexToRgb(hex) {
  hex = String(hex || '').trim();
  if (hex[0] !== '#' || hex.length !== 7) return null;
  return {
    r: parseInt(hex.slice(1, 3), 16),
    g: parseInt(hex.slice(3, 5), 16),
    b: parseInt(hex.slice(5, 7), 16)
  };
}
function chanLin(c) { var s = c / 255; return s <= 0.03928 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4); }
function relLum(hex) { var rgb = hexToRgb(hex); if (!rgb) return 0; return 0.2126*chanLin(rgb.r)+0.7152*chanLin(rgb.g)+0.0722*chanLin(rgb.b); }
function contrast(fg, bg) {
  var L1 = relLum(fg), L2 = relLum(bg);
  var hi = Math.max(L1, L2), lo = Math.min(L1, L2);
  return (hi + 0.05) / (lo + 0.05);
}

// ─── Browser-ish sandbox (CSS var setProperty/getPropertyValue + listeners) ───
function makeSandbox() {
  const root = {
    style: {
      _vars: {},
      setProperty(k, v) { this._vars[k] = String(v); },
      getPropertyValue(k) { return this._vars[k] || ''; },
      removeProperty(k) { delete this._vars[k]; }
    },
    getAttribute() { return null; },
    setAttribute() {}
  };
  const body = {
    _attrs: {},
    setAttribute(k, v) { this._attrs[k] = v; },
    getAttribute(k) { return this._attrs[k] || null; },
    removeAttribute(k) { delete this._attrs[k]; },
    dataset: {}
  };
  const listeners = {};
  const storage = {
    _data: {},
    getItem(k) { return Object.prototype.hasOwnProperty.call(this._data, k) ? this._data[k] : null; },
    setItem(k, v) { this._data[k] = String(v); },
    removeItem(k) { delete this._data[k]; }
  };
  const sandbox = {
    window: null,
    document: {
      documentElement: root,
      body: body,
      readyState: 'complete',
      getElementById() { return null; },
      createElement() {
        var el = { _children: [], style: {}, textContent: '', id: '',
                   setAttribute() {}, appendChild(c) { this._children.push(c); } };
        return el;
      },
      head: { appendChild() {} },
      addEventListener() {},
    },
    localStorage: storage,
    console: console,
    setTimeout: setTimeout,
    clearTimeout: clearTimeout,
    addEventListener(ev, cb) { (listeners[ev] = listeners[ev] || []).push(cb); },
    dispatchEvent(ev) { (listeners[ev.type] || []).forEach(function (cb) { cb(ev); }); return true; },
    CustomEvent: function (type, opts) { this.type = type; this.detail = opts && opts.detail; },
    Event: function (type) { this.type = type; },
    fetch: function () { return { then: function () { return { then: function () { return { catch: function () {} }; }, catch: function () {} }; } }; },
    matchMedia: function () { return { matches: false }; },
    // getComputedStyle reads from the root.style._vars set by cb-presets
    getComputedStyle: function (el) {
      return {
        getPropertyValue: function (k) {
          return (root.style._vars[k] || '');
        }
      };
    }
  };
  sandbox.window = sandbox;
  return { sandbox, root, body, storage, listeners };
}

console.log('\n=== #1407 A: ROLE_COLORS is NOT the static legacy palette ===');
let env;
try {
  env = makeSandbox();
  vm.createContext(env.sandbox);
  vm.runInContext(rolesSrc, env.sandbox);
  vm.runInContext(presetsSrc, env.sandbox);
} catch (e) {
  assert(false, 'sandbox load failed: ' + e.message);
}

const RC = env && env.sandbox.window.ROLE_COLORS;
assert(!!RC, 'window.ROLE_COLORS is defined');
// MUTATION GUARD: ROLE_COLORS must be exposed via a getter that reads live
// CSS vars — NOT a plain hardcoded data property. The bug is that it's a
// static literal disconnected from --mc-role-* CSS vars.
const RCDesc = env && Object.getOwnPropertyDescriptor(env.sandbox.window, 'ROLE_COLORS');
assert(RCDesc && typeof RCDesc.get === 'function',
  'window.ROLE_COLORS must be a getter property (live read of --mc-role-* CSS vars), not a static literal');

// Direct CSS-var test: simulate what cb-presets.js does without going through
// applyPreset's legacy ROLE_COLORS mutation path. Set the CSS var directly →
// ROLE_COLORS getter must reflect it.
env.root.style.setProperty('--mc-role-repeater', '#abcdef');
const live = env.sandbox.window.ROLE_COLORS.repeater;
assert(String(live).toLowerCase() === '#abcdef',
  'ROLE_COLORS.repeater reflects live --mc-role-repeater CSS var (got ' + live + ')');
env.root.style.removeProperty('--mc-role-repeater');

console.log('\n=== #1407 B: ROLE_COLORS tracks --mc-role-* CSS vars live ===');
const MCP = env && env.sandbox.window.MeshCorePresets;
assert(!!MCP, 'MeshCorePresets exists');
if (MCP) {
  // Apply default preset → CSS vars become Wong → ROLE_COLORS should report Wong.
  MCP.applyPreset('default');
  const def = String(env.sandbox.window.ROLE_COLORS.repeater).toLowerCase();
  assert(def === '#d55e00', 'after applyPreset("default") ROLE_COLORS.repeater === #D55E00 Wong (got ' + def + ')');

  // Switch to deut → ROLE_COLORS.repeater should change to IBM orange #FE6100.
  MCP.applyPreset('deut');
  const deut = String(env.sandbox.window.ROLE_COLORS.repeater).toLowerCase();
  assert(deut === '#fe6100', 'after applyPreset("deut") ROLE_COLORS.repeater === #FE6100 IBM orange (got ' + deut + ')');

  // Switch to achromat → should be dark gray #333333.
  MCP.applyPreset('achromat');
  const ach = String(env.sandbox.window.ROLE_COLORS.repeater).toLowerCase();
  assert(ach === '#333333', 'after applyPreset("achromat") ROLE_COLORS.repeater === #333333 (got ' + ach + ')');
}

console.log('\n=== #1407 C: ROLE_STYLE.color also reads live ===');
if (MCP) {
  MCP.applyPreset('trit');
  const rs = env.sandbox.window.ROLE_STYLE && env.sandbox.window.ROLE_STYLE.repeater;
  const c = rs && String(rs.color || '').toLowerCase();
  assert(c === '#cc6677', 'after applyPreset("trit") ROLE_STYLE.repeater.color === #CC6677 (got ' + c + ')');
}

console.log('\n=== #1407 D: applyPreset writes --mc-role-X-text CSS vars ===');
if (MCP) {
  ['default', 'deut', 'prot', 'trit', 'achromat'].forEach(function (id) {
    MCP.applyPreset(id);
    ['repeater', 'companion', 'room', 'sensor', 'observer'].forEach(function (role) {
      const v = env.root.style.getPropertyValue('--mc-role-' + role + '-text');
      assert(/^#[0-9a-f]{6}$/i.test(v), 'preset "' + id + '" sets --mc-role-' + role + '-text (got "' + v + '")');
    });
  });
}

console.log('\n=== #1407 E: WCAG 1.4.3 AA — every (preset, role) pair ≥ 4.5:1 ===');
if (MCP) {
  ['default', 'deut', 'prot', 'trit', 'achromat'].forEach(function (id) {
    MCP.applyPreset(id);
    const preset = MCP.list.find(function (p) { return p.id === id; });
    ['repeater', 'companion', 'room', 'sensor', 'observer'].forEach(function (role) {
      const bg = preset.roleColors[role];
      const text = env.root.style.getPropertyValue('--mc-role-' + role + '-text');
      const ratio = contrast(text, bg);
      assert(ratio >= 4.5,
        'WCAG 1.4.3 AA: preset "' + id + '" role "' + role + '" bg=' + bg +
        ' text=' + text + ' contrast=' + ratio.toFixed(2) + ':1 (need ≥4.5)');
    });
  });
}

console.log('\n=== #1407 F: pill text color is driven by CSS var, not hardcoded ===');
// style.css `.mc-pill` rule must use var(--mc-role-*-text) — NOT hardcoded #1a1a1a.
const pillRuleMatch = styleSrc.match(/\.mc-cluster\s+\.mc-pill\s*\{[^}]*\}/);
assert(pillRuleMatch, '.mc-cluster .mc-pill rule found in style.css');
if (pillRuleMatch) {
  const block = pillRuleMatch[0];
  assert(/var\(--mc-pill-text|var\(--mc-role-/.test(block),
    '.mc-cluster .mc-pill uses var(--mc-...-text) for color (got: ' + block.replace(/\s+/g,' ').slice(0,200) + ')');
}
// map.js inline style: must not hardcode color:#1a1a1a on the pill
const inlineHardcoded = /color:\s*#1a1a1a/.test(mapSrc);
assert(!inlineHardcoded, 'public/map.js does not hardcode color:#1a1a1a on .mc-pill inline style');

console.log('\n=== Summary ===');
console.log('  passed: ' + passed);
console.log('  failed: ' + failed);
if (failed > 0) process.exit(1);

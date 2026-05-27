/**
 * #1418 — cb-presets.js writes --mc-rt-ramp-0..4 + fires cb-preset-changed.
 *
 * `route-view.js` reads CSS vars --mc-rt-ramp-0..4 to color the edge gradient
 * via getComputedStyle. When the user switches color-blind preset,
 * applyPreset() must:
 *   1. Write 5 ramp stops from preset.routeRamp (or fallback viridis).
 *   2. Fire a cb-preset-changed CustomEvent so route-view.js recolorRoute
 *      can walk .mc-rt-edge / .mc-rt-row / .mc-rt-spark-dot live.
 *
 * Pattern mirrors test-issue-1407-cb-preset-propagation.js sandbox shape.
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
const routeSrc   = fs.readFileSync(path.join(__dirname, 'public', 'route-view.js'), 'utf8');

console.log('\n=== #1418 ramp A: route-view reads --mc-rt-ramp-* CSS vars ===');
assert(/--mc-rt-ramp-/.test(routeSrc),
  'route-view.js references --mc-rt-ramp-* CSS vars');
assert(/cb-preset-changed/.test(routeSrc),
  'route-view.js listens for cb-preset-changed event');
// Selectors recolorRoute touches
assert(/mc-rt-edge|mc-rt-spark-dot|mc-rt-row/.test(routeSrc),
  'recolorRoute touches mc-rt-edge / mc-rt-spark-dot / mc-rt-row classes');

console.log('\n=== #1418 ramp B: cb-presets writes ramp stops ===');
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
    removeAttribute(k) { delete this._attrs[k]; }
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
      documentElement: root, body: body, readyState: 'complete',
      getElementById() { return null; },
      createElement() {
        return { _children: [], style: {}, textContent: '', id: '',
                 setAttribute() {}, appendChild(c) { this._children.push(c); } };
      },
      head: { appendChild() {} },
      addEventListener() {}
    },
    localStorage: storage,
    console: console,
    setTimeout: setTimeout,
    clearTimeout: clearTimeout,
    fetch: function () { return Promise.resolve({ ok: false }); },
    matchMedia: function () { return { matches: false, addEventListener() {}, addListener() {} }; },
    addEventListener(ev, cb) { (listeners[ev] = listeners[ev] || []).push(cb); },
    dispatchEvent(ev) { (listeners[ev.type] || []).forEach(function (cb) { cb(ev); }); return true; },
    CustomEvent: function (type, opts) { this.type = type; this.detail = opts && opts.detail; },
    Event: function (type) { this.type = type; },
    getComputedStyle: function () {
      return { getPropertyValue: function (k) { return root.style._vars[k] || ''; } };
    }
  };
  sandbox.window = sandbox;
  return { sandbox, root, body, storage, listeners };
}

let env;
try {
  env = makeSandbox();
  vm.createContext(env.sandbox);
  vm.runInContext(rolesSrc, env.sandbox);
  vm.runInContext(presetsSrc, env.sandbox);
} catch (e) {
  assert(false, 'sandbox load failed: ' + e.message);
}

const MCP = env && env.sandbox.window.MeshCorePresets;
assert(!!MCP, 'MeshCorePresets exported');

if (MCP) {
  console.log('\n  --- ramp-stop count for every preset ---');
  ['default', 'deut', 'prot', 'trit', 'achromat'].forEach(function (id) {
    MCP.applyPreset(id);
    let stopsSet = 0;
    for (let i = 0; i < 5; i++) {
      const v = env.root.style.getPropertyValue('--mc-rt-ramp-' + i);
      if (/^#[0-9a-f]{6}$/i.test(v)) stopsSet++;
    }
    assert(stopsSet === 5,
      'preset "' + id + '" sets all 5 ramp stops (--mc-rt-ramp-0..4) — got ' + stopsSet);
  });

  console.log('\n  --- preset routeRamp values land in CSS vars ---');
  MCP.applyPreset('default');
  const preset0 = MCP.list.find(p => p.id === 'default');
  for (let i = 0; i < 5; i++) {
    const expected = preset0.routeRamp[i].toLowerCase();
    const actual = env.root.style.getPropertyValue('--mc-rt-ramp-' + i).toLowerCase();
    assert(actual === expected,
      'default --mc-rt-ramp-' + i + ' = ' + expected + ' (got ' + actual + ')');
  }

  console.log('\n  --- switching preset rewrites all 5 stops ---');
  MCP.applyPreset('deut');
  const deut = MCP.list.find(p => p.id === 'deut');
  let allRewritten = true;
  for (let i = 0; i < 5; i++) {
    const actual = env.root.style.getPropertyValue('--mc-rt-ramp-' + i).toLowerCase();
    if (actual !== deut.routeRamp[i].toLowerCase()) allRewritten = false;
  }
  assert(allRewritten, 'switching to deut overwrites every ramp stop');

  console.log('\n  --- achromat ramp is luminance (B/W) ---');
  MCP.applyPreset('achromat');
  const achr = MCP.list.find(p => p.id === 'achromat');
  // Achromat ramp is the gray luminance ramp per cb-presets.js line 170.
  const stop0 = env.root.style.getPropertyValue('--mc-rt-ramp-0').toLowerCase();
  const stop4 = env.root.style.getPropertyValue('--mc-rt-ramp-4').toLowerCase();
  assert(stop0 === '#222222', 'achromat ramp[0] === #222222 (got ' + stop0 + ')');
  assert(stop4 === '#eeeeee', 'achromat ramp[4] === #eeeeee (got ' + stop4 + ')');
}

console.log('\n=== #1418 ramp C: applyPreset fires cb-preset-changed event ===');
if (MCP) {
  let fired = false, detailId = null;
  env.sandbox.addEventListener('cb-preset-changed', function (ev) {
    fired = true;
    detailId = ev.detail && ev.detail.id;
  });
  MCP.applyPreset('prot');
  assert(fired === true, 'cb-preset-changed event fired on applyPreset()');
  assert(detailId === 'prot', 'event detail.id === applied preset id (got ' + detailId + ')');
}

console.log('\n=== Summary ===');
console.log('  passed: ' + passed);
console.log('  failed: ' + failed);
if (failed > 0) process.exit(1);

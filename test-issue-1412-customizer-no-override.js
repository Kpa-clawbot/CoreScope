/**
 * #1412 — customizer nodeColors must NOT auto-push server config into
 * ROLE_COLORS overrides, or it defeats CB-preset propagation.
 *
 * Bug (CDP-verified on staging): PR #1408 made window.ROLE_COLORS a live
 * getter that reads --mc-role-* CSS vars. cb-presets.applyPreset() writes
 * those vars, so consumers SHOULD see new colors. But customize-v2.js:553
 * runs early on every page load and pushes effectiveConfig.nodeColors
 * (server config, legacy April palette) into the override map, which the
 * getter prefers over CSS vars. Net effect: ROLE_COLORS is frozen on the
 * legacy palette forever; presets only update the CSS, not the JS.
 *
 * Fix: server-config nodeColors must only write --node-* CSS var (legacy
 * compat for anything still reading --node-*). It must NOT touch the
 * override map. User-chosen colors in the customizer continue to win via
 * setRoleColorOverride() (explicit, intentional override).
 *
 * Test strategy: extract the actual code block from customize-v2.js that
 * processes effectiveConfig.nodeColors, run it in a vm sandbox with a
 * legacy-palette config, apply preset "deut", assert ROLE_COLORS reflects
 * the preset (not the server config).
 *
 * Mutation guard: re-introducing the `window.ROLE_COLORS[role] = nc[role]`
 * write to customize-v2.js makes the first test fail.
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
const cv2Src     = fs.readFileSync(path.join(__dirname, 'public', 'customize-v2.js'), 'utf8');

// Browser-ish sandbox (CSS var setProperty/getPropertyValue).
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
  const sandbox = {
    window: null,
    document: {
      documentElement: root,
      body: body,
      readyState: 'complete',
      getElementById() { return null; },
      createElement() { return { style: {}, setAttribute() {}, appendChild() {} }; },
      head: { appendChild() {} },
      addEventListener() {},
    },
    console: console,
    setTimeout: setTimeout,
    clearTimeout: clearTimeout,
    addEventListener() {},
    dispatchEvent() { return true; },
    fetch: function () { return { then: function () { return { then: function () { return { catch: function () {} }; }, catch: function () {} }; } }; },
    matchMedia: function () { return { matches: false }; },
    CustomEvent: function (type, opts) { this.type = type; this.detail = opts && opts.detail; },
    Event: function (type) { this.type = type; },
    getComputedStyle: function () {
      return { getPropertyValue: function (k) { return (root.style._vars[k] || ''); } };
    }
  };
  sandbox.window = sandbox;
  return { sandbox, root, body };
}

// ─── Extract the two nodeColors-processing blocks from customize-v2.js. ───
// We want to execute the REAL source so reverting the fix breaks the test.
// Block 1: the effective-config apply path (≈ line 550).
// Block 2: the early-overrides apply path (≈ line 2146).
function extractBlock(src, anchor) {
  const idx = src.indexOf(anchor);
  if (idx === -1) throw new Error('anchor not found: ' + anchor);
  // Walk forward to the matching closing brace of the surrounding `if (nc) { ... }`.
  // Slice forward a generous window then balance braces from the first '{' after anchor.
  const start = src.indexOf('{', idx);
  if (start === -1) throw new Error('open brace not found after anchor');
  let depth = 0, end = -1;
  for (let i = start; i < src.length; i++) {
    if (src[i] === '{') depth++;
    else if (src[i] === '}') { depth--; if (depth === 0) { end = i; break; } }
  }
  if (end === -1) throw new Error('matching close brace not found');
  return src.slice(idx, end + 1);
}

// Block A — main effective-config push:  `var nc = effectiveConfig.nodeColors;`
const blockA = extractBlock(cv2Src, 'var nc = effectiveConfig.nodeColors;');
// Block B — early overrides:  `if (earlyOverrides.nodeColors) {`
const blockB = extractBlock(cv2Src, 'if (earlyOverrides.nodeColors) {');

console.log('\n=== #1412 A: server-config nodeColors does NOT clobber preset ROLE_COLORS ===');
{
  const env = makeSandbox();
  vm.createContext(env.sandbox);
  vm.runInContext(rolesSrc, env.sandbox);
  vm.runInContext(presetsSrc, env.sandbox);

  // Simulate user choosing the "deut" preset.
  env.sandbox.window.MeshCorePresets.applyPreset('deut');
  // CSS var should be IBM orange now.
  assert(env.root.style.getPropertyValue('--mc-role-repeater').toLowerCase() === '#fe6100',
    'precondition: --mc-role-repeater is #FE6100 after applyPreset("deut")');

  // Now simulate customize-v2 picking up the server config (legacy palette).
  const setupBlockA =
    'var root = document.documentElement.style;\n' +
    'var userOverrides = undefined;\n' +
    'var effectiveConfig = { nodeColors: { repeater: "#dc2626", companion: "#2563eb", room: "#16a34a", sensor: "#d97706", observer: "#8b5cf6" } };\n' +
    blockA + '\n';
  vm.runInContext(setupBlockA, env.sandbox);

  // The --node-* CSS vars should still be written for legacy consumers.
  assert(env.root.style.getPropertyValue('--node-repeater') === '#dc2626',
    '--node-repeater CSS var is still written (legacy compat preserved)');

  // The KEY assertion: ROLE_COLORS must still reflect the preset, NOT the
  // server-config legacy palette.
  const got = String(env.sandbox.window.ROLE_COLORS.repeater).toLowerCase();
  assert(got === '#fe6100',
    'ROLE_COLORS.repeater === #FE6100 after server-config push (got ' + got + ')');

  const gotCompanion = String(env.sandbox.window.ROLE_COLORS.companion).toLowerCase();
  assert(gotCompanion !== '#2563eb',
    'ROLE_COLORS.companion is NOT the server-config legacy #2563eb (got ' + gotCompanion + ')');
}

console.log('\n=== #1412 B: early-overrides path also stays out of ROLE_COLORS override map ===');
{
  const env = makeSandbox();
  vm.createContext(env.sandbox);
  vm.runInContext(rolesSrc, env.sandbox);
  vm.runInContext(presetsSrc, env.sandbox);
  env.sandbox.window.MeshCorePresets.applyPreset('deut');

  const setupBlockB =
    'var root = document.documentElement.style;\n' +
    'var earlyOverrides = { nodeColors: { repeater: "#dc2626", companion: "#2563eb" } };\n' +
    blockB + '\n';
  // earlyOverrides path also writes --node-* and (per fix) only --node-*.
  // The extracted block may not write --node-* — that's fine; we only care
  // it does NOT push into the override map.
  try { vm.runInContext(setupBlockB, env.sandbox); }
  catch (e) { /* if the block touches APIs we didn't stub, ignore — the
                  override-map assertion below is what matters */ }

  const got = String(env.sandbox.window.ROLE_COLORS.repeater).toLowerCase();
  assert(got === '#fe6100',
    'ROLE_COLORS.repeater === #FE6100 after early-overrides push (got ' + got + ')');
}

console.log('\n=== #1412 C: explicit setRoleColorOverride() still wins (user customizer pick) ===');
{
  const env = makeSandbox();
  vm.createContext(env.sandbox);
  vm.runInContext(rolesSrc, env.sandbox);
  vm.runInContext(presetsSrc, env.sandbox);
  env.sandbox.window.MeshCorePresets.applyPreset('deut');

  // User manually picks a node color in the customizer.
  env.sandbox.window.setRoleColorOverride('repeater', '#ff00ff');
  const got = String(env.sandbox.window.ROLE_COLORS.repeater).toLowerCase();
  assert(got === '#ff00ff',
    'after setRoleColorOverride("repeater","#ff00ff") ROLE_COLORS.repeater === #ff00ff (got ' + got + ')');

  // Clearing the override lets the preset show through again.
  env.sandbox.window.setRoleColorOverride('repeater', '');
  const got2 = String(env.sandbox.window.ROLE_COLORS.repeater).toLowerCase();
  assert(got2 === '#fe6100',
    'after clearing override, ROLE_COLORS.repeater reverts to preset #FE6100 (got ' + got2 + ')');
}

console.log('\n=== #1412 D: customize.js per-key node-color picker uses setRoleColorOverride ===');
{
  // Static guard: the legacy customizer (customize.js) handlers for the node
  // color pickers must call setRoleColorOverride(key, value) — NOT mutate
  // ROLE_COLORS directly. The proxy-on-read trick in roles.js handles direct
  // assignment, but going through the explicit API keeps semantics obvious
  // and lets us delete the proxy layer later.
  const customizeSrc = fs.readFileSync(path.join(__dirname, 'public', 'customize.js'), 'utf8');

  // Grep for the two affected handlers (data-node input handler + reset).
  // Locate the input[data-node] handler — slice forward through the inner forEach callback.
  const nodeInputStart = customizeSrc.indexOf("querySelectorAll('input[data-node]')");
  const nodeInputHandler = nodeInputStart >= 0 ? [customizeSrc.slice(nodeInputStart, nodeInputStart + 800)] : null;
  assert(nodeInputHandler, 'node color input handler block found in customize.js');
  if (nodeInputHandler) {
    assert(/setRoleColorOverride\s*\(/.test(nodeInputHandler[0]),
      'node color input handler calls setRoleColorOverride()');
    assert(!/window\.ROLE_COLORS\s*\[[^\]]+\]\s*=/.test(nodeInputHandler[0]),
      'node color input handler does NOT assign window.ROLE_COLORS[key] = … directly');
  }

  const nodeResetStart = customizeSrc.indexOf("querySelectorAll('[data-reset-node]')");
  const nodeResetHandler = nodeResetStart >= 0 ? [customizeSrc.slice(nodeResetStart, nodeResetStart + 800)] : null;
  assert(nodeResetHandler, 'node color reset handler block found in customize.js');
  if (nodeResetHandler) {
    assert(/setRoleColorOverride\s*\(/.test(nodeResetHandler[0]),
      'node color reset handler calls setRoleColorOverride()');
    assert(!/window\.ROLE_COLORS\[/.test(nodeResetHandler[0]),
      'node color reset handler does NOT write window.ROLE_COLORS[key] directly');
  }
}

console.log('\n=== Summary ===');
console.log('  passed: ' + passed);
console.log('  failed: ' + failed);
if (failed > 0) process.exit(1);

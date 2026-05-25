/**
 * #1354 — Restore thin per-role marker outline convention.
 *
 * #1334 introduced hardcoded stroke="#fff" stroke-width="2" on every SVG
 * shape in makeRoleMarkerSVG, which reads as a heavy white-bordered
 * candy on the live & static map. Operators reported it as too thick.
 *
 * Pre-#1334 prod uses per-role L.circleMarker convention:
 *   - repeater: stroke-width 1.5, stroke-opacity 0.6
 *   - everything else: stroke-width 0.5, stroke-opacity 0.3
 *   - stroke color #fff (unchanged)
 *
 * This test exercises window.makeRoleMarkerSVG (loaded via roles.js)
 * and asserts the per-role stroke attributes appear in the emitted SVG.
 * It also asserts a window.ROLE_STROKE map is exposed as a single source
 * of truth (mirrors the ROLE_SHAPES pattern from #1293).
 *
 * Pure-string assertions — runs in the JS unit-tests CI step alongside
 * the #1293 test.
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

// Load roles.js into a sandbox that mimics the browser-global
// `window` object so the IIFE's `window.X = ...` assignments stick.
const rolesSrc = fs.readFileSync(path.join(__dirname, 'public', 'roles.js'), 'utf8');
const sandbox = {
  window: {},
  document: {
    readyState: 'complete',
    addEventListener: () => {},
    getElementById: () => null,
    createElement: () => ({ style: {}, appendChild: () => {} }),
    head: { appendChild: () => {} },
    documentElement: { getAttribute: () => null }
  },
  fetch: () => Promise.reject(new Error('no fetch in unit test')),
  matchMedia: () => ({ matches: false }),
  navigator: {}
};
sandbox.window.matchMedia = sandbox.matchMedia;
vm.createContext(sandbox);
try {
  vm.runInContext(rolesSrc, sandbox, { filename: 'public/roles.js' });
} catch (e) {
  // fetch rejection is swallowed by .catch; other errors fail loudly
  console.error('roles.js threw during load:', e.message);
  process.exit(1);
}

const win = sandbox.window;

console.log('\n=== #1354: window.ROLE_STROKE single source of truth ===');

assert(typeof win.ROLE_STROKE === 'object' && win.ROLE_STROKE !== null,
  'window.ROLE_STROKE is exposed');
assert(win.ROLE_STROKE && win.ROLE_STROKE.repeater &&
       win.ROLE_STROKE.repeater.width === 1.5 &&
       win.ROLE_STROKE.repeater.opacity === 0.6,
  'ROLE_STROKE.repeater = { width: 1.5, opacity: 0.6 }');
assert(win.ROLE_STROKE && win.ROLE_STROKE._default &&
       win.ROLE_STROKE._default.width === 0.5 &&
       win.ROLE_STROKE._default.opacity === 0.3,
  'ROLE_STROKE._default = { width: 0.5, opacity: 0.3 }');

console.log('\n=== #1354: makeRoleMarkerSVG emits per-role stroke attrs ===');

assert(typeof win.makeRoleMarkerSVG === 'function',
  'window.makeRoleMarkerSVG is a function');

const repSvg = win.makeRoleMarkerSVG('repeater', '#ff0000', 8);
assert(/stroke="#fff"/.test(repSvg),
  'repeater SVG keeps stroke="#fff"');
assert(/stroke-width="1\.5"/.test(repSvg),
  'repeater SVG has stroke-width="1.5"');
assert(/stroke-opacity="0\.6"/.test(repSvg),
  'repeater SVG has stroke-opacity="0.6"');
assert(!/stroke-width="2"/.test(repSvg),
  'repeater SVG NO LONGER has hardcoded stroke-width="2"');

for (const role of ['companion', 'room', 'sensor', 'observer']) {
  const svg = win.makeRoleMarkerSVG(role, '#ff0000', 8);
  assert(/stroke="#fff"/.test(svg),
    role + ' SVG keeps stroke="#fff"');
  assert(/stroke-width="0\.5"/.test(svg),
    role + ' SVG has stroke-width="0.5"');
  assert(/stroke-opacity="0\.3"/.test(svg),
    role + ' SVG has stroke-opacity="0.3"');
  assert(!/stroke-width="2"/.test(svg),
    role + ' SVG NO LONGER has hardcoded stroke-width="2"');
}

// Unknown / unmapped role falls back to _default values
const unkSvg = win.makeRoleMarkerSVG('made-up-role', '#ff0000', 8);
assert(/stroke-width="0\.5"/.test(unkSvg) && /stroke-opacity="0\.3"/.test(unkSvg),
  'unmapped role falls back to _default stroke (0.5 / 0.3)');

console.log('\n=== Summary ===');
console.log(`  Passed: ${passed}`);
console.log(`  Failed: ${failed}`);
if (failed > 0) { console.error('\n#1354 FAIL'); process.exit(1); }
console.log('\n#1354 PASS');

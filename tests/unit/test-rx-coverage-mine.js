'use strict';
const REPO_ROOT = require('path').resolve(__dirname, '..', '..');
// "My coverage" toggle (docs/specs/2026-10-08-companion-linking-design.md,
// Coverage attribution): shown to logged-in users only, it adds mine=1 to the
// signal-layer request. The real public/rx-coverage.js is loaded in a vm, with
// a window that has no addEventListener, like test-rx-coverage-config-race.js.
const assert = require('assert');
const fs = require('fs');
const path = require('path');
const vm = require('vm');

const code = fs.readFileSync(path.join(REPO_ROOT, 'public', 'rx-coverage.js'), 'utf8');
const sandbox = {
  window: {},
  document: { getElementById: function () { return null; } },
  registerPage: function () {},
  console: { warn: function () {} },
  Promise: Promise,
  setTimeout: function () {}, clearTimeout: function () {},
  fetch: function () { throw new Error('no fetch expected'); },
  L: {}, getComputedStyle: function () { return { getPropertyValue: function () { return ''; } }; },
};
vm.createContext(sandbox);
vm.runInContext(code, sandbox);
const t = sandbox.window.CSRxCoverage && sandbox.window.CSRxCoverage._test;
assert.ok(t, 'rx-coverage.js should expose window.CSRxCoverage._test');

// The request URL: mine=1 only with the toggle on, after the rx filter.
assert.strictEqual(t.coverageUrl('1,2,3,4', 9, 7, '', false), '/api/rx-coverage?bbox=1,2,3,4&z=9&days=7');
assert.strictEqual(t.coverageUrl('1,2,3,4', 9, 7, '', true), '/api/rx-coverage?bbox=1,2,3,4&z=9&days=7&mine=1');
assert.strictEqual(t.coverageUrl('1,2,3,4', 9, 30, 'ab cd', true), '/api/rx-coverage?bbox=1,2,3,4&z=9&days=30&rx=ab%20cd&mine=1');

// The toggle: nothing for a logged-out visitor, a pressed/unpressed button otherwise.
assert.strictEqual(t.mineBtnHtml(null, false), '', 'no toggle for a logged-out visitor');
const off = t.mineBtnHtml({ id: 1 }, false);
assert.ok(/data-mine="1"/.test(off) && /aria-pressed="false"/.test(off) && />My coverage</.test(off) && !/class="active"/.test(off), off);
const on = t.mineBtnHtml({ id: 1 }, true);
assert.ok(/aria-pressed="true"/.test(on) && /class="active"/.test(on), on);

// Only a logged-in user of an instance with accounts enabled counts.
const u = { id: 1 };
assert.strictEqual(t.authUser(undefined), null);
assert.strictEqual(t.authUser({ isEnabled: function () { return false; }, user: function () { return u; } }), null);
assert.strictEqual(t.authUser({ isEnabled: function () { return true; }, user: function () { return null; } }), null);
assert.strictEqual(t.authUser({ isEnabled: function () { return true; }, user: function () { return u; } }), u);

// Wiring: the signal layer builds its URL with the toggle, and the bar starts hidden.
assert.ok(code.indexOf('coverageUrl(bbox, map.getZoom(), days, selectedRx, mine && !!authUser(window.CSAuth))') !== -1,
  'drawSignalLayer must build its URL with coverageUrl and the toggle');
assert.ok(/id="rxMineBar"[^>]*hidden/.test(code), 'the toggle bar must start hidden');

console.log('rx-coverage My coverage OK');

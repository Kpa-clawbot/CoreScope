'use strict';
// Unit test for #13: a direct land on #/rx-coverage must not read the coverage
// feature flag before MeshConfigReady resolves. init() should defer its
// enabled/disabled decision until the config promise settles, instead of
// synchronously rendering "not enabled" when the flag is still undefined.
const assert = require('assert');
const fs = require('fs');
const path = require('path');
const vm = require('vm');

const code = fs.readFileSync(path.join(__dirname, 'public', 'rx-coverage.js'), 'utf8');

let page = null;
let resolveConfig;
const sandbox = {
  window: { MeshConfigReady: new Promise(function (r) { resolveConfig = r; }) }, // MC_CLIENT_RX_COVERAGE undefined for now
  document: { getElementById: function () { return null; } },
  registerPage: function (name, obj) { page = obj; },
  console: { warn: function () {} },
  Promise: Promise,
  setTimeout: function () {}, clearTimeout: function () {},
  fetch: function () { var c = { then: function () { return c; }, catch: function () { return c; } }; return c; },
  L: {}, getComputedStyle: function () { return { getPropertyValue: function () { return ''; } }; },
};
vm.createContext(sandbox);
vm.runInContext(code, sandbox);
assert.ok(page && typeof page.init === 'function', 'registerPage should expose init');

(async function () {
  const container = { innerHTML: '' };
  page.init(container);
  // Before MeshConfigReady resolves, init must NOT have decided yet. The old
  // code read the (undefined) flag synchronously and rendered "not enabled".
  assert.strictEqual(container.innerHTML, '', 'init must defer until MeshConfigReady resolves');

  // Resolve config with the feature OFF → only now should it render not-enabled.
  sandbox.window.MC_CLIENT_RX_COVERAGE = false;
  resolveConfig();
  await new Promise(function (r) { setTimeout(r, 10); });
  assert.ok(/not enabled/i.test(container.innerHTML), 'after config (off) resolves, shows not-enabled');

  console.log('rx-coverage config race OK');
})().catch(function (e) { console.error(e); process.exit(1); });

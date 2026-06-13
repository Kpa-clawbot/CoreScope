/**
 * test-a11y-axe-1668-selftest.js
 *
 * Deterministic, browser-free unit test for the M5 axe gate's
 * allowlist parser and shape. Runs in <100ms on any Node host.
 *
 * The full axe browser run (test-a11y-axe-1668.js) executes in the
 * Playwright E2E block after the fixture server + chromium are up.
 * THIS file guards the gate's metadata: route list, theme list,
 * allowlist parser, and the "expires_at refuses suppression" policy.
 */

'use strict';

const assert = require('assert');
const fs = require('fs');
const path = require('path');

const mod = require('./test-a11y-axe-1668.js');

// ---- routes / themes --------------------------------------------------------
assert.ok(Array.isArray(mod.ROUTES), 'ROUTES must be an array');
assert.ok(mod.ROUTES.length >= 18, `ROUTES too small: ${mod.ROUTES.length}`);
assert.deepStrictEqual(mod.THEMES, ['dark', 'light'], 'THEMES must be [dark,light]');

// Spot-check key routes from the M1 audit baseline
for (const r of ['/', '/packets', '/nodes', '/live', '/map', '/analytics?tab=collisions', '/audio-lab']) {
  assert.ok(mod.ROUTES.includes(r), `ROUTES missing ${r}`);
}

// ---- parser: empty + flow ---------------------------------------------------
assert.deepStrictEqual(mod.parseAllowlistYaml('[]'), [], 'empty flow list');
assert.deepStrictEqual(mod.parseAllowlistYaml(''),   [], 'empty string');
assert.deepStrictEqual(mod.parseAllowlistYaml('# only a comment\n'), [], 'comment-only');

// ---- parser: block list with two entries ------------------------------------
const sample = `
- route: /analytics?tab=channels
  selector: ".some-stale"
  rule: color-contrast
  issue: 1234
  expires_at: 2099-01-01
- route: /packets
  selector: .badge-quirk
  rule: color-contrast
  issue: 5678
  expires_at: 2099-06-01
`;
const parsed = mod.parseAllowlistYaml(sample);
assert.strictEqual(parsed.length, 2, 'parsed two entries');
assert.strictEqual(parsed[0].route, '/analytics?tab=channels');
assert.strictEqual(parsed[0].issue, 1234);
assert.strictEqual(parsed[0].selector, '.some-stale');
assert.strictEqual(parsed[1].route, '/packets');
assert.strictEqual(parsed[1].issue, 5678);

// ---- violationAllowed: simple match + miss ----------------------------------
const al = [{ route: '/a', rule: 'color-contrast', selector: '.x' }];
assert.strictEqual(
  mod.violationAllowed('/a', 'color-contrast', { target: ['.x'] }, al),
  true,
  'exact match should suppress'
);
assert.strictEqual(
  mod.violationAllowed('/b', 'color-contrast', { target: ['.x'] }, al),
  false,
  'route mismatch must not suppress'
);
assert.strictEqual(
  mod.violationAllowed('/a', 'color-contrast', { target: ['.y'] }, al),
  false,
  'selector mismatch must not suppress'
);
assert.strictEqual(
  mod.violationAllowed('/a', 'image-alt', { target: ['.x'] }, al),
  false,
  'rule mismatch must not suppress'
);

// ---- repo allowlist file: shape sanity --------------------------------------
const allowPath = path.join(__dirname, 'tests', 'a11y-allowlist.yaml');
assert.ok(fs.existsSync(allowPath), `tests/a11y-allowlist.yaml missing at ${allowPath}`);
const entries = mod.loadAllowlist();
for (const e of entries) {
  assert.ok(e.route && e.selector && e.rule && e.issue && e.expires_at,
    `allowlist entry missing required field: ${JSON.stringify(e)}`);
}
console.log(`PASS: a11y-axe-1668 selftest — routes=${mod.ROUTES.length} themes=${mod.THEMES.length} allowlist=${entries.length}`);

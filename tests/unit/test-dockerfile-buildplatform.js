/* test-dockerfile-buildplatform.js — the build stage runs on the building
 * machine's own architecture.
 *
 * BuildKit sets BUILDPLATFORM to the host doing the build. Giving the global
 * ARG a default (it used to say linux/amd64) overrides that, so a native build
 * on an arm64 host — a Raspberry Pi, Apple silicon — pulled the amd64 Go image
 * and failed at the first RUN with "exec format error". CI never saw it: its
 * arm64 image is cross-built on an amd64 runner, where the default happened to
 * be right.
 */
'use strict';
const fs = require('fs');
const path = require('path');
const assert = require('assert');

const dockerfile = fs.readFileSync(path.join(__dirname, '..', '..', 'Dockerfile'), 'utf8');
const lines = dockerfile.split('\n').map((l) => l.trim()).filter((l) => l && !l.startsWith('#'));

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log(`  ✅ ${name}`); }
  catch (e) { failed++; console.log(`  ❌ ${name}: ${e.message}`); }
}

console.log('\n=== Dockerfile: build platform ===');

test('BUILDPLATFORM is never given a default', () => {
  const withDefault = lines.filter((l) => /^ARG\s+BUILDPLATFORM\s*=/.test(l));
  assert.deepStrictEqual(withDefault, [], 'found: ' + withDefault.join(' | '));
});

test('the Go builder stage is pinned to BUILDPLATFORM (native, no QEMU)', () => {
  const from = lines.find((l) => /^FROM\b.*golang:.*\bAS\s+builder\b/i.test(l));
  assert.ok(from, 'builder FROM line not found');
  assert.ok(/--platform=\$BUILDPLATFORM\b/.test(from), from);
});

test('no stage hard-codes a platform', () => {
  const pinned = lines.filter((l) => /^FROM\b/.test(l) && /--platform=(?!\$)/.test(l));
  assert.deepStrictEqual(pinned, [], 'found: ' + pinned.join(' | '));
});

console.log(`\n${passed} passed, ${failed} failed`);
if (failed) process.exit(1);

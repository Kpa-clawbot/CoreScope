'use strict';
const REPO_ROOT = require('path').resolve(__dirname, '..', '..');
// The RF noise layer colours a hex by its median noise floor. The axis runs the
// opposite way to the SNR layer: a LOWER (more negative) dBm is quieter, so it
// must map to the "strong" (green) token. Getting the comparison direction
// wrong paints the quietest cells red, which is the mistake this pins.
//
// Like test-rx-coverage-escape.js, the real thresholds and noiseColorVar are
// sliced out of public/rx-coverage.js and run in a vm sandbox, so the test
// tracks the source rather than a copy.
const assert = require('assert');
const fs = require('fs');
const path = require('path');
const vm = require('vm');

const src = fs.readFileSync(path.join(REPO_ROOT, 'public', 'rx-coverage.js'), 'utf8');

const startMarker = 'var NOISE_QUIET_MAX';
const fnMarker = 'function noiseColorVar(p) {';
const startIdx = src.indexOf(startMarker);
assert.ok(startIdx >= 0, 'could not locate NOISE_QUIET_MAX in rx-coverage.js');
const fnIdx = src.indexOf(fnMarker, startIdx);
assert.ok(fnIdx >= 0, 'could not locate noiseColorVar in rx-coverage.js');
const endIdx = src.indexOf('\n  }\n', fnIdx);
assert.ok(endIdx >= 0, 'could not locate the end of noiseColorVar');
const block = src.slice(startIdx, endIdx + 4);

const sandbox = {};
vm.createContext(sandbox);
vm.runInContext(block + '\nthis.noiseColorVar = noiseColorVar; this.QUIET = NOISE_QUIET_MAX; this.BUSY = NOISE_BUSY_MIN;', sandbox);
const colour = (dbm) => sandbox.noiseColorVar({ median_noise_floor: dbm });

let passed = 0;
function check(name, fn) { fn(); passed++; console.log('  ✓ ' + name); }

check('thresholds are ordered quiet < busy', () => {
  assert.ok(sandbox.QUIET < sandbox.BUSY, `quiet max ${sandbox.QUIET} must be below busy min ${sandbox.BUSY}`);
});
check('a quiet floor is the strong (green) token', () => {
  assert.strictEqual(colour(-125), '--nq-cov-strong');
  assert.strictEqual(colour(sandbox.QUIET), '--nq-cov-strong', 'the quiet bound itself is quiet');
});
check('between the bounds is mid', () => {
  assert.strictEqual(colour(sandbox.QUIET + 1), '--nq-cov-mid');
  assert.strictEqual(colour(sandbox.BUSY), '--nq-cov-mid', 'the busy bound itself is still mid');
});
check('a busy floor is the weak (red) token', () => {
  assert.strictEqual(colour(sandbox.BUSY + 1), '--nq-cov-weak');
  assert.strictEqual(colour(-80), '--nq-cov-weak');
});
check('a string median from the API is cast, not compared as text', () => {
  assert.strictEqual(colour('-120'), '--nq-cov-strong');
  assert.strictEqual(colour('-90'), '--nq-cov-weak');
});

console.log(`rx-coverage noise colours: ${passed} passed, 0 failed`);

// test-path-inspector.js — vm.createContext sandbox tests for path-inspector.js
'use strict';
const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

const src = fs.readFileSync(__dirname + '/public/path-inspector.js', 'utf8');

function createSandbox() {
  const sandbox = {
    window: {},
    document: {
      getElementById: () => ({ textContent: '', innerHTML: '', addEventListener: () => {}, querySelectorAll: () => [] }),
      querySelectorAll: () => []
    },
    location: { hash: '#/tools/path-inspector' },
    history: { replaceState: () => {} },
    fetch: () => Promise.resolve({ ok: true, json: () => Promise.resolve({ candidates: [] }) }),
    URLSearchParams: URLSearchParams,
    registerPage: function () {},
    escapeHtml: s => s,
    console: console
  };
  sandbox.self = sandbox;
  sandbox.globalThis = sandbox;
  const ctx = vm.createContext(sandbox);
  vm.runInContext(src, ctx);
  return sandbox;
}

// Test: parsePrefixes accepts comma-separated.
(function testParseComma() {
  const sb = createSandbox();
  const result = sb.window.PathInspector.parsePrefixes('2C,A1,F4');
  assert.strictEqual(JSON.stringify(result), JSON.stringify(['2c', 'a1', 'f4']));
  console.log('✓ parsePrefixes comma-separated');
})();

// Test: parsePrefixes accepts space-separated.
(function testParseSpace() {
  const sb = createSandbox();
  const result = sb.window.PathInspector.parsePrefixes('2C A1 F4');
  assert.strictEqual(JSON.stringify(result), JSON.stringify(['2c', 'a1', 'f4']));
  console.log('✓ parsePrefixes space-separated');
})();

// Test: parsePrefixes accepts mixed.
(function testParseMixed() {
  const sb = createSandbox();
  const result = sb.window.PathInspector.parsePrefixes(' 2C, A1  F4 ');
  assert.strictEqual(JSON.stringify(result), JSON.stringify(['2c', 'a1', 'f4']));
  console.log('✓ parsePrefixes mixed separators');
})();

// Test: validatePrefixes rejects empty.
(function testValidateEmpty() {
  const sb = createSandbox();
  const err = sb.window.PathInspector.validatePrefixes([]);
  assert.ok(err !== null, 'should reject empty');
  console.log('✓ validatePrefixes rejects empty');
})();

// Test: validatePrefixes rejects odd-length.
(function testValidateOdd() {
  const sb = createSandbox();
  const err = sb.window.PathInspector.validatePrefixes(['abc']);
  assert.ok(err !== null && err.includes('Odd'), 'should reject odd-length');
  console.log('✓ validatePrefixes rejects odd-length');
})();

// Test: validatePrefixes rejects >3 bytes.
(function testValidateTooLong() {
  const sb = createSandbox();
  const err = sb.window.PathInspector.validatePrefixes(['aabbccdd']);
  assert.ok(err !== null && err.includes('too long'), 'should reject >3 bytes');
  console.log('✓ validatePrefixes rejects >3 bytes');
})();

// Test: validatePrefixes rejects mixed lengths.
(function testValidateMixed() {
  const sb = createSandbox();
  const err = sb.window.PathInspector.validatePrefixes(['aa', 'bbcc']);
  assert.ok(err !== null && err.includes('Mixed'), 'should reject mixed');
  console.log('✓ validatePrefixes rejects mixed lengths');
})();

// Test: validatePrefixes accepts valid input.
(function testValidateValid() {
  const sb = createSandbox();
  const err = sb.window.PathInspector.validatePrefixes(['2c', 'a1', 'f4']);
  assert.strictEqual(err, null);
  console.log('✓ validatePrefixes accepts valid');
})();

// Test: validatePrefixes rejects invalid hex.
(function testValidateInvalidHex() {
  const sb = createSandbox();
  const err = sb.window.PathInspector.validatePrefixes(['zz']);
  assert.ok(err !== null && err.includes('Invalid hex'), 'should reject invalid hex');
  console.log('✓ validatePrefixes rejects invalid hex');
})();

// Anti-tautology: if validation were removed (always return null), the odd-length test would fail.
// Mental revert: validatePrefixes = () => null; → testValidateOdd would fail because err would be null.

console.log('\nAll path-inspector tests passed!');

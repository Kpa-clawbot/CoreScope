/**
 * Tests for AES-128-ECB decryption in public/channel-decrypt.js.
 *
 * Background: the original implementation simulated ECB via Web Crypto
 * AES-CBC with a zero IV and a dummy PKCS7 padding block. Web Crypto
 * validates PKCS7 padding on the decrypted output and throws an
 * `OperationError` whenever the last 16 bytes of the (CBC-decrypted)
 * output don't form a valid PKCS7 padding sequence — which is the
 * common case here, since the input is real ciphertext, not a padded
 * second block. This test pins decryptECB() to the FIPS-197 NIST
 * AES-128-ECB known-answer vector (Appendix B / C.1) so that the
 * implementation cannot regress to any Web Crypto + ECB hack.
 *
 * Vector (FIPS-197 Appendix C.1, single-block AES-128 ECB):
 *   key        = 000102030405060708090a0b0c0d0e0f
 *   plaintext  = 00112233445566778899aabbccddeeff
 *   ciphertext = 69c4e0d86a7b0430d8cdb78070b4c55a
 */
'use strict';

const vm = require('vm');
const fs = require('fs');
const path = require('path');
const { subtle } = require('crypto').webcrypto;

let passed = 0;
let failed = 0;

function assert(cond, msg) {
  if (cond) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}

function loadChannelDecrypt() {
  const storage = {};
  const localStorage = {
    getItem: (k) => storage[k] !== undefined ? storage[k] : null,
    setItem: (k, v) => { storage[k] = String(v); },
    removeItem: (k) => { delete storage[k]; },
  };
  const sandbox = {
    window: {}, crypto: { subtle }, TextEncoder, TextDecoder, Uint8Array,
    localStorage, console, Date, JSON, parseInt, Math, String, Number,
    Object, Array, RegExp, Error, Promise, setTimeout,
  };
  sandbox.window = sandbox; sandbox.self = sandbox;
  vm.createContext(sandbox);

  // Load vendored AES (if present) before channel-decrypt.js.
  const vendorPath = path.join(__dirname, 'public/vendor/aes-ecb.js');
  if (fs.existsSync(vendorPath)) {
    vm.runInContext(fs.readFileSync(vendorPath, 'utf8'), sandbox);
  }
  vm.runInContext(
    fs.readFileSync(path.join(__dirname, 'public/channel-decrypt.js'), 'utf8'),
    sandbox
  );
  return sandbox.window.ChannelDecrypt;
}

async function runTests() {
  console.log('\n=== AES-128-ECB known-answer vector (FIPS-197 C.1) ===');

  const CD = loadChannelDecrypt();

  const key = CD.hexToBytes('000102030405060708090a0b0c0d0e0f');
  const ct  = CD.hexToBytes('69c4e0d86a7b0430d8cdb78070b4c55a');
  const expectedPlaintextHex = '00112233445566778899aabbccddeeff';

  let result, threw = null;
  try {
    result = await CD.decryptECB(key, ct);
  } catch (e) {
    threw = e;
  }

  assert(threw === null, 'decryptECB does not throw on valid ciphertext (got: ' + (threw && threw.message) + ')');
  assert(result instanceof Uint8Array, 'decryptECB returns a Uint8Array');
  assert(
    result && CD.bytesToHex(result) === expectedPlaintextHex,
    'decryptECB matches FIPS-197 vector (got ' + (result ? CD.bytesToHex(result) : 'null') + ')'
  );

  // Multi-block: two copies of the same block must produce two copies
  // of the same plaintext (true ECB property — no chaining).
  console.log('\n=== AES-128-ECB multi-block (no chaining) ===');
  const ct2 = new Uint8Array(32);
  ct2.set(ct, 0); ct2.set(ct, 16);
  let result2, threw2 = null;
  try { result2 = await CD.decryptECB(key, ct2); }
  catch (e) { threw2 = e; }
  assert(threw2 === null, 'decryptECB does not throw on 2-block ciphertext');
  assert(
    result2 &&
      CD.bytesToHex(result2.slice(0, 16)) === expectedPlaintextHex &&
      CD.bytesToHex(result2.slice(16, 32)) === expectedPlaintextHex,
    'decryptECB on duplicated block yields duplicated plaintext (ECB, no chaining)'
  );

  // Empty / misaligned input must return null (existing contract).
  console.log('\n=== Edge cases ===');
  const empty = await CD.decryptECB(key, new Uint8Array(0));
  assert(empty === null, 'empty ciphertext returns null');
  const misaligned = await CD.decryptECB(key, new Uint8Array(15));
  assert(misaligned === null, 'misaligned ciphertext returns null');

  console.log('\n=== Results ===');
  console.log('Passed: ' + passed + ', Failed: ' + failed);
  process.exit(failed > 0 ? 1 : 0);
}

runTests().catch(e => { console.error(e); process.exit(1); });

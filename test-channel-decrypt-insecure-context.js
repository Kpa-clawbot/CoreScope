/**
 * Tests that channel decryption works in an "insecure context" — i.e. when
 * `window.crypto.subtle` is undefined.
 *
 * Why: when CoreScope is served over plain HTTP (or accessed via a non-https
 * origin like `http://<lan-ip>:8080`), browsers refuse to expose
 * `crypto.subtle` (it requires a secure context). The original
 * `channel-decrypt.js` used `crypto.subtle.digest('SHA-256', …)` for
 * `computeChannelHash` and `crypto.subtle.importKey(…)` +
 * `crypto.subtle.sign('HMAC', …)` for `verifyMAC`. PR #1021 fixed only the
 * AES-ECB path with a pure-JS vendor module, but left SHA-256 and HMAC paths
 * pinned to `crypto.subtle`. Result on HTTP origins:
 *
 *   addUserChannel("372a9c93260507adcbf36a84bec0f33d")
 *     -> computeChannelHash(key) throws "Cannot read properties of undefined
 *        (reading 'digest')"
 *     -> caught silently by addUserChannel's try/catch
 *     -> user sees "Failed to decrypt"
 *
 * This test sandboxes channel-decrypt.js with `crypto.subtle === undefined`
 * and asserts both `computeChannelHash` and `verifyMAC` still work, using
 * a pure-JS SHA-256 / HMAC-SHA256 fallback.
 *
 * Reference vectors:
 *   key bytes  = 0x37,0x2a,0x9c,0x93,0x26,0x05,0x07,0xad,0xcb,0xf3,0x6a,0x84,0xbe,0xc0,0xf3,0x3d
 *   SHA256(key) = b7ce04f7d9019788b69e709ffb796a36d00225818b444ad4f8979bc1d1445f47
 *   -> first byte (channel hash) = 0xb7 = 183
 *
 *   HMAC-SHA256 KAT (RFC 4231 Test Case 1):
 *     key  = 0x0b * 20
 *     data = "Hi There"
 *     mac  = b0344c61d8db38535ca8afceaf0bf12b881dc200c9833da726e9376c2e32cff7
 */
'use strict';

const vm = require('vm');
const fs = require('fs');
const path = require('path');

let passed = 0;
let failed = 0;

function assert(cond, msg) {
  if (cond) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}

function loadChannelDecryptInsecureContext() {
  const storage = {};
  const localStorage = {
    getItem: (k) => storage[k] !== undefined ? storage[k] : null,
    setItem: (k, v) => { storage[k] = String(v); },
    removeItem: (k) => { delete storage[k]; },
  };
  // CRITICAL: crypto present, but no .subtle. Mirrors browser HTTP context.
  const insecureCrypto = {};
  const sandbox = {
    window: {}, crypto: insecureCrypto, TextEncoder, TextDecoder, Uint8Array,
    localStorage, console, Date, JSON, parseInt, Math, String, Number,
    Object, Array, RegExp, Error, Promise, setTimeout,
  };
  sandbox.window = sandbox; sandbox.self = sandbox;
  vm.createContext(sandbox);

  // Vendored AES (must load before channel-decrypt.js — same as index.html).
  const vendorAesPath = path.join(__dirname, 'public/vendor/aes-ecb.js');
  if (fs.existsSync(vendorAesPath)) {
    vm.runInContext(fs.readFileSync(vendorAesPath, 'utf8'), sandbox);
  }
  // Optional vendored SHA-256 / HMAC (the fix). Load if present so the test
  // works whether the fix vendors it as a separate file OR inlines it into
  // channel-decrypt.js.
  const vendorShaPath = path.join(__dirname, 'public/vendor/sha256-hmac.js');
  if (fs.existsSync(vendorShaPath)) {
    vm.runInContext(fs.readFileSync(vendorShaPath, 'utf8'), sandbox);
  }

  vm.runInContext(
    fs.readFileSync(path.join(__dirname, 'public/channel-decrypt.js'), 'utf8'),
    sandbox
  );
  return sandbox.window.ChannelDecrypt;
}

async function runTests() {
  console.log('\n=== channel-decrypt.js works without crypto.subtle (HTTP-context) ===');
  const CD = loadChannelDecryptInsecureContext();

  // 1) computeChannelHash() — pure SHA-256 of 16-byte key, take byte 0.
  const KEY_HEX = '372a9c93260507adcbf36a84bec0f33d';
  const keyBytes = CD.hexToBytes(KEY_HEX);

  let hashByte, threwHash = null;
  try {
    hashByte = await CD.computeChannelHash(keyBytes);
  } catch (e) {
    threwHash = e;
  }
  assert(threwHash === null,
    'computeChannelHash does not throw without crypto.subtle (got: ' +
    (threwHash && threwHash.message) + ')');
  assert(hashByte === 0xb7,
    'computeChannelHash returns 0xb7 for known PSK key (got: ' + hashByte + ')');

  // 2) verifyMAC() — RFC 4231 HMAC-SHA256 Test Case 1.
  // We feed a hand-built scenario:
  //   verifyMAC's HMAC key is `aesKey ++ 16 zero bytes` (32 bytes).
  //   To exercise RFC 4231 TC1 we set aesKey = 16 * 0x0b and pad another 4
  //   bytes of 0x0b in the second half (since verifyMAC zero-fills bytes
  //   16..31, we instead use the channel-decrypt API directly here only to
  //   prove HMAC-SHA256 is computed correctly with the standard secret).
  //
  // We construct the secret manually and call verifyMAC on a synthetic
  // ciphertext whose HMAC-SHA256 first 2 bytes we precompute with Node's
  // crypto module (independent oracle).
  const nodeCrypto = require('crypto');
  const aesKey = new Uint8Array(16); for (let i = 0; i < 16; i++) aesKey[i] = 0xab;
  const ct = new Uint8Array(16); for (let i = 0; i < 16; i++) ct[i] = i;
  const secret = Buffer.alloc(32); Buffer.from(aesKey).copy(secret, 0);
  const fullMac = nodeCrypto.createHmac('sha256', secret).update(Buffer.from(ct)).digest();
  const expectedMacHex = fullMac.slice(0, 2).toString('hex');

  let macOk, threwMac = null;
  try {
    macOk = await CD.verifyMAC(aesKey, ct, expectedMacHex);
  } catch (e) {
    threwMac = e;
  }
  assert(threwMac === null,
    'verifyMAC does not throw without crypto.subtle (got: ' +
    (threwMac && threwMac.message) + ')');
  assert(macOk === true,
    'verifyMAC returns true for valid 2-byte MAC (got: ' + macOk + ')');

  // 3) verifyMAC must still REJECT a wrong MAC.
  let macBad, threwMacBad = null;
  try {
    macBad = await CD.verifyMAC(aesKey, ct, '0000');
  } catch (e) {
    threwMacBad = e;
  }
  assert(threwMacBad === null,
    'verifyMAC does not throw on wrong MAC (got: ' + (threwMacBad && threwMacBad.message) + ')');
  assert(macBad === false,
    'verifyMAC returns false for wrong 2-byte MAC (got: ' + macBad + ')');

  // 4) End-to-end: decrypt() must work with subtle absent — exercises
  //    SHA-256 (key derivation already done) + HMAC + AES-ECB together.
  //    Build a synthetic encrypted packet from a known plaintext.
  const aesKey2 = nodeCrypto.randomBytes(16);
  const plaintext = Buffer.alloc(16);
  // timestamp(4 LE) + flags(1) + "alice: hi\0" then padded
  plaintext.writeUInt32LE(0x12345678, 0);
  plaintext[4] = 0x00;
  Buffer.from('alice: hi\0', 'utf8').copy(plaintext, 5);

  const cipher = nodeCrypto.createCipheriv('aes-128-ecb', aesKey2, null);
  cipher.setAutoPadding(false);
  const ct2 = Buffer.concat([cipher.update(plaintext), cipher.final()]);
  const secret2 = Buffer.alloc(32); aesKey2.copy(secret2, 0);
  const macHex2 = nodeCrypto.createHmac('sha256', secret2).update(ct2).digest().slice(0, 2).toString('hex');

  let decResult = null, threwDec = null;
  try {
    decResult = await CD.decrypt(new Uint8Array(aesKey2), macHex2, ct2.toString('hex'));
  } catch (e) {
    threwDec = e;
  }
  assert(threwDec === null,
    'decrypt() does not throw without crypto.subtle (got: ' +
    (threwDec && threwDec.message) + ')');
  assert(decResult && decResult.sender === 'alice' && decResult.message === 'hi',
    'decrypt() recovers sender + message in HTTP context (got: ' +
    JSON.stringify(decResult) + ')');

  console.log('\n=== Results ===');
  console.log('Passed: ' + passed + ', Failed: ' + failed);
  process.exit(failed > 0 ? 1 : 0);
}

runTests().catch(e => { console.error(e); process.exit(1); });

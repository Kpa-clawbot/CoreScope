/* SPDX-License-Identifier: MIT
 *
 * Minimal pure-JS SHA-256 + HMAC-SHA256.
 *
 * Why: Web Crypto's SubtleCrypto (`window.crypto.subtle`) is only exposed
 * in **secure contexts** (HTTPS or localhost). When CoreScope is served
 * over plain HTTP — common for self-hosted instances and LAN-side
 * deployments — `crypto.subtle` is undefined and any
 * `crypto.subtle.digest(...)` / `crypto.subtle.importKey(...)` call
 * throws `Cannot read properties of undefined`. PR #1021 fixed the
 * AES-ECB path for the same reason; this module does the same for the
 * SHA-256 / HMAC paths used by `computeChannelHash` and `verifyMAC`.
 *
 * Implementation: textbook FIPS-180-4 SHA-256 + RFC 2104 HMAC. Operates
 * on Uint8Array inputs; returns Uint8Array outputs. ~120 LOC, no deps.
 *
 * API:
 *   window.PureCrypto.sha256(bytes: Uint8Array) -> Uint8Array(32)
 *   window.PureCrypto.hmacSha256(key: Uint8Array, msg: Uint8Array) -> Uint8Array(32)
 */
/* eslint-disable no-var */
(function (root) {
  'use strict';

  // SHA-256 round constants (FIPS-180-4 §4.2.2).
  var K = new Uint32Array([
    0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
    0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
    0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
    0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
    0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
    0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
    0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
    0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2
  ]);

  function ror(x, n) { return (x >>> n) | (x << (32 - n)); }

  // Process a single 64-byte block, mutating `H` (8 × uint32 state).
  function processBlock(H, M) {
    var W = new Uint32Array(64);
    for (var i = 0; i < 16; i++) {
      W[i] = (M[i * 4] << 24) | (M[i * 4 + 1] << 16) | (M[i * 4 + 2] << 8) | M[i * 4 + 3];
    }
    for (var t = 16; t < 64; t++) {
      var s0 = ror(W[t - 15], 7) ^ ror(W[t - 15], 18) ^ (W[t - 15] >>> 3);
      var s1 = ror(W[t - 2], 17) ^ ror(W[t - 2], 19) ^ (W[t - 2] >>> 10);
      W[t] = (W[t - 16] + s0 + W[t - 7] + s1) >>> 0;
    }

    var a = H[0], b = H[1], c = H[2], d = H[3];
    var e = H[4], f = H[5], g = H[6], h = H[7];

    for (var j = 0; j < 64; j++) {
      var S1 = ror(e, 6) ^ ror(e, 11) ^ ror(e, 25);
      var ch = (e & f) ^ ((~e) & g);
      var temp1 = (h + S1 + ch + K[j] + W[j]) >>> 0;
      var S0 = ror(a, 2) ^ ror(a, 13) ^ ror(a, 22);
      var maj = (a & b) ^ (a & c) ^ (b & c);
      var temp2 = (S0 + maj) >>> 0;

      h = g; g = f; f = e;
      e = (d + temp1) >>> 0;
      d = c; c = b; b = a;
      a = (temp1 + temp2) >>> 0;
    }

    H[0] = (H[0] + a) >>> 0;
    H[1] = (H[1] + b) >>> 0;
    H[2] = (H[2] + c) >>> 0;
    H[3] = (H[3] + d) >>> 0;
    H[4] = (H[4] + e) >>> 0;
    H[5] = (H[5] + f) >>> 0;
    H[6] = (H[6] + g) >>> 0;
    H[7] = (H[7] + h) >>> 0;
  }

  function sha256(bytes) {
    if (!(bytes instanceof Uint8Array)) {
      throw new Error('sha256: input must be a Uint8Array');
    }
    var bitLen = bytes.length * 8;
    // Padding: 0x80 then zeros until length ≡ 56 (mod 64), then 8-byte big-endian bit-length.
    var padLen = ((bytes.length + 9 + 63) & ~63) - bytes.length;
    var padded = new Uint8Array(bytes.length + padLen);
    padded.set(bytes, 0);
    padded[bytes.length] = 0x80;
    // 64-bit big-endian bit length. JS bitwise ops are 32-bit, so split.
    var hi = Math.floor(bitLen / 0x100000000);
    var lo = bitLen >>> 0;
    var off = padded.length - 8;
    padded[off]     = (hi >>> 24) & 0xff;
    padded[off + 1] = (hi >>> 16) & 0xff;
    padded[off + 2] = (hi >>>  8) & 0xff;
    padded[off + 3] =  hi         & 0xff;
    padded[off + 4] = (lo >>> 24) & 0xff;
    padded[off + 5] = (lo >>> 16) & 0xff;
    padded[off + 6] = (lo >>>  8) & 0xff;
    padded[off + 7] =  lo         & 0xff;

    var H = new Uint32Array([
      0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a,
      0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19
    ]);

    for (var i = 0; i < padded.length; i += 64) {
      processBlock(H, padded.subarray(i, i + 64));
    }

    var out = new Uint8Array(32);
    for (var k = 0; k < 8; k++) {
      out[k * 4]     = (H[k] >>> 24) & 0xff;
      out[k * 4 + 1] = (H[k] >>> 16) & 0xff;
      out[k * 4 + 2] = (H[k] >>>  8) & 0xff;
      out[k * 4 + 3] =  H[k]         & 0xff;
    }
    return out;
  }

  // RFC 2104 HMAC.
  function hmacSha256(key, msg) {
    if (!(key instanceof Uint8Array) || !(msg instanceof Uint8Array)) {
      throw new Error('hmacSha256: key and msg must be Uint8Array');
    }
    var blockSize = 64;
    var k = key;
    if (k.length > blockSize) k = sha256(k);
    if (k.length < blockSize) {
      var padded = new Uint8Array(blockSize);
      padded.set(k, 0);
      k = padded;
    }
    var oKeyPad = new Uint8Array(blockSize);
    var iKeyPad = new Uint8Array(blockSize);
    for (var i = 0; i < blockSize; i++) {
      oKeyPad[i] = k[i] ^ 0x5c;
      iKeyPad[i] = k[i] ^ 0x36;
    }
    var inner = new Uint8Array(blockSize + msg.length);
    inner.set(iKeyPad, 0);
    inner.set(msg, blockSize);
    var innerHash = sha256(inner);
    var outer = new Uint8Array(blockSize + innerHash.length);
    outer.set(oKeyPad, 0);
    outer.set(innerHash, blockSize);
    return sha256(outer);
  }

  root.PureCrypto = { sha256: sha256, hmacSha256: hmacSha256 };
})(typeof window !== 'undefined' ? window
   : typeof self !== 'undefined' ? self
   : this);

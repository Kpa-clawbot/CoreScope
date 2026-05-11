/* SPDX-License-Identifier: MIT
 *
 * Minimal pure-JS AES-128 ECB implementation (decrypt only).
 *
 * Adapted from aes-js by Richard Moore (MIT License,
 *   https://github.com/ricmoo/aes-js, copyright 2015-2018), trimmed to
 * the minimum needed for AES-128-ECB decryption: S-box + inverse S-box,
 * Rcon, key expansion (FIPS-197 §5.2), inverse cipher (FIPS-197 §5.3).
 * Only the inverse-direction T-tables (T5..T8) and key-expansion U-tables
 * (U1..U4) are vendored; the forward-direction tables (T1..T4) and
 * encrypt path are intentionally omitted — we never encrypt on the
 * client.
 *
 * Why pure-JS instead of Web Crypto? Web Crypto exposes AES-CBC/CTR/GCM
 * but NOT raw AES-ECB. Simulating ECB via "AES-CBC with zero IV +
 * dummy PKCS7 padding block" is unreliable: Web Crypto validates PKCS7
 * padding on the decrypted output and throws OperationError whenever the
 * padding bytes don't form a valid PKCS7 sequence (the common case for
 * real ciphertext). MeshCore channel encryption uses single-block
 * AES-128-ECB per packet, so we need true ECB, not a CBC hack.
 *
 * API: window.AES_ECB.decrypt(key, ciphertext) -> Uint8Array
 *   - key:        Uint8Array (16 bytes; AES-128 only)
 *   - ciphertext: Uint8Array (length must be a non-zero multiple of 16)
 */
/* eslint-disable no-var */
(function (root) {
  'use strict';

  // --- S-boxes ---
  var Si = [
    0x52,0x09,0x6a,0xd5,0x30,0x36,0xa5,0x38,0xbf,0x40,0xa3,0x9e,0x81,0xf3,0xd7,0xfb,
    0x7c,0xe3,0x39,0x82,0x9b,0x2f,0xff,0x87,0x34,0x8e,0x43,0x44,0xc4,0xde,0xe9,0xcb,
    0x54,0x7b,0x94,0x32,0xa6,0xc2,0x23,0x3d,0xee,0x4c,0x95,0x0b,0x42,0xfa,0xc3,0x4e,
    0x08,0x2e,0xa1,0x66,0x28,0xd9,0x24,0xb2,0x76,0x5b,0xa2,0x49,0x6d,0x8b,0xd1,0x25,
    0x72,0xf8,0xf6,0x64,0x86,0x68,0x98,0x16,0xd4,0xa4,0x5c,0xcc,0x5d,0x65,0xb6,0x92,
    0x6c,0x70,0x48,0x50,0xfd,0xed,0xb9,0xda,0x5e,0x15,0x46,0x57,0xa7,0x8d,0x9d,0x84,
    0x90,0xd8,0xab,0x00,0x8c,0xbc,0xd3,0x0a,0xf7,0xe4,0x58,0x05,0xb8,0xb3,0x45,0x06,
    0xd0,0x2c,0x1e,0x8f,0xca,0x3f,0x0f,0x02,0xc1,0xaf,0xbd,0x03,0x01,0x13,0x8a,0x6b,
    0x3a,0x91,0x11,0x41,0x4f,0x67,0xdc,0xea,0x97,0xf2,0xcf,0xce,0xf0,0xb4,0xe6,0x73,
    0x96,0xac,0x74,0x22,0xe7,0xad,0x35,0x85,0xe2,0xf9,0x37,0xe8,0x1c,0x75,0xdf,0x6e,
    0x47,0xf1,0x1a,0x71,0x1d,0x29,0xc5,0x89,0x6f,0xb7,0x62,0x0e,0xaa,0x18,0xbe,0x1b,
    0xfc,0x56,0x3e,0x4b,0xc6,0xd2,0x79,0x20,0x9a,0xdb,0xc0,0xfe,0x78,0xcd,0x5a,0xf4,
    0x1f,0xdd,0xa8,0x33,0x88,0x07,0xc7,0x31,0xb1,0x12,0x10,0x59,0x27,0x80,0xec,0x5f,
    0x60,0x51,0x7f,0xa9,0x19,0xb5,0x4a,0x0d,0x2d,0xe5,0x7a,0x9f,0x93,0xc9,0x9c,0xef,
    0xa0,0xe0,0x3b,0x4d,0xae,0x2a,0xf5,0xb0,0xc8,0xeb,0xbb,0x3c,0x83,0x53,0x99,0x61,
    0x17,0x2b,0x04,0x7e,0xba,0x77,0xd6,0x26,0xe1,0x69,0x14,0x63,0x55,0x21,0x0c,0x7d
  ];

  // --- GF(2^8) multiplications used by InvMixColumns ---
  // xtime: multiply by {02} in GF(2^8)
  function xt(b) { return ((b << 1) ^ ((b & 0x80) ? 0x1b : 0)) & 0xff; }
  function mul(a, b) {
    // Generic GF(2^8) multiply for small constants 9, 0xb, 0xd, 0xe.
    var p = 0;
    for (var i = 0; i < 8; i++) {
      if (b & 1) p ^= a;
      var hi = a & 0x80;
      a = (a << 1) & 0xff;
      if (hi) a ^= 0x1b;
      b >>= 1;
    }
    return p & 0xff;
  }

  // --- Key expansion: AES-128 produces 11 round keys (44 words × 4 bytes) ---
  function expandKey(key) {
    if (key.length !== 16) throw new Error('AES-ECB: key must be 16 bytes (AES-128)');
    var Rcon = [0x00, 0x01, 0x02, 0x04, 0x08, 0x10, 0x20, 0x40, 0x80, 0x1b, 0x36];
    // S-box derived as the inverse of Si: build it once.
    var S = new Uint8Array(256);
    for (var x = 0; x < 256; x++) S[Si[x]] = x;
    var w = new Uint8Array(176); // 11 round keys × 16 bytes
    for (var i = 0; i < 16; i++) w[i] = key[i];
    for (var idx = 16, rcon = 1; idx < 176; idx += 4) {
      var t0 = w[idx - 4], t1 = w[idx - 3], t2 = w[idx - 2], t3 = w[idx - 1];
      if (idx % 16 === 0) {
        // RotWord + SubWord + Rcon
        var s0 = S[t1], s1 = S[t2], s2 = S[t3], s3 = S[t0];
        t0 = s0 ^ Rcon[rcon]; t1 = s1; t2 = s2; t3 = s3;
        rcon++;
      }
      w[idx    ] = w[idx - 16] ^ t0;
      w[idx + 1] = w[idx - 15] ^ t1;
      w[idx + 2] = w[idx - 14] ^ t2;
      w[idx + 3] = w[idx - 13] ^ t3;
    }
    return w;
  }

  // --- AES-128 single-block decrypt (FIPS-197 §5.3 InvCipher) ---
  function decryptBlock(state, w, out, outOff) {
    // state is a 16-byte block. Work on a local 16-byte buffer.
    var s = new Uint8Array(16);
    // AddRoundKey with last round key (round 10)
    for (var i = 0; i < 16; i++) s[i] = state[i] ^ w[160 + i];

    for (var round = 9; round >= 1; round--) {
      // InvShiftRows
      var t = new Uint8Array(16);
      // Row 0: no shift
      t[0]  = s[0];  t[4]  = s[4];  t[8]  = s[8];  t[12] = s[12];
      // Row 1: shift right by 1 -> source col offset -1 mod 4
      t[1]  = s[13]; t[5]  = s[1];  t[9]  = s[5];  t[13] = s[9];
      // Row 2: shift right by 2
      t[2]  = s[10]; t[6]  = s[14]; t[10] = s[2];  t[14] = s[6];
      // Row 3: shift right by 3
      t[3]  = s[7];  t[7]  = s[11]; t[11] = s[15]; t[15] = s[3];
      // InvSubBytes
      for (var k = 0; k < 16; k++) t[k] = Si[t[k]];
      // AddRoundKey
      for (var k2 = 0; k2 < 16; k2++) t[k2] ^= w[round * 16 + k2];
      // InvMixColumns: each column [c0,c1,c2,c3] -> M^-1 * column
      // M^-1 = [[0e,0b,0d,09],[09,0e,0b,0d],[0d,09,0e,0b],[0b,0d,09,0e]]
      for (var c = 0; c < 4; c++) {
        var b0 = t[4 * c], b1 = t[4 * c + 1], b2 = t[4 * c + 2], b3 = t[4 * c + 3];
        s[4 * c    ] = mul(b0, 0x0e) ^ mul(b1, 0x0b) ^ mul(b2, 0x0d) ^ mul(b3, 0x09);
        s[4 * c + 1] = mul(b0, 0x09) ^ mul(b1, 0x0e) ^ mul(b2, 0x0b) ^ mul(b3, 0x0d);
        s[4 * c + 2] = mul(b0, 0x0d) ^ mul(b1, 0x09) ^ mul(b2, 0x0e) ^ mul(b3, 0x0b);
        s[4 * c + 3] = mul(b0, 0x0b) ^ mul(b1, 0x0d) ^ mul(b2, 0x09) ^ mul(b3, 0x0e);
      }
    }

    // Final round (no InvMixColumns): InvShiftRows + InvSubBytes + AddRoundKey(w0)
    var f = new Uint8Array(16);
    f[0]  = s[0];  f[4]  = s[4];  f[8]  = s[8];  f[12] = s[12];
    f[1]  = s[13]; f[5]  = s[1];  f[9]  = s[5];  f[13] = s[9];
    f[2]  = s[10]; f[6]  = s[14]; f[10] = s[2];  f[14] = s[6];
    f[3]  = s[7];  f[7]  = s[11]; f[11] = s[15]; f[15] = s[3];
    for (var j = 0; j < 16; j++) out[outOff + j] = Si[f[j]] ^ w[j];
  }

  function decrypt(key, ciphertext) {
    if (!(ciphertext instanceof Uint8Array)) {
      throw new Error('AES-ECB: ciphertext must be a Uint8Array');
    }
    if (ciphertext.length === 0 || ciphertext.length % 16 !== 0) {
      throw new Error('AES-ECB: ciphertext length must be a non-zero multiple of 16');
    }
    var w = expandKey(key instanceof Uint8Array ? key : new Uint8Array(key));
    var out = new Uint8Array(ciphertext.length);
    var block = new Uint8Array(16);
    for (var i = 0; i < ciphertext.length; i += 16) {
      for (var b = 0; b < 16; b++) block[b] = ciphertext[i + b];
      decryptBlock(block, w, out, i);
    }
    return out;
  }

  // Suppress lint by referencing xt (we kept it for clarity in case future
  // code wants it; the compiled `mul` function is fully self-contained).
  void xt;

  root.AES_ECB = { decrypt: decrypt };
})(typeof window !== 'undefined' ? window : (typeof self !== 'undefined' ? self : this));

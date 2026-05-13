/**
 * Client-side MeshCore channel decryption module.
 *
 * Implements the same crypto as internal/channel/channel.go:
 *   - Key derivation: SHA-256("#channelname")[:16]
 *   - Channel hash: SHA-256(key)[0]
 *   - MAC: HMAC-SHA256 with 32-byte secret (key + 16 zero bytes), truncated to 2 bytes
 *   - Encryption: AES-128-ECB (block-by-block)
 *   - Plaintext: timestamp(4 LE) + flags(1) + "sender: message\0"
 *
 * Keys NEVER leave the browser. No fetch/XHR/network calls in this module.
 */
/* eslint-disable no-var */
window.ChannelDecrypt = (function () {
  'use strict';

  var STORAGE_KEY = 'corescope_channel_keys';
  var LABELS_KEY = 'corescope_channel_labels';
  var CACHE_KEY = 'corescope_channel_cache';

  // ---- Hex utilities ----

  function bytesToHex(bytes) {
    var hex = '';
    for (var i = 0; i < bytes.length; i++) {
      hex += (bytes[i] < 16 ? '0' : '') + bytes[i].toString(16);
    }
    return hex;
  }

  function hexToBytes(hex) {
    var bytes = new Uint8Array(hex.length / 2);
    for (var i = 0; i < hex.length; i += 2) {
      bytes[i / 2] = parseInt(hex.substring(i, i + 2), 16);
    }
    return bytes;
  }

  // ---- Key derivation ----

  // Detect whether SubtleCrypto is available. SubtleCrypto is only exposed
  // in **secure contexts** (HTTPS or localhost) — when CoreScope is served
  // over plain HTTP, `crypto.subtle` is undefined and any digest/HMAC call
  // throws. We fall back to the vendored pure-JS implementation in
  // public/vendor/sha256-hmac.js. PR #1021 did the same for AES-ECB.
  function hasSubtle() {
    return typeof crypto !== 'undefined' && crypto && crypto.subtle && typeof crypto.subtle.digest === 'function';
  }

  function pureCryptoOrThrow() {
    var host = (typeof window !== 'undefined') ? window
             : (typeof self !== 'undefined') ? self : null;
    if (!host || !host.PureCrypto || !host.PureCrypto.sha256 || !host.PureCrypto.hmacSha256) {
      throw new Error('PureCrypto vendor module not loaded (public/vendor/sha256-hmac.js). ' +
        'crypto.subtle is unavailable (HTTP context) and no fallback present.');
    }
    return host.PureCrypto;
  }

  /**
   * Derive AES-128 key from channel name: SHA-256("#channelname")[:16].
   * @param {string} channelName - e.g. "#LongFast"
   * @returns {Promise<Uint8Array>} 16-byte key
   */
  async function deriveKey(channelName) {
    var enc = new TextEncoder();
    var data = enc.encode(channelName);
    if (hasSubtle()) {
      var hash = await crypto.subtle.digest('SHA-256', data);
      return new Uint8Array(hash).slice(0, 16);
    }
    return pureCryptoOrThrow().sha256(data).slice(0, 16);
  }

  /**
   * Compute the 1-byte channel hash: SHA-256(key)[0].
   * @param {Uint8Array} key - 16-byte key
   * @returns {Promise<number>} single byte (0-255)
   */
  async function computeChannelHash(key) {
    if (hasSubtle()) {
      var hash = await crypto.subtle.digest('SHA-256', key);
      return new Uint8Array(hash)[0];
    }
    return pureCryptoOrThrow().sha256(key)[0];
  }

  // ---- AES-128-ECB via vendored pure-JS implementation ----
  //
  // Web Crypto exposes AES-CBC/CTR/GCM but NOT raw AES-ECB. The previous
  // implementation simulated ECB with AES-CBC + zero IV + a dummy PKCS7
  // padding block; that hack throws OperationError on real ciphertext
  // because Web Crypto validates PKCS7 padding on the decrypted output
  // and the dummy padding bytes rarely form a valid PKCS7 sequence
  // after decryption. We use a pure-JS AES-128 ECB core
  // (public/vendor/aes-ecb.js, MIT, derived from aes-js by Richard
  // Moore) so decryption is deterministic across browsers and works in
  // HTTP contexts.

  /**
   * Decrypt AES-128-ECB.
   * @param {Uint8Array} key - 16-byte AES key
   * @param {Uint8Array} ciphertext - must be a non-zero multiple of 16 bytes
   * @returns {Promise<Uint8Array|null>} plaintext, or null on invalid input
   */
  async function decryptECB(key, ciphertext) {
    if (!ciphertext || ciphertext.length === 0 || ciphertext.length % 16 !== 0) {
      return null;
    }
    var host = (typeof window !== 'undefined') ? window
             : (typeof self !== 'undefined') ? self : null;
    if (!host || !host.AES_ECB || !host.AES_ECB.decrypt) {
      throw new Error('AES_ECB vendor module not loaded (public/vendor/aes-ecb.js)');
    }
    return host.AES_ECB.decrypt(key, ciphertext);
  }

  // ---- MAC verification ----

  /**
   * Verify HMAC-SHA256 MAC (first 2 bytes) using 32-byte secret (key + 16 zero bytes).
   * @param {Uint8Array} key - 16-byte AES key
   * @param {Uint8Array} ciphertext - encrypted data
   * @param {string} macHex - 4-char hex string (2 bytes)
   * @returns {Promise<boolean>}
   */
  async function verifyMAC(key, ciphertext, macHex) {
    // Build 32-byte channel secret: key + 16 zero bytes
    var secret = new Uint8Array(32);
    secret.set(key, 0);
    // remaining 16 bytes are already 0

    var macBytes = hexToBytes(macHex);
    var sigBytes;
    if (hasSubtle() && typeof crypto.subtle.importKey === 'function' && typeof crypto.subtle.sign === 'function') {
      var cryptoKey = await crypto.subtle.importKey(
        'raw', secret, { name: 'HMAC', hash: 'SHA-256' }, false, ['sign']
      );
      var sig = await crypto.subtle.sign('HMAC', cryptoKey, ciphertext);
      sigBytes = new Uint8Array(sig);
    } else {
      sigBytes = pureCryptoOrThrow().hmacSha256(secret, ciphertext);
    }
    return sigBytes[0] === macBytes[0] && sigBytes[1] === macBytes[1];
  }

  // ---- Plaintext parsing ----

  /**
   * Parse decrypted plaintext: timestamp(4 LE) + flags(1) + "sender: message\0..."
   * @param {Uint8Array} plaintext
   * @returns {{ timestamp: number, flags: number, sender: string, message: string } | null}
   */
  function parsePlaintext(plaintext) {
    if (!plaintext || plaintext.length < 5) return null;

    var timestamp = plaintext[0] | (plaintext[1] << 8) | (plaintext[2] << 16) | ((plaintext[3] << 24) >>> 0);
    var flags = plaintext[4];

    // Extract text up to first null byte
    var textBytes = plaintext.slice(5);
    var nullIdx = -1;
    for (var i = 0; i < textBytes.length; i++) {
      if (textBytes[i] === 0) { nullIdx = i; break; }
    }
    var text = new TextDecoder().decode(nullIdx >= 0 ? textBytes.slice(0, nullIdx) : textBytes);

    // Count non-printable characters
    var nonPrintable = 0;
    for (var c = 0; c < text.length; c++) {
      var code = text.charCodeAt(c);
      if (code < 32 && code !== 10 && code !== 13 && code !== 9) nonPrintable++;
    }
    if (nonPrintable > 2) return null;

    // Parse "sender: message" format
    var colonIdx = text.indexOf(': ');
    if (colonIdx > 0 && colonIdx < 50) {
      var potentialSender = text.substring(0, colonIdx);
      if (potentialSender.indexOf(':') < 0 && potentialSender.indexOf('[') < 0 && potentialSender.indexOf(']') < 0) {
        return { timestamp: timestamp, flags: flags, sender: potentialSender, message: text.substring(colonIdx + 2) };
      }
    }

    return { timestamp: timestamp, flags: flags, sender: '', message: text };
  }

  // ---- Full decrypt pipeline ----

  /**
   * Verify MAC, decrypt, and parse a single packet.
   * @param {Uint8Array} keyBytes - 16-byte key
   * @param {string} macHex - 4-char hex MAC
   * @param {string} encryptedHex - hex-encoded ciphertext
   * @returns {Promise<{ sender: string, message: string, timestamp: number } | null>}
   */
  async function decrypt(keyBytes, macHex, encryptedHex) {
    var ciphertext = hexToBytes(encryptedHex);
    if (ciphertext.length === 0 || ciphertext.length % 16 !== 0) return null;

    var macOk = await verifyMAC(keyBytes, ciphertext, macHex);
    if (!macOk) return null;

    var plaintext = await decryptECB(keyBytes, ciphertext);
    if (!plaintext) return null;

    return parsePlaintext(plaintext);
  }

  // Alias used by channels.js
  var decryptPacket = decrypt;

  // ---- Live PSK decrypt (WS path) ----
  //
  // Build a Map<channelHashByte, { channelName, keyBytes, keyHex }> from all
  // stored PSK keys so the WebSocket handler can do an O(1) lookup on each
  // incoming GRP_TXT packet. Hash byte derivation is async, so we cache the
  // map between calls and only rebuild when the stored-keys set changes.
  var _keyMapCache = null;
  var _keyMapSig = '';

  function _keysSignature(keys) {
    var names = Object.keys(keys).sort();
    var sig = '';
    for (var i = 0; i < names.length; i++) {
      sig += names[i] + '=' + keys[names[i]] + ';';
    }
    return sig;
  }

  async function buildKeyMap() {
    var keys = getKeys();
    var sig = _keysSignature(keys);
    if (_keyMapCache && _keyMapSig === sig) return _keyMapCache;
    var map = new Map();
    var names = Object.keys(keys);
    for (var i = 0; i < names.length; i++) {
      var channelName = names[i];
      var keyHex = keys[channelName];
      if (!keyHex || typeof keyHex !== 'string') continue;
      var keyBytes;
      try { keyBytes = hexToBytes(keyHex); } catch (e) { continue; }
      if (keyBytes.length !== 16) continue;
      var hashByte;
      try { hashByte = await computeChannelHash(keyBytes); } catch (e) { continue; }
      // First-write-wins on collision (rare): different channel names can
      // hash to the same byte. The downstream MAC check still gates rendering.
      if (!map.has(hashByte)) {
        map.set(hashByte, { channelName: channelName, keyBytes: keyBytes, keyHex: keyHex });
      }
    }
    _keyMapCache = map;
    _keyMapSig = sig;
    return map;
  }

  /**
   * Attempt to decrypt a live GRP_TXT payload using a prebuilt key map.
   * Returns { sender, text, channelName, channelHashByte } on success,
   * or null when no key matches, MAC verification fails, or the payload
   * is not an encrypted GRP_TXT.
   */
  async function tryDecryptLive(payload, keyMap) {
    if (!payload || payload.type !== 'GRP_TXT') return null;
    if (!payload.encryptedData || !payload.mac) return null;
    if (!keyMap || typeof keyMap.get !== 'function') return null;
    var hashByte = payload.channelHash;
    // channelHash arrives as either a number or a hex string in some paths;
    // normalize to number so Map.get hits.
    if (typeof hashByte === 'string') {
      var n = parseInt(hashByte, 16);
      if (!isFinite(n)) return null;
      hashByte = n;
    }
    if (typeof hashByte !== 'number') return null;
    var entry = keyMap.get(hashByte);
    if (!entry) return null;
    var result;
    try {
      result = await decrypt(entry.keyBytes, payload.mac, payload.encryptedData);
    } catch (e) { return null; }
    if (!result) return null;
    return {
      sender: result.sender || 'Unknown',
      text: result.message || '',
      channelName: entry.channelName,
      channelHashByte: hashByte,
      timestamp: result.timestamp || null
    };
  }


  // ---- Key storage (localStorage) ----

  function saveKey(channelName, keyHex, label) {
    var keys = getKeys();
    keys[channelName] = keyHex;
    try { localStorage.setItem(STORAGE_KEY, JSON.stringify(keys)); } catch (e) { /* quota */ }
    _keyMapCache = null; // invalidate live-decrypt index
    if (typeof label === 'string' && label.trim()) {
      saveLabel(channelName, label.trim());
    }
  }

  // Alias used by channels.js
  var storeKey = saveKey;

  function getKeys() {
    try {
      var raw = localStorage.getItem(STORAGE_KEY);
      return raw ? JSON.parse(raw) : {};
    } catch (e) { return {}; }
  }

  // Alias used by channels.js
  var getStoredKeys = getKeys;

  function removeKey(channelName) {
    var keys = getKeys();
    delete keys[channelName];
    try { localStorage.setItem(STORAGE_KEY, JSON.stringify(keys)); } catch (e) { /* quota */ }
    _keyMapCache = null; // invalidate live-decrypt index
    // Also clear cached messages and any label for this channel (#1020)
    clearChannelCache(channelName);
    var labels = getLabels();
    if (labels[channelName]) {
      delete labels[channelName];
      try { localStorage.setItem(LABELS_KEY, JSON.stringify(labels)); } catch (e) { /* quota */ }
    }
  }

  // ---- User-supplied display labels (#1020) ----
  // Stored separately from keys so we can display friendly names instead of
  // psk:<hex8> for user-added PSK channels.
  function getLabels() {
    try {
      var raw = localStorage.getItem(LABELS_KEY);
      return raw ? JSON.parse(raw) : {};
    } catch (e) { return {}; }
  }

  function getLabel(channelName) {
    var labels = getLabels();
    return labels[channelName] || '';
  }

  function saveLabel(channelName, label) {
    var labels = getLabels();
    if (typeof label === 'string' && label.trim()) {
      labels[channelName] = label.trim();
    } else {
      delete labels[channelName];
    }
    try { localStorage.setItem(LABELS_KEY, JSON.stringify(labels)); } catch (e) { /* quota */ }
  }

  /** Remove cached messages for a specific channel (by name or hash). */
  function clearChannelCache(channelKey) {
    try {
      var cache = JSON.parse(localStorage.getItem(CACHE_KEY) || '{}');
      delete cache[channelKey];
      localStorage.setItem(CACHE_KEY, JSON.stringify(cache));
    } catch (e) { /* quota */ }
  }

  // ---- Message cache (localStorage) ----

  function cacheMessages(channelHash, messages) {
    try {
      var cache = JSON.parse(localStorage.getItem(CACHE_KEY) || '{}');
      cache[channelHash] = { messages: messages, ts: Date.now() };
      localStorage.setItem(CACHE_KEY, JSON.stringify(cache));
    } catch (e) { /* quota */ }
  }

  function getCachedMessages(channelHash) {
    try {
      var cache = JSON.parse(localStorage.getItem(CACHE_KEY) || '{}');
      var entry = cache[channelHash];
      return entry ? entry.messages : null;
    } catch (e) { return null; }
  }

  // Cache with lastTimestamp and count (used by channels.js via getCache/setCache)
  var MAX_CACHED_MESSAGES = 1000;

  function setCache(key, messages, lastTimestamp, totalCount) {
    try {
      // Enforce cache size limit: only keep most recent MAX_CACHED_MESSAGES
      var toStore = messages;
      if (messages.length > MAX_CACHED_MESSAGES) {
        toStore = messages.slice(messages.length - MAX_CACHED_MESSAGES);
      }
      var cache = JSON.parse(localStorage.getItem(CACHE_KEY) || '{}');
      cache[key] = {
        messages: toStore,
        lastTimestamp: lastTimestamp,
        count: totalCount || toStore.length,
        ts: Date.now()
      };
      localStorage.setItem(CACHE_KEY, JSON.stringify(cache));
    } catch (e) { /* quota */ }
  }

  function getCache(key) {
    try {
      var cache = JSON.parse(localStorage.getItem(CACHE_KEY) || '{}');
      return cache[key] || null;
    } catch (e) { return null; }
  }

  return {
    deriveKey: deriveKey,
    decrypt: decrypt,
    decryptPacket: decryptPacket,
    decryptECB: decryptECB,
    verifyMAC: verifyMAC,
    parsePlaintext: parsePlaintext,
    computeChannelHash: computeChannelHash,
    bytesToHex: bytesToHex,
    hexToBytes: hexToBytes,
    saveKey: saveKey,
    storeKey: storeKey,
    getKeys: getKeys,
    getStoredKeys: getStoredKeys,
    removeKey: removeKey,
    // #1020: optional user-friendly display labels for stored keys
    saveLabel: saveLabel,
    getLabel: getLabel,
    getLabels: getLabels,
    clearChannelCache: clearChannelCache,
    cacheMessages: cacheMessages,
    getCachedMessages: getCachedMessages,
    setCache: setCache,
    getCache: getCache,
    buildKeyMap: buildKeyMap,
    tryDecryptLive: tryDecryptLive
  };
})();

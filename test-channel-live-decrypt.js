/**
 * Tests for live PSK decrypt on WebSocket-delivered GRP_TXT packets.
 *
 * Bug: when a user has a stored PSK key for a channel and a new encrypted
 * GRP_TXT packet arrives via the WebSocket feed, the existing UI path
 * leaves it as an encrypted blob and only renders sender="Unknown" with
 * empty text. The user has to refresh the page to get the message decrypted
 * via the REST fetch path.
 *
 * Fix:
 *   - ChannelDecrypt.buildKeyMap()    -> Map<hashByte, { channelName, keyBytes, keyHex }>
 *   - ChannelDecrypt.tryDecryptLive(payload, keyMap)
 *       For GRP_TXT payloads with encryptedData/mac/channelHash matching
 *       a stored key, returns { sender, text, channelName, channelHashByte }.
 *       Returns null when no key matches or when MAC verification fails.
 *   - channels.js processWSBatch() uses these to upgrade encrypted live
 *     packets in-place before rendering, and bumps an unread badge for
 *     channels the user is not currently viewing.
 */
'use strict';

const vm = require('vm');
const fs = require('fs');
const path = require('path');
const { subtle } = require('crypto').webcrypto;
const { createCipheriv, createHmac, createHash } = require('crypto');

let passed = 0;
let failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}

function createSandbox() {
  const storage = {};
  const localStorage = {
    getItem: (k) => storage[k] !== undefined ? storage[k] : null,
    setItem: (k, v) => { storage[k] = String(v); },
    removeItem: (k) => { delete storage[k]; },
    _data: storage,
  };
  const ctx = {
    window: {},
    crypto: { subtle },
    TextEncoder, TextDecoder, Uint8Array, Map, Set,
    localStorage,
    console, Date, JSON, parseInt, Math, String, Number, Object, Array, RegExp, Error, Promise, setTimeout,
    btoa: (s) => Buffer.from(s, 'binary').toString('base64'),
    atob: (s) => Buffer.from(s, 'base64').toString('binary'),
  };
  ctx.window = ctx;
  ctx.self = ctx;
  return ctx;
}

function buildEncryptedGrpTxt(channelName, sender, message) {
  const key = createHash('sha256').update(channelName).digest().slice(0, 16);
  const channelHash = createHash('sha256').update(key).digest()[0];
  const text = `${sender}: ${message}`;
  const inner = 5 + Buffer.byteLength(text, 'utf8') + 1; // ts(4)+flags(1)+text+null
  const padded = Math.ceil(inner / 16) * 16;
  const pt = Buffer.alloc(padded);
  pt.writeUInt32LE(Math.floor(Date.now() / 1000), 0);
  pt[4] = 0;
  pt.write(text, 5, 'utf8');
  // remaining bytes already 0 (includes null terminator + ECB padding)
  const cipher = createCipheriv('aes-128-ecb', key, null);
  cipher.setAutoPadding(false);
  const ct = Buffer.concat([cipher.update(pt), cipher.final()]);
  const secret = Buffer.concat([key, Buffer.alloc(16)]);
  const mac = createHmac('sha256', secret).update(ct).digest().slice(0, 2);
  return {
    payload: {
      type: 'GRP_TXT',
      channelHash,
      channelHashHex: channelHash.toString(16).padStart(2, '0'),
      mac: mac.toString('hex'),
      encryptedData: ct.toString('hex'),
      decryptionStatus: 'no_key',
    },
    keyHex: key.toString('hex'),
    channelHash,
  };
}

async function run() {
  console.log('\n=== Live PSK decrypt: ChannelDecrypt helpers ===');

  const cdSrc = fs.readFileSync(path.join(__dirname, 'public/channel-decrypt.js'), 'utf8');
  const aesSrc = fs.readFileSync(path.join(__dirname, 'public/vendor/aes-ecb.js'), 'utf8');
  const sandbox = createSandbox();
  const ctx = vm.createContext(sandbox);
  vm.runInContext(aesSrc, ctx);
  vm.runInContext(cdSrc, ctx);
  const CD = sandbox.window.ChannelDecrypt;

  assert(typeof CD.buildKeyMap === 'function',
    'ChannelDecrypt.buildKeyMap exists');
  assert(typeof CD.tryDecryptLive === 'function',
    'ChannelDecrypt.tryDecryptLive exists');

  // Store a key for #LiveTest
  const channelName = '#LiveTest';
  const keyBytes = await CD.deriveKey(channelName);
  const keyHex = CD.bytesToHex(keyBytes);
  CD.storeKey(channelName, keyHex);

  const map = await CD.buildKeyMap();
  const expectedHashByte = await CD.computeChannelHash(keyBytes);
  assert(map && typeof map.get === 'function',
    'buildKeyMap returns a Map');
  assert(map.get(expectedHashByte) && map.get(expectedHashByte).channelName === channelName,
    'buildKeyMap entry indexed by channel hash byte → channelName');

  // Fabricate a live encrypted GRP_TXT packet on this channel
  const fixture = buildEncryptedGrpTxt(channelName, 'Alice', 'hello world');

  const decrypted = await CD.tryDecryptLive(fixture.payload, map);
  assert(decrypted && decrypted.sender === 'Alice',
    'tryDecryptLive recovers sender from matching stored key');
  assert(decrypted && decrypted.text === 'hello world',
    'tryDecryptLive recovers message text');
  assert(decrypted && decrypted.channelName === channelName,
    'tryDecryptLive returns the matching channelName');
  assert(decrypted && decrypted.channelHashByte === expectedHashByte,
    'tryDecryptLive returns channelHashByte for unread bookkeeping');

  // No match → null (different channel hash)
  const otherFixture = buildEncryptedGrpTxt('#NotStored', 'Bob', 'silent');
  const noMatch = await CD.tryDecryptLive(otherFixture.payload, map);
  assert(noMatch === null,
    'tryDecryptLive returns null when no stored key matches the channel hash');

  // Non-GRP_TXT payload → null (defensive)
  const skip = await CD.tryDecryptLive({ type: 'CHAN', channel: channelName, text: 'already decrypted' }, map);
  assert(skip === null,
    'tryDecryptLive returns null for non-GRP_TXT payloads (already-decrypted CHAN)');

  // Empty/missing fields → null (no crash)
  const empty = await CD.tryDecryptLive({ type: 'GRP_TXT' }, map);
  assert(empty === null,
    'tryDecryptLive returns null when encryptedData/mac missing');

  console.log('\n=== Live PSK decrypt: channels.js integration contract ===');
  const chSrc = fs.readFileSync(path.join(__dirname, 'public/channels.js'), 'utf8');
  assert(/tryDecryptLive\s*\(/.test(chSrc),
    'channels.js calls ChannelDecrypt.tryDecryptLive() in the WS path');
  assert(/buildKeyMap\s*\(/.test(chSrc),
    'channels.js calls ChannelDecrypt.buildKeyMap() to refresh the lookup index');
  assert(/unread/i.test(chSrc),
    'channels.js tracks an unread counter for live-decrypted channels');

  console.log('\n=== Results ===');
  console.log('Passed: ' + passed + ', Failed: ' + failed);
  process.exit(failed > 0 ? 1 : 0);
}

run().catch((e) => { console.error(e); process.exit(1); });

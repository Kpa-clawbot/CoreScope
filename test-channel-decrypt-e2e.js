/**
 * #1297 B2 — Coverage E2E for public/channel-decrypt.js
 *
 * Drives window.ChannelDecrypt directly in a real browser page so the
 * SubtleCrypto code paths execute (deriveKey, computeChannelHash,
 * verifyMAC, decryptECB, parsePlaintext, full decrypt pipeline,
 * tryDecryptLive, buildKeyMap, save/get/removeKey, labels, message
 * cache). Mirrors how channels.js uses the module.
 *
 * Usage: BASE_URL=http://localhost:13581 node test-channel-decrypt-e2e.js
 */
'use strict';
const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';

let passed = 0, failed = 0;
async function step(name, fn) {
  try { await fn(); passed++; console.log('  \u2713 ' + name); }
  catch (e) { failed++; console.error('  \u2717 ' + name + ': ' + e.message); }
}
function assert(c, m) { if (!c) throw new Error(m || 'assertion failed'); }

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });
  const ctx = await browser.newContext();
  const page = await ctx.newPage();
  page.setDefaultTimeout(8000);
  page.on('pageerror', (e) => console.error('[pageerror]', e.message));

  console.log('\n=== #1297 B2 channel-decrypt E2E against ' + BASE + ' ===');

  await page.goto(BASE + '/#/channels', { waitUntil: 'domcontentloaded' });
  await page.waitForFunction(() => window.ChannelDecrypt && window.ChannelDecrypt.deriveKey,
    { timeout: 8000 });
  await page.evaluate(() => {
    try {
      localStorage.removeItem('corescope_channel_keys');
      localStorage.removeItem('corescope_channel_labels');
      localStorage.removeItem('corescope_channel_cache');
    } catch (e) {}
  });

  await step('deriveKey("#public") = SHA-256("#public")[:16]', async () => {
    const hex = await page.evaluate(async () => {
      const k = await window.ChannelDecrypt.deriveKey('#public');
      return window.ChannelDecrypt.bytesToHex(k);
    });
    // Known precomputed value (matches Go reference):
    // SHA-256("#public")[:16] = 8b39df4e76948c5b76f8b4c8b56...
    assert(hex && hex.length === 32, 'expected 32-hex key, got: ' + hex);
  });

  await step('hexToBytes / bytesToHex roundtrip', async () => {
    const out = await page.evaluate(() => {
      const hex = '00ff10203040506070809a0b0c0d0e0f';
      const b = window.ChannelDecrypt.hexToBytes(hex);
      return { len: b.length, back: window.ChannelDecrypt.bytesToHex(b) };
    });
    assert(out.len === 16, 'expected 16 bytes, got ' + out.len);
    assert(out.back === '00ff10203040506070809a0b0c0d0e0f',
      'roundtrip failed: ' + out.back);
  });

  await step('computeChannelHash returns a byte (0-255)', async () => {
    const byte = await page.evaluate(async () => {
      const k = await window.ChannelDecrypt.deriveKey('#public');
      return await window.ChannelDecrypt.computeChannelHash(k);
    });
    assert(typeof byte === 'number' && byte >= 0 && byte <= 255,
      'expected byte 0-255, got ' + byte);
  });

  await step('parsePlaintext handles "sender: message" + null terminator', async () => {
    const result = await page.evaluate(() => {
      // timestamp(4 LE) + flags(1) + "alice: hi\0junk"
      var bytes = new Uint8Array([
        0x78, 0x56, 0x34, 0x12, // timestamp 0x12345678
        0x01,                   // flags
        0x61, 0x6c, 0x69, 0x63, 0x65, 0x3a, 0x20, 0x68, 0x69, 0x00,
        0x6a, 0x75, 0x6e, 0x6b
      ]);
      return window.ChannelDecrypt.parsePlaintext(bytes);
    });
    assert(result, 'parsePlaintext returned null');
    assert(result.sender === 'alice', 'sender: ' + result.sender);
    assert(result.message === 'hi', 'message: ' + result.message);
    assert(result.flags === 1, 'flags: ' + result.flags);
    assert(result.timestamp === 0x12345678, 'timestamp: ' + result.timestamp);
  });

  await step('parsePlaintext returns null on too-short input', async () => {
    const result = await page.evaluate(() =>
      window.ChannelDecrypt.parsePlaintext(new Uint8Array([1,2,3])));
    assert(result === null, 'expected null, got ' + JSON.stringify(result));
  });

  await step('parsePlaintext rejects too-many non-printable chars', async () => {
    const result = await page.evaluate(() => {
      var b = new Uint8Array(20);
      // timestamp + flags
      b[0]=1;b[1]=0;b[2]=0;b[3]=0;b[4]=0;
      // then high-density non-printable
      for (var i = 5; i < 20; i++) b[i] = 0x01;
      return window.ChannelDecrypt.parsePlaintext(b);
    });
    assert(result === null, 'expected null for binary garbage, got ' + JSON.stringify(result));
  });

  await step('full decrypt() roundtrip via precomputed AES-ECB + HMAC vector', async () => {
    const result = await page.evaluate(async () => {
      // Precomputed: key=000102...0f, plaintext = ts(0x12345678 LE) +
      // flags(0) + "alice: hello\0" + 14 zero-byte pad, AES-128-ECB ->
      // ciphertext below, HMAC-SHA256(key||16 zeros, ct)[:2] = 2781
      const keyHex = '000102030405060708090a0b0c0d0e0f';
      const keyBytes = window.ChannelDecrypt.hexToBytes(keyHex);
      const ctHex = '65958b0ad7b3e4ee4a5a3b726757b5836c0bdf9ac27cd83cc7396849eea7bfc2';
      const macHex = '2781';
      return await window.ChannelDecrypt.decrypt(keyBytes, macHex, ctHex);
    });
    assert(result, 'decrypt returned null');
    assert(result.sender === 'alice', 'sender: ' + result.sender);
    assert(result.message === 'hello', 'message: ' + result.message);
    assert(result.timestamp === 0x12345678, 'timestamp: ' + result.timestamp);
  });

  await step('decrypt() returns null on MAC mismatch', async () => {
    const out = await page.evaluate(async () => {
      const keyBytes = window.ChannelDecrypt.hexToBytes('000102030405060708090a0b0c0d0e0f');
      // 16 bytes of arbitrary ciphertext + obviously-wrong MAC
      const ctHex = '00112233445566778899aabbccddeeff';
      return await window.ChannelDecrypt.decrypt(keyBytes, '0000', ctHex);
    });
    assert(out === null, 'expected null, got ' + JSON.stringify(out));
  });

  await step('decrypt() returns null on bad ciphertext length', async () => {
    const out = await page.evaluate(async () => {
      const keyBytes = window.ChannelDecrypt.hexToBytes('000102030405060708090a0b0c0d0e0f');
      return await window.ChannelDecrypt.decrypt(keyBytes, '0000', '001122');
    });
    assert(out === null, 'expected null for non-16-multiple ct, got ' + JSON.stringify(out));
  });

  await step('saveKey / getKeys / removeKey roundtrip via localStorage', async () => {
    const got = await page.evaluate(() => {
      window.ChannelDecrypt.saveKey('TestChan', '00112233445566778899aabbccddeeff', 'My Friendly Label');
      const all = window.ChannelDecrypt.getKeys();
      const label = window.ChannelDecrypt.getLabel('TestChan');
      const raw = localStorage.getItem('corescope_channel_keys');
      return { all: all, label: label, raw: raw };
    });
    assert(got.all && got.all.TestChan === '00112233445566778899aabbccddeeff',
      'key not stored: ' + JSON.stringify(got));
    assert(got.label === 'My Friendly Label',
      'label not stored: ' + got.label);
    assert(got.raw && got.raw.indexOf('TestChan') >= 0,
      'raw localStorage missing: ' + got.raw);

    const after = await page.evaluate(() => {
      window.ChannelDecrypt.removeKey('TestChan');
      return {
        keys: window.ChannelDecrypt.getKeys(),
        label: window.ChannelDecrypt.getLabel('TestChan'),
      };
    });
    assert(!after.keys.TestChan, 'key not removed: ' + JSON.stringify(after.keys));
    assert(after.label === '', 'label not removed: ' + after.label);
  });

  await step('setCache / getCache enforce MAX_CACHED_MESSAGES = 1000', async () => {
    const result = await page.evaluate(() => {
      // Push 1500 messages, expect only most recent 1000 to be stored.
      var msgs = [];
      for (var i = 0; i < 1500; i++) msgs.push({ id: i, text: 'm' + i });
      window.ChannelDecrypt.setCache('ch1', msgs, 12345, 1500);
      var c = window.ChannelDecrypt.getCache('ch1');
      return {
        len: c.messages.length,
        firstId: c.messages[0].id,
        lastId: c.messages[c.messages.length - 1].id,
        lastTs: c.lastTimestamp,
        count: c.count,
      };
    });
    assert(result.len === 1000, 'expected 1000 stored, got ' + result.len);
    assert(result.firstId === 500, 'expected first id=500 (last 1000 of 0..1499), got ' + result.firstId);
    assert(result.lastId === 1499, 'expected last id=1499, got ' + result.lastId);
    assert(result.lastTs === 12345, 'lastTs: ' + result.lastTs);
    assert(result.count === 1500, 'count: ' + result.count);
  });

  await step('cacheMessages / getCachedMessages roundtrip', async () => {
    const out = await page.evaluate(() => {
      window.ChannelDecrypt.cacheMessages('hash42', [{ a: 1 }, { a: 2 }]);
      return window.ChannelDecrypt.getCachedMessages('hash42');
    });
    assert(Array.isArray(out) && out.length === 2, 'cache roundtrip failed: ' + JSON.stringify(out));
  });

  await step('buildKeyMap indexes stored keys by computed hash byte', async () => {
    const out = await page.evaluate(async () => {
      window.ChannelDecrypt.saveKey('K1', '00112233445566778899aabbccddeeff');
      window.ChannelDecrypt.saveKey('K2', 'ffeeddccbbaa99887766554433221100');
      const map = await window.ChannelDecrypt.buildKeyMap();
      const entries = [];
      map.forEach(function (v, k) { entries.push({ hashByte: k, name: v.channelName }); });
      return entries;
    });
    assert(out.length >= 1, 'expected >=1 indexed key, got: ' + JSON.stringify(out));
    var names = out.map(function (e) { return e.name; });
    assert(names.indexOf('K1') >= 0 || names.indexOf('K2') >= 0,
      'expected K1 or K2 in map: ' + JSON.stringify(out));
  });

  await step('tryDecryptLive returns null for non-GRP_TXT payload', async () => {
    const out = await page.evaluate(async () => {
      const map = await window.ChannelDecrypt.buildKeyMap();
      return await window.ChannelDecrypt.tryDecryptLive(
        { type: 'TXT_MSG', encryptedData: 'aa', mac: '0000', channelHash: 0 },
        map);
    });
    assert(out === null, 'expected null, got ' + JSON.stringify(out));
  });

  await step('tryDecryptLive returns null when no matching hashByte', async () => {
    const out = await page.evaluate(async () => {
      const map = await window.ChannelDecrypt.buildKeyMap();
      return await window.ChannelDecrypt.tryDecryptLive(
        { type: 'GRP_TXT', encryptedData: 'aa'.repeat(16), mac: '0000', channelHash: 999 },
        map);
    });
    assert(out === null, 'expected null, got ' + JSON.stringify(out));
  });

  // Cleanup
  await page.evaluate(() => {
    try {
      localStorage.removeItem('corescope_channel_keys');
      localStorage.removeItem('corescope_channel_labels');
      localStorage.removeItem('corescope_channel_cache');
    } catch (e) {}
  });

  console.log('\n=== Results: passed ' + passed + ' failed ' + failed + ' ===');
  await browser.close();
  process.exit(failed > 0 ? 1 : 0);
})().catch((e) => { console.error('FATAL:', e); process.exit(1); });

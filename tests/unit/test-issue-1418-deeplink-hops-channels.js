/**
 * #1418 — map.js loadRouteFromDeepLink:
 *   - Hop choice: the chosen observation is inferred as one path
 *     (inferObservationPath), resolved_path as a hint, raw prefixes as the
 *     fallback.
 *   - GRP_TXT channel hash → name resolution (enc_ placeholder, SHA-256 byte
 *     match for keyed channels, fallback to "channel 0x<HEX>").
 *
 * The deep-link loader is a giant async function; we don't run it end-to-end.
 * Instead we verify:
 *   1. Source invariants: the deep link infers the chosen observation.
 *   2. The real inference helper, with and without a resolved_path hint.
 *   3. Replica of the channel-match predicate (the same `find` callback).
 *   4. Live SubtleCrypto comparison: SHA-256(name)[0] === target byte
 *      reproduced via node's built-in crypto.
 */
'use strict';
const REPO_ROOT = require('path').resolve(__dirname, '..', '..');

const fs = require('fs');
const path = require('path');
const crypto = require('crypto');

let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}

const mapSrc = fs.readFileSync(path.join(REPO_ROOT, 'public', 'map.js'), 'utf8');

console.log('\n=== #1418 hop choice A: the deep link infers the chosen observation ===');
// The chosen observation's hops are inferred as one path (shared with the
// packet detail pane); its resolved_path is a hint inside that inference, and
// raw prefixes are the fallback when the resolver is unavailable.
assert(/inferredHops = inferObservationPath\(chosen,/.test(mapSrc),
  'the deep link infers the chosen observation with inferObservationPath');
assert(/const chosenPath = inferredHops \? [^;]*: rawHops;/.test(mapSrc),
  'chosenPath falls back to the raw prefixes without an inference');
assert(/inferredHops: inferredHops/.test(mapSrc),
  'the inferred hops are handed to the route renderer');

console.log('\n=== #1418 hop choice B: resolved_path is a hint to the inference ===');
{
  const vm = require('vm');
  const ctx = { window: {}, console };
  vm.createContext(ctx);
  for (const f of ['packet-helpers.js', 'hop-resolver.js']) {
    vm.runInContext(fs.readFileSync(path.join(REPO_ROOT, 'public', f), 'utf8'), ctx);
  }
  const W = ctx.window;
  const pad = s => s + '0'.repeat(64 - s.length);
  const near = { public_key: pad('aa01'), name: 'AA-NEAR', role: 'repeater', lat: 40.0, lon: -100 };
  const hinted = { public_key: pad('aa02'), name: 'AA-HINTED', role: 'repeater', lat: 40.2, lon: -100 };
  const bb = { public_key: pad('bb01'), name: 'BB', role: 'repeater', lat: 40.1, lon: -100 };
  W.HopResolver.init([near, hinted, bb]);
  const observer = { lat: 40.0, lon: -100 };
  const infer = rp => W.inferObservationPath({ observer_id: 'O', path_json: '["bb","aa"]', resolved_path: rp }, observer, null)
    .map(e => e.name).join(',');
  assert(infer(null) === 'BB,AA-NEAR', 'no resolved_path: the hop nearest the observer (' + infer(null) + ')');
  assert(infer(JSON.stringify([null, hinted.public_key])) === 'BB,AA-HINTED',
    'a resolved_path entry names that hop; a null entry is inferred');
  assert(infer(JSON.stringify([hinted.public_key])) === 'BB,AA-NEAR',
    'a resolved_path of the wrong length is ignored');
}

console.log('\n=== #1418 channel A: GRP_TXT match predicate (sync part) ===');

// Replica of the channel-find predicate from loadRouteFromDeepLink.
function findChannelSync(chList, wantHex) {
  const wantUp = String(wantHex).toUpperCase();
  return chList.find(c => {
    const ch = String(c.hash || '').toUpperCase();
    const nm = String(c.name || '').toUpperCase();
    return ch.startsWith(wantUp) ||
           ch === 'ENC_' + wantUp ||
           nm.includes('0X' + wantUp);
  }) || null;
}

const channels = [
  { hash: 'public_full_hash_AB...', name: 'Public' },
  { hash: 'enc_77', name: 'Encrypted (0x77)', encrypted: true },
  { hash: 'unknown', name: 'channel 0xCD' }
];

// hash starts with target hex
let m = findChannelSync([{ hash: 'AB1234', name: 'Test' }], 'AB');
assert(m && m.name === 'Test', 'finds channel where hash starts with target hex');

// enc_<HEX> placeholder
m = findChannelSync(channels, '77');
assert(m && m.name === 'Encrypted (0x77)',
  'matches enc_<HEX> placeholder ("enc_77") for encrypted channel');

// name contains "0x<HEX>"
m = findChannelSync(channels, 'CD');
assert(m && m.name === 'channel 0xCD',
  'matches name containing "0x<HEX>" placeholder');

// Case-insensitive
m = findChannelSync([{ hash: 'enc_ff', name: 'lower' }], 'FF');
assert(m && m.name === 'lower', 'case-insensitive match on enc_<HEX>');

// No match → null (caller falls back to "channel 0x<HEX>")
m = findChannelSync(channels, 'XX');
assert(m === null, 'no match → null (so caller renders "channel 0x<HEX>" fallback)');

console.log('\n=== #1418 channel B: SHA-256(name)[0] keyed-channel match ===');

// The async fallback (SubtleCrypto) computes SHA-256(name)[0] and checks
// it against the target byte. Reproduce in node and verify the formula
// matches the firmware/decoder convention (first byte of SHA-256).
function sha256Byte0(name) {
  const buf = crypto.createHash('sha256').update(name, 'utf8').digest();
  return buf[0].toString(16).padStart(2, '0').toUpperCase();
}

// Known channel name → its derived byte
const wellKnown = ['Public', 'Test Channel', 'mesh-control', 'general'];
wellKnown.forEach(name => {
  const byte = sha256Byte0(name);
  assert(/^[0-9A-F]{2}$/.test(byte),
    'SHA-256("' + name + '")[0] = 0x' + byte + ' (valid 2-hex)');
});

// Construct a fixture where we deliberately want to match channel "Public"
const target = sha256Byte0('Public');
// Simulate the async match loop: walk the channel list, hash each name,
// return the one whose first byte === target.
function findChannelAsync(chList, wantHex) {
  const wantUp = String(wantHex).toUpperCase();
  for (const c of chList) {
    if (c.encrypted) continue;
    if (!c.name) continue;
    if (sha256Byte0(c.name) === wantUp) return c;
  }
  return null;
}

const result = findChannelAsync([
  { name: 'Public' },
  { name: 'Other' },
  { name: 'Public', encrypted: true } // would match but encrypted → skipped
], target);
assert(result && result.name === 'Public' && !result.encrypted,
  'SHA-256 match: returns first non-encrypted channel whose name SHA-256[0] === target byte');

// Source invariants: the async block exists in map.js
assert(/window\.crypto\.subtle/.test(mapSrc), 'map.js uses window.crypto.subtle for SHA-256 fallback');
assert(/'SHA-256'/.test(mapSrc), 'map.js requests SHA-256 specifically');
assert(/if\s*\(c\.encrypted\)\s*continue/.test(mapSrc),
  'async loop skips already-known encrypted/placeholder channels');
assert(/byteHex\s*===\s*wantUp/.test(mapSrc),
  'async loop compares first-byte hex to target (byteHex === wantUp)');

console.log('\n=== #1418 channel C: fallback label format ===');
// When no match found, caller renders "Encrypted (0x<HEX>)" for encrypted,
// "channel 0x<HEX>" otherwise. Just guard the literal templates exist.
assert(/Encrypted \(0x/.test(mapSrc),
  'encrypted-channel fallback label "Encrypted (0x..." present in map.js');

console.log('\n=== Summary ===');
console.log('  passed: ' + passed);
console.log('  failed: ' + failed);
if (failed > 0) process.exit(1);

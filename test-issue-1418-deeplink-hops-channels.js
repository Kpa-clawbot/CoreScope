/**
 * #1418 — map.js loadRouteFromDeepLink:
 *   - Hop resolution priority (server resolved_path > HopResolver > raw).
 *   - GRP_TXT channel hash → name resolution (enc_ placeholder, SHA-256 byte
 *     match for keyed channels, fallback to "channel 0x<HEX>").
 *
 * The deep-link loader is a giant async function; we don't run it end-to-end.
 * Instead we verify:
 *   1. Source invariants: priority order is unambiguous in code.
 *   2. Replica of the chosen-path resolution logic, exercised on fixtures.
 *   3. Replica of the channel-match predicate (the same `find` callback).
 *   4. Live SubtleCrypto comparison: SHA-256(name)[0] === target byte
 *      reproduced via node's built-in crypto.
 */
'use strict';

const fs = require('fs');
const path = require('path');
const crypto = require('crypto');

let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}

const mapSrc = fs.readFileSync(path.join(__dirname, 'public', 'map.js'), 'utf8');

console.log('\n=== #1418 hop-priority A: source invariants (3-tier priority) ===');
// Priority comment is documented; assert the structural keywords are in order.
const priorityBlock = mapSrc.match(/Priority:[\s\S]{0,800}rawHops/);
assert(!!priorityBlock,
  'priority block documented in map.js');
if (priorityBlock) {
  const blk = priorityBlock[0];
  const iResolved = blk.indexOf('resolved_path');
  const iHopRes = blk.indexOf('HopResolver');
  const iRaw = blk.indexOf('raw');
  assert(iResolved >= 0 && iHopRes >= 0 && iRaw >= 0,
    'priority block mentions all three: resolved_path, HopResolver, raw');
  assert(iResolved < iHopRes && iHopRes < iRaw,
    'priority order in comment: resolved_path → HopResolver → raw');
}
// Structural code path: resolved_path branch checked first, then HopResolver,
// then naked rawHops fallback.
assert(/if\s*\(\s*Array\.isArray\(resolvedHops\)[^\)]*\)\s*\{[\s\S]{0,200}\}\s*else if\s*\(\s*window\.HopResolver/.test(mapSrc),
  'code structure: if (resolvedHops valid) else if (window.HopResolver) else (rawHops)');

console.log('\n=== #1418 hop-priority B: replica of chosen-path selection ===');

// Replicate the chooseChosenPath logic exactly. window.HopResolver shim
// returns a per-pubkey dict; resolveResult[h] is consulted per raw hop.
function chooseChosenPath(rawHops, resolvedHopsRaw, hopResolver) {
  let resolvedHops = null;
  try {
    if (resolvedHopsRaw) {
      resolvedHops = typeof resolvedHopsRaw === 'string' ? JSON.parse(resolvedHopsRaw) : resolvedHopsRaw;
    }
  } catch (_) {}
  if (Array.isArray(resolvedHops) && resolvedHops.length === rawHops.length) {
    return rawHops.map((h, i) => resolvedHops[i] || h);
  }
  if (hopResolver && typeof hopResolver.resolve === 'function' && rawHops.length) {
    try {
      const result = hopResolver.resolve(rawHops);
      return rawHops.map(h => {
        const r = result ? result[h] : null;
        return r && r.pubkey ? r.pubkey : h;
      });
    } catch (_) { return rawHops; }
  }
  return rawHops;
}

const rawHops = ['AA', 'BB', 'CC'];

// Tier 1: server resolved_path takes priority over HopResolver
const serverResolved = ['AAFULL1', 'BBFULL2', 'CCFULL3'];
const naiveResolver = { resolve: () => ({ AA: { pubkey: 'WRONG_A' }, BB: { pubkey: 'WRONG_B' }, CC: { pubkey: 'WRONG_C' }}) };
let chosen = chooseChosenPath(rawHops, serverResolved, naiveResolver);
assert(JSON.stringify(chosen) === JSON.stringify(serverResolved),
  'server resolved_path wins over HopResolver (returns ' + JSON.stringify(chosen) + ')');

// Tier 1 with JSON string input (server returns it stringified sometimes)
chosen = chooseChosenPath(rawHops, JSON.stringify(serverResolved), naiveResolver);
assert(JSON.stringify(chosen) === JSON.stringify(serverResolved),
  'server resolved_path accepts JSON-string input (parses it)');

// Tier 2: no resolved_path → use HopResolver
const smartResolver = { resolve: () => ({ AA: { pubkey: 'AAFULL_DIFF' }, BB: { pubkey: 'BBFULL_DIFF' }, CC: { pubkey: 'CCFULL_DIFF' }}) };
chosen = chooseChosenPath(rawHops, null, smartResolver);
assert(JSON.stringify(chosen) === JSON.stringify(['AAFULL_DIFF', 'BBFULL_DIFF', 'CCFULL_DIFF']),
  'no resolved_path → HopResolver result used (returns ' + JSON.stringify(chosen) + ')');

// HopResolver returns different from naive prefix → values change
chosen = chooseChosenPath(['AB'], null, { resolve: () => ({ AB: { pubkey: 'ABcorrect123' } }) });
assert(chosen[0] === 'ABcorrect123',
  'HopResolver overrides naive prefix when it returns a longer pubkey');

// HopResolver throws → fallback to raw
chosen = chooseChosenPath(rawHops, null, { resolve: () => { throw new Error('boom'); } });
assert(JSON.stringify(chosen) === JSON.stringify(rawHops),
  'HopResolver throw → fallback to rawHops');

// Tier 3: no resolved_path, no HopResolver → raw prefixes
chosen = chooseChosenPath(rawHops, null, null);
assert(JSON.stringify(chosen) === JSON.stringify(rawHops),
  'no resolved_path AND no HopResolver → raw prefixes returned as-is');

// Length mismatch: resolved_path is wrong length → falls through to HopResolver
chosen = chooseChosenPath(rawHops, ['only_one'], smartResolver);
assert(JSON.stringify(chosen) === JSON.stringify(['AAFULL_DIFF', 'BBFULL_DIFF', 'CCFULL_DIFF']),
  'resolved_path with mismatched length → falls through to HopResolver');

// Per-element falsy in resolved_path → falls back to raw for THAT index
chosen = chooseChosenPath(rawHops, ['AAFULL1', null, 'CCFULL3'], null);
assert(JSON.stringify(chosen) === JSON.stringify(['AAFULL1', 'BB', 'CCFULL3']),
  'per-index null in resolved_path → falls back to raw for that index only');

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

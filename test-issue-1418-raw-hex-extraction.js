/**
 * #1418 — map.js loadRouteFromDeepLink raw_hex byte extraction.
 *
 * The deep-link loader peeks at chosen.raw_hex when decoded JSON is empty,
 * to extract src/destHash and (for GRP_TXT) channel_hash. Wire layout per
 * cmd/ingestor/decoder.go:
 *   byte0=route+type, byte1=path_len, then path bytes, then ...
 *
 *   TXT_MSG (type 2):  destHash + srcHash bytes after path
 *   RESPONSE (type 1): destHash + srcHash bytes after path
 *   ANON_REQ (type 7): destHash ONLY (no srcHash byte — sender anonymous)
 *   PATH (type 8):     destHash + srcHash bytes after path
 *   GRP_TXT (type 5):  channel_hash byte after path
 *
 * This test asserts behavior by replicating the exact extraction logic
 * from public/map.js and exercising it on hand-built raw_hex fixtures
 * built to mirror real wire packets.
 *
 * Source invariants (string grep on map.js) also guarded so any code-move
 * that drops the extraction is caught.
 */
'use strict';

const fs = require('fs');
const path = require('path');

let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}

const mapSrc = fs.readFileSync(path.join(__dirname, 'public', 'map.js'), 'utf8');

console.log('\n=== #1418 raw_hex A: source invariants in map.js ===');
assert(/TYPES_WITH_DST_SRC\s*=\s*\[\s*1\s*,\s*2\s*,\s*7\s*,\s*8\s*\]/.test(mapSrc),
  'TYPES_WITH_DST_SRC = [1, 2, 7, 8] (RESPONSE, TXT_MSG, ANON_REQ, PATH)');
assert(/payload_type\s*!==\s*7/.test(mapSrc),
  'ANON_REQ (type 7) special-cased to skip srcHash extraction');
assert(/payload_type\s*===\s*5/.test(mapSrc),
  'GRP_TXT (type 5) branch present for channel_hash extraction');
assert(/PAYLOAD_TYPE_MAP\s*=\s*\{[^}]*0:\s*'REQ'[^}]*1:\s*'RESPONSE'[^}]*2:\s*'TXT_MSG'/m.test(mapSrc),
  'PAYLOAD_TYPE_MAP covers 0=REQ, 1=RESPONSE, 2=TXT_MSG');
assert(/5:\s*'GRP_TXT'[^}]*7:\s*'ANON_REQ'[^}]*8:\s*'PATH'/m.test(mapSrc),
  'PAYLOAD_TYPE_MAP covers 5=GRP_TXT, 7=ANON_REQ, 8=PATH');

// Polish review (djb): pathLen MUST be bounded before slicing. A crafted
// pathLen=200 byte would surface random body bytes as srcHash/destHash.
// Cap at MeshCore wire max of 64 hops in BOTH the TXT-family branch and
// the GRP_TXT channel-hash branch.
assert((mapSrc.match(/pathLen[^>]*>\s*64/g) || []).length >= 2,
  'raw_hex pathLen capped at >64 in both TXT and GRP_TXT branches (#1423 review/djb)');
assert(/Number\.isFinite\(pathLen\)/.test(mapSrc),
  'raw_hex pathLen guarded with Number.isFinite (rejects NaN from non-hex byte)');

console.log('\n=== #1418 raw_hex B: replica extractor reproduces map.js logic ===');

// Pure replica of the extractor inside loadRouteFromDeepLink. If map.js's
// logic changes, this replica MUST be updated and the diff explained.
function extractSrcDst(rawHex, payloadType) {
  const TYPES = [1, 2, 7, 8];
  if (TYPES.indexOf(payloadType) < 0) return { src: null, dst: null };
  try {
    const pathLen = parseInt(rawHex.slice(2, 4), 16);
    if (!Number.isFinite(pathLen) || pathLen < 0 || pathLen > 64) {
      return { src: null, dst: null };
    }
    const destOff = 4 + pathLen * 2;
    if (rawHex.length < destOff + 2) return { src: null, dst: null };
    const dst = rawHex.slice(destOff, destOff + 2).toUpperCase();
    let src = null;
    if (payloadType !== 7 && rawHex.length >= destOff + 4) {
      src = rawHex.slice(destOff + 2, destOff + 4).toUpperCase();
    }
    return { src, dst };
  } catch (_) { return { src: null, dst: null }; }
}

function extractChannelHash(rawHex, payloadType) {
  if (payloadType !== 5) return null;
  try {
    const pathLen = parseInt(rawHex.slice(2, 4), 16);
    if (!Number.isFinite(pathLen) || pathLen < 0 || pathLen > 64) return null;
    const chOff = 4 + pathLen * 2;
    if (rawHex.length < chOff + 2) return null;
    return rawHex.slice(chOff, chOff + 2).toUpperCase();
  } catch (_) { return null; }
}

// Build a hex string: route+type byte, path_len, path bytes, then payload.
function build(routeType, pathBytes, payloadBytes) {
  const lenHex = pathBytes.length.toString(16).padStart(2, '0');
  return routeType + lenHex + pathBytes.join('') + payloadBytes.join('');
}

// Fixture 1: TXT_MSG (type 2), 2 path hops AB,CD, destHash=42, srcHash=99
const txt = build('02', ['AB', 'CD'], ['42', '99', 'FF', 'EE']);
let r = extractSrcDst(txt, 2);
assert(r.dst === '42' && r.src === '99',
  'TXT_MSG (type 2) extracts destHash=42, srcHash=99 after 2-hop path (got dst=' + r.dst + ', src=' + r.src + ')');

// Fixture 2: RESPONSE (type 1), 0-hop path
const resp = build('01', [], ['7A', '3C']);
r = extractSrcDst(resp, 1);
assert(r.dst === '7A' && r.src === '3C',
  'RESPONSE (type 1) extracts destHash + srcHash on 0-hop path (got dst=' + r.dst + ', src=' + r.src + ')');

// Fixture 3: ANON_REQ (type 7) — destHash present, srcHash MUST be null
const anon = build('07', ['11'], ['DD', 'BB', 'CC']);
r = extractSrcDst(anon, 7);
assert(r.dst === 'DD', 'ANON_REQ (type 7) extracts destHash=DD');
assert(r.src === null, 'ANON_REQ (type 7) MUST NOT extract srcHash (anonymous sender) — got ' + r.src);

// Fixture 4: PATH (type 8) carries both hashes
const pathPkt = build('08', ['AA', 'BB', 'CC'], ['11', '22']);
r = extractSrcDst(pathPkt, 8);
assert(r.dst === '11' && r.src === '22',
  'PATH (type 8) extracts destHash + srcHash after 3-hop path (got dst=' + r.dst + ', src=' + r.src + ')');

// Fixture 5: GRP_TXT (type 5) — channel_hash extraction, NOT src/dst
const grp = build('05', ['77'], ['AB', 'XX']);
const ch = extractChannelHash(grp, 5);
assert(ch === 'AB', 'GRP_TXT (type 5) extracts channel_hash=AB after 1-hop path (got ' + ch + ')');
r = extractSrcDst(grp, 5);
assert(r.src === null && r.dst === null,
  'GRP_TXT (type 5) is NOT in TYPES_WITH_DST_SRC — extractor returns nulls');

// Fixture 6: non-extracting types (REQ=0, ACK=3, ADVERT=4, MULTIPART=10, …)
[0, 3, 4, 6, 9, 10, 11, 12].forEach(function (pt) {
  r = extractSrcDst('00' + '00' + 'FFFF', pt);
  assert(r.src === null && r.dst === null,
    'payload_type=' + pt + ' (not in TYPES_WITH_DST_SRC) → no extraction');
});

// Edge case: raw_hex too short (path length claims more bytes than present)
r = extractSrcDst('02' + '04' + 'AB', 2); // claims 4-hop path, only 1 byte payload
assert(r.src === null && r.dst === null, 'truncated raw_hex → null extraction (no crash)');

// Polish review (djb): malicious pathLen=200 (0xC8) MUST be rejected even
// when the body is long enough to slice. Without the cap, the extractor
// would surface random body bytes as src/destHash strings in the UI.
const evil = '02' + 'C8' + 'AB'.repeat(500); // pathLen=200, plenty of body to slice
r = extractSrcDst(evil, 2);
assert(r.src === null && r.dst === null,
  'malicious pathLen=200 → rejected, no OOB-style byte surfacing');
const evilCh = extractChannelHash('05' + 'C8' + 'AB'.repeat(500), 5);
assert(evilCh === null, 'malicious pathLen=200 (GRP_TXT) → rejected');
// Boundary: pathLen=64 (max) still works; 65 rejected.
const okBig = '02' + '40' + 'AB'.repeat(64) + 'EE' + 'FF';
r = extractSrcDst(okBig, 2);
assert(r.dst === 'EE' && r.src === 'FF', 'pathLen=64 (max allowed) still extracts');
const tooBig = '02' + '41' + 'AB'.repeat(65) + 'EE' + 'FF';
r = extractSrcDst(tooBig, 2);
assert(r.src === null && r.dst === null, 'pathLen=65 → rejected (above wire max of 64)');

console.log('\n=== #1418 raw_hex C: channel_hash NOT extracted for non-GRP_TXT ===');
[0, 1, 2, 3, 4, 6, 7, 8, 9, 10, 11, 12].forEach(function (pt) {
  const v = extractChannelHash('05' + '00' + 'AB', pt);
  assert(v === null, 'payload_type=' + pt + ' returns null channel_hash');
});

console.log('\n=== Summary ===');
console.log('  passed: ' + passed);
console.log('  failed: ' + failed);
if (failed > 0) process.exit(1);

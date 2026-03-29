'use strict';

const helpers = require('./server-helpers');
const path = require('path');
const fs = require('fs');
const os = require('os');

let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log(`  ✅ ${msg}`); }
  else { failed++; console.error(`  ❌ ${msg}`); }
}

console.log('── server-helpers tests ──\n');

// --- loadConfigFile ---
console.log('loadConfigFile:');
{
  // Returns {} when no files exist
  const result = helpers.loadConfigFile(['/nonexistent/path.json']);
  assert(typeof result === 'object' && Object.keys(result).length === 0, 'returns {} for missing files');

  // Loads valid JSON
  const tmp = path.join(os.tmpdir(), `test-config-${Date.now()}.json`);
  fs.writeFileSync(tmp, JSON.stringify({ hello: 'world' }));
  const result2 = helpers.loadConfigFile([tmp]);
  assert(result2.hello === 'world', 'loads valid JSON file');
  fs.unlinkSync(tmp);

  // Falls back to second path
  const tmp2 = path.join(os.tmpdir(), `test-config2-${Date.now()}.json`);
  fs.writeFileSync(tmp2, JSON.stringify({ fallback: true }));
  const result3 = helpers.loadConfigFile(['/nonexistent.json', tmp2]);
  assert(result3.fallback === true, 'falls back to second path');
  fs.unlinkSync(tmp2);

  // Handles malformed JSON
  const tmp3 = path.join(os.tmpdir(), `test-config3-${Date.now()}.json`);
  fs.writeFileSync(tmp3, 'not json{{{');
  const result4 = helpers.loadConfigFile([tmp3]);
  assert(Object.keys(result4).length === 0, 'returns {} for malformed JSON');
  fs.unlinkSync(tmp3);
}

// --- loadThemeFile ---
console.log('\nloadThemeFile:');
{
  const result = helpers.loadThemeFile(['/nonexistent/theme.json']);
  assert(typeof result === 'object' && Object.keys(result).length === 0, 'returns {} for missing files');

  const tmp = path.join(os.tmpdir(), `test-theme-${Date.now()}.json`);
  fs.writeFileSync(tmp, JSON.stringify({ theme: { accent: '#ff0000' } }));
  const result2 = helpers.loadThemeFile([tmp]);
  assert(result2.theme.accent === '#ff0000', 'loads theme file');
  fs.unlinkSync(tmp);
}

// --- buildHealthConfig ---
console.log('\nbuildHealthConfig:');
{
  const h = helpers.buildHealthConfig({});
  assert(h.infraDegraded === 24, 'default infraDegraded');
  assert(h.infraSilent === 72, 'default infraSilent');
  assert(h.nodeDegraded === 1, 'default nodeDegraded');
  assert(h.nodeSilent === 24, 'default nodeSilent');

  const h2 = helpers.buildHealthConfig({ healthThresholds: { infraDegradedHours: 2 } });
  assert(h2.infraDegraded === 2, 'custom infraDegraded');
  assert(h2.nodeDegraded === 1, 'other defaults preserved');

  const h3 = helpers.buildHealthConfig(null);
  assert(h3.infraDegraded === 24, 'handles null config');
}

// --- getHealthMs ---
console.log('\ngetHealthMs:');
{
  const HEALTH = helpers.buildHealthConfig({});

  const rep = helpers.getHealthMs('repeater', HEALTH);
  assert(rep.degradedMs === 24 * 3600000, 'repeater uses infra degraded');
  assert(rep.silentMs === 72 * 3600000, 'repeater uses infra silent');

  const room = helpers.getHealthMs('room', HEALTH);
  assert(room.degradedMs === 24 * 3600000, 'room uses infra degraded');

  const comp = helpers.getHealthMs('companion', HEALTH);
  assert(comp.degradedMs === 1 * 3600000, 'companion uses node degraded');
  assert(comp.silentMs === 24 * 3600000, 'companion uses node silent');

  const sensor = helpers.getHealthMs('sensor', HEALTH);
  assert(sensor.degradedMs === 1 * 3600000, 'sensor uses node degraded');

  const undef = helpers.getHealthMs(undefined, HEALTH);
  assert(undef.degradedMs === 1 * 3600000, 'undefined role uses node degraded');
}

// --- isHashSizeFlipFlop ---
console.log('\nisHashSizeFlipFlop:');
{
  assert(helpers.isHashSizeFlipFlop(null, null) === false, 'null seq returns false');
  assert(helpers.isHashSizeFlipFlop([1, 2], new Set([1, 2])) === false, 'too few samples');
  assert(helpers.isHashSizeFlipFlop([1, 1, 1], new Set([1])) === false, 'single size');
  assert(helpers.isHashSizeFlipFlop([1, 1, 1, 2, 2, 2], new Set([1, 2])) === false, 'clean upgrade (1 transition)');
  assert(helpers.isHashSizeFlipFlop([1, 2, 1], new Set([1, 2])) === true, 'flip-flop detected');
  assert(helpers.isHashSizeFlipFlop([1, 2, 1, 2], new Set([1, 2])) === true, 'repeated flip-flop');
  assert(helpers.isHashSizeFlipFlop([2, 1, 2], new Set([1, 2])) === true, 'reverse flip-flop');
  assert(helpers.isHashSizeFlipFlop([1, 2, 3], new Set([1, 2, 3])) === true, 'three sizes, 2 transitions');
}

// --- computeContentHash ---
console.log('\ncomputeContentHash:');
{
  // Minimal packet: header + path byte + payload
  // header=0x04, path_byte=0x00 (hash_size=1, 0 hops), payload=0xABCD
  const hex1 = '0400abcd';
  const h1 = helpers.computeContentHash(hex1);
  assert(typeof h1 === 'string' && h1.length === 16, 'returns 16-char hash');

  // Same payload, different path should give same hash
  // header=0x04, path_byte=0x41 (hash_size=2, 1 hop), path=0x1234, payload=0xABCD
  const hex2 = '04411234abcd';
  const h2 = helpers.computeContentHash(hex2);
  assert(h1 === h2, 'same content different path = same hash');

  // Different payload = different hash
  const hex3 = '0400ffff';
  const h3 = helpers.computeContentHash(hex3);
  assert(h3 !== h1, 'different payload = different hash');

  // Very short hex
  const h4 = helpers.computeContentHash('04');
  assert(h4 === '04', 'short hex returns prefix');

  // Invalid hex
  const h5 = helpers.computeContentHash('xyz');
  assert(typeof h5 === 'string', 'handles invalid hex gracefully');
}

// --- geoDist ---
console.log('\ngeoDist:');
{
  assert(helpers.geoDist(0, 0, 0, 0) === 0, 'same point = 0');
  assert(helpers.geoDist(0, 0, 3, 4) === 5, 'pythagorean triple');
  assert(helpers.geoDist(37.7749, -122.4194, 37.7749, -122.4194) === 0, 'SF to SF = 0');
  const d = helpers.geoDist(37.0, -122.0, 38.0, -122.0);
  assert(Math.abs(d - 1.0) < 0.001, '1 degree latitude diff');
}

// --- deriveHashtagChannelKey ---
console.log('\nderiveHashtagChannelKey:');
{
  const k1 = helpers.deriveHashtagChannelKey('test');
  assert(typeof k1 === 'string' && k1.length === 32, 'returns 32-char key');
  const k2 = helpers.deriveHashtagChannelKey('test');
  assert(k1 === k2, 'deterministic');
  const k3 = helpers.deriveHashtagChannelKey('other');
  assert(k3 !== k1, 'different input = different key');
}

// --- buildBreakdown ---
console.log('\nbuildBreakdown:');
{
  const r1 = helpers.buildBreakdown(null, null, null, null);
  assert(JSON.stringify(r1) === '{}', 'null rawHex returns empty');

  const r2 = helpers.buildBreakdown('04', null, null, null);
  assert(r2.ranges.length === 1, 'single-byte returns header only');
  assert(r2.ranges[0].label === 'Header', 'header range');

  // 2 bytes: header + path byte, no payload
  const r3 = helpers.buildBreakdown('0400', null, null, null);
  assert(r3.ranges.length === 2, 'two bytes: header + path length');
  assert(r3.ranges[1].label === 'Path Length', 'path length range');

  // With payload: header=04, path_byte=00, payload=abcd
  const r4 = helpers.buildBreakdown('0400abcd', null, null, null);
  assert(r4.ranges.some(r => r.label === 'Payload'), 'has payload range');

  // With path hops: header=04, path_byte=0x41 (size=2, count=1), path=1234, payload=ff
  const r5 = helpers.buildBreakdown('04411234ff', null, null, null);
  assert(r5.ranges.some(r => r.label === 'Path'), 'has path range');

  // ADVERT with enough payload
  // flags=0x90 (0x10=GPS + 0x80=Name)
  const advertHex = '0400' + 'aa'.repeat(32) + 'bb'.repeat(4) + 'cc'.repeat(64) + '90' + 'dddddddddddddddd' + '48656c6c6f';
  const r6 = helpers.buildBreakdown(advertHex, { type: 'ADVERT' }, null, null);
  assert(r6.ranges.some(r => r.label === 'PubKey'), 'ADVERT has PubKey sub-range');
  assert(r6.ranges.some(r => r.label === 'Flags'), 'ADVERT has Flags sub-range');
  assert(r6.ranges.some(r => r.label === 'Latitude'), 'ADVERT with GPS flag has Latitude');
  assert(r6.ranges.some(r => r.label === 'Name'), 'ADVERT with name flag has Name');
}

// --- disambiguateHops ---
console.log('\ndisambiguateHops:');
{
  const nodes = [
    { public_key: 'aabb11223344', name: 'Node-A', lat: 37.0, lon: -122.0 },
    { public_key: 'ccdd55667788', name: 'Node-C', lat: 37.1, lon: -122.1 },
  ];
  // Single unique match
  const r1 = helpers.disambiguateHops(['aabb'], nodes);
  assert(r1.length === 1, 'resolves single hop');
  assert(r1[0].name === 'Node-A', 'resolves to correct node');
  assert(r1[0].pubkey === 'aabb11223344', 'includes pubkey');

  // Unknown hop
  delete nodes._prefixIdx; delete nodes._prefixIdxName;
  const r2 = helpers.disambiguateHops(['ffff'], nodes);
  assert(r2[0].name === 'ffff', 'unknown hop uses hex as name');

  // Multiple hops
  delete nodes._prefixIdx; delete nodes._prefixIdxName;
  const r3 = helpers.disambiguateHops(['aabb', 'ccdd'], nodes);
  assert(r3.length === 2, 'resolves multiple hops');
  assert(r3[0].name === 'Node-A' && r3[1].name === 'Node-C', 'both resolved');
}

// --- updateHashSizeForPacket ---
console.log('\nupdateHashSizeForPacket:');
{
  const map = new Map(), allMap = new Map(), seqMap = new Map();

  // ADVERT packet (payload_type=4)
  // path byte 0x40 = hash_size 2 (bits 7-6 = 01)
  const p1 = {
    payload_type: 4,
    raw_hex: '0440' + 'aa'.repeat(100),
    decoded_json: JSON.stringify({ pubKey: 'abc123' }),
    path_json: null
  };
  helpers.updateHashSizeForPacket(p1, map, allMap, seqMap);
  assert(map.get('abc123') === 2, 'ADVERT sets hash_size=2');
  assert(allMap.get('abc123').has(2), 'all map has size 2');
  assert(seqMap.get('abc123')[0] === 2, 'seq map records size');

  // Non-ADVERT with path_json fallback
  const map2 = new Map(), allMap2 = new Map(), seqMap2 = new Map();
  const p2 = {
    payload_type: 1,
    raw_hex: '0140ff',  // path byte 0x40 = hash_size 2
    decoded_json: JSON.stringify({ pubKey: 'def456' }),
    path_json: JSON.stringify(['aabb'])
  };
  helpers.updateHashSizeForPacket(p2, map2, allMap2, seqMap2);
  assert(map2.get('def456') === 2, 'non-ADVERT falls back to path byte');

  // Already-parsed decoded_json (object, not string)
  const map3 = new Map(), allMap3 = new Map(), seqMap3 = new Map();
  const p3 = {
    payload_type: 4,
    raw_hex: '04c0' + 'aa'.repeat(100),  // 0xC0 = bits 7-6 = 11 = hash_size 4
    decoded_json: { pubKey: 'ghi789' },
    path_json: null
  };
  helpers.updateHashSizeForPacket(p3, map3, allMap3, seqMap3);
  assert(map3.get('ghi789') === 4, 'handles object decoded_json');
}

// --- rebuildHashSizeMap ---
console.log('\nrebuildHashSizeMap:');
{
  const map = new Map(), allMap = new Map(), seqMap = new Map();
  const packets = [
    // Newest first (as packet store provides)
    { payload_type: 4, raw_hex: '0480' + 'bb'.repeat(50), decoded_json: JSON.stringify({ pubKey: 'node1' }), path_json: null },
    { payload_type: 4, raw_hex: '0440' + 'aa'.repeat(50), decoded_json: JSON.stringify({ pubKey: 'node1' }), path_json: null },
  ];
  helpers.rebuildHashSizeMap(packets, map, allMap, seqMap);
  assert(map.get('node1') === 3, 'first seen (newest) wins for map');
  assert(allMap.get('node1').size === 2, 'all map has both sizes');
  // Seq should be reversed to chronological: [2, 3]
  const seq = seqMap.get('node1');
  assert(seq[0] === 2 && seq[1] === 3, 'sequence is chronological (reversed)');

  // Pass 2 fallback: node without advert
  const map2 = new Map(), allMap2 = new Map(), seqMap2 = new Map();
  const packets2 = [
    { payload_type: 1, raw_hex: '0140ff', decoded_json: JSON.stringify({ pubKey: 'node2' }), path_json: JSON.stringify(['aabb']) },
  ];
  helpers.rebuildHashSizeMap(packets2, map2, allMap2, seqMap2);
  assert(map2.get('node2') === 2, 'pass 2 fallback from path');
}

// --- requireApiKey ---
console.log('\nrequireApiKey:');
{
  // No API key configured
  const mw1 = helpers.requireApiKey(null);
  let nextCalled = false;
  mw1({headers: {}, query: {}}, {}, () => { nextCalled = true; });
  assert(nextCalled, 'no key configured = passes through');

  // Valid key
  const mw2 = helpers.requireApiKey('secret123');
  nextCalled = false;
  mw2({headers: {'x-api-key': 'secret123'}, query: {}}, {}, () => { nextCalled = true; });
  assert(nextCalled, 'valid header key passes');

  // Valid key via query
  nextCalled = false;
  mw2({headers: {}, query: {apiKey: 'secret123'}}, {}, () => { nextCalled = true; });
  assert(nextCalled, 'valid query key passes');

  // Invalid key
  let statusCode = null, jsonBody = null;
  const mockRes = {
    status(code) { statusCode = code; return { json(body) { jsonBody = body; } }; }
  };
  nextCalled = false;
  mw2({headers: {'x-api-key': 'wrong'}, query: {}}, mockRes, () => { nextCalled = true; });
  assert(!nextCalled && statusCode === 401, 'invalid key returns 401');
}

console.log(`\n═══════════════════════════════════════`);
console.log(`  PASSED: ${passed}`);
console.log(`  FAILED: ${failed}`);
console.log(`═══════════════════════════════════════`);
if (failed > 0) process.exit(1);

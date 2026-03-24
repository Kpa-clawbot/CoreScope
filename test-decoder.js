/* Unit tests for decoder.js */
'use strict';
const assert = require('assert');
const { decodePacket, validateAdvert, ROUTE_TYPES, PAYLOAD_TYPES, VALID_ROLES } = require('./decoder');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log(`  ✅ ${name}`); }
  catch (e) { failed++; console.log(`  ❌ ${name}: ${e.message}`); }
}

// === Constants ===
console.log('\n=== Constants ===');
test('ROUTE_TYPES has 4 entries', () => assert.strictEqual(Object.keys(ROUTE_TYPES).length, 4));
test('PAYLOAD_TYPES has 9 entries', () => assert.strictEqual(Object.keys(PAYLOAD_TYPES).length, 9));
test('VALID_ROLES has repeater, companion, room, sensor', () => {
  for (const r of ['repeater', 'companion', 'room', 'sensor']) assert(VALID_ROLES.has(r));
});

// === Header decoding ===
console.log('\n=== Header decoding ===');
test('FLOOD + ADVERT = 0x11', () => {
  const p = decodePacket('1100' + '00'.repeat(101));
  assert.strictEqual(p.header.routeType, 1);
  assert.strictEqual(p.header.routeTypeName, 'FLOOD');
  assert.strictEqual(p.header.payloadType, 4);
  assert.strictEqual(p.header.payloadTypeName, 'ADVERT');
});

test('TRANSPORT_FLOOD = routeType 0', () => {
  // 0x00 = TRANSPORT_FLOOD + REQ(0), needs transport codes + 16 byte payload
  const hex = '0000' + 'AABB' + 'CCDD' + '00'.repeat(16);
  const p = decodePacket(hex);
  assert.strictEqual(p.header.routeType, 0);
  assert.strictEqual(p.header.routeTypeName, 'TRANSPORT_FLOOD');
  assert.notStrictEqual(p.transportCodes, null);
  assert.strictEqual(p.transportCodes.nextHop, 'AABB');
  assert.strictEqual(p.transportCodes.lastHop, 'CCDD');
});

test('TRANSPORT_DIRECT = routeType 3', () => {
  const hex = '0300' + '1122' + '3344' + '00'.repeat(16);
  const p = decodePacket(hex);
  assert.strictEqual(p.header.routeType, 3);
  assert.strictEqual(p.header.routeTypeName, 'TRANSPORT_DIRECT');
  assert.strictEqual(p.transportCodes.nextHop, '1122');
});

test('DIRECT = routeType 2, no transport codes', () => {
  const hex = '0200' + '00'.repeat(16);
  const p = decodePacket(hex);
  assert.strictEqual(p.header.routeType, 2);
  assert.strictEqual(p.header.routeTypeName, 'DIRECT');
  assert.strictEqual(p.transportCodes, null);
});

test('payload version extracted', () => {
  // 0xC1 = 11_0000_01 → version=3, payloadType=0, routeType=1
  const hex = 'C100' + '00'.repeat(16);
  const p = decodePacket(hex);
  assert.strictEqual(p.header.payloadVersion, 3);
});

// === Path decoding ===
console.log('\n=== Path decoding ===');
test('hashSize=1, hashCount=3', () => {
  // pathByte = 0x03 → (0>>6)+1=1, 3&0x3F=3
  const hex = '1103' + 'AABBCC' + '00'.repeat(101);
  const p = decodePacket(hex);
  assert.strictEqual(p.path.hashSize, 1);
  assert.strictEqual(p.path.hashCount, 3);
  assert.strictEqual(p.path.hops.length, 3);
  assert.strictEqual(p.path.hops[0], 'AA');
  assert.strictEqual(p.path.hops[1], 'BB');
  assert.strictEqual(p.path.hops[2], 'CC');
});

test('hashSize=2, hashCount=2', () => {
  // pathByte = 0x42 → (1>>0=1)+1=2, 2&0x3F=2
  const hex = '1142' + 'AABB' + 'CCDD' + '00'.repeat(101);
  const p = decodePacket(hex);
  assert.strictEqual(p.path.hashSize, 2);
  assert.strictEqual(p.path.hashCount, 2);
  assert.strictEqual(p.path.hops[0], 'AABB');
  assert.strictEqual(p.path.hops[1], 'CCDD');
});

test('hashSize=4 from pathByte 0xC1', () => {
  // 0xC1 = 11_000001 → hashSize=(3)+1=4, hashCount=1
  const hex = '11C1' + 'DEADBEEF' + '00'.repeat(101);
  const p = decodePacket(hex);
  assert.strictEqual(p.path.hashSize, 4);
  assert.strictEqual(p.path.hashCount, 1);
  assert.strictEqual(p.path.hops[0], 'DEADBEEF');
});

test('zero hops', () => {
  const hex = '1100' + '00'.repeat(101);
  const p = decodePacket(hex);
  assert.strictEqual(p.path.hashCount, 0);
  assert.strictEqual(p.path.hops.length, 0);
});

// === Payload types ===
console.log('\n=== ADVERT payload ===');
test('ADVERT with name and location', () => {
  const pkt = decodePacket(
    '11451000D818206D3AAC152C8A91F89957E6D30CA51F36E28790228971C473B755F244F718754CF5EE4A2FD58D944466E42CDED140C66D0CC590183E32BAF40F112BE8F3F2BDF6012B4B2793C52F1D36F69EE054D9A05593286F78453E56C0EC4A3EB95DDA2A7543FCCC00B939CACC009278603902FC12BCF84B706120526F6F6620536F6C6172'
  );
  assert.strictEqual(pkt.payload.type, 'ADVERT');
  assert.strictEqual(pkt.payload.name, 'Kpa Roof Solar');
  assert(pkt.payload.pubKey.length === 64);
  assert(pkt.payload.timestamp > 0);
  assert(pkt.payload.timestampISO);
  assert(pkt.payload.signature.length === 128);
});

test('ADVERT flags: chat type=1', () => {
  const pubKey = 'AB'.repeat(32);
  const ts = '01000000';
  const sig = 'CC'.repeat(64);
  const flags = '01'; // type=1 → chat
  const hex = '1100' + pubKey + ts + sig + flags;
  const p = decodePacket(hex);
  assert.strictEqual(p.payload.flags.type, 1);
  assert.strictEqual(p.payload.flags.chat, true);
  assert.strictEqual(p.payload.flags.repeater, false);
});

test('ADVERT flags: repeater type=2', () => {
  const pubKey = 'AB'.repeat(32);
  const ts = '01000000';
  const sig = 'CC'.repeat(64);
  const flags = '02';
  const hex = '1100' + pubKey + ts + sig + flags;
  const p = decodePacket(hex);
  assert.strictEqual(p.payload.flags.type, 2);
  assert.strictEqual(p.payload.flags.repeater, true);
});

test('ADVERT flags: room type=3', () => {
  const pubKey = 'AB'.repeat(32);
  const ts = '01000000';
  const sig = 'CC'.repeat(64);
  const flags = '03';
  const hex = '1100' + pubKey + ts + sig + flags;
  const p = decodePacket(hex);
  assert.strictEqual(p.payload.flags.type, 3);
  assert.strictEqual(p.payload.flags.room, true);
});

test('ADVERT flags: sensor type=4', () => {
  const pubKey = 'AB'.repeat(32);
  const ts = '01000000';
  const sig = 'CC'.repeat(64);
  const flags = '04';
  const hex = '1100' + pubKey + ts + sig + flags;
  const p = decodePacket(hex);
  assert.strictEqual(p.payload.flags.type, 4);
  assert.strictEqual(p.payload.flags.sensor, true);
});

test('ADVERT flags: hasLocation', () => {
  const pubKey = 'AB'.repeat(32);
  const ts = '01000000';
  const sig = 'CC'.repeat(64);
  // flags=0x12 → type=2(repeater), hasLocation=true
  const flags = '12';
  const lat = '40420f00'; // 1000000 → 1.0 degrees
  const lon = '80841e00'; // 2000000 → 2.0 degrees
  const hex = '1100' + pubKey + ts + sig + flags + lat + lon;
  const p = decodePacket(hex);
  assert.strictEqual(p.payload.flags.hasLocation, true);
  assert.strictEqual(p.payload.lat, 1.0);
  assert.strictEqual(p.payload.lon, 2.0);
});

test('ADVERT flags: hasName', () => {
  const pubKey = 'AB'.repeat(32);
  const ts = '01000000';
  const sig = 'CC'.repeat(64);
  // flags=0x82 → type=2(repeater), hasName=true
  const flags = '82';
  const name = Buffer.from('MyNode').toString('hex');
  const hex = '1100' + pubKey + ts + sig + flags + name;
  const p = decodePacket(hex);
  assert.strictEqual(p.payload.flags.hasName, true);
  assert.strictEqual(p.payload.name, 'MyNode');
});

test('ADVERT too short', () => {
  const hex = '1100' + '00'.repeat(50);
  const p = decodePacket(hex);
  assert(p.payload.error);
});

console.log('\n=== GRP_TXT payload ===');
test('GRP_TXT basic decode', () => {
  // payloadType=5 → (5<<2)|1 = 0x15
  const hex = '1500' + 'FF' + 'AABB' + 'CCDDEE';
  const p = decodePacket(hex);
  assert.strictEqual(p.payload.type, 'GRP_TXT');
  assert.strictEqual(p.payload.channelHash, 0xFF);
  assert.strictEqual(p.payload.mac, 'aabb');
});

test('GRP_TXT too short', () => {
  const hex = '1500' + 'FF' + 'AA';
  const p = decodePacket(hex);
  assert(p.payload.error);
});

console.log('\n=== TXT_MSG payload ===');
test('TXT_MSG decode', () => {
  // payloadType=2 → (2<<2)|1 = 0x09
  const hex = '0900' + '00'.repeat(20);
  const p = decodePacket(hex);
  assert.strictEqual(p.payload.type, 'TXT_MSG');
  assert(p.payload.destHash);
  assert(p.payload.srcHash);
  assert(p.payload.mac);
});

console.log('\n=== ACK payload ===');
test('ACK decode', () => {
  // payloadType=3 → (3<<2)|1 = 0x0D
  const hex = '0D00' + '00'.repeat(18);
  const p = decodePacket(hex);
  assert.strictEqual(p.payload.type, 'ACK');
  assert(p.payload.destHash);
  assert(p.payload.srcHash);
  assert(p.payload.extraHash);
});

test('ACK too short', () => {
  const hex = '0D00' + '00'.repeat(10);
  const p = decodePacket(hex);
  assert(p.payload.error);
});

console.log('\n=== REQ payload ===');
test('REQ decode', () => {
  // payloadType=0 → (0<<2)|1 = 0x01
  const hex = '0100' + '00'.repeat(20);
  const p = decodePacket(hex);
  assert.strictEqual(p.payload.type, 'REQ');
});

console.log('\n=== RESPONSE payload ===');
test('RESPONSE decode', () => {
  // payloadType=1 → (1<<2)|1 = 0x05
  const hex = '0500' + '00'.repeat(20);
  const p = decodePacket(hex);
  assert.strictEqual(p.payload.type, 'RESPONSE');
});

console.log('\n=== ANON_REQ payload ===');
test('ANON_REQ decode', () => {
  // payloadType=7 → (7<<2)|1 = 0x1D
  const hex = '1D00' + '00'.repeat(50);
  const p = decodePacket(hex);
  assert.strictEqual(p.payload.type, 'ANON_REQ');
  assert(p.payload.destHash);
  assert(p.payload.ephemeralPubKey);
  assert(p.payload.mac);
});

test('ANON_REQ too short', () => {
  const hex = '1D00' + '00'.repeat(20);
  const p = decodePacket(hex);
  assert(p.payload.error);
});

console.log('\n=== PATH payload ===');
test('PATH decode', () => {
  // payloadType=8 → (8<<2)|1 = 0x21
  const hex = '2100' + '00'.repeat(20);
  const p = decodePacket(hex);
  assert.strictEqual(p.payload.type, 'PATH');
  assert(p.payload.destHash);
  assert(p.payload.srcHash);
});

test('PATH too short', () => {
  const hex = '2100' + '00'.repeat(8);
  const p = decodePacket(hex);
  assert(p.payload.error);
});

console.log('\n=== TRACE payload ===');
test('TRACE decode', () => {
  // payloadType=9 → (9<<2)|1 = 0x25
  const hex = '2500' + '00'.repeat(12);
  const p = decodePacket(hex);
  assert.strictEqual(p.payload.type, 'TRACE');
  assert.strictEqual(p.payload.flags, 0);
  assert(p.payload.tag !== undefined);
  assert(p.payload.destHash);
});

test('TRACE too short', () => {
  const hex = '2500' + '00'.repeat(5);
  const p = decodePacket(hex);
  assert(p.payload.error);
});

console.log('\n=== UNKNOWN payload ===');
test('Unknown payload type', () => {
  // payloadType=6 → (6<<2)|1 = 0x19
  const hex = '1900' + 'DEADBEEF';
  const p = decodePacket(hex);
  assert.strictEqual(p.payload.type, 'UNKNOWN');
  assert(p.payload.raw);
});

// === Edge cases ===
console.log('\n=== Edge cases ===');
test('Packet too short throws', () => {
  assert.throws(() => decodePacket('FF'), /too short/);
});

test('Packet with spaces in hex', () => {
  const hex = '11 00 ' + '00'.repeat(101);
  const p = decodePacket(hex);
  assert.strictEqual(p.header.payloadTypeName, 'ADVERT');
});

test('Transport route too short throws', () => {
  assert.throws(() => decodePacket('0000'), /too short for transport/);
});

// === Real packets from API ===
console.log('\n=== Real packets ===');
test('Real GRP_TXT packet', () => {
  const p = decodePacket('150115D96CFF1FC90E7917B91729B76C1B509AE7789BBBD87D5AC3837E6C1487B47B0958AED8C7A6');
  assert.strictEqual(p.header.payloadTypeName, 'GRP_TXT');
  assert.strictEqual(p.header.routeTypeName, 'FLOOD');
  assert.strictEqual(p.path.hashCount, 1);
});

test('Real ADVERT packet FLOOD with 3 hops', () => {
  const p = decodePacket('11036CEF52206D763E1EACFD52FBAD4EF926887D0694C42A618AAF480A67C41120D3785950EFE0C1');
  assert.strictEqual(p.header.payloadTypeName, 'ADVERT');
  assert.strictEqual(p.header.routeTypeName, 'FLOOD');
  assert.strictEqual(p.path.hashCount, 3);
  assert.strictEqual(p.path.hashSize, 1);
  // Payload is too short for full ADVERT but decoder handles it
  assert.strictEqual(p.payload.type, 'ADVERT');
});

test('Real DIRECT TXT_MSG packet', () => {
  // 0x0A = DIRECT(2) + TXT_MSG(2)
  const p = decodePacket('0A403220AD034C0394C2C449810E3D86399C53AEE7FE355BA67002FFC3627B1175A257A181AE');
  assert.strictEqual(p.header.payloadTypeName, 'TXT_MSG');
  assert.strictEqual(p.header.routeTypeName, 'DIRECT');
});

// === validateAdvert ===
console.log('\n=== validateAdvert ===');
test('valid advert', () => {
  const a = { pubKey: 'AB'.repeat(16), flags: { repeater: true, room: false, sensor: false } };
  assert.deepStrictEqual(validateAdvert(a), { valid: true });
});

test('null advert', () => {
  assert.strictEqual(validateAdvert(null).valid, false);
});

test('advert with error', () => {
  assert.strictEqual(validateAdvert({ error: 'bad' }).valid, false);
});

test('pubkey too short', () => {
  assert.strictEqual(validateAdvert({ pubKey: 'AABB' }).valid, false);
});

test('pubkey all zeros', () => {
  assert.strictEqual(validateAdvert({ pubKey: '0'.repeat(64) }).valid, false);
});

test('invalid lat', () => {
  assert.strictEqual(validateAdvert({ pubKey: 'AB'.repeat(16), lat: 200 }).valid, false);
});

test('invalid lon', () => {
  assert.strictEqual(validateAdvert({ pubKey: 'AB'.repeat(16), lon: -200 }).valid, false);
});

test('name with control chars', () => {
  assert.strictEqual(validateAdvert({ pubKey: 'AB'.repeat(16), name: 'test\x00bad' }).valid, false);
});

test('name too long', () => {
  assert.strictEqual(validateAdvert({ pubKey: 'AB'.repeat(16), name: 'A'.repeat(65) }).valid, false);
});

test('valid name', () => {
  assert.strictEqual(validateAdvert({ pubKey: 'AB'.repeat(16), name: 'My Node' }).valid, true);
});

test('valid lat/lon', () => {
  const r = validateAdvert({ pubKey: 'AB'.repeat(16), lat: 37.3, lon: -121.9 });
  assert.strictEqual(r.valid, true);
});

test('NaN lat invalid', () => {
  assert.strictEqual(validateAdvert({ pubKey: 'AB'.repeat(16), lat: NaN }).valid, false);
});

// === Summary ===
console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);

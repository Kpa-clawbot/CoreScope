/* Unit tests for packet filter language */
'use strict';
const vm = require('vm');
const fs = require('fs');

const code = fs.readFileSync('public/packet-filter.js', 'utf8');
const ctx = { window: {}, console };
vm.createContext(ctx);
vm.runInContext(code, ctx);
const PF = ctx.window.PacketFilter;

let pass = 0, fail = 0;
function test(name, fn) {
  try { fn(); pass++; }
  catch (e) { console.log(`FAIL: ${name} — ${e.message}`); fail++; }
}
function assert(cond, msg) { if (!cond) throw new Error(msg || 'assertion failed'); }

const pkt = {
  route_type: 1, payload_type: 5, snr: 8.5, rssi: -45,
  hash: 'abc123def456', raw_hex: '110500aabbccdd',
  path_json: '["8A","B5","97"]',
  decoded_json: JSON.stringify({
    name: "ESP1 Gilroy Repeater", lat: 37.005, lon: -121.567,
    pubKey: "f81d265c03c5c1b2", text: "Hello mesh", sender: "KpaPocket",
    flags: { raw: 147, type: 2, repeater: true, room: false, hasLocation: true, hasName: true }
  }),
  observer_name: 'kpabap', observer_id: '2301ACD8E9DCEDE5',
  observation_count: 3, timestamp: new Date().toISOString(),
};

const nullSnrPkt = { ...pkt, snr: null, rssi: null };

// --- Firmware type names ---
test('type == GRP_TXT', () => { assert(PF.compile('type == GRP_TXT').filter(pkt)); });
test('type == grp_txt (case insensitive)', () => { assert(PF.compile('type == grp_txt').filter(pkt)); });
test('type == ADVERT is false', () => { assert(!PF.compile('type == ADVERT').filter(pkt)); });
test('type == TXT_MSG is false', () => { assert(!PF.compile('type == TXT_MSG').filter(pkt)); });
test('type != GRP_TXT is false', () => { assert(!PF.compile('type != GRP_TXT').filter(pkt)); });
test('type != ADVERT is true', () => { assert(PF.compile('type != ADVERT').filter(pkt)); });

// --- Type aliases ---
test('type == channel (alias)', () => { assert(PF.compile('type == channel').filter(pkt)); });
test('type == "Channel Msg" (alias)', () => { assert(PF.compile('type == "Channel Msg"').filter(pkt)); });
test('type == dm is false', () => { assert(!PF.compile('type == dm').filter(pkt)); });
test('type == request is false', () => { assert(!PF.compile('type == request').filter(pkt)); });

// --- Route ---
test('route == FLOOD', () => { assert(PF.compile('route == FLOOD').filter(pkt)); });
test('route == DIRECT is false', () => { assert(!PF.compile('route == DIRECT').filter(pkt)); });

// --- Transport route filters (issue #339) ---
const tFloodPkt   = { ...pkt, route_type: 0 }; // TRANSPORT_FLOOD
const floodPkt    = { ...pkt, route_type: 1 }; // FLOOD
const directPkt   = { ...pkt, route_type: 2 }; // DIRECT
const tDirectPkt  = { ...pkt, route_type: 3 }; // TRANSPORT_DIRECT

test('route == TRANSPORT_FLOOD matches route_type 0', () => {
  assert(PF.compile('route == TRANSPORT_FLOOD').filter(tFloodPkt));
  assert(!PF.compile('route == TRANSPORT_FLOOD').filter(floodPkt));
});
test('route == TRANSPORT_DIRECT matches route_type 3', () => {
  assert(PF.compile('route == TRANSPORT_DIRECT').filter(tDirectPkt));
  assert(!PF.compile('route == TRANSPORT_DIRECT').filter(directPkt));
});
test('route == T_FLOOD alias matches route_type 0', () => {
  assert(PF.compile('route == T_FLOOD').filter(tFloodPkt));
  assert(!PF.compile('route == T_FLOOD').filter(floodPkt));
  assert(!PF.compile('route == T_FLOOD').filter(directPkt));
});
test('route == T_DIRECT alias matches route_type 3', () => {
  assert(PF.compile('route == T_DIRECT').filter(tDirectPkt));
  assert(!PF.compile('route == T_DIRECT').filter(directPkt));
  assert(!PF.compile('route == T_DIRECT').filter(tFloodPkt));
});
test('transport == true matches TRANSPORT_FLOOD and TRANSPORT_DIRECT', () => {
  assert(PF.compile('transport == true').filter(tFloodPkt));
  assert(PF.compile('transport == true').filter(tDirectPkt));
  assert(!PF.compile('transport == true').filter(floodPkt));
  assert(!PF.compile('transport == true').filter(directPkt));
});
test('transport == false matches non-transported FLOOD and DIRECT', () => {
  assert(PF.compile('transport == false').filter(floodPkt));
  assert(PF.compile('transport == false').filter(directPkt));
  assert(!PF.compile('transport == false').filter(tFloodPkt));
  assert(!PF.compile('transport == false').filter(tDirectPkt));
});
test('bare transport (truthy) matches transported packets', () => {
  assert(PF.compile('transport').filter(tFloodPkt));
  assert(PF.compile('transport').filter(tDirectPkt));
  assert(!PF.compile('transport').filter(floodPkt));
  assert(!PF.compile('transport').filter(directPkt));
});

// --- Hash ---
test('hash == abc123def456', () => { assert(PF.compile('hash == abc123def456').filter(pkt)); });
test('hash contains abc', () => { assert(PF.compile('hash contains abc').filter(pkt)); });
test('hash starts_with abc', () => { assert(PF.compile('hash starts_with abc').filter(pkt)); });
test('hash ends_with 456', () => { assert(PF.compile('hash ends_with 456').filter(pkt)); });

// --- Numeric ---
test('snr > 5', () => { assert(PF.compile('snr > 5').filter(pkt)); });
test('snr > 10 is false', () => { assert(!PF.compile('snr > 10').filter(pkt)); });
test('snr >= 8.5', () => { assert(PF.compile('snr >= 8.5').filter(pkt)); });
test('snr < 8.5 is false', () => { assert(!PF.compile('snr < 8.5').filter(pkt)); });
test('rssi < -40', () => { assert(PF.compile('rssi < -40').filter(pkt)); });
test('rssi < -50 is false', () => { assert(!PF.compile('rssi < -50').filter(pkt)); });

// --- Hops ---
test('hops == 3', () => { assert(PF.compile('hops == 3').filter(pkt)); });
test('hops > 2', () => { assert(PF.compile('hops > 2').filter(pkt)); });
test('hops > 3 is false', () => { assert(!PF.compile('hops > 3').filter(pkt)); });

// --- Observer ---
test('observer == kpabap', () => { assert(PF.compile('observer == kpabap').filter(pkt)); });
test('observer contains kpa', () => { assert(PF.compile('observer contains kpa').filter(pkt)); });

// --- Observations ---
test('observations > 1', () => { assert(PF.compile('observations > 1').filter(pkt)); });
test('observations == 3', () => { assert(PF.compile('observations == 3').filter(pkt)); });

// --- Size ---
test('size > 3', () => { assert(PF.compile('size > 3').filter(pkt)); });

// --- Payload dot notation ---
test('payload.name contains "Gilroy"', () => { assert(PF.compile('payload.name contains "Gilroy"').filter(pkt)); });
test('payload.name contains "Oakland" is false', () => { assert(!PF.compile('payload.name contains "Oakland"').filter(pkt)); });
test('payload.name starts_with "ESP1"', () => { assert(PF.compile('payload.name starts_with "ESP1"').filter(pkt)); });
test('payload.lat > 37', () => { assert(PF.compile('payload.lat > 37').filter(pkt)); });
test('payload.lat > 38 is false', () => { assert(!PF.compile('payload.lat > 38').filter(pkt)); });
test('payload.lon < -121', () => { assert(PF.compile('payload.lon < -121').filter(pkt)); });
test('payload.pubKey starts_with "f81d"', () => { assert(PF.compile('payload.pubKey starts_with "f81d"').filter(pkt)); });
test('payload.text contains "Hello"', () => { assert(PF.compile('payload.text contains "Hello"').filter(pkt)); });
test('payload.sender == "KpaPocket"', () => { assert(PF.compile('payload.sender == "KpaPocket"').filter(pkt)); });
test('payload.flags.hasLocation (truthy)', () => { assert(PF.compile('payload.flags.hasLocation').filter(pkt)); });
test('payload.flags.room is false (truthy)', () => { assert(!PF.compile('payload.flags.room').filter(pkt)); });
test('payload.flags.raw == 147', () => { assert(PF.compile('payload.flags.raw == 147').filter(pkt)); });
test('payload_hex contains "aabb"', () => { assert(PF.compile('payload_hex contains "aabb"').filter(pkt)); });

// --- Logic ---
test('type == GRP_TXT && snr > 5', () => { assert(PF.compile('type == GRP_TXT && snr > 5').filter(pkt)); });
test('type == GRP_TXT && snr > 10 is false', () => { assert(!PF.compile('type == GRP_TXT && snr > 10').filter(pkt)); });
test('type == ADVERT || snr > 5', () => { assert(PF.compile('type == ADVERT || snr > 5').filter(pkt)); });
test('type == ADVERT || snr > 10 is false', () => { assert(!PF.compile('type == ADVERT || snr > 10').filter(pkt)); });
test('!(type == ADVERT)', () => { assert(PF.compile('!(type == ADVERT)').filter(pkt)); });
test('!(type == GRP_TXT) is false', () => { assert(!PF.compile('!(type == GRP_TXT)').filter(pkt)); });

// --- Parentheses ---
test('(type == ADVERT || type == GRP_TXT) && snr > 5', () => {
  assert(PF.compile('(type == ADVERT || type == GRP_TXT) && snr > 5').filter(pkt));
});
test('(type == ADVERT) && snr > 5 is false', () => {
  assert(!PF.compile('(type == ADVERT) && snr > 5').filter(pkt));
});

// --- Complex ---
test('type == GRP_TXT && snr > 5 && hops > 2', () => {
  assert(PF.compile('type == GRP_TXT && snr > 5 && hops > 2').filter(pkt));
});
test('!(type == ACK) && !(type == PATH)', () => {
  assert(PF.compile('!(type == ACK) && !(type == PATH)').filter(pkt));
});
test('payload.lat >= 37 && payload.lat <= 38 && payload.lon >= -122 && payload.lon <= -121', () => {
  assert(PF.compile('payload.lat >= 37 && payload.lat <= 38 && payload.lon >= -122 && payload.lon <= -121').filter(pkt));
});

// --- Edge cases: null fields ---
test('snr > 5 with null snr → false', () => { assert(!PF.compile('snr > 5').filter(nullSnrPkt)); });
test('rssi < -50 with null rssi → false', () => { assert(!PF.compile('rssi < -50').filter(nullSnrPkt)); });
test('payload.nonexistent == "x" → false', () => { assert(!PF.compile('payload.nonexistent == "x"').filter(pkt)); });
test('payload.flags.nonexistent (truthy) → false', () => { assert(!PF.compile('payload.flags.nonexistent').filter(pkt)); });

// --- Error handling ---
test('empty filter → no error', () => {
  const c = PF.compile('');
  assert(c.error === null, 'should have no error');
});
test('invalid syntax → error message', () => {
  const c = PF.compile('== broken');
  assert(c.error !== null, 'should have error');
});
test('@@@ garbage → error', () => {
  const c = PF.compile('@@@ garbage');
  assert(c.error !== null, 'should have error');
});
test('unclosed quote → error', () => {
  const c = PF.compile('type == "hello');
  assert(c.error !== null, 'should have error');
});

console.log(`\n=== Results: ${pass} passed, ${fail} failed ===`);
process.exit(fail > 0 ? 1 : 0);

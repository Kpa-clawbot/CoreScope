/* Unit tests for packet filter timestamp predicates (issue #289) */
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

const NOW = Date.now();
function isoOffset(ms) { return new Date(NOW - ms).toISOString(); }

// Packets at known offsets
const pkt30m  = { payload_type: 4, timestamp: isoOffset(30 * 60 * 1000) };          // 30 minutes ago
const pkt2h   = { payload_type: 4, timestamp: isoOffset(2 * 60 * 60 * 1000) };      // 2 hours ago
const pkt2d   = { payload_type: 4, timestamp: isoOffset(2 * 24 * 60 * 60 * 1000) }; // 2 days ago
const pkt2024 = { payload_type: 4, timestamp: '2024-06-15T12:00:00Z' };
const pkt2023 = { payload_type: 4, timestamp: '2023-06-15T12:00:00Z' };
const pkt2025 = { payload_type: 4, timestamp: '2025-06-15T12:00:00Z' };

// --- after / before on time field ---
test('time after 2024-01-01 matches 2024 packet', () => {
  assert(PF.compile('time after "2024-01-01"').filter(pkt2024));
});
test('time after 2024-01-01 rejects 2023 packet', () => {
  assert(!PF.compile('time after "2024-01-01"').filter(pkt2023));
});
test('time before 2024-12-31 matches 2024 packet', () => {
  assert(PF.compile('time before "2024-12-31"').filter(pkt2024));
});
test('time before 2024-12-31 rejects 2025 packet', () => {
  assert(!PF.compile('time before "2024-12-31"').filter(pkt2025));
});

// --- between on time field ---
test('time between matches packet inside range', () => {
  assert(PF.compile('time between "2024-01-01" "2024-12-31"').filter(pkt2024));
});
test('time between rejects packet outside range', () => {
  assert(!PF.compile('time between "2024-01-01" "2024-12-31"').filter(pkt2023));
  assert(!PF.compile('time between "2024-01-01" "2024-12-31"').filter(pkt2025));
});

// --- ISO datetime with time component ---
test('time after with full ISO datetime', () => {
  assert(PF.compile('time after "2024-06-15T00:00:00Z"').filter(pkt2024));
  assert(!PF.compile('time after "2024-06-16T00:00:00Z"').filter(pkt2024));
});

// --- age with relative durations ---
test('age < 1h matches 30-min-old packet', () => {
  assert(PF.compile('age < 1h').filter(pkt30m));
});
test('age < 1h rejects 2-hour-old packet', () => {
  assert(!PF.compile('age < 1h').filter(pkt2h));
});
test('age > 1h matches 2-hour-old packet', () => {
  assert(PF.compile('age > 1h').filter(pkt2h));
});
test('age > 24h matches 2-day-old packet', () => {
  assert(PF.compile('age > 24h').filter(pkt2d));
});
test('age > 24h rejects 30-min-old packet', () => {
  assert(!PF.compile('age > 24h').filter(pkt30m));
});
test('age < 7d matches 2-day-old packet', () => {
  assert(PF.compile('age < 7d').filter(pkt2d));
});
test('age units: m (minutes)', () => {
  assert(PF.compile('age > 15m').filter(pkt30m));
  assert(!PF.compile('age > 60m').filter(pkt30m));
});
test('age units: s (seconds)', () => {
  assert(PF.compile('age > 60s').filter(pkt30m));
});

// --- combining time predicates with logic ---
test('age < 1h && type == ADVERT', () => {
  assert(PF.compile('age < 1h && type == ADVERT').filter(pkt30m));
  assert(!PF.compile('age < 1h && type == ADVERT').filter(pkt2h));
});

// --- null timestamp safety ---
test('time predicate on packet without timestamp → false', () => {
  assert(!PF.compile('time after "2024-01-01"').filter({ payload_type: 4 }));
  assert(!PF.compile('age < 1h').filter({ payload_type: 4 }));
  assert(!PF.compile('time between "2024-01-01" "2024-12-31"').filter({ payload_type: 4 }));
});

// --- error handling ---
test('invalid datetime → error', () => {
  const c = PF.compile('time after "not-a-date"');
  assert(c.error !== null, 'should error on invalid date');
});
test('invalid duration → error', () => {
  const c = PF.compile('age < 1xyz');
  assert(c.error !== null, 'should error on invalid duration');
});

// --- packets.first_seen fallback ---
test('time field falls back to first_seen when timestamp missing', () => {
  const p = { payload_type: 4, first_seen: '2024-06-15T12:00:00Z' };
  assert(PF.compile('time after "2024-01-01"').filter(p));
});

console.log(`\n=== Results: ${pass} passed, ${fail} failed ===`);
process.exit(fail > 0 ? 1 : 0);

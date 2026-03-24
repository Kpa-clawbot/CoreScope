/* Unit tests for packet-store.js — uses a mock db module */
'use strict';
const assert = require('assert');
const PacketStore = require('./packet-store');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log(`  ✅ ${name}`); }
  catch (e) { failed++; console.log(`  ❌ ${name}: ${e.message}`); }
}

// Mock db module — minimal stubs for PacketStore
function createMockDb() {
  let txIdCounter = 1;
  let obsIdCounter = 1000;
  return {
    db: {
      prepare: (sql) => ({
        get: (...args) => {
          if (sql.includes('sqlite_master')) return { name: 'transmissions' };
          if (sql.includes('nodes')) return null;
          if (sql.includes('observers')) return [];
          return null;
        },
        all: (...args) => [],
      }),
    },
    insertTransmission: (data) => ({
      transmissionId: txIdCounter++,
      observationId: obsIdCounter++,
    }),
  };
}

function makePacketData(overrides = {}) {
  return {
    raw_hex: 'AABBCCDD',
    hash: 'abc123',
    timestamp: new Date().toISOString(),
    route_type: 1,
    payload_type: 5,
    payload_version: 0,
    decoded_json: JSON.stringify({ pubKey: 'DEADBEEF'.repeat(8) }),
    observer_id: 'obs1',
    observer_name: 'Observer1',
    snr: 8.5,
    rssi: -45,
    path_json: '["AA","BB"]',
    direction: 'rx',
    ...overrides,
  };
}

// === Constructor ===
console.log('\n=== PacketStore constructor ===');
test('creates empty store', () => {
  const store = new PacketStore(createMockDb());
  assert.strictEqual(store.packets.length, 0);
  assert.strictEqual(store.loaded, false);
});

test('respects maxMemoryMB config', () => {
  const store = new PacketStore(createMockDb(), { maxMemoryMB: 512 });
  assert.strictEqual(store.maxBytes, 512 * 1024 * 1024);
});

// === Load ===
console.log('\n=== Load ===');
test('load sets loaded flag', () => {
  const store = new PacketStore(createMockDb());
  store.load();
  assert.strictEqual(store.loaded, true);
});

test('sqliteOnly mode skips RAM', () => {
  const orig = process.env.NO_MEMORY_STORE;
  process.env.NO_MEMORY_STORE = '1';
  const store = new PacketStore(createMockDb());
  store.load();
  assert.strictEqual(store.sqliteOnly, true);
  assert.strictEqual(store.packets.length, 0);
  process.env.NO_MEMORY_STORE = orig || '';
  if (!orig) delete process.env.NO_MEMORY_STORE;
});

// === Insert ===
console.log('\n=== Insert ===');
test('insert adds packet to memory', () => {
  const store = new PacketStore(createMockDb());
  store.load();
  store.insert(makePacketData());
  assert.strictEqual(store.packets.length, 1);
  assert.strictEqual(store.stats.inserts, 1);
});

test('insert deduplicates by hash', () => {
  const store = new PacketStore(createMockDb());
  store.load();
  store.insert(makePacketData({ hash: 'dup1' }));
  store.insert(makePacketData({ hash: 'dup1', observer_id: 'obs2' }));
  assert.strictEqual(store.packets.length, 1);
  assert.strictEqual(store.packets[0].observations.length, 2);
  assert.strictEqual(store.packets[0].observation_count, 2);
});

test('insert dedup: same observer+path skipped', () => {
  const store = new PacketStore(createMockDb());
  store.load();
  store.insert(makePacketData({ hash: 'dup2' }));
  store.insert(makePacketData({ hash: 'dup2' })); // same observer_id + path_json
  assert.strictEqual(store.packets[0].observations.length, 1);
});

test('insert indexes by node pubkey', () => {
  const store = new PacketStore(createMockDb());
  store.load();
  const pk = 'DEADBEEF'.repeat(8);
  store.insert(makePacketData({ hash: 'n1', decoded_json: JSON.stringify({ pubKey: pk }) }));
  assert(store.byNode.has(pk));
  assert.strictEqual(store.byNode.get(pk).length, 1);
});

test('insert indexes byObserver', () => {
  const store = new PacketStore(createMockDb());
  store.load();
  store.insert(makePacketData({ observer_id: 'obs-test' }));
  assert(store.byObserver.has('obs-test'));
});

test('insert updates first_seen for earlier timestamp', () => {
  const store = new PacketStore(createMockDb());
  store.load();
  store.insert(makePacketData({ hash: 'ts1', timestamp: '2025-01-02T00:00:00Z', observer_id: 'o1' }));
  store.insert(makePacketData({ hash: 'ts1', timestamp: '2025-01-01T00:00:00Z', observer_id: 'o2' }));
  assert.strictEqual(store.packets[0].first_seen, '2025-01-01T00:00:00Z');
});

test('insert indexes ADVERT observer', () => {
  const store = new PacketStore(createMockDb());
  store.load();
  const pk = 'AA'.repeat(32);
  store.insert(makePacketData({ hash: 'adv1', payload_type: 4, decoded_json: JSON.stringify({ pubKey: pk }), observer_id: 'obs-adv' }));
  assert(store._advertByObserver.has(pk));
  assert(store._advertByObserver.get(pk).has('obs-adv'));
});

// === Query ===
console.log('\n=== Query ===');
test('query returns all packets', () => {
  const store = new PacketStore(createMockDb());
  store.load();
  store.insert(makePacketData({ hash: 'q1' }));
  store.insert(makePacketData({ hash: 'q2' }));
  const r = store.query();
  assert.strictEqual(r.total, 2);
  assert.strictEqual(r.packets.length, 2);
});

test('query by type filter', () => {
  const store = new PacketStore(createMockDb());
  store.load();
  store.insert(makePacketData({ hash: 'qt1', payload_type: 4 }));
  store.insert(makePacketData({ hash: 'qt2', payload_type: 5 }));
  const r = store.query({ type: 4 });
  assert.strictEqual(r.total, 1);
  assert.strictEqual(r.packets[0].payload_type, 4);
});

test('query by route filter', () => {
  const store = new PacketStore(createMockDb());
  store.load();
  store.insert(makePacketData({ hash: 'qr1', route_type: 0 }));
  store.insert(makePacketData({ hash: 'qr2', route_type: 1 }));
  const r = store.query({ route: 1 });
  assert.strictEqual(r.total, 1);
});

test('query by hash (index path)', () => {
  const store = new PacketStore(createMockDb());
  store.load();
  store.insert(makePacketData({ hash: 'qh1' }));
  store.insert(makePacketData({ hash: 'qh2' }));
  const r = store.query({ hash: 'qh1' });
  assert.strictEqual(r.total, 1);
  assert.strictEqual(r.packets[0].hash, 'qh1');
});

test('query by observer (index path)', () => {
  const store = new PacketStore(createMockDb());
  store.load();
  store.insert(makePacketData({ hash: 'qo1', observer_id: 'obsA' }));
  store.insert(makePacketData({ hash: 'qo2', observer_id: 'obsB' }));
  const r = store.query({ observer: 'obsA' });
  assert.strictEqual(r.total, 1);
});

test('query with limit and offset', () => {
  const store = new PacketStore(createMockDb());
  store.load();
  for (let i = 0; i < 10; i++) store.insert(makePacketData({ hash: `ql${i}`, observer_id: `o${i}` }));
  const r = store.query({ limit: 3, offset: 2 });
  assert.strictEqual(r.packets.length, 3);
  assert.strictEqual(r.total, 10);
});

test('query by since filter', () => {
  const store = new PacketStore(createMockDb());
  store.load();
  store.insert(makePacketData({ hash: 'qs1', timestamp: '2025-01-01T00:00:00Z' }));
  store.insert(makePacketData({ hash: 'qs2', timestamp: '2025-06-01T00:00:00Z', observer_id: 'o2' }));
  const r = store.query({ since: '2025-03-01T00:00:00Z' });
  assert.strictEqual(r.total, 1);
});

test('query by until filter', () => {
  const store = new PacketStore(createMockDb());
  store.load();
  store.insert(makePacketData({ hash: 'qu1', timestamp: '2025-01-01T00:00:00Z' }));
  store.insert(makePacketData({ hash: 'qu2', timestamp: '2025-06-01T00:00:00Z', observer_id: 'o2' }));
  const r = store.query({ until: '2025-03-01T00:00:00Z' });
  assert.strictEqual(r.total, 1);
});

test('query ASC order', () => {
  const store = new PacketStore(createMockDb());
  store.load();
  store.insert(makePacketData({ hash: 'qa1', timestamp: '2025-06-01T00:00:00Z' }));
  store.insert(makePacketData({ hash: 'qa2', timestamp: '2025-01-01T00:00:00Z', observer_id: 'o2' }));
  const r = store.query({ order: 'ASC' });
  assert(r.packets[0].timestamp < r.packets[1].timestamp);
});

// === queryGrouped ===
console.log('\n=== queryGrouped ===');
test('queryGrouped returns grouped data', () => {
  const store = new PacketStore(createMockDb());
  store.load();
  store.insert(makePacketData({ hash: 'qg1' }));
  store.insert(makePacketData({ hash: 'qg1', observer_id: 'obs2' }));
  store.insert(makePacketData({ hash: 'qg2', observer_id: 'obs3' }));
  const r = store.queryGrouped();
  assert.strictEqual(r.total, 2);
  const g1 = r.packets.find(p => p.hash === 'qg1');
  assert(g1);
  assert.strictEqual(g1.observation_count, 2);
  assert.strictEqual(g1.observer_count, 2);
});

// === getNodesByAdvertObservers ===
console.log('\n=== getNodesByAdvertObservers ===');
test('finds nodes by observer', () => {
  const store = new PacketStore(createMockDb());
  store.load();
  const pk = 'BB'.repeat(32);
  store.insert(makePacketData({ hash: 'nao1', payload_type: 4, decoded_json: JSON.stringify({ pubKey: pk }), observer_id: 'obs-x' }));
  const result = store.getNodesByAdvertObservers(['obs-x']);
  assert(result.has(pk));
});

test('returns empty for unknown observer', () => {
  const store = new PacketStore(createMockDb());
  store.load();
  const result = store.getNodesByAdvertObservers(['nonexistent']);
  assert.strictEqual(result.size, 0);
});

// === Other methods ===
console.log('\n=== Other methods ===');
test('getById returns observation', () => {
  const store = new PacketStore(createMockDb());
  store.load();
  const id = store.insert(makePacketData({ hash: 'gbi1' }));
  const obs = store.getById(id);
  assert(obs);
});

test('getSiblings returns observations for hash', () => {
  const store = new PacketStore(createMockDb());
  store.load();
  store.insert(makePacketData({ hash: 'sib1' }));
  store.insert(makePacketData({ hash: 'sib1', observer_id: 'obs2' }));
  const sibs = store.getSiblings('sib1');
  assert.strictEqual(sibs.length, 2);
});

test('getSiblings empty for unknown hash', () => {
  const store = new PacketStore(createMockDb());
  store.load();
  assert.deepStrictEqual(store.getSiblings('nope'), []);
});

test('all() returns packets', () => {
  const store = new PacketStore(createMockDb());
  store.load();
  store.insert(makePacketData({ hash: 'all1' }));
  assert.strictEqual(store.all().length, 1);
});

test('filter() works', () => {
  const store = new PacketStore(createMockDb());
  store.load();
  store.insert(makePacketData({ hash: 'f1', payload_type: 4 }));
  store.insert(makePacketData({ hash: 'f2', payload_type: 5, observer_id: 'o2' }));
  assert.strictEqual(store.filter(p => p.payload_type === 4).length, 1);
});

test('countForNode returns counts', () => {
  const store = new PacketStore(createMockDb());
  store.load();
  const pk = 'CC'.repeat(32);
  store.insert(makePacketData({ hash: 'cn1', decoded_json: JSON.stringify({ pubKey: pk }) }));
  store.insert(makePacketData({ hash: 'cn1', decoded_json: JSON.stringify({ pubKey: pk }), observer_id: 'o2' }));
  const c = store.countForNode(pk);
  assert.strictEqual(c.transmissions, 1);
  assert.strictEqual(c.observations, 2);
});

test('getStats returns stats object', () => {
  const store = new PacketStore(createMockDb());
  store.load();
  const s = store.getStats();
  assert.strictEqual(s.inMemory, 0);
  assert(s.indexes);
  assert.strictEqual(s.sqliteOnly, false);
});

test('getTimestamps returns timestamps', () => {
  const store = new PacketStore(createMockDb());
  store.load();
  store.insert(makePacketData({ hash: 'gt1', timestamp: '2025-06-01T00:00:00Z' }));
  store.insert(makePacketData({ hash: 'gt2', timestamp: '2025-06-02T00:00:00Z', observer_id: 'o2' }));
  const ts = store.getTimestamps('2025-05-01T00:00:00Z');
  assert.strictEqual(ts.length, 2);
});

// === Eviction ===
console.log('\n=== Eviction ===');
test('evicts oldest when over maxPackets', () => {
  const store = new PacketStore(createMockDb(), { maxMemoryMB: 1, estimatedPacketBytes: 500000 });
  // maxPackets will be very small
  store.load();
  for (let i = 0; i < 10; i++) store.insert(makePacketData({ hash: `ev${i}`, observer_id: `o${i}` }));
  assert(store.packets.length <= store.maxPackets);
  assert(store.stats.evicted > 0);
});

// === findPacketsForNode ===
console.log('\n=== findPacketsForNode ===');
test('finds by pubkey', () => {
  const store = new PacketStore(createMockDb());
  store.load();
  const pk = 'DD'.repeat(32);
  store.insert(makePacketData({ hash: 'fpn1', decoded_json: JSON.stringify({ pubKey: pk }) }));
  store.insert(makePacketData({ hash: 'fpn2', decoded_json: JSON.stringify({ pubKey: 'other' }), observer_id: 'o2' }));
  const r = store.findPacketsForNode(pk);
  assert.strictEqual(r.packets.length, 1);
  assert.strictEqual(r.pubkey, pk);
});

test('finds by text search in decoded_json', () => {
  const store = new PacketStore(createMockDb());
  store.load();
  store.insert(makePacketData({ hash: 'fpn3', decoded_json: JSON.stringify({ name: 'MySpecialNode' }) }));
  const r = store.findPacketsForNode('MySpecialNode');
  assert.strictEqual(r.packets.length, 1);
});

// === Summary ===
console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);

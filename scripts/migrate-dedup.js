#!/usr/bin/env node
/**
 * Offline legacy SQLite tooling. Not part of PostgreSQL upgrades.
 * 
 * Creates `transmissions` and `observations` tables from the existing `packets` table.
 * Idempotent — drops and recreates new tables on each run.
 * Does NOT touch the original `packets` table.
 * 
 * Usage: node scripts/migrate-dedup.js --legacy-sqlite-offline <recovery-copy.db>
 */

if (process.argv[2] !== '--legacy-sqlite-offline') {
  console.error('Offline legacy SQLite tooling only. Use docs/postgresql-upgrade.md for current upgrades. To operate on a stopped, disposable recovery copy, pass --legacy-sqlite-offline explicitly.');
  process.exit(1);
}
process.argv.splice(2, 1);

const Database = require('better-sqlite3');
const path = require('path');

const dbPath = process.argv[2];
if (!dbPath) {
  console.error('Usage: node scripts/migrate-dedup.js --legacy-sqlite-offline <recovery-copy.db>');
  process.exit(1);
}

const start = Date.now();
const db = new Database(dbPath);
db.pragma('journal_mode = WAL');
db.pragma('foreign_keys = ON');

// --- Drop existing new tables (idempotent) ---
console.log('Dropping existing transmissions/observations tables if they exist...');
db.exec('DROP TABLE IF EXISTS observations');
db.exec('DROP TABLE IF EXISTS transmissions');

// --- Create new tables ---
console.log('Creating transmissions and observations tables...');
db.exec(`
  CREATE TABLE transmissions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    raw_hex TEXT NOT NULL,
    hash TEXT NOT NULL UNIQUE,
    first_seen TEXT NOT NULL,
    route_type INTEGER,
    payload_type INTEGER,
    payload_version INTEGER,
    decoded_json TEXT,
    created_at TEXT DEFAULT (datetime('now'))
  );

  CREATE TABLE observations (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    transmission_id INTEGER NOT NULL REFERENCES transmissions(id),
    hash TEXT NOT NULL,
    observer_id TEXT,
    observer_name TEXT,
    direction TEXT,
    snr REAL,
    rssi REAL,
    score INTEGER,
    path_json TEXT,
    timestamp TEXT NOT NULL,
    created_at TEXT DEFAULT (datetime('now'))
  );

  CREATE INDEX idx_transmissions_hash ON transmissions(hash);
  CREATE INDEX idx_transmissions_first_seen ON transmissions(first_seen);
  CREATE INDEX idx_transmissions_payload_type ON transmissions(payload_type);
  CREATE INDEX idx_observations_hash ON observations(hash);
  CREATE INDEX idx_observations_transmission_id ON observations(transmission_id);
  CREATE INDEX idx_observations_observer_id ON observations(observer_id);
  CREATE INDEX idx_observations_timestamp ON observations(timestamp);
`);

// --- Read all packets ordered by timestamp ---
console.log('Reading packets...');
const packets = db.prepare('SELECT * FROM packets ORDER BY timestamp ASC').all();
const totalPackets = packets.length;
console.log(`Total packets: ${totalPackets}`);

// --- Group by hash and migrate ---
const insertTransmission = db.prepare(`
  INSERT OR IGNORE INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, payload_version, decoded_json)
  VALUES (?, ?, ?, ?, ?, ?, ?)
`);

const insertObservation = db.prepare(`
  INSERT INTO observations (transmission_id, hash, observer_id, observer_name, direction, snr, rssi, score, path_json, timestamp)
  VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`);

const hashToTransmissionId = new Map();
let transmissionCount = 0;

const lookupTransmission = db.prepare('SELECT id FROM transmissions WHERE hash = ?');

const migrate = db.transaction(() => {
  for (const pkt of packets) {
    let txId = hashToTransmissionId.get(pkt.hash);
    if (txId === undefined) {
      const result = insertTransmission.run(
        pkt.raw_hex, pkt.hash, pkt.timestamp,
        pkt.route_type, pkt.payload_type, pkt.payload_version, pkt.decoded_json
      );
      if (result.changes > 0) {
        txId = result.lastInsertRowid;
      } else {
        // Already inserted by dual-write, look up existing
        txId = lookupTransmission.get(pkt.hash).id;
      }
      hashToTransmissionId.set(pkt.hash, txId);
      transmissionCount++;
    }
    insertObservation.run(
      txId, pkt.hash, pkt.observer_id, pkt.observer_name, pkt.direction,
      pkt.snr, pkt.rssi, pkt.score, pkt.path_json, pkt.timestamp
    );
  }
});

migrate();

// --- Verify ---
const obsCount = db.prepare('SELECT COUNT(*) as c FROM observations').get().c;
const txCount = db.prepare('SELECT COUNT(*) as c FROM transmissions').get().c;
const distinctHash = db.prepare('SELECT COUNT(DISTINCT hash) as c FROM packets').get().c;

const elapsed = ((Date.now() - start) / 1000).toFixed(2);

console.log('\n=== Migration Stats ===');
console.log(`Total packets (source):       ${totalPackets}`);
console.log(`Unique transmissions created:  ${transmissionCount}`);
console.log(`Observations created:          ${obsCount}`);
console.log(`Dedup ratio:                   ${(totalPackets / transmissionCount).toFixed(2)}x`);
console.log(`Time taken:                    ${elapsed}s`);

console.log('\n=== Verification ===');
const obsOk = obsCount === totalPackets;
const txOk = txCount === distinctHash;
console.log(`observations (${obsCount}) = packets (${totalPackets}): ${obsOk ? 'PASS ✓' : 'FAIL ✗'}`);
console.log(`transmissions (${txCount}) = distinct hashes (${distinctHash}): ${txOk ? 'PASS ✓' : 'FAIL ✗'}`);

if (!obsOk || !txOk) {
  console.error('\nVerification FAILED!');
  process.exit(1);
}

console.log('\nMigration complete!');
db.close();

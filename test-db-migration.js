'use strict';

// Test v3 migration: create old-schema DB, run db.js to migrate, verify results
const path = require('path');
const fs = require('fs');
const os = require('os');
const { execSync } = require('child_process');
const Database = require('better-sqlite3');

let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log(`  ✅ ${msg}`); }
  else { failed++; console.error(`  ❌ ${msg}`); }
}

console.log('── db.js v3 migration tests ──\n');

// Helper: create a DB with old (v2) schema and test data
function createOldSchemaDB(dbPath) {
  const db = new Database(dbPath);
  db.pragma('journal_mode = WAL');
  db.pragma('foreign_keys = ON');

  db.exec(`
    CREATE TABLE nodes (
      public_key TEXT PRIMARY KEY,
      name TEXT,
      role TEXT,
      lat REAL,
      lon REAL,
      last_seen TEXT,
      first_seen TEXT,
      advert_count INTEGER DEFAULT 0
    );

    CREATE TABLE observers (
      id TEXT PRIMARY KEY,
      name TEXT,
      iata TEXT,
      last_seen TEXT,
      first_seen TEXT,
      packet_count INTEGER DEFAULT 0,
      model TEXT,
      firmware TEXT,
      client_version TEXT,
      radio TEXT,
      battery_mv INTEGER,
      uptime_secs INTEGER,
      noise_floor INTEGER
    );

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
    CREATE INDEX idx_observations_hash ON observations(hash);
    CREATE INDEX idx_observations_transmission_id ON observations(transmission_id);
    CREATE INDEX idx_observations_observer_id ON observations(observer_id);
    CREATE INDEX idx_observations_timestamp ON observations(timestamp);
    CREATE UNIQUE INDEX idx_observations_dedup ON observations(hash, observer_id, COALESCE(path_json, ''));
  `);

  // Insert test observers
  db.prepare(`INSERT INTO observers (id, name, iata, last_seen, first_seen, packet_count) VALUES (?, ?, ?, ?, ?, ?)`).run(
    'aabbccdd11223344aabbccdd11223344aabbccdd11223344aabbccdd11223344', 'Observer Alpha', 'SFO',
    '2025-06-01T12:00:00Z', '2025-01-01T00:00:00Z', 100
  );
  db.prepare(`INSERT INTO observers (id, name, iata, last_seen, first_seen, packet_count) VALUES (?, ?, ?, ?, ?, ?)`).run(
    'deadbeef12345678deadbeef12345678deadbeef12345678deadbeef12345678', 'Observer Beta', 'LAX',
    '2025-06-01T11:00:00Z', '2025-02-01T00:00:00Z', 50
  );

  // Insert test transmissions
  db.prepare(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json) VALUES (?, ?, ?, ?, ?, ?)`).run(
    '0400aabbccdd', 'hash-mig-001', '2025-06-01T10:00:00Z', 1, 4, '{"type":"ADVERT"}'
  );
  db.prepare(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json) VALUES (?, ?, ?, ?, ?, ?)`).run(
    '0400deadbeef', 'hash-mig-002', '2025-06-01T10:30:00Z', 2, 5, '{"type":"GRP_TXT"}'
  );

  // Insert test observations (old schema: has hash, observer_id, observer_name, text timestamp)
  db.prepare(`INSERT INTO observations (transmission_id, hash, observer_id, observer_name, direction, snr, rssi, score, path_json, timestamp) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`).run(
    1, 'hash-mig-001', 'aabbccdd11223344aabbccdd11223344aabbccdd11223344aabbccdd11223344', 'Observer Alpha',
    'rx', 12.5, -80, 85, '["aabb","ccdd"]', '2025-06-01T10:00:00Z'
  );
  db.prepare(`INSERT INTO observations (transmission_id, hash, observer_id, observer_name, direction, snr, rssi, score, path_json, timestamp) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`).run(
    1, 'hash-mig-001', 'deadbeef12345678deadbeef12345678deadbeef12345678deadbeef12345678', 'Observer Beta',
    'rx', 8.0, -92, 70, '["aabb"]', '2025-06-01T10:01:00Z'
  );
  db.prepare(`INSERT INTO observations (transmission_id, hash, observer_id, observer_name, direction, snr, rssi, score, path_json, timestamp) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`).run(
    2, 'hash-mig-002', 'aabbccdd11223344aabbccdd11223344aabbccdd11223344aabbccdd11223344', 'Observer Alpha',
    'rx', 15.0, -75, 90, null, '2025-06-01T10:30:00Z'
  );

  db.close();
}

// Helper: require db.js in a child process with a given DB_PATH, return schema info
function runDbModule(dbPath) {
  const scriptPath = path.join(os.tmpdir(), 'meshcore-mig-test-script.js');
  fs.writeFileSync(scriptPath, `
    process.env.DB_PATH = ${JSON.stringify(dbPath)};
    const db = require(${JSON.stringify(path.resolve(__dirname, 'db'))});
    const cols = db.db.pragma('table_info(observations)').map(c => c.name);
    const sv = db.db.prepare('SELECT version FROM schema_version ORDER BY version DESC LIMIT 1').get();
    const obsCount = db.db.prepare('SELECT COUNT(*) as c FROM observations').get().c;
    const viewRows = db.db.prepare('SELECT * FROM packets_v ORDER BY id').all();
    const rawObs = db.db.prepare('SELECT * FROM observations ORDER BY id').all();
    console.log(JSON.stringify({
      columns: cols,
      schemaVersion: sv ? sv.version : 0,
      obsCount,
      viewRows,
      rawObs
    }));
    db.db.close();
  `);
  const result = execSync(`node ${JSON.stringify(scriptPath)}`, {
    cwd: __dirname,
    encoding: 'utf8',
    timeout: 30000,
  });
  fs.unlinkSync(scriptPath);
  const lines = result.trim().split('\n');
  for (let i = lines.length - 1; i >= 0; i--) {
    try { return JSON.parse(lines[i]); } catch {}
  }
  throw new Error('No JSON output from child process: ' + result);
}

// --- Test 1: Migration from old schema ---
console.log('Migration from old schema:');
{
  const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), 'meshcore-mig-test-'));
  const dbPath = path.join(tmpDir, 'test-mig.db');

  createOldSchemaDB(dbPath);

  // Run db.js which should trigger migration
  const info = runDbModule(dbPath);

  // Verify schema
  assert(info.schemaVersion === 3, 'schema version is 3 after migration');
  assert(info.columns.includes('observer_idx'), 'has observer_idx column');
  assert(!info.columns.includes('observer_id'), 'no observer_id column');
  assert(!info.columns.includes('observer_name'), 'no observer_name column');
  assert(!info.columns.includes('hash'), 'no hash column');

  // Verify row count
  assert(info.obsCount === 3, `all 3 rows migrated (got ${info.obsCount})`);

  // Verify raw observation data
  const obs0 = info.rawObs[0];
  assert(typeof obs0.timestamp === 'number', 'timestamp is integer');
  assert(obs0.timestamp === Math.floor(new Date('2025-06-01T10:00:00Z').getTime() / 1000), 'timestamp epoch correct');
  assert(obs0.observer_idx !== null, 'observer_idx populated');

  // Verify view backward compat
  const vr0 = info.viewRows[0];
  assert(vr0.observer_id === 'aabbccdd11223344aabbccdd11223344aabbccdd11223344aabbccdd11223344', 'view observer_id correct');
  assert(vr0.observer_name === 'Observer Alpha', 'view observer_name correct');
  assert(typeof vr0.timestamp === 'string', 'view timestamp is string');
  assert(vr0.hash === 'hash-mig-001', 'view hash correct');
  assert(vr0.snr === 12.5, 'view snr correct');
  assert(vr0.path_json === '["aabb","ccdd"]', 'view path_json correct');

  // Third row has null path_json
  const vr2 = info.viewRows[2];
  assert(vr2.path_json === null, 'null path_json preserved');

  // Verify backup file created
  assert(fs.existsSync(dbPath + '.pre-v3-backup'), 'backup file exists');

  fs.rmSync(tmpDir, { recursive: true });
}

// --- Test 2: Migration doesn't re-run ---
console.log('\nMigration idempotency:');
{
  const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), 'meshcore-mig-test2-'));
  const dbPath = path.join(tmpDir, 'test-mig2.db');

  createOldSchemaDB(dbPath);

  // First run — triggers migration
  let info = runDbModule(dbPath);
  assert(info.schemaVersion === 3, 'first run migrates to v3');

  // Second run — should NOT re-run migration (no backup overwrite, same data)
  const backupMtime = fs.statSync(dbPath + '.pre-v3-backup').mtimeMs;
  info = runDbModule(dbPath);
  assert(info.schemaVersion === 3, 'second run still v3');
  assert(info.obsCount === 3, 'rows still intact');

  fs.rmSync(tmpDir, { recursive: true });
}

// --- Test 3: Backup failure aborts migration ---
console.log('\nBackup failure aborts migration:');
{
  const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), 'meshcore-mig-test3-'));
  const dbPath = path.join(tmpDir, 'test-mig3.db');

  createOldSchemaDB(dbPath);

  // Create backup path as a directory so copyFileSync fails
  fs.mkdirSync(dbPath + '.pre-v3-backup');

  // Run db.js — migration should abort, old schema preserved
  const info = runDbModule(dbPath);

  // Old schema should be preserved
  assert(info.columns.includes('observer_id'), 'old observer_id column preserved');
  assert(info.schemaVersion < 3, 'schema version not updated');
  assert(info.obsCount === 3, 'old rows still intact');

  fs.rmSync(tmpDir, { recursive: true });
}

// --- Test 4: v3 ingestion via child process ---
console.log('\nv3 ingestion test:');
{
  const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), 'meshcore-mig-test4-'));
  const dbPath = path.join(tmpDir, 'test-v3-ingest.db');

  const scriptPath = path.join(os.tmpdir(), 'meshcore-ingest-test-script.js');
  fs.writeFileSync(scriptPath, `
    process.env.DB_PATH = ${JSON.stringify(dbPath)};
    const db = require(${JSON.stringify(path.resolve(__dirname, 'db'))});

    db.upsertObserver({ id: 'test-obs', name: 'Test Obs' });

    const r = db.insertTransmission({
      raw_hex: '0400ff',
      hash: 'h-001',
      timestamp: '2025-06-01T12:00:00Z',
      observer_id: 'test-obs',
      observer_name: 'Test Obs',
      direction: 'rx',
      snr: 10,
      rssi: -85,
      path_json: '["aa"]',
      route_type: 1,
      payload_type: 4,
    });

    const r2 = db.insertTransmission({
      raw_hex: '0400ff',
      hash: 'h-001',
      timestamp: '2025-06-01T12:00:00Z',
      observer_id: 'test-obs',
      direction: 'rx',
      snr: 10,
      rssi: -85,
      path_json: '["aa"]',
    });

    const pkt = db.db.prepare('SELECT * FROM packets_v WHERE hash = ?').get('h-001');

    console.log(JSON.stringify({
      r1_ok: r !== null && r.transmissionId > 0,
      r2_deduped: r2.observationId === 0,
      obs_count: db.db.prepare('SELECT COUNT(*) as c FROM observations').get().c,
      view_observer_id: pkt.observer_id,
      view_observer_name: pkt.observer_name,
      view_ts_type: typeof pkt.timestamp,
    }));
    db.db.close();
  `);

  const result = execSync(`node ${JSON.stringify(scriptPath)}`, {
    cwd: __dirname, encoding: 'utf8', timeout: 30000,
  });
  fs.unlinkSync(scriptPath);
  const lines = result.trim().split('\n');
  let info;
  for (let i = lines.length - 1; i >= 0; i--) {
    try { info = JSON.parse(lines[i]); break; } catch {}
  }

  assert(info.r1_ok, 'first insertion succeeded');
  assert(info.r2_deduped, 'duplicate caught by dedup');
  assert(info.obs_count === 1, 'only one observation row');
  assert(info.view_observer_id === 'test-obs', 'view resolves observer_id');
  assert(info.view_observer_name === 'Test Obs', 'view resolves observer_name');
  assert(info.view_ts_type === 'string', 'view timestamp is string');

  fs.rmSync(tmpDir, { recursive: true });
}

console.log(`\n═══════════════════════════════════════`);
console.log(`  PASSED: ${passed}`);
console.log(`  FAILED: ${failed}`);
console.log(`═══════════════════════════════════════`);
if (failed > 0) process.exit(1);

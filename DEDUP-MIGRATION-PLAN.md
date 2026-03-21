# Packet Deduplication — Normalized Schema Migration Plan

## Overview

Split the monolithic `packets` table into two tables:
- **`packets`** — one row per unique physical transmission (keyed by content hash)
- **`observations`** — one row per observer sighting (SNR, RSSI, path, observer, timestamp)

This fixes inflated packet counts across the entire app and enables proper "1 transmission seen N times" semantics.

## Current State

**`packets` table**: 1 row per observation. ~61MB, ~30K+ rows. Same hash appears N times (once per observer). Fields mix transmission data (raw_hex, payload_type, decoded_json, hash) with observation data (observer_id, snr, rssi, path_json).

**`packet-store.js`**: In-memory mirror of packets table. Indexes: `byId`, `byHash` (hash → [packets]), `byObserver`, `byNode`. All reads served from RAM. SQLite is write-only for packets.

**Touch surface**: ~66 SQL queries across db.js/server.js/packet-store.js. ~12 frontend files consume packet data.

---

## Milestone 1: Schema Migration (Backend Only)

**Goal**: New tables exist, data migrated, old table preserved as backup. No behavioral changes yet.

### Tasks
1. **Create new schema** in `db.js` init:
   ```sql
   CREATE TABLE IF NOT EXISTS transmissions (
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
   
   CREATE TABLE IF NOT EXISTS observations (
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
   ```

2. **Write migration script** (`scripts/migrate-dedup.js`):
   - Read all rows from `packets` ordered by timestamp
   - Group by hash
   - For each unique hash: INSERT into `transmissions` (use first observation's raw_hex, decoded_json, etc.)
   - For each row: INSERT into `observations` with foreign key to transmission
   - Verify counts: `SELECT COUNT(*) FROM observations` = old packets count
   - Verify: `SELECT COUNT(*) FROM transmissions` < observations count
   - **Do NOT drop old `packets` table** — rename to `packets_backup`

3. **Print migration stats**: total packets, unique transmissions, dedup ratio, time taken

### Validation
- `COUNT(*) FROM observations` = `COUNT(*) FROM packets_backup`
- `COUNT(*) FROM transmissions` = `COUNT(DISTINCT hash) FROM packets_backup`
- Spot-check: pick 5 known multi-observer packets, verify transmission + observations match

### Risk: LOW — additive only, old data preserved

---

## Milestone 2: Dual-Write Ingest

**Goal**: New packets written to both old and new tables. Read path unchanged. Zero downtime.

### Tasks
1. **Update `db.js` `insertPacket()`**:
   - On new packet: check if `transmissions` row exists for hash
   - If not: INSERT into `transmissions`, get id
   - If yes: UPDATE `first_seen` if this timestamp is earlier
   - INSERT into `observations` with transmission_id
   - **Still also write to old `packets` table** (dual-write for safety)

2. **Update `packet-store.js` `insert()`**: Mirror the dual-write in memory model
   - Maintain both old flat array AND new `byTransmission` Map

### Validation
- Send test packets, verify they appear in both old and new tables
- Verify multi-observer packet creates 1 transmission + N observations

### Risk: LOW — old read path still works as fallback

---

## Milestone 3: In-Memory Store Restructure

**Goal**: `packet-store.js` switches from flat packet array to transmission-centric model.

### Tasks
1. **New in-memory data model**:
   ```
   transmissions: Map<hash, {id, raw_hex, hash, first_seen, payload_type, decoded_json, observations: []}>
   ```
   Each observation: `{id, observer_id, observer_name, snr, rssi, path_json, timestamp}`

2. **Update indexes**:
   - `byHash`: hash → transmission object (1:1 instead of 1:N)
   - `byObserver`: observer_id → [observation references]
   - `byNode`: pubkey → [transmission references] (deduped!)
   - `byId`: observation.id → observation (for backward compat with packet detail links)

3. **Update `load()`**: Read from `transmissions` JOIN `observations` instead of `packets`

4. **Update query methods**:
   - `findPackets()` — returns transmissions by default, with `.observations` attached
   - `findPacketsForNode()` — returns transmissions where node appears in ANY observation's path/decoded_json
   - `getSiblings()` — becomes `getObservations(hash)` — trivial, just return `transmission.observations`
   - `countForNode()` — returns `{transmissions: N, observations: M}`

### Validation
- All existing API endpoints return valid data
- Packet counts decrease (correctly!) for multi-observer nodes
- `/api/perf` shows no regression

### Risk: MEDIUM — core read path changes. Test thoroughly.

---

## Milestone 4: API Response Changes

**Goal**: APIs return deduped data with observation counts.

### Tasks
1. **`GET /api/packets`**:
   - Default: return transmissions (1 row per unique packet)
   - Each transmission includes `observation_count` and optionally `observations[]`
   - `?expand=observations` to include full observation list
   - `?groupByHash` becomes the default behavior (deprecate param)
   - Preserve `observer` filter: return transmissions where at least one observation matches

2. **`GET /api/nodes/:pubkey/health`**:
   - `stats.totalPackets` → `stats.totalTransmissions` (distinct hashes)
   - Add `stats.totalObservations` (old count, for reference)
   - `recentPackets` → returns transmissions with observation_count

3. **`GET /api/nodes/bulk-health`**: Same changes as health

4. **`GET /api/nodes/network-status`**: Use transmission counts

5. **`GET /api/nodes/:pubkey/analytics`**: All throughput charts use transmission counts

6. **WebSocket broadcast**: Include `observation_count` when sibling observations exist for same hash

### Backward Compatibility
- Add `?legacy=1` param that returns old-style flat observations (for any external consumers)
- Include both `totalTransmissions` and `totalObservations` in health responses during transition

### Risk: MEDIUM — frontend expects certain shapes. May need coordinated deploy with Milestone 5.

---

## Milestone 5: Frontend Updates

**Goal**: UI shows correct counts and leverages observation data.

### Tasks
1. **Packets page**:
   - Default view shows transmissions (already has groupByHash mode — make it default)
   - Expand row to see individual observations with their paths/SNR/RSSI
   - Badge: "×3 observers" on grouped rows

2. **Node detail panel** (nodes.js + live.js):
   - Show "X transmissions" not "X packets"  
   - Or "X packets (seen Y times)" to show both

3. **Home page**: Network stats use transmission counts

4. **Node analytics**: Throughput charts use transmissions

5. **Observer detail**: Keep observation counts (correct metric for observers)

6. **Analytics page**: Topology/RF analysis uses all observations (SNR per observation is valid data)

### Risk: LOW-MEDIUM — mostly display changes

---

## Milestone 6: Cleanup

**Goal**: Remove dual-write, drop old table, clean up.

### Tasks
1. Remove dual-write from `insertPacket()`
2. Drop `packets_backup` table (after confirming everything works for 1+ week)
3. Remove `?legacy=1` support if unused
4. Update DEDUP-DESIGN.md → mark as complete
5. VACUUM the database
6. Tag release (v2.3.0?)

### Risk: LOW — cleanup only, all functional changes already proven

---

## Estimated Scope

| Milestone | Files Modified | Complexity | Can Deploy Independently? |
|-----------|---------------|------------|--------------------------|
| 1. Schema Migration | db.js, new script | Low | Yes — additive only |
| 2. Dual-Write | db.js, packet-store.js | Low | Yes — old reads unchanged |
| 3. Memory Store | packet-store.js | Medium | No — must deploy with M4 |
| 4. API Changes | server.js, db.js | Medium | No — must deploy with M5 |
| 5. Frontend | 8+ public/*.js files | Medium | No — must deploy with M4 |
| 6. Cleanup | db.js, server.js | Low | Yes — after bake period |

**Milestones 1-2**: Safe to deploy independently, no user-visible changes.  
**Milestones 3-5**: Must ship together (API shape changes + frontend expects new shape).  
**Milestone 6**: Ship after 1 week bake.

## Open Questions

1. **Table naming**: `transmissions` + `observations`? Or keep `packets` + add `observations`? The word "transmission" is more accurate but "packet" is what the whole UI calls them.
2. **Packet detail URLs**: Currently `#/packet/123` uses the observation ID. Keep observation IDs as the URL key? Or switch to hash?
3. **Path dedup in paths table**: The `paths` table also has per-observation entries. Normalize that too, or leave as-is?
4. **Migration on prod**: Run migration script before deploying new code, or make new code handle both old and new schema?

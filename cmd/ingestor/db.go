package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/meshcore-analyzer/dbschema"
	"github.com/meshcore-analyzer/packetpath"
	"github.com/meshcore-analyzer/pgutil"
)

// DBStats tracks operational metrics for the ingestor database.
type DBStats struct {
	TransmissionsInserted  atomic.Int64
	ObservationsInserted   atomic.Int64
	DuplicateTransmissions atomic.Int64
	NodeUpserts            atomic.Int64
	ObserverUpserts        atomic.Int64
	WriteErrors            atomic.Int64
	SignatureDrops         atomic.Int64
	// RelayTouches counts nodes.last_seen refreshes driven by relay
	// participation rather than an ADVERT (#1598).
	RelayTouches atomic.Int64
	// WALCommits tracks every successful tx.Commit() that may have flushed
	// WAL pages.
	WALCommits atomic.Int64
	// BackfillUpdates tracks per-named-backfill row write counts so an
	// infinite-loop backfill (cf #1119) is obvious from the perf page.
	BackfillUpdates sync.Map // name (string) -> *atomic.Int64
}

// IncBackfill increments the backfill counter for the given name, allocating
// the counter on first use.
func (s *DBStats) IncBackfill(name string) {
	v, ok := s.BackfillUpdates.Load(name)
	if !ok {
		nc := new(atomic.Int64)
		actual, loaded := s.BackfillUpdates.LoadOrStore(name, nc)
		if loaded {
			v = actual
		} else {
			v = nc
		}
	}
	v.(*atomic.Int64).Add(1)
}

// SnapshotBackfills returns a name->count copy of all backfill counters.
func (s *DBStats) SnapshotBackfills() map[string]int64 {
	out := make(map[string]int64)
	s.BackfillUpdates.Range(func(k, v interface{}) bool {
		out[k.(string)] = v.(*atomic.Int64).Load()
		return true
	})
	return out
}

// Store wraps the PostgreSQL database for packet ingestion.
type Store struct {
	db       *sql.DB
	path     string // compatibility anchor for local file queues; never a database URL
	stateDir string
	Stats    DBStats

	stmtGetTxByHash            *sql.Stmt
	stmtInsertTransmission     *sql.Stmt
	stmtUpdateTxFirstSeen      *sql.Stmt
	stmtBumpTxLastSeen         *sql.Stmt
	stmtInsertObservation      *sql.Stmt
	stmtInsertAdvertEvidence   *sql.Stmt
	stmtUpsertNode             *sql.Stmt
	stmtIncrementAdvertCount   *sql.Stmt
	stmtUpsertObserver         *sql.Stmt
	stmtGetObserverRowid       *sql.Stmt
	stmtUpdateObserverLastSeen *sql.Stmt
	stmtUpdateNodeTelemetry    *sql.Stmt
	stmtTouchNodeLastSeen      *sql.Stmt
	stmtUpsertMetrics          *sql.Stmt

	stmtGetLegacyAdvertObservation *sql.Stmt

	sampleIntervalSec int
	backfillWg        sync.WaitGroup

	advertEvidenceComplete atomic.Bool // restored from durable migration status

	// prefixIdx holds the prefix → pubkey index used by the
	// resolved_path writer (#1547). Rebuilt on startup and once per
	// neighbor-edges builder tick (60s).
	prefixIdx prefixIdxHolder

	// neighborGraph holds the in-memory NeighborGraph snapshot used
	// by the context-aware resolver (#1560). Rebuilt on startup and
	// once per neighbor-edges builder tick (60s).
	neighborGraph neighborGraphHolder

	// relayTouched is the debounce map for touchRelayNodesLocked:
	// pubkey -> rxTime of the last last_seen write (#1598). Guarded by
	// writerMu, which InsertTransmission holds for its whole body.
	relayTouched map[string]time.Time
}

// relayTouchDebounce is the minimum interval between two last_seen writes
// for the same relay node. A backbone repeater appears in thousands of
// paths per hour; without this the ingest path would issue one UPDATE per
// observation for no added freshness.
const relayTouchDebounce = 5 * time.Minute

// relayTouchedMaxEntries caps the debounce map. One entry per node ever
// seen relaying — ~10^3-10^4 on real deployments — but a long-lived
// process on a large mesh should not grow it without bound. On overflow
// we drop entries older than two debounce windows, which can only cause
// an extra UPDATE, never a missed one.
const relayTouchedMaxEntries = 50000

// OpenStore connects with a restricted telemetry writer credential.
func OpenStore(databaseURL string) (*Store, error) { return OpenStoreWithInterval(databaseURL, 300) }
func OpenStoreWithInterval(databaseURL string, sampleIntervalSec int) (*Store, error) {
	return OpenStoreWithState(databaseURL, "data", sampleIntervalSec)
}
func OpenStoreWithState(databaseURL, stateDir string, sampleIntervalSec int) (*Store, error) {
	db, err := pgutil.Open(databaseURL, false)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			db.Close()
		}
	}()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	var synchronous string
	if err = db.QueryRow(`SHOW synchronous_commit`).Scan(&synchronous); err != nil {
		return nil, err
	}
	if synchronous != "on" {
		return nil, fmt.Errorf("telemetry writer requires synchronous_commit=on")
	}

	if err = dbschema.AssertReady(db); err != nil {
		return nil, err
	}
	if err = dbschema.AssertWriter(db); err != nil {
		return nil, err
	}
	if stateDir == "" {
		stateDir = "data"
	}
	if err = os.MkdirAll(stateDir, 0700); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}
	s := &Store{db: db, path: filepath.Join(stateDir, "meshcore"), stateDir: stateDir, sampleIntervalSec: sampleIntervalSec}
	if err = s.prepareStatements(); err != nil {
		return nil, fmt.Errorf("prepare writer statements: %w", err)
	}
	if err = s.LoadScopeMatchTotals(); err != nil {
		log.Printf("[regions] restoring scope-match tally: %v", err)
	}
	var evidenceStatus string
	_ = db.QueryRow(`SELECT status FROM _async_migrations WHERE name='advert_route_evidence_v1'`).Scan(&evidenceStatus)
	s.advertEvidenceComplete.Store(evidenceStatus == "done")
	// The data backfill is resumable bookkeeping, never runtime DDL. It does
	// not block readiness, and only rows not maintained by live ingest match.
	if err = s.RunAsyncMigration(context.Background(), "tx_last_seen_backfill_v1", func(ctx context.Context, d *sql.DB) error {
		_, e := d.ExecContext(ctx, `UPDATE transmissions SET last_seen=COALESCE((SELECT MAX(timestamp) FROM observations WHERE transmission_id=transmissions.id),last_seen) WHERE last_seen=0`)
		return e
	}); err != nil {
		return nil, err
	}

	ok = true
	return s, nil
}

func (s *Store) prepareStatements() error {
	var err error
	s.stmtInsertAdvertEvidence, err = s.db.Prepare(insertAdvertEvidenceSQL)
	if err != nil {
		return err
	}
	s.stmtGetLegacyAdvertObservation, err = s.db.Prepare(legacyAdvertObservationSQL)
	if err != nil {
		return err
	}

	s.stmtGetTxByHash, err = s.db.Prepare("SELECT id, first_seen FROM transmissions WHERE hash = $1")
	if err != nil {
		return err
	}

	s.stmtInsertTransmission, err = s.db.Prepare(`
		INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, payload_version, decoded_json, channel_hash, scope_name, from_pubkey, last_seen, code1, code2)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13) RETURNING id
	`)
	if err != nil {
		return err
	}

	s.stmtUpdateTxFirstSeen, err = s.db.Prepare("UPDATE transmissions SET first_seen = $1 WHERE id = $2")
	if err != nil {
		return err
	}

	// #1690: bump transmissions.last_seen to MAX(current, ?) on every
	// observation insert so cold-load can filter on effective recency.
	// This is NOT a migration — it's the steady-state writer path. The
	// one-time backfill (BackfillPathJSONAsync-shaped) runs via
	// RunAsyncMigration above; this prepared-statement UPDATE is the
	// per-row maintenance that keeps the column current after the
	// backfill completes. Recorded in _migrations under
	// "tx_last_seen_backfill_v1".
	// PREFLIGHT: async=true reason="prepared-statement row-level UPDATE BY PRIMARY KEY (transmissions.id) — single-row touch per observation, indexed by PK, constant-time at any scale. Not a migration."
	s.stmtTouchNodeLastSeen, err = s.db.Prepare(
		"UPDATE nodes SET last_seen = $1 WHERE public_key = $2 AND (last_seen IS NULL OR last_seen < $3)")
	if err != nil {
		return fmt.Errorf("preparing touch node last_seen: %w", err)
	}

	s.stmtBumpTxLastSeen, err = s.db.Prepare("UPDATE transmissions SET last_seen = $1 WHERE id = $2 AND last_seen < $3")
	if err != nil {
		return err
	}

	s.stmtInsertObservation, err = s.db.Prepare(`
		INSERT INTO observations (transmission_id, observer_idx, direction, snr, rssi, score, path_json, timestamp, raw_hex, resolved_path)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT(transmission_id, observer_idx, COALESCE(path_json, '')) DO UPDATE SET
			snr           = COALESCE(excluded.snr,           observations.snr),
			rssi          = COALESCE(excluded.rssi,          observations.rssi),
			score         = COALESCE(excluded.score,         observations.score),
			raw_hex       = COALESCE(excluded.raw_hex,       observations.raw_hex),
			resolved_path = COALESCE(excluded.resolved_path, observations.resolved_path)
	`)
	if err != nil {
		return err
	}

	s.stmtUpsertNode, err = s.db.Prepare(`
		INSERT INTO nodes (public_key, name, role, lat, lon, last_seen, first_seen)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT(public_key) DO UPDATE SET
			name = COALESCE($8, nodes.name),
			role = COALESCE($9, nodes.role),
			lat = COALESCE($10, nodes.lat),
			lon = COALESCE($11, nodes.lon),
			last_seen = GREATEST(LEAST(COALESCE(nodes.last_seen, ''), $12), $13)
	`)
	if err != nil {
		return err
	}

	s.stmtIncrementAdvertCount, err = s.db.Prepare(`
		UPDATE nodes SET advert_count = advert_count + 1 WHERE public_key = $1
	`)
	if err != nil {
		return err
	}

	s.stmtUpsertObserver, err = s.db.Prepare(`
		INSERT INTO observers (id, name, iata, last_seen, first_seen, packet_count, model, firmware, client_version, radio, battery_mv, uptime_secs, noise_floor, can_relay, can_relay_seen)
		VALUES ($1, $2, $3, $4, $5, 1, $6, $7, $8, $9, $10, $11, $12, COALESCE($13, 1), CASE WHEN $14::bigint IS NULL THEN 0 ELSE 1 END)
		ON CONFLICT(id) DO UPDATE SET
			name = COALESCE($15, observers.name),
			iata = COALESCE($16, observers.iata),
			last_seen = GREATEST(LEAST(COALESCE(observers.last_seen, ''), $17), $18),
			packet_count = observers.packet_count + 1,
			model = COALESCE($19, observers.model),
			firmware = COALESCE($20, observers.firmware),
			client_version = COALESCE($21, observers.client_version),
			radio = COALESCE($22, observers.radio),
			battery_mv = COALESCE($23, observers.battery_mv),
			uptime_secs = COALESCE($24, observers.uptime_secs),
			noise_floor = COALESCE($25, observers.noise_floor),
			can_relay = COALESCE($26, observers.can_relay),
			can_relay_seen = CASE WHEN $27::bigint IS NULL THEN observers.can_relay_seen ELSE 1 END
	`)
	if err != nil {
		return err
	}

	s.stmtGetObserverRowid, err = s.db.Prepare("SELECT rowid FROM observers WHERE id = $1")
	if err != nil {
		return err
	}

	// Args: ingestNow, rxTime, ingestNow, rxTime, rowid
	// MIN(existing, ingestNow) clamps any future value already in the DB before
	// taking MAX with rxTime, so the guard never locks in a past bug's stale future.
	s.stmtUpdateObserverLastSeen, err = s.db.Prepare(`
		UPDATE observers SET
			last_seen      = GREATEST(LEAST(COALESCE(last_seen, ''), $1), $2),
			last_packet_at = GREATEST(LEAST(COALESCE(last_packet_at, ''), $3), $4)
		WHERE rowid = $5`)
	if err != nil {
		return err
	}

	s.stmtUpdateNodeTelemetry, err = s.db.Prepare(`
		UPDATE nodes SET
			battery_mv = COALESCE($1, battery_mv),
			temperature_c = COALESCE($2, temperature_c)
		WHERE public_key = $3
	`)
	if err != nil {
		return err
	}

	s.stmtUpsertMetrics, err = s.db.Prepare(`
		INSERT INTO observer_metrics (observer_id, timestamp, noise_floor, tx_air_secs, rx_air_secs, recv_errors, battery_mv, packets_sent, packets_recv)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
 ON CONFLICT(observer_id,timestamp) DO UPDATE SET noise_floor=excluded.noise_floor,
 tx_air_secs=excluded.tx_air_secs,rx_air_secs=excluded.rx_air_secs,recv_errors=excluded.recv_errors,
 battery_mv=excluded.battery_mv,packets_sent=excluded.packets_sent,packets_recv=excluded.packets_recv
 `)
	if err != nil {
		return err
	}

	return nil
}

// InsertTransmission inserts a decoded packet into transmissions + observations.
// Returns true if a new transmission was created (not a duplicate hash).
func (s *Store) InsertTransmission(data *PacketData) (bool, error) {
	if data.Hash == "" {
		return false, nil
	}
	waitStart := time.Now()
	writerMu.Lock()
	wait := time.Since(waitStart)
	holdStart := time.Now()
	defer func() {
		writerMu.Unlock()
		recordWriterTiming("mqtt_handler", wait, time.Since(holdStart), "InsertTransmission")
	}()
	tx, err := beginWrite(s.db)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	ingestNow := time.Now().UTC().Format(time.RFC3339)
	rxTime := data.Timestamp
	if rxTime == "" {
		rxTime = ingestNow
	}
	var txID int64
	var firstSeen string
	err = tx.Stmt(s.stmtGetTxByHash).QueryRow(data.Hash).Scan(&txID, &firstSeen)
	isNew := err == sql.ErrNoRows
	if err != nil && !isNew {
		return false, err
	}
	if isNew {
		err = tx.Stmt(s.stmtInsertTransmission).QueryRow(data.RawHex, data.Hash, rxTime, data.RouteType, data.PayloadType, data.PayloadVersion, data.DecodedJSON, nilIfEmpty(data.ChannelHash), scopeNameForDB(data), nilIfEmpty(data.FromPubkey), epochSecondsForLastSeen(rxTime), nilIfEmpty(data.Code1), nilIfEmpty(data.Code2)).Scan(&txID)
	} else if rxTime < firstSeen {
		_, err = tx.Stmt(s.stmtUpdateTxFirstSeen).Exec(rxTime, txID)
	}
	if err != nil {
		s.Stats.WriteErrors.Add(1)
		return false, fmt.Errorf("write transmission: %w", err)
	}
	if data.PayloadType == 4 {
		if bit := packetpath.AdvertRouteEvidence(data.RawHex); bit != 0 {
			if err := optionalWrite(tx, func() error { _, e := tx.Stmt(s.stmtInsertAdvertEvidence).Exec(txID, bit, txID, bit); return e }); err != nil {
				s.Stats.WriteErrors.Add(1)
				log.Printf("[db] record advert route evidence (non-fatal): %v", err)
			}
		}
	}
	var observerIdx *int64
	if data.ObserverID != "" {
		var id int64
		err = tx.Stmt(s.stmtGetObserverRowid).QueryRow(data.ObserverID).Scan(&id)
		if err == nil {
			observerIdx = &id
			_, err = tx.Stmt(s.stmtUpdateObserverLastSeen).Exec(ingestNow, ingestNow, ingestNow, ingestNow, id)
		}
		if err != nil && err != sql.ErrNoRows {
			return false, err
		}
	}
	if !isNew && data.PayloadType == 4 && observerIdx != nil && data.RawHex != "" {
		if err := optionalWrite(tx, func() error { return s.preserveLegacyAdvertObservation(tx, txID, *observerIdx, data.PathJSON) }); err != nil {
			s.Stats.WriteErrors.Add(1)
			log.Printf("[db] preserve legacy advert evidence (non-fatal): %v", err)
		}
	}
	epochTs := time.Now().Unix()
	if t, e := time.Parse(time.RFC3339, rxTime); e == nil {
		epochTs = t.Unix()
	}
	resolved := resolvePathWithContext(parsePathArray(data.PathJSON), strings.ToLower(data.FromPubkey), s.neighborGraph.load(), s.prefixIdx.load())
	_, err = tx.Stmt(s.stmtInsertObservation).Exec(txID, observerIdx, data.Direction, data.SNR, data.RSSI, data.Score, data.PathJSON, epochTs, nilIfEmpty(data.RawHex), nilIfEmpty(marshalResolvedPath(resolved)))
	if err != nil {
		s.Stats.WriteErrors.Add(1)
		return false, fmt.Errorf("insert observation: %w", err)
	}
	if _, err = tx.Stmt(s.stmtBumpTxLastSeen).Exec(epochTs, txID, epochTs); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		s.Stats.WriteErrors.Add(1)
		return false, fmt.Errorf("commit packet: %w", err)
	}
	if isNew {
		s.Stats.TransmissionsInserted.Add(1)
	} else {
		s.Stats.DuplicateTransmissions.Add(1)
	}
	s.Stats.ObservationsInserted.Add(1)
	s.Stats.WALCommits.Add(1)
	s.touchRelayNodesLocked(resolvedPubkeys(resolved), rxTime)
	return isNew, nil
}

// beginWrite serializes ID allocation before any statement can consume an
// identity value. A transaction lock releases only after commit or rollback.
func beginWrite(db *sql.DB) (*sql.Tx, error) { return beginWriteContext(context.Background(), db) }
func beginWriteContext(ctx context.Context, db *sql.DB) (*sql.Tx, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(current_schema(),$1))`, dbschema.WriterLockKey); err != nil {
		tx.Rollback()
		return nil, err
	}
	return tx, nil
}

// Analytics are best effort; a failed PostgreSQL statement otherwise poisons
// the packet transaction. Savepoints preserve the existing non-fatal behavior.
func optionalWrite(tx *sql.Tx, fn func() error) error {
	if _, err := tx.Exec(`SAVEPOINT packet_analytics`); err != nil {
		return err
	}
	err := fn()
	if err != nil {
		if _, rollbackErr := tx.Exec(`ROLLBACK TO SAVEPOINT packet_analytics`); rollbackErr != nil {
			return rollbackErr
		}
	}
	_, releaseErr := tx.Exec(`RELEASE SAVEPOINT packet_analytics`)
	if err != nil {
		return err
	}
	return releaseErr
}

// UpsertNode inserts or updates a node.
func (s *Store) UpsertNode(pubKey, name, role string, lat, lon *float64, lastSeen string) error {
	ingestNow := time.Now().UTC().Format(time.RFC3339)
	now := lastSeen
	if now == "" {
		now = ingestNow
	}
	_, err := s.stmtUpsertNode.Exec(
		pubKey, name, role, lat, lon, now, now,
		name, role, lat, lon, ingestNow, now,
	)
	if err != nil {
		s.Stats.WriteErrors.Add(1)
	} else {
		s.Stats.NodeUpserts.Add(1)
	}
	return err
}

// IncrementAdvertCount increments advert_count for a node by public key.
func (s *Store) IncrementAdvertCount(pubKey string) error {
	_, err := s.stmtIncrementAdvertCount.Exec(pubKey)
	return err
}

// MarkNodeForeign sets foreign_advert=1 on the node row identified by pubKey.
// Used when an ADVERT arrives whose GPS lies outside the configured geofilter
// polygon (#730). Idempotent — safe to call repeatedly. No-op if pubKey is
// empty.
func (s *Store) MarkNodeForeign(pubKey string) error {
	if pubKey == "" {
		return nil
	}
	_, err := s.db.Exec(`UPDATE nodes SET foreign_advert = 1 WHERE public_key = $1`, pubKey)
	if err != nil {
		s.Stats.WriteErrors.Add(1)
	}
	return err
}

// UpdateNodeTelemetry updates battery and temperature for a node.
func (s *Store) UpdateNodeTelemetry(pubKey string, batteryMv *int, temperatureC *float64) error {
	var bv, tc interface{}
	if batteryMv != nil {
		bv = *batteryMv
	}
	if temperatureC != nil {
		tc = *temperatureC
	}
	_, err := s.stmtUpdateNodeTelemetry.Exec(bv, tc, pubKey)
	if err != nil {
		s.Stats.WriteErrors.Add(1)
	}
	return err
}

// ObserverMeta holds optional observer hardware metadata.
type ObserverMeta struct {
	Model         *string  // e.g., L1
	Firmware      *string  // firmware version string
	ClientVersion *string  // client app version string
	Radio         *string  // radio chipset/platform string
	BatteryMv     *int     // millivolts, always integer
	UptimeSecs    *int64   // seconds, always integer
	NoiseFloor    *float64 // dBm, may have decimals
	TxAirSecs     *int     // cumulative TX seconds since boot
	RxAirSecs     *int     // cumulative RX seconds since boot
	RecvErrors    *int     // cumulative CRC/decode failures since boot
	PacketsSent   *int     // cumulative packets sent since boot
	PacketsRecv   *int     // cumulative packets received since boot
	// CanRelay reflects the firmware 1.16 /status `repeat` flag (#1290).
	// nil means the firmware did not send the field — caller must
	// preserve the existing observers.can_relay value (default 1).
	// true → relay-capable (`repeat:on`); false → listener-only
	// (`repeat:off`), which causes the server-side disambiguator to
	// exclude this observer's pubkey from path-hop candidate sets.
	CanRelay *bool
}

// UpsertObserver inserts or updates an observer using the current wall-clock
// time as last_seen. Use UpsertObserverAt when the message envelope provides
// an observer receive-time (e.g. MQTT status and data packet handlers).
func (s *Store) UpsertObserver(id, name, iata string, meta *ObserverMeta) error {
	return s.UpsertObserverAt(id, name, iata, meta, time.Now().UTC().Format(time.RFC3339))
}

// UpsertObserverAt inserts or updates an observer with an explicit lastSeen
// timestamp (typically the observer receive-time from the MQTT envelope). The
// SQL uses MAX so last_seen never moves backwards — a retained or replayed
// message whose rxTime pre-dates the existing last_seen is a no-op for that
// field, preventing offline observers from flashing as Online on reconnect.
func (s *Store) UpsertObserverAt(id, name, iata string, meta *ObserverMeta, lastSeen string) error {
	ingestNow := time.Now().UTC().Format(time.RFC3339)
	if lastSeen == "" {
		lastSeen = ingestNow
	}
	normalizedIATA := strings.TrimSpace(strings.ToUpper(iata))

	model, firmware, clientVersion, radio, batteryMv, uptimeSecs, noiseFloor, canRelay := observerMetaColumns(meta)

	_, err := s.stmtUpsertObserver.Exec(
		id, name, normalizedIATA, lastSeen, lastSeen, model, firmware, clientVersion, radio, batteryMv, uptimeSecs, noiseFloor, canRelay, canRelay,
		name, normalizedIATA, ingestNow, lastSeen, model, firmware, clientVersion, radio, batteryMv, uptimeSecs, noiseFloor, canRelay, canRelay,
	)
	if err != nil {
		s.Stats.WriteErrors.Add(1)
		return err
	}
	s.Stats.ObserverUpserts.Add(1)

	// Reactivate if this observer was previously marked inactive
	s.db.Exec(`UPDATE observers SET inactive = 0 WHERE id = $1 AND inactive = 1`, id)
	return nil
}

// UpsertObserverRetained applies the metadata from a RETAINED status message
// without treating it as a sign of life. The broker replays retained messages
// on every subscribe, so an ingestor restart would otherwise stamp last_seen
// with the restart time for every observer that ever published one — making
// dead observers permanently un-ageable by RemoveStaleObservers, and undoing
// any inactive flag it did manage to set.
//
// Deliberately narrower than UpsertObserverAt: it updates metadata columns on
// an existing row only. It does not advance last_seen, does not clear
// inactive, does not bump packet_count, and does not INSERT — a retained-only
// observer the analyzer has never heard from live describes a past that may be
// months old and does not belong in the list. A live message from the same
// observer arrives moments later and creates the row through the normal path.
func (s *Store) UpsertObserverRetained(id, name, iata string, meta *ObserverMeta) error {
	normalizedIATA := strings.TrimSpace(strings.ToUpper(iata))
	model, firmware, clientVersion, radio, batteryMv, uptimeSecs, noiseFloor, canRelay := observerMetaColumns(meta)

	_, err := s.db.Exec(`
		UPDATE observers SET
			name = COALESCE($1, name),
			iata = COALESCE($2, iata),
			model = COALESCE($3, model),
			firmware = COALESCE($4, firmware),
			client_version = COALESCE($5, client_version),
			radio = COALESCE($6, radio),
			battery_mv = COALESCE($7, battery_mv),
			uptime_secs = COALESCE($8, uptime_secs),
			noise_floor = COALESCE($9, noise_floor),
			can_relay = COALESCE($10, can_relay),
			can_relay_seen = CASE WHEN $11::bigint IS NULL THEN can_relay_seen ELSE 1 END
		WHERE id = $12`,
		name, normalizedIATA, model, firmware, clientVersion, radio,
		batteryMv, uptimeSecs, noiseFloor, canRelay, canRelay, id,
	)
	if err != nil {
		s.Stats.WriteErrors.Add(1)
		return err
	}
	return nil
}

// observerMetaColumns flattens an *ObserverMeta into the driver args the
// observer upserts bind. A nil field stays nil so the COALESCE in the SQL
// leaves the existing column untouched (#1290).
func observerMetaColumns(meta *ObserverMeta) (model, firmware, clientVersion, radio, batteryMv, uptimeSecs, noiseFloor, canRelay interface{}) {
	if meta != nil {
		if meta.Model != nil {
			model = *meta.Model
		}
		if meta.Firmware != nil {
			firmware = *meta.Firmware
		}
		if meta.ClientVersion != nil {
			clientVersion = *meta.ClientVersion
		}
		if meta.Radio != nil {
			radio = *meta.Radio
		}
		if meta.BatteryMv != nil {
			batteryMv = *meta.BatteryMv
		}
		if meta.UptimeSecs != nil {
			uptimeSecs = *meta.UptimeSecs
		}
		if meta.NoiseFloor != nil {
			noiseFloor = *meta.NoiseFloor
		}
		// Issue #1290: nil → leave DB column unchanged (COALESCE in
		// the prepared stmt); 0/1 written when firmware provided
		// the `repeat` field. INSERT branch defaults to 1 via the
		// COALESCE in the VALUES clause.
		if meta.CanRelay != nil {
			if *meta.CanRelay {
				canRelay = 1
			} else {
				canRelay = 0
			}
		}
	}
	return model, firmware, clientVersion, radio, batteryMv, uptimeSecs, noiseFloor, canRelay
}

// Close waits for data backfills and closes the database.
func (s *Store) Close() error {
	s.backfillWg.Wait()
	return s.db.Close()
}

// RoundToInterval rounds a time to the nearest sample interval boundary.
func RoundToInterval(t time.Time, intervalSec int) time.Time {
	if intervalSec <= 0 {
		intervalSec = 300
	}
	epoch := t.Unix()
	half := int64(intervalSec) / 2
	rounded := ((epoch + half) / int64(intervalSec)) * int64(intervalSec)
	return time.Unix(rounded, 0).UTC()
}

// MetricsData holds the fields to insert into observer_metrics.
type MetricsData struct {
	ObserverID  string
	NoiseFloor  *float64
	TxAirSecs   *int
	RxAirSecs   *int
	RecvErrors  *int
	BatteryMv   *int
	PacketsSent *int
	PacketsRecv *int
}

// InsertMetrics inserts a metrics sample for an observer using ingestor wall clock.
func (s *Store) InsertMetrics(data *MetricsData) error {
	ts := RoundToInterval(time.Now().UTC(), s.sampleIntervalSec)
	tsStr := ts.Format(time.RFC3339)

	var nf, txAir, rxAir, recvErr, batt, pktSent, pktRecv interface{}
	if data.NoiseFloor != nil {
		nf = *data.NoiseFloor
	}
	if data.TxAirSecs != nil {
		txAir = *data.TxAirSecs
	}
	if data.RxAirSecs != nil {
		rxAir = *data.RxAirSecs
	}
	if data.RecvErrors != nil {
		recvErr = *data.RecvErrors
	}
	if data.BatteryMv != nil {
		batt = *data.BatteryMv
	}
	if data.PacketsSent != nil {
		pktSent = *data.PacketsSent
	}
	if data.PacketsRecv != nil {
		pktRecv = *data.PacketsRecv
	}

	_, err := s.stmtUpsertMetrics.Exec(data.ObserverID, tsStr, nf, txAir, rxAir, recvErr, batt, pktSent, pktRecv)
	if err != nil {
		s.Stats.WriteErrors.Add(1)
		return fmt.Errorf("insert metrics: %w", err)
	}
	return nil
}

// PruneOldMetrics deletes observer_metrics rows older than retentionDays.
func (s *Store) PruneOldMetrics(retentionDays int) (int64, error) {
	cutoff := time.Now().UTC().AddDate(0, 0, -retentionDays).Format(time.RFC3339)
	// Tagged for /api/perf writer-lock visibility (#1340).
	result, err := s.instrumentedExec("prune_metrics", `DELETE FROM observer_metrics WHERE timestamp < $1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("prune metrics: %w", err)
	}
	n, _ := result.RowsAffected()
	if n > 0 {
		log.Printf("[metrics] Pruned %d rows older than %d days", n, retentionDays)
	}
	return n, nil
}

// BackfillPathJSONAsync launches the path_json backfill in a background goroutine.
// It processes observations with NULL/empty path_json that have raw_hex available,
// decoding hop paths and updating the column. Safe to run concurrently with ingest
// because new observations get path_json at write time; this only touches NULL rows.
// Idempotent: skips if migration already recorded.
func (s *Store) BackfillPathJSONAsync() {
	s.backfillWg.Add(1)
	go func() {
		defer s.backfillWg.Done()
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[backfill] path_json async panic recovered: %v", r)
			}
		}()

		var migDone int
		row := s.db.QueryRow("SELECT 1 FROM _migrations WHERE name = 'backfill_path_json_from_raw_hex_v1'")
		if row.Scan(&migDone) == nil {
			return // already done
		}

		log.Println("[backfill] Starting async path_json backfill from raw_hex...")
		updated := 0
		errored := false
		const batchSize = 1000
		batchNum := 0
		for {
			rows, err := s.db.Query(`
				SELECT o.id, o.raw_hex
				FROM observations o
				JOIN transmissions t ON o.transmission_id = t.id
				WHERE o.raw_hex IS NOT NULL AND o.raw_hex != ''
				-- NB: '[]' is the "already attempted, no hops" sentinel; excluded
				-- to prevent the infinite re-UPDATE loop fixed in #1119.
				AND (o.path_json IS NULL OR o.path_json = '')
				AND t.payload_type != 9
				LIMIT $1`, batchSize)
			if err != nil {
				log.Printf("[backfill] path_json query error: %v", err)
				errored = true
				break
			}
			type pendingRow struct {
				id     int64
				rawHex string
			}
			var batch []pendingRow
			for rows.Next() {
				var r pendingRow
				if err := rows.Scan(&r.id, &r.rawHex); err == nil {
					batch = append(batch, r)
				}
			}
			rows.Close()
			if len(batch) == 0 {
				break
			}
			for _, r := range batch {
				hops, err := packetpath.DecodePathFromRawHex(r.rawHex)
				if err != nil || len(hops) == 0 {
					if _, execErr := s.db.Exec(`UPDATE observations SET path_json = '[]' WHERE id = $1`, r.id); execErr != nil {
						log.Printf("[backfill] write error (id=%d): %v", r.id, execErr)
					} else {
						s.Stats.IncBackfill("path_json")
					}
					continue
				}
				b, _ := json.Marshal(hops)
				if _, execErr := s.db.Exec(`UPDATE observations SET path_json = $1 WHERE id = $2`, string(b), r.id); execErr != nil {
					log.Printf("[backfill] write error (id=%d): %v", r.id, execErr)
				} else {
					updated++
					s.Stats.IncBackfill("path_json")
				}
			}
			batchNum++
			if batchNum%50 == 0 {
				log.Printf("[backfill] progress: %d observations updated so far (%d batches)", updated, batchNum)
			}
			// Throttle: yield to ingest writers between batches
			time.Sleep(50 * time.Millisecond)
		}
		log.Printf("[backfill] Async path_json backfill complete: %d observations updated", updated)
		if !errored {
			s.db.Exec(`INSERT INTO _migrations (name) VALUES ('backfill_path_json_from_raw_hex_v1')`)
		} else {
			log.Printf("[backfill] NOT recording migration due to errors — will retry on next restart")
		}
	}()
}

// BackfillDefaultScopeAsync populates default_scope for existing nodes that have
// transport-scoped ADVERT rows (scope_name IS NOT NULL AND scope_name != “).
// Runs in a background goroutine so it does not block MQTT startup.
// Uses the from_pubkey index — O(nodes × indexed lookup), not a full table scan.
//
// Concurrency: the store uses SetMaxOpenConns(1) so all DB writes — including
// MQTT packet inserts and any concurrent backfill goroutines — serialize through
// the single connection pool. busy_timeout(5000) handles transient cross-process
// contention with the read-only server process. No additional locking is needed.
func (s *Store) BackfillDefaultScopeAsync(regionSet *regionKeySet) {
	// No region keys at all — every scope_name is NULL, nothing to backfill.
	// The emptiness gate is read once here rather than per row: the loop below
	// uses no keys, it only copies names that are already stored.
	if len(regionSet.snapshot().all) == 0 {
		return
	}
	s.backfillWg.Add(1)
	go func() {
		defer s.backfillWg.Done()
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[backfill] default_scope async panic recovered: %v", r)
			}
		}()

		var done int
		if s.db.QueryRow("SELECT 1 FROM _migrations WHERE name = 'backfill_default_scope_v1'").Scan(&done) == nil {
			return // already ran
		}

		res, err := s.db.Exec(`
			UPDATE nodes SET default_scope = (
				SELECT t.scope_name FROM transmissions t
				WHERE t.from_pubkey = nodes.public_key
				  AND t.payload_type = 4
				  AND t.scope_name IS NOT NULL AND t.scope_name != ''
				ORDER BY t.first_seen DESC LIMIT 1  -- most-recently observed scope wins; first_seen is insertion time
			) WHERE EXISTS (
				SELECT 1 FROM transmissions t
				WHERE t.from_pubkey = nodes.public_key
				  AND t.payload_type = 4
				  AND t.scope_name IS NOT NULL AND t.scope_name != ''
			)`)
		if err != nil {
			log.Printf("[backfill] default_scope: %v", err)
			return
		}
		n, _ := res.RowsAffected()
		s.Stats.IncBackfill("default_scope")
		log.Printf("[backfill] default_scope populated for %d nodes", n)
		s.db.Exec(`INSERT INTO _migrations (name) VALUES ('backfill_default_scope_v1')`)
	}()
}

// BackfillTransportCodesAsync populates transmissions.code1/code2 for rows
// inserted before the transport_codes_v1 migration, by re-parsing raw_hex.
// Runs in a background goroutine so it does not block MQTT startup.
//
// Batched: the ingestor retains a single write connection, so a
// multi-million-row UPDATE in one transaction would stall live ingest for the
// whole run. 5k-row batches keep each transaction short.
func (s *Store) BackfillTransportCodesAsync() {
	s.backfillWg.Add(1)
	go func() {
		defer s.backfillWg.Done()
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[backfill] transport_codes async panic recovered: %v", r)
			}
		}()

		var done int
		if s.db.QueryRow("SELECT 1 FROM _migrations WHERE name = 'backfill_transport_codes_v1'").Scan(&done) == nil {
			return // already ran
		}

		total, err := s.backfillTransportCodes(5000)
		if err != nil {
			log.Printf("[backfill] transport_codes: %v", err)
			return // NOT recording the guard row — retry from scratch on next restart
		}
		s.Stats.IncBackfill("transport_codes")
		log.Printf("[backfill] transport_codes populated for %d transmissions", total)
		s.db.Exec(`INSERT INTO _migrations (name) VALUES ('backfill_transport_codes_v1')`)
	}()
}

// backfillTransportCodes does the actual batched decode-and-update work,
// returning the number of rows updated. Uses keyset pagination on id rather
// than counting decoded items per page: a page can hit the LIMIT while
// containing undecodable rows, so "items returned < batchSize" does NOT mean
// "no rows remain" — that conflation previously caused the backfill to record
// its completion migration after silently truncating on the first page with
// an undecodable row. Termination is driven by how many rows the page
// actually scanned (seen), not by how many decoded successfully; lastID
// advances past undecodable rows too, so they are stepped over rather than
// re-selected forever.
//
// Invariant: the caller records the backfill_transport_codes_v1 guard row iff
// this returns a nil error, i.e. iff the loop ran to genuine exhaustion of all
// matching rows. Every error path below returns non-nil immediately, without
// finishing the run, so the guard is withheld and the next startup retries.
func (s *Store) backfillTransportCodes(batchSize int) (int, error) {
	var total int
	var lastID int64
	for {
		rows, err := s.db.Query(`
			SELECT id, raw_hex FROM transmissions
			WHERE id > $1 AND code1 IS NULL AND route_type IN (0, 3)
			ORDER BY id
			LIMIT $2`, lastID, batchSize)
		if err != nil {
			return total, fmt.Errorf("transport_codes select: %w", err)
		}
		type item struct {
			id           int64
			code1, code2 string
		}
		var items []item
		var seen int
		for rows.Next() {
			var id int64
			var raw string
			if err := rows.Scan(&id, &raw); err != nil {
				rows.Close()
				return total, fmt.Errorf("transport_codes scan: %w", err)
			}
			seen++
			lastID = id // advance past undecodable rows too, or they are re-selected forever
			decoded, err := DecodePacket(raw, nil, false)
			if err != nil || decoded.TransportCodes == nil {
				continue
			}
			items = append(items, item{id, decoded.TransportCodes.Code1, decoded.TransportCodes.Code2})
		}
		rows.Close()
		if seen == 0 {
			break // no rows left matching the filter
		}

		if len(items) > 0 {
			tx, err := beginWrite(s.db)
			if err != nil {
				return total, fmt.Errorf("transport_codes begin: %w", err)
			}
			stmt, err := tx.Prepare(`UPDATE transmissions SET code1 = $1, code2 = $2 WHERE id = $3`)
			if err != nil {
				tx.Rollback()
				return total, fmt.Errorf("transport_codes prepare: %w", err)
			}
			for _, it := range items {
				if _, err := stmt.Exec(it.code1, it.code2, it.id); err != nil {
					stmt.Close()
					tx.Rollback()
					return total, fmt.Errorf("transport_codes update id=%d: %w", it.id, err)
				}
			}
			stmt.Close()
			if err := tx.Commit(); err != nil {
				return total, fmt.Errorf("transport_codes commit: %w", err)
			}
			total += len(items)
		}
		if seen < batchSize {
			break // last partial page
		}
	}
	return total, nil
}

// LogStats logs current operational metrics.
func (s *Store) LogStats() {
	log.Printf("[stats] tx_inserted=%d tx_dupes=%d obs_inserted=%d node_upserts=%d observer_upserts=%d write_errors=%d sig_drops=%d",
		s.Stats.TransmissionsInserted.Load(),
		s.Stats.DuplicateTransmissions.Load(),
		s.Stats.ObservationsInserted.Load(),
		s.Stats.NodeUpserts.Load(),
		s.Stats.ObserverUpserts.Load(),
		s.Stats.WriteErrors.Load(),
		s.Stats.SignatureDrops.Load(),
	)
}

// touchRelayNodesLocked refreshes nodes.last_seen for nodes observed as
// relay hops, so a node that forwards traffic stays fresh even when it
// adverts rarely or not at all.
//
// MUST be called with writerMu held. InsertTransmission holds it for its
// entire body, so the debounce map needs no lock of its own; the name
// carries the requirement for future callers.
//
// Ownership (#1283/#1287/#1289): nodes is written by the ingestor only.
// The server uses a restricted PostgreSQL reader; its former touchRelayLastSeen has been
// failing on every call since that refactor and is removed in this change.
// cmd/server/readonly_invariant_test.go now guards against reintroduction.
//
// Callers pass the resolved pubkeys already computed for
// observations.resolved_path (#1547/#1560) — only unambiguously resolved
// hops reach this function, so a 1-byte prefix collision cannot keep a
// silent node alive. resolvedPubkeys already dedups, so no second pass here.
//
// Never inserts: the UPDATE matches an existing row or does nothing.
// Never rewinds: the last_seen guard makes out-of-order ingest a no-op.
// UpsertNode's ON CONFLICT clause is monotonic in the same direction
// (GREATEST(LEAST(last_seen, ingestNow), rxTime) reduces to MAX(last_seen, rxTime)
// for any non-future stored value), so an ADVERT cannot undo a touch.
func (s *Store) touchRelayNodesLocked(pubkeys []string, rxTime string) {
	if len(pubkeys) == 0 || s.stmtTouchNodeLastSeen == nil {
		return
	}
	// Reject unparsable timestamps rather than writing them into the node
	// directory. Callers hand us the observation rxTime, which comes off
	// the wire and is not guaranteed well-formed.
	ts, err := time.Parse(time.RFC3339, rxTime)
	if err != nil {
		return
	}
	// Same layout UpsertNode uses for last_seen, so the SQL comparisons
	// above stay lexicographic-equals-chronological.
	stamp := ts.UTC().Format(time.RFC3339)

	if s.relayTouched == nil {
		s.relayTouched = make(map[string]time.Time)
	}
	if len(s.relayTouched) >= relayTouchedMaxEntries {
		s.compactRelayTouched(ts)
	}
	for _, pk := range pubkeys {
		if pk == "" {
			continue
		}
		if last, ok := s.relayTouched[pk]; ok && ts.Sub(last) < relayTouchDebounce {
			continue
		}
		res, err := s.stmtTouchNodeLastSeen.Exec(stamp, pk, stamp)
		if err != nil {
			s.Stats.WriteErrors.Add(1)
			continue
		}
		// Debounce on attempt, not on row match: an unknown pubkey would
		// otherwise be retried on every observation it appears in.
		//
		// Side effect: a pubkey touched while absent from nodes, then
		// inserted by an ADVERT moments later, is skipped for the rest of
		// the window. Harmless — the ADVERT wrote last_seen itself, and it
		// is newer than anything this window would have written.
		s.relayTouched[pk] = ts
		if n, _ := res.RowsAffected(); n > 0 {
			s.Stats.RelayTouches.Add(n)
		}
	}
}

// compactRelayTouched drops debounce entries older than two windows.
// Caller must hold writerMu.
func (s *Store) compactRelayTouched(now time.Time) {
	cutoff := now.Add(-2 * relayTouchDebounce)
	for pk, t := range s.relayTouched {
		if t.Before(cutoff) {
			delete(s.relayTouched, pk)
		}
	}
}

// MoveStaleNodes moves nodes not seen in nodeDays to the inactive_nodes table.
// Returns the number of nodes moved.
func (s *Store) MoveStaleNodes(nodeDays int) (int64, error) {
	cutoff := time.Now().UTC().AddDate(0, 0, -nodeDays).Format(time.RFC3339)
	tx, err := beginWrite(s.db)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	_, err = tx.Exec(`INSERT INTO inactive_nodes (public_key,name,role,lat,lon,last_seen,first_seen,advert_count,battery_mv,temperature_c,foreign_advert,default_scope,configured_scope,configured_scope_at,multibyte_sup,multibyte_evidence) SELECT public_key,name,role,lat,lon,last_seen,first_seen,advert_count,battery_mv,temperature_c,foreign_advert,default_scope,configured_scope,configured_scope_at,multibyte_sup,multibyte_evidence FROM nodes WHERE last_seen < $1 ON CONFLICT(public_key) DO UPDATE SET name=excluded.name,role=excluded.role,lat=excluded.lat,lon=excluded.lon,last_seen=excluded.last_seen,first_seen=excluded.first_seen,advert_count=excluded.advert_count,battery_mv=excluded.battery_mv,temperature_c=excluded.temperature_c,foreign_advert=excluded.foreign_advert,default_scope=excluded.default_scope,configured_scope=excluded.configured_scope,configured_scope_at=excluded.configured_scope_at,multibyte_sup=excluded.multibyte_sup,multibyte_evidence=excluded.multibyte_evidence`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("insert inactive: %w", err)
	}
	result, err := tx.Exec(`DELETE FROM nodes WHERE last_seen < $1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("delete stale: %w", err)
	}
	moved, _ := result.RowsAffected()
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	if moved > 0 {
		log.Printf("Moved %d node(s) to inactive_nodes (not seen in %d days)", moved, nodeDays)
	}
	return moved, nil
}

// RemoveStaleObservers marks observers that have not actively sent data in observerDays
// as inactive (soft-delete). This preserves JOIN integrity for observations.observer_idx
// and observer_metrics.observer_id — historical data still references the correct observer.
// An observer must actively send data to stay listed — being seen by another node does not count.
// observerDays <= -1 means never remove (keep forever).
func (s *Store) RemoveStaleObservers(observerDays int) (int64, error) {
	if observerDays <= -1 {
		return 0, nil // keep forever
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -observerDays).Format(time.RFC3339)
	// Tagged for /api/perf writer-lock visibility (#1340).
	result, err := s.instrumentedExec("prune_observers", `UPDATE observers SET inactive = 1 WHERE last_seen < $1 AND (inactive IS NULL OR inactive = 0)`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("mark stale observers inactive: %w", err)
	}
	removed, _ := result.RowsAffected()
	if removed > 0 {
		// Clean up orphaned metrics for now-inactive observers
		_, _ = s.instrumentedExec("prune_observers", `DELETE FROM observer_metrics WHERE observer_id IN (SELECT id FROM observers WHERE inactive = 1)`)
		log.Printf("Marked %d observer(s) as inactive (not seen in %d days)", removed, observerDays)
	}
	return removed, nil
}

// PurgeStaleObservers hard-deletes rows RemoveStaleObservers already soft-deleted,
// once they are older than purgeDays and nothing references them any more. It is
// the second stage of observer retention: the soft-delete hides the observer,
// this reclaims the row after its packets have aged out via packetDays.
//
// observations.observer_idx is a bare rowid with no foreign key, so deleting a
// still-referenced observer silently orphans history — packets_v stops resolving
// the observer and the packets are mis-attributed. The three NOT EXISTS guards
// are what make the delete safe; they are correctness, not defensive padding.
// Each is an index seek per candidate row (observers is O(100)), so the whole
// statement stays cheap enough for the daily retention tick.
//
// purgeDays <= 0 disables the purge (the default).
func (s *Store) PurgeStaleObservers(purgeDays int) (int64, error) {
	if purgeDays <= 0 {
		return 0, nil // disabled
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -purgeDays).Format(time.RFC3339)
	// Tagged for /api/perf writer-lock visibility (#1340).
	result, err := s.instrumentedExec("purge_observers", `
		DELETE FROM observers
		WHERE inactive = 1
		  AND last_seen < $1
		  AND NOT EXISTS (SELECT 1 FROM observations o WHERE o.observer_idx = observers.rowid)
		  AND NOT EXISTS (SELECT 1 FROM observer_metrics m WHERE m.observer_id = observers.id)
		  AND NOT EXISTS (SELECT 1 FROM dropped_packets d WHERE d.observer_id = observers.id)`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("purge stale observers: %w", err)
	}
	purged, _ := result.RowsAffected()
	if purged > 0 {
		log.Printf("Purged %d inactive observer(s) with no remaining data (not seen in %d days)", purged, purgeDays)
	}
	return purged, nil
}

// DroppedPacket holds data for a packet rejected during ingest.
type DroppedPacket struct {
	Hash         string
	RawHex       string
	Reason       string
	ObserverID   string
	ObserverName string
	NodePubKey   string
	NodeName     string
}

// InsertDroppedPacket records a rejected packet in the dropped_packets table.
func (s *Store) InsertDroppedPacket(dp *DroppedPacket) error {
	_, err := s.db.Exec(
		`INSERT INTO dropped_packets (hash, raw_hex, reason, observer_id, observer_name, node_pubkey, node_name) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		dp.Hash, dp.RawHex, dp.Reason, dp.ObserverID, dp.ObserverName, dp.NodePubKey, dp.NodeName,
	)
	if err != nil {
		s.Stats.WriteErrors.Add(1)
		return fmt.Errorf("insert dropped packet: %w", err)
	}
	s.Stats.SignatureDrops.Add(1)
	return nil
}

// PruneDroppedPackets removes dropped_packets older than retentionDays.
func (s *Store) PruneDroppedPackets(retentionDays int) (int64, error) {
	if retentionDays <= 0 {
		return 0, nil
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -retentionDays).Format(time.RFC3339)
	result, err := s.db.Exec(`DELETE FROM dropped_packets WHERE dropped_at < $1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("prune dropped packets: %w", err)
	}
	n, _ := result.RowsAffected()
	if n > 0 {
		log.Printf("Pruned %d dropped packet(s) older than %d days", n, retentionDays)
	}
	return n, nil
}

// PacketData holds the data needed to insert a packet into the DB.
type PacketData struct {
	RawHex            string
	Timestamp         string
	ObserverID        string
	ObserverName      string
	SNR               *float64
	RSSI              *float64
	Score             *float64
	Direction         *string
	Hash              string
	RouteType         int
	PayloadType       int
	PayloadVersion    int
	PathJSON          string
	DecodedJSON       string
	ChannelHash       string // grouping key for channel queries (#762)
	ScopeName         string // matched region name, or "" for unknown-scoped
	IsTransportScoped bool   // true when route_type IN (0,3) AND Code1 ≠ "0000"
	Code1             string // transport code 1 (scope), "" when the route carries none
	Code2             string // transport code 2 (return-region hint), "" when absent
	Region            string // observer region: payload > topic > source config (#788)
	Foreign           bool   // true when ADVERT GPS lies outside configured geofilter (#730)
	FromPubkey        string // pubkey of the originating node, for exact-match attribution (#1143)
}

// nilIfEmpty returns nil for empty strings (for nullable DB columns).
func nilIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// boolToInt converts a bool to the retained 0/1 integer representation.
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// scopeNameForDB encodes PacketData scope semantics for DB storage:
// non-transport-scoped → nil (SQL NULL); transport-scoped → pointer to ScopeName
// (may be "" for unknown region, "#name" for matched region).
func scopeNameForDB(data *PacketData) *string {
	if !data.IsTransportScoped {
		return nil
	}
	s := data.ScopeName
	return &s
}

// UpdateNodeDefaultScope records the most-recently observed region scope for a
// node. Skips the UPDATE when the stored value already matches to avoid
// redundant writes on the hot MQTT ingest path. Updates both nodes and
// inactive_nodes to stay consistent.
//
// Defense-in-depth (#1534): an empty scope is treated as a no-op. The call
// site at handleMessage is the primary guard (shouldUpdateDefaultScope),
// but this layer refuses the invalid write so a future caller cannot
// reintroduce the bug by passing "" directly.
func (s *Store) UpdateNodeDefaultScope(pubkey, scope string) error {
	if scope == "" {
		return nil
	}
	// Short-circuit: skip if already stored.
	var cur sql.NullString
	row := s.db.QueryRow(`SELECT default_scope FROM nodes WHERE public_key = $1`, pubkey)
	if row.Scan(&cur) == nil && cur.Valid && cur.String == scope {
		return nil
	}
	if _, err := s.db.Exec(`UPDATE nodes SET default_scope = $1 WHERE public_key = $2`, scope, pubkey); err != nil {
		return err
	}
	// Mirror to inactive_nodes (node may be there if recently moved by retention).
	_, err := s.db.Exec(`UPDATE inactive_nodes SET default_scope = $1 WHERE public_key = $2`, scope, pubkey)
	return err
}

// normalizeReportTS parses an observer report timestamp and returns it in
// canonical UTC RFC3339 form ("2006-01-02T15:04:05Z"). It accepts both the
// firmware's fractional/offset form (e.g. "2026-07-26T09:43:48.000000+00:00")
// and plain "Z"/offset variants. An empty or unparseable input returns "" so
// the caller writes an empty configured_scope_at and skips the ordering guard;
// this keeps every stored timestamp either canonical or empty, never a mix of
// offset/precision formats that would break lexicographic last-write-wins.
func normalizeReportTS(raw string) string {
	if raw == "" {
		return ""
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UTC().Format(time.RFC3339)
		}
	}
	return ""
}

// normalizeScopeList canonicalises the comma-separated region-scope string an
// observer reports for a node, so configured_scope is directly comparable with
// default_scope instead of only looking similar (#1865, raised by @cwichura).
//
// The OTA scope query returns bare region names ("dk,eu,dk-aarhus"), while
// everything else in CoreScope carries the leading "#": every stored
// default_scope value on a live 1425-node instance starts with one (361 of
// 361), and the hashRegions/hashChannels config paths already prefix a missing
// "#" before deriving the channel key from it (main.go). The "#" is therefore
// part of the region's name here, not decoration, so a bare "dk" and a stored
// "#dk" are the same region and must be written the same way.
//
// Rules, in order: entries are trimmed; empty entries are dropped; "*" is a
// wildcard rather than a region name and is passed through untouched; anything
// else gains a leading "#" if it lacks one. Order is preserved and duplicates
// are NOT collapsed, because the observer's ordering carries meaning we do not
// interpret here. Case is left alone: region keys are derived as
// sha256("#name"), so folding case would silently retarget a scope.
//
// An empty or all-empty input returns "", which UpdateNodeConfiguredScope
// stores as a valid "responded, no scopes configured" statement.
func normalizeScopeList(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if p != "*" && !strings.HasPrefix(p, "#") {
			p = "#" + p
		}
		out = append(out, p)
	}
	return strings.Join(out, ",")
}

// UpdateNodeConfiguredScope records the region scopes a node has CONFIGURED,
// as concrete evidence from an observer /neighbors report (#1865). Unlike
// UpdateNodeDefaultScope (inferred, overwritten on every observation), this is
// only called for the observer's own `self` scopes and for neighbors whose OTA
// scope query returned status="responded" — so the caller, not this method,
// gates on status. An empty scope IS a valid "responded, no scopes configured"
// statement and is stored; a timeout must simply not reach this method.
//
// reportedAt is the report envelope timestamp (ISO-8601). It is stored in
// configured_scope_at and used for last-write-wins: an out-of-order older
// report must not clobber a newer confirmed value. A blank reportedAt skips
// the ordering guard (always writes).
func (s *Store) UpdateNodeConfiguredScope(pubkey, scope, reportedAt string) error {
	if pubkey == "" {
		return nil
	}
	// Normalize to canonical UTC RFC3339 so the last-write-wins comparison is
	// chronological, not lexicographic (see normalizeReportTS). Stored values
	// are therefore always canonical or empty.
	reportedAt = normalizeReportTS(reportedAt)
	// The report's timestamp is chosen by the publisher. Last-write-wins
	// below means a report stamped far in the future would be written once
	// and then block every genuine later report, so drop those.
	if reportTooFarInFuture(reportedAt) {
		return nil
	}
	// Canonicalise the scope syntax for the same reason: a value that is stored
	// differently from default_scope cannot be compared against it (see
	// normalizeScopeList).
	scope = normalizeScopeList(scope)
	if len(scope) > maxConfiguredScopeLen {
		return nil
	}
	// Last-write-wins: skip if the stored confirmation is newer-or-equal.
	if reportedAt != "" {
		var curAt sql.NullString
		row := s.db.QueryRow(`SELECT configured_scope_at FROM nodes WHERE public_key = $1`, pubkey)
		if row.Scan(&curAt) == nil && curAt.Valid && curAt.String != "" && curAt.String >= reportedAt {
			return nil
		}
	}
	if _, err := s.db.Exec(
		`UPDATE nodes SET configured_scope = $1, configured_scope_at = $2 WHERE public_key = $3`,
		scope, reportedAt, pubkey); err != nil {
		return err
	}
	// Mirror to inactive_nodes (node may be there if recently moved by retention).
	_, err := s.db.Exec(
		`UPDATE inactive_nodes SET configured_scope = $1, configured_scope_at = $2 WHERE public_key = $3`,
		scope, reportedAt, pubkey)
	return err
}

// RecordNaiveSkew is called when resolveRxTime() clamps a packet's envelope
// timestamp because the observer is emitting a zone-less local-time string
// off from UTC by more than 15 min (issue #1478). Stamps the observer's
// clock_skew_seconds / clock_skew_count_24h / clock_last_naive_at so the
// server can surface a ⚠️ chip + banner in the UI.
//
// The count is reset to 1 (not incremented) if no event has been recorded in
// the past 24h, otherwise incremented. deltaSec is signed: negative = observer
// clock is behind UTC, positive = ahead.
func (s *Store) RecordNaiveSkew(observerID string, deltaSec int64, now time.Time) error {
	if observerID == "" {
		return nil
	}
	nowStr := now.UTC().Format(time.RFC3339)
	cutoff := now.Add(-24 * time.Hour).UTC().Format(time.RFC3339)
	// One INSERT-or-UPDATE round trip. ON CONFLICT path resets the rolling
	// counter when the previous event is older than the 24h window, otherwise
	// increments it.
	_, err := s.db.Exec(`
		INSERT INTO observers (id, clock_skew_seconds, clock_skew_count_24h, clock_last_naive_at)
		VALUES ($1, $2, 1, $3)
		ON CONFLICT(id) DO UPDATE SET
			clock_skew_seconds = excluded.clock_skew_seconds,
			clock_last_naive_at = excluded.clock_last_naive_at,
			clock_skew_count_24h = CASE
				WHEN observers.clock_last_naive_at IS NULL OR observers.clock_last_naive_at < $4
					THEN 1
				ELSE COALESCE(observers.clock_skew_count_24h, 0) + 1
			END
	`, observerID, deltaSec, nowStr, cutoff)
	return err
}

// MQTTPacketMessage is the JSON payload from an MQTT raw packet message.
type MQTTPacketMessage struct {
	Raw       string   `json:"raw"`
	SNR       *float64 `json:"SNR"`
	RSSI      *float64 `json:"RSSI"`
	Score     *float64 `json:"score"`
	Direction *string  `json:"direction"`
	Origin    string   `json:"origin"`
	Region    string   `json:"region,omitempty"`    // optional region override (#788)
	Timestamp string   `json:"timestamp,omitempty"` // observer receive time, resolved by handler
}

// BuildPacketData constructs a PacketData from a decoded packet and MQTT message.
// path_json is derived directly from raw_hex header bytes (not decoded.Path.Hops)
// to guarantee the stored path always matches the raw bytes. This matters for
// TRACE packets where decoded.Path.Hops is overwritten with payload hops (#886).
//
// Timestamp is server ingest time (time.Now()), NOT msg.Timestamp (#1370):
// PR #1233 (commit 498fbc03) routed the envelope timestamp into
// PacketData.Timestamp on the premise that uploader-stamped envelope time
// was trustworthy. Issue #1370 disproved that premise — observers with
// broken client clocks (staging Voodoo3 tx 304114: 4/5 obs stamped 18:42
// while genuine receive was 01:42) poisoned transmissions.first_seen /
// observations.timestamp and dragged the /api/channels lastActivity 7h
// into the past. Packet ordering is owned by the server clock; client
// clocks are untrusted. msg.Timestamp still flows into observer.last_seen
// via UpsertObserverAt — that's #1233's MAX/MIN guarded path and is fine.
func BuildPacketData(msg *MQTTPacketMessage, decoded *DecodedPacket, observerID, region string, regionSet *regionKeySet) *PacketData {
	pathJSON := "[]"
	// For TRACE packets, path_json must be the payload-decoded route hops
	// (decoded.Path.Hops), NOT the raw_hex header bytes which are SNR values.
	// For all other packet types, derive path from raw_hex (#886).
	if !packetpath.PathBytesAreHops(byte(decoded.Header.PayloadType)) {
		if len(decoded.Path.Hops) > 0 {
			b, _ := json.Marshal(decoded.Path.Hops)
			pathJSON = string(b)
		}
	} else if hops, err := packetpath.DecodePathFromRawHex(msg.Raw); err == nil && len(hops) > 0 {
		b, _ := json.Marshal(hops)
		pathJSON = string(b)
	}

	pd := &PacketData{
		RawHex:         msg.Raw,
		Timestamp:      time.Now().UTC().Format(time.RFC3339), // #1370 (counters #1233)
		ObserverID:     observerID,
		ObserverName:   msg.Origin,
		SNR:            msg.SNR,
		RSSI:           msg.RSSI,
		Score:          msg.Score,
		Direction:      msg.Direction,
		Hash:           ComputeContentHash(msg.Raw),
		RouteType:      decoded.Header.RouteType,
		PayloadType:    decoded.Header.PayloadType,
		PayloadVersion: decoded.Header.PayloadVersion,
		PathJSON:       pathJSON,
		DecodedJSON:    PayloadJSON(&decoded.Payload),
	}

	// Region priority: payload field > topic-derived parameter (#788)
	if msg.Region != "" {
		pd.Region = msg.Region
	} else {
		pd.Region = region
	}

	// Populate channel_hash for fast channel queries (#762)
	if decoded.Header.PayloadType == PayloadGRP_TXT {
		if decoded.Payload.Type == "CHAN" && decoded.Payload.Channel != "" {
			pd.ChannelHash = decoded.Payload.Channel
		} else if decoded.Payload.Type == "GRP_TXT" && decoded.Payload.ChannelHashHex != "" {
			pd.ChannelHash = "enc_" + decoded.Payload.ChannelHashHex
		}
	}

	if decoded.TransportCodes != nil {
		pd.Code1 = decoded.TransportCodes.Code1
		pd.Code2 = decoded.TransportCodes.Code2
		if decoded.TransportCodes.Code1 != "0000" {
			pd.IsTransportScoped = true
			pd.ScopeName = regionSet.matchScopeName(byte(decoded.Header.PayloadType), decoded.payloadRaw, decoded.TransportCodes.Code1)
		}
	}

	// Populate from_pubkey at write time (#1143). ADVERTs carry the
	// originating node's pubkey directly; other packet types stay NULL
	// (downstream attribution queries handle NULL gracefully).
	if decoded.Header.PayloadType == PayloadADVERT && decoded.Payload.PubKey != "" {
		pd.FromPubkey = decoded.Payload.PubKey
	}

	return pd
}

// ─── Writer-lock instrumentation (issue #1340) ────────────────────────────
//
// Make serialized writer-lock starvation visible to operators. Per-component
// wait_ms / hold_ms / contention_total histograms, surfaced via
// /api/perf/write-sources under the "writer_perf" key. Component tags:
// neighbor_builder, mqtt_handler, prune_packets, prune_observers,
// prune_metrics, mbcap_persist (deferred — see PR body), vacuum.
//
// The single writer connection (SetMaxOpenConns(1)) means writes serialise
// inside the driver and the wait is invisible to Go. writerMu measures the
// wait Go can see (everyone queueing behind the current holder) by gating
// every wrapped call site through the same package-level mutex.

// WriterStatsSnapshot is a per-component wait/hold latency snapshot
// surfaced via /api/perf to make serialized writer-lock starvation visible
// to operators (issue #1340). Times are in milliseconds.
type WriterStatsSnapshot struct {
	Count           int64   `json:"count"`
	ContentionTotal int64   `json:"contention_total"`
	WaitMsP50       float64 `json:"wait_ms_p50"`
	WaitMsP95       float64 `json:"wait_ms_p95"`
	WaitMsP99       float64 `json:"wait_ms_p99"`
	WaitMsMax       float64 `json:"wait_ms_max"`
	HoldMsP50       float64 `json:"hold_ms_p50"`
	HoldMsP95       float64 `json:"hold_ms_p95"`
	HoldMsP99       float64 `json:"hold_ms_p99"`
	HoldMsMax       float64 `json:"hold_ms_max"`
}

const (
	// writerSampleWindow bounds the per-component rolling window so a
	// long-running ingestor doesn't grow this unbounded.
	writerSampleWindow = 1024
	// contentionThresholdMs: wait_ms above this counts as a "contended"
	// write (per #1340 spec).
	contentionThresholdMs = 100.0
	defaultSlowWriterMs   = 500.0
)

// slowWriterThresholdMsAtomic — hold_ms threshold above which writes
// emit a [db-slow-writer] log line. Read on the hot path; written once
// at startup by SetSlowWriterThresholdMs.
var slowWriterThresholdMsAtomic atomic.Uint64

// SetSlowWriterThresholdMs sets the [db-slow-writer] log threshold.
// ms<=0 restores the 500ms default. Operators can also set
// CORESCOPE_DB_SLOW_WRITER_MS at process start — see initSlowWriterFromEnv.
func SetSlowWriterThresholdMs(ms float64) {
	if ms <= 0 {
		ms = defaultSlowWriterMs
	}
	slowWriterThresholdMsAtomic.Store(uint64(ms))
}

func getSlowWriterThresholdMs() float64 {
	v := slowWriterThresholdMsAtomic.Load()
	if v == 0 {
		return defaultSlowWriterMs
	}
	return float64(v)
}

// initSlowWriterFromEnv is called once from package init so operators can
// override the threshold via CORESCOPE_DB_SLOW_WRITER_MS without a
// Go-side Config change.
func initSlowWriterFromEnv() {
	v := os.Getenv("CORESCOPE_DB_SLOW_WRITER_MS")
	if v == "" {
		return
	}
	var ms float64
	if _, err := fmt.Sscanf(v, "%f", &ms); err == nil && ms > 0 {
		SetSlowWriterThresholdMs(ms)
	}
}

func init() { initSlowWriterFromEnv() }

type writerComponentStats struct {
	mu              sync.Mutex
	count           int64
	contentionTotal int64
	waitMs          []float64
	holdMs          []float64
	waitMax         float64
	holdMax         float64
}

func (c *writerComponentStats) record(waitMs, holdMs float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.count++
	if waitMs > contentionThresholdMs {
		c.contentionTotal++
	}
	if waitMs > c.waitMax {
		c.waitMax = waitMs
	}
	if holdMs > c.holdMax {
		c.holdMax = holdMs
	}
	c.waitMs = appendBoundedFloat(c.waitMs, waitMs, writerSampleWindow)
	c.holdMs = appendBoundedFloat(c.holdMs, holdMs, writerSampleWindow)
}

func appendBoundedFloat(s []float64, v float64, max int) []float64 {
	if len(s) < max {
		return append(s, v)
	}
	copy(s, s[1:])
	s[len(s)-1] = v
	return s
}

func (c *writerComponentStats) snapshot() WriterStatsSnapshot {
	c.mu.Lock()
	wait := append([]float64(nil), c.waitMs...)
	hold := append([]float64(nil), c.holdMs...)
	snap := WriterStatsSnapshot{
		Count:           c.count,
		ContentionTotal: c.contentionTotal,
		WaitMsMax:       c.waitMax,
		HoldMsMax:       c.holdMax,
	}
	c.mu.Unlock()
	sort.Float64s(wait)
	sort.Float64s(hold)
	snap.WaitMsP50 = nearestRankPercentile(wait, 0.50)
	snap.WaitMsP95 = nearestRankPercentile(wait, 0.95)
	snap.WaitMsP99 = nearestRankPercentile(wait, 0.99)
	snap.HoldMsP50 = nearestRankPercentile(hold, 0.50)
	snap.HoldMsP95 = nearestRankPercentile(hold, 0.95)
	snap.HoldMsP99 = nearestRankPercentile(hold, 0.99)
	return snap
}

func nearestRankPercentile(sorted []float64, p float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n == 1 {
		return sorted[0]
	}
	idx := int(p*float64(n-1) + 0.5)
	if idx < 0 {
		idx = 0
	}
	if idx >= n {
		idx = n - 1
	}
	return sorted[idx]
}

type writerStatsAggregator struct {
	mu         sync.Mutex
	components map[string]*writerComponentStats
}

var writerStatsAgg = &writerStatsAggregator{
	components: make(map[string]*writerComponentStats),
}

func (a *writerStatsAggregator) get(component string) *writerComponentStats {
	a.mu.Lock()
	defer a.mu.Unlock()
	c, ok := a.components[component]
	if !ok {
		c = &writerComponentStats{}
		a.components[component] = c
	}
	return c
}

// reset clears all per-component samples. Test-only: lets a single
// scenario assert against a clean aggregator without prior-test noise
// in the same package run (TestWriterStarvationVisibleInPerf would
// otherwise mix this run's 5 starved samples with thousands of fast
// InsertTransmission samples from earlier tests and the p99 would
// collapse below the 50s threshold).
func (a *writerStatsAggregator) reset() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.components = make(map[string]*writerComponentStats)
}

// ResetWriterStatsForTest wipes the per-component writer stats
// aggregator. Test-only; not safe to call from production code paths.
func ResetWriterStatsForTest() { writerStatsAgg.reset() }

func (a *writerStatsAggregator) snapshot() map[string]WriterStatsSnapshot {
	a.mu.Lock()
	keys := make([]string, 0, len(a.components))
	stats := make([]*writerComponentStats, 0, len(a.components))
	for k, v := range a.components {
		keys = append(keys, k)
		stats = append(stats, v)
	}
	a.mu.Unlock()
	out := make(map[string]WriterStatsSnapshot, len(keys))
	for i, k := range keys {
		out[k] = stats[i].snapshot()
	}
	return out
}

// WriterStatsSnapshot returns a per-component wait/hold/contention
// snapshot for exposure on /api/perf/write-sources (issue #1340).
func (s *Store) WriterStatsSnapshot() map[string]WriterStatsSnapshot {
	return writerStatsAgg.snapshot()
}

// recordWriterTiming aggregates a single sample under component and
// emits [db-slow-writer] if hold_ms > configured threshold (default
// 500ms). queryForLog is truncated to 200 chars.
func recordWriterTiming(component string, wait, hold time.Duration, queryForLog string) {
	waitMs := float64(wait.Nanoseconds()) / 1e6
	holdMs := float64(hold.Nanoseconds()) / 1e6
	writerStatsAgg.get(component).record(waitMs, holdMs)
	if holdMs > getSlowWriterThresholdMs() {
		q := queryForLog
		if len(q) > 200 {
			q = q[:200]
		}
		log.Printf("[db-slow-writer] component=%s duration=%.1fms query=%s", component, holdMs, q)
	}
}

// writerMu serialises every wrapped writer call so the wait the next
// caller sees is the wait the perf snapshot can attribute. The
// connection pool also serializes writes (SetMaxOpenConns(1)),
// but the wait inside the driver is invisible to Go — writerMu makes
// it Go-visible.
var writerMu sync.Mutex

// WriterExec wraps s.db.Exec with per-component wait/hold/contention
// instrumentation (issue #1340).
func (s *Store) WriterExec(component, query string, args ...interface{}) (sql.Result, error) {
	waitStart := time.Now()
	writerMu.Lock()
	wait := time.Since(waitStart)
	holdStart := time.Now()
	res, err := s.db.Exec(query, args...)
	hold := time.Since(holdStart)
	writerMu.Unlock()
	recordWriterTiming(component, wait, hold, query)
	return res, err
}

// WriterTx wraps Begin → fn → Commit under component tagging.
// hold_ms covers the whole tx so a slow body counts against its owner.
func (s *Store) WriterTx(component string, fn func(*sql.Tx) error) error {
	waitStart := time.Now()
	writerMu.Lock()
	wait := time.Since(waitStart)
	holdStart := time.Now()
	tx, err := beginWrite(s.db)
	if err != nil {
		hold := time.Since(holdStart)
		writerMu.Unlock()
		recordWriterTiming(component, wait, hold, "BEGIN")
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		hold := time.Since(holdStart)
		writerMu.Unlock()
		recordWriterTiming(component, wait, hold, "tx-body")
		return err
	}
	err = tx.Commit()
	hold := time.Since(holdStart)
	writerMu.Unlock()
	recordWriterTiming(component, wait, hold, "COMMIT")
	return err
}

// Wrap helpers below tag existing call sites with the canonical
// component names so the call sites read naturally. These keep the
// instrumentation out of the hot-path business logic.

// instrumentedExec is the package-internal pass-through used by call
// sites already inside db.go (PruneOldMetrics, RemoveStaleObservers,
// vacuum). Equivalent to WriterExec, kept short for readability.
func (s *Store) instrumentedExec(component, query string, args ...interface{}) (sql.Result, error) {
	return s.WriterExec(component, query, args...)
}

// epochSecondsForLastSeen parses an RFC3339 timestamp to a unix-second
// value for the transmissions.last_seen denormalized column (#1690).
// Falls back to the current time on parse failure so the column is
// never seeded with 0 for a brand-new row.
func epochSecondsForLastSeen(rfc3339 string) int64 {
	if t, err := time.Parse(time.RFC3339, rfc3339); err == nil {
		return t.Unix()
	}
	return time.Now().UTC().Unix()
}

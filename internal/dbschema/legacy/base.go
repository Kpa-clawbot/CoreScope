package legacy

import (
	"database/sql"
	"fmt"
	"log"
	"strings"
)

func ApplyBase(db *sql.DB) error {
	// auto_vacuum=INCREMENTAL is set via DSN pragma (must be before journal_mode).
	// Logging of current mode is handled by CheckAutoVacuum — no duplicate log here.

	schema := `
		CREATE TABLE IF NOT EXISTS nodes (
			public_key TEXT PRIMARY KEY,
			name TEXT,
			role TEXT,
			lat REAL,
			lon REAL,
			last_seen TEXT,
			first_seen TEXT,
			advert_count INTEGER DEFAULT 0,
			battery_mv INTEGER,
			temperature_c REAL,
			foreign_advert INTEGER DEFAULT 0
		);

		CREATE TABLE IF NOT EXISTS scope_match_totals (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			since_unix INTEGER NOT NULL,
			unique_matches INTEGER NOT NULL,
			explicit_over_derived INTEGER NOT NULL,
			ambiguous INTEGER NOT NULL,
			none_matches INTEGER NOT NULL,
			updated_unix INTEGER NOT NULL
		);

		CREATE TABLE IF NOT EXISTS observers (
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
			noise_floor REAL,
			inactive INTEGER DEFAULT 0,
			last_packet_at TEXT DEFAULT NULL,
			clock_skew_seconds INTEGER DEFAULT NULL,
			clock_skew_count_24h INTEGER DEFAULT 0,
			clock_last_naive_at TEXT DEFAULT NULL,
			can_relay INTEGER DEFAULT 1,
			can_relay_seen INTEGER DEFAULT 0
		);

		CREATE INDEX IF NOT EXISTS idx_nodes_last_seen ON nodes(last_seen);
		CREATE INDEX IF NOT EXISTS idx_observers_last_seen ON observers(last_seen);

		CREATE TABLE IF NOT EXISTS inactive_nodes (
			public_key TEXT PRIMARY KEY,
			name TEXT,
			role TEXT,
			lat REAL,
			lon REAL,
			last_seen TEXT,
			first_seen TEXT,
			advert_count INTEGER DEFAULT 0,
			battery_mv INTEGER,
			temperature_c REAL,
			foreign_advert INTEGER DEFAULT 0
		);

		CREATE INDEX IF NOT EXISTS idx_inactive_nodes_last_seen ON inactive_nodes(last_seen);

		CREATE TABLE IF NOT EXISTS transmissions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			raw_hex TEXT NOT NULL,
			hash TEXT NOT NULL UNIQUE,
			first_seen TEXT NOT NULL,
			route_type INTEGER,
			payload_type INTEGER,
			payload_version INTEGER,
			decoded_json TEXT,
			from_pubkey TEXT,
			last_seen INTEGER NOT NULL DEFAULT 0,
			created_at TEXT DEFAULT (datetime('now')),
			code1 TEXT,
			code2 TEXT
		);

		CREATE INDEX IF NOT EXISTS idx_transmissions_hash ON transmissions(hash);
		CREATE INDEX IF NOT EXISTS idx_transmissions_first_seen ON transmissions(first_seen);
		CREATE INDEX IF NOT EXISTS idx_transmissions_payload_type ON transmissions(payload_type);
		-- idx_tx_code1 is created by the transport_codes_v1 migration below,
		-- after the ALTER runs — same reasoning as idx_transmissions_from_pubkey
		-- and idx_tx_last_seen_zero above: a legacy DB (table-exists,
		-- column-missing) would trip on this CREATE INDEX referencing code1
		-- before the column exists, since CREATE TABLE IF NOT EXISTS is a
		-- no-op against a pre-existing table.
		-- idx_transmissions_from_pubkey is created by the from_pubkey_v1
		-- migration after the column is added on legacy DBs (#1143).
		-- idx_tx_last_seen_zero (partial, WHERE last_seen=0) is created by
		-- dbschema.Apply after ensuring the last_seen column exists (#1690,
		-- partial-index swap #1740) — keep it OUT of this base schema block
		-- so legacy DBs (table-exists, column-missing) don't trip on the
		-- CREATE INDEX before the ALTER runs.

		-- Mobile client RX coverage: a roaming companion = a mobile observer
		-- with a moving GPS position, so it gets its own table rather than
		-- observations (which assumes a fixed observer/location).
		CREATE TABLE IF NOT EXISTS client_receptions (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			rx_pubkey     TEXT NOT NULL,
			heard_key     TEXT NOT NULL,
			heard_keylen  INTEGER NOT NULL,
			rssi          INTEGER,
			snr           REAL,
			lat           REAL NOT NULL,
			lon           REAL NOT NULL,
			pos_acc_m     REAL,
			rx_at         TEXT NOT NULL,
			ingested_at   TEXT NOT NULL,
			src           TEXT NOT NULL,
			UNIQUE(rx_pubkey, heard_key, rx_at)
		);
		-- Coverage queries filter by bbox AND match the heard node either by full
		-- key (heard_keylen=32 AND heard_key=?) or by 2-3 byte prefix. The composite
		-- (heard_key, heard_keylen, lat, lon) serves the heard_key-equality seek and
		-- carries lat/lon so the bbox range is satisfied from the index; it also
		-- supersedes the old single-column heard_key index. idx_client_recept_latlon
		-- lets the planner instead drive from a selective bbox. (#5, #18)
		CREATE INDEX IF NOT EXISTS idx_client_recept_heard_geo ON client_receptions(heard_key, heard_keylen, lat, lon);
		CREATE INDEX IF NOT EXISTS idx_client_recept_latlon ON client_receptions(lat, lon);
		-- rx_at backs both the retention reaper (DELETE WHERE rx_at < ?) and the
		-- leaderboard, which range-scans WHERE rx_at >= ? and aggregates per
		-- rx_pubkey in Go (see rxLeaderboard's frontier-weighted scoring). Without
		-- this index either would full-scan the table under the writer lock
		-- (verified by an EXPLAIN test). A dedicated rx_pubkey index stays
		-- redundant — the leaderboard no longer groups by rx_pubkey in SQL.
		CREATE INDEX IF NOT EXISTS idx_client_recept_rxat ON client_receptions(rx_at);
		DROP INDEX IF EXISTS idx_client_recept_rxpk;

		-- Self-reported name of each mobile client (companion), from the SELF_INFO
		-- name the app sends as "origin". Lets the leaderboard show a name even
		-- when the companion never advertised (so it isn't in the nodes table).
		CREATE TABLE IF NOT EXISTS client_observers (
			pubkey    TEXT PRIMARY KEY,
			name      TEXT,
			last_seen TEXT
		);

		-- Diagnostic RF observations from mobile clients. Unlike client_receptions
		-- this holds EVERY decodable packet, attributable or not, so it must never
		-- be used for coverage. pkt_hash is ComputeContentHash() — identical to
		-- transmissions.hash — so dark-traffic queries are a plain equality join.
		CREATE TABLE IF NOT EXISTS client_rx_observations (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			rx_pubkey     TEXT NOT NULL,
			rx_at         TEXT NOT NULL,
			ingested_at   TEXT NOT NULL,
			pkt_hash      TEXT NOT NULL,
			route_type    INTEGER NOT NULL,
			payload_type  INTEGER NOT NULL,
			code1         TEXT,
			code2         TEXT,
			scope_name    TEXT,
			hash_size     INTEGER NOT NULL,
			hop_count     INTEGER NOT NULL,
			path_json     TEXT,
			forwarder     TEXT,
			snr           REAL,
			rssi          INTEGER,
			lat           REAL NOT NULL,
			lon           REAL NOT NULL,
			pos_acc_m     REAL,
			UNIQUE(rx_pubkey, pkt_hash, rx_at)
		);
		CREATE INDEX IF NOT EXISTS idx_cro_prune     ON client_rx_observations(rx_at);
		CREATE INDEX IF NOT EXISTS idx_cro_hash      ON client_rx_observations(pkt_hash, rx_at);
		CREATE INDEX IF NOT EXISTS idx_cro_forwarder ON client_rx_observations(forwarder, rx_at);
		CREATE INDEX IF NOT EXISTS idx_cro_scope     ON client_rx_observations(scope_name, rx_at);

		-- RF environment samples from mobile clients: radio counters paired with
		-- a GPS point. Absolutes only — deltas are computed at query time, and a
		-- decrease in uptime_secs (reboot) or any counter (wrap) breaks the chain.
		CREATE TABLE IF NOT EXISTS client_rf_samples (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			rx_pubkey    TEXT NOT NULL,
			sampled_at   TEXT NOT NULL,
			ingested_at  TEXT NOT NULL,
			lat          REAL NOT NULL,
			lon          REAL NOT NULL,
			pos_acc_m    REAL,
			stationary   INTEGER NOT NULL DEFAULT 0,
			uptime_secs  INTEGER NOT NULL,
			battery_mv   INTEGER,
			queue_len    INTEGER,
			errors       INTEGER,
			noise_floor  INTEGER,
			last_rssi    INTEGER,
			last_snr     REAL,
			tx_air_secs  INTEGER,
			rx_air_secs  INTEGER,
			recv         INTEGER,
			sent         INTEGER,
			flood_rx     INTEGER,
			direct_rx    INTEGER,
			flood_tx     INTEGER,
			direct_tx    INTEGER,
			recv_errors  INTEGER,
			UNIQUE(rx_pubkey, sampled_at)
		);
		CREATE INDEX IF NOT EXISTS idx_crf_prune ON client_rf_samples(sampled_at);
		CREATE INDEX IF NOT EXISTS idx_crf_track ON client_rf_samples(rx_pubkey, sampled_at);

		-- Declared region lists reported by repeaters via ANON_REQ_TYPE_REGIONS.
		-- Observations, never state: "current" is the greatest observed_at for a
		-- target, NOT the greatest ingested_at — a drive buffered offline can
		-- arrive days late and must not overwrite a fresher reading.
		CREATE TABLE IF NOT EXISTS node_declared_regions (
			id             INTEGER PRIMARY KEY AUTOINCREMENT,
			target         TEXT NOT NULL,
			rx_pubkey      TEXT NOT NULL,
			observed_at    TEXT NOT NULL,
			ingested_at    TEXT NOT NULL,
			regions_csv    TEXT NOT NULL,
			truncated      INTEGER NOT NULL DEFAULT 0,
			lat            REAL,
			lon            REAL,
			pos_acc_m      REAL,
			repeater_clock INTEGER,
			UNIQUE(target, rx_pubkey, observed_at)
		);
		CREATE INDEX IF NOT EXISTS idx_ndr_target ON node_declared_regions(target, observed_at);
		CREATE INDEX IF NOT EXISTS idx_ndr_prune  ON node_declared_regions(observed_at);
	`
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("base schema: %w", err)
	}

	// Create observations table (v3 schema)
	obsExists := false
	row := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='observations'")
	var dummy string
	if row.Scan(&dummy) == nil {
		obsExists = true
	}

	if !obsExists {
		obs := `
			CREATE TABLE observations (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				transmission_id INTEGER NOT NULL REFERENCES transmissions(id),
				observer_idx INTEGER,
				direction TEXT,
				snr REAL,
				rssi REAL,
				score INTEGER,
				path_json TEXT,
				timestamp INTEGER NOT NULL
			);
			CREATE INDEX idx_observations_transmission_id ON observations(transmission_id);
			CREATE INDEX idx_observations_observer_idx ON observations(observer_idx);
			CREATE INDEX idx_observations_timestamp ON observations(timestamp);
			CREATE UNIQUE INDEX IF NOT EXISTS idx_observations_dedup ON observations(transmission_id, observer_idx, COALESCE(path_json, ''));
		`
		if _, err := db.Exec(obs); err != nil {
			return fmt.Errorf("observations schema: %w", err)
		}
	}

	// Create/rebuild packets_v view (v3 schema: observer_idx → observers.rowid)
	// The Go server reads this view; without it fresh installs get "no such table: packets_v".
	db.Exec(`DROP VIEW IF EXISTS packets_v`)
	_, vErr := db.Exec(`
		CREATE VIEW packets_v AS
			SELECT o.id, COALESCE(o.raw_hex, t.raw_hex) AS raw_hex,
				   datetime(o.timestamp, 'unixepoch') AS timestamp,
				   obs.id AS observer_id, obs.name AS observer_name,
				   o.direction, o.snr, o.rssi, o.score, t.hash, t.route_type,
				   t.payload_type, t.payload_version, o.path_json, t.decoded_json,
				   t.created_at
			FROM observations o
			JOIN transmissions t ON t.id = o.transmission_id
			LEFT JOIN observers obs ON obs.rowid = o.observer_idx AND (obs.inactive IS NULL OR obs.inactive = 0)
	`)
	if vErr != nil {
		return fmt.Errorf("packets_v view: %w", vErr)
	}

	// One-time migration: recalculate advert_count to count unique transmissions only
	db.Exec(`CREATE TABLE IF NOT EXISTS _migrations (name TEXT PRIMARY KEY)`)
	var migDone int
	row = db.QueryRow("SELECT 1 FROM _migrations WHERE name = 'advert_count_unique_v1'")
	if row.Scan(&migDone) != nil {
		log.Println("[migration] Recalculating advert_count (unique transmissions only)...")
		// Note: this migration is gated on a one-shot _migrations row, so it
		// runs at most once per DB. The historical version used a LIKE-on-JSON
		// substring match (#1143). Switching to from_pubkey here is safe even
		// though the column may not yet be backfilled on legacy DBs: the
		// migration is already marked done on those DBs and won't re-run.
		db.Exec(`
			UPDATE nodes SET advert_count = (
				SELECT COUNT(*) FROM transmissions t
				WHERE t.payload_type = 4
				  AND t.from_pubkey = nodes.public_key
			)
		`)
		db.Exec(`INSERT INTO _migrations (name) VALUES ('advert_count_unique_v1')`)
		log.Println("[migration] advert_count recalculated")
	}

	// One-time migration: change noise_floor from INTEGER to REAL affinity.
	// SQLite doesn't support ALTER COLUMN, but existing float values are stored
	// as REAL regardless of column affinity. New table definition already uses REAL.
	// This migration casts any integer-stored noise_floor values to real.
	row = db.QueryRow("SELECT 1 FROM _migrations WHERE name = 'noise_floor_real_v1'")
	if row.Scan(&migDone) != nil {
		log.Println("[migration] Ensuring noise_floor values are stored as REAL...")
		db.Exec(`UPDATE observers SET noise_floor = CAST(noise_floor AS REAL) WHERE noise_floor IS NOT NULL AND typeof(noise_floor) = 'integer'`)
		db.Exec(`INSERT INTO _migrations (name) VALUES ('noise_floor_real_v1')`)
		log.Println("[migration] noise_floor migration complete")
	}

	// One-time migration: add telemetry columns to nodes and inactive_nodes tables.
	row = db.QueryRow("SELECT 1 FROM _migrations WHERE name = 'node_telemetry_v1'")
	if row.Scan(&migDone) != nil {
		log.Println("[migration] Adding telemetry columns to nodes/inactive_nodes...")

		// checkAndAddColumn checks whether `column` already exists in `table`
		// using PRAGMA table_info, and adds it if missing. All call sites pass
		// hardcoded table/column/type literals so there is no SQL injection risk.
		checkAndAddColumn := func(table, column, colType string) error {
			rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
			if err != nil {
				return fmt.Errorf("querying table info for %s: %w", table, err)
			}
			defer rows.Close()

			exists := false
			for rows.Next() {
				var cid int
				var name, ctype string
				var notnull, pk int
				var dfltValue sql.NullString
				if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
					return fmt.Errorf("scanning table info for %s: %w", table, err)
				}
				if name == column {
					exists = true
					break
				}
			}
			if err := rows.Err(); err != nil {
				return fmt.Errorf("iterating table info for %s: %w", table, err)
			}
			if exists {
				return nil
			}
			if _, err := db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, colType)); err != nil {
				return fmt.Errorf("adding column %s to %s: %w", column, table, err)
			}
			return nil
		}

		if err := checkAndAddColumn("nodes", "battery_mv", "INTEGER"); err != nil {
			return err
		}
		if err := checkAndAddColumn("nodes", "temperature_c", "REAL"); err != nil {
			return err
		}
		if err := checkAndAddColumn("inactive_nodes", "battery_mv", "INTEGER"); err != nil {
			return err
		}
		if err := checkAndAddColumn("inactive_nodes", "temperature_c", "REAL"); err != nil {
			return err
		}
		if _, err := db.Exec(`INSERT INTO _migrations (name) VALUES ('node_telemetry_v1')`); err != nil {
			return fmt.Errorf("recording node_telemetry_v1 migration: %w", err)
		}
		log.Println("[migration] node telemetry columns added")
	}

	// One-time migration: add timestamp index on observations for fast stats queries.
	// Older databases created before this index was added suffer from full table scans
	// on COUNT(*) WHERE timestamp > ?, causing /api/stats to take 30s+.
	row = db.QueryRow("SELECT 1 FROM _migrations WHERE name = 'obs_timestamp_index_v1'")
	if row.Scan(&migDone) != nil {
		log.Println("[migration] Adding timestamp index on observations...")
		db.Exec(`CREATE INDEX IF NOT EXISTS idx_observations_timestamp ON observations(timestamp)`)
		db.Exec(`INSERT INTO _migrations (name) VALUES ('obs_timestamp_index_v1')`)
		log.Println("[migration] observations timestamp index created")
	}

	// #1481 P0-3: covering index for GetObserverPacketCounts. The query
	// joins observations → observers and GROUP BYs observer_idx with a
	// timestamp WHERE filter; a composite (observer_idx, timestamp)
	// index lets SQLite resolve the grouping + range filter from the
	// index alone instead of a 1.9M-row scan.
	//
	// CONVERTED TO ASYNC (preflight-async-migration-gate). Scheduling
	// happens in OpenStore() once the real *Store exists so the
	// backfill WaitGroup is shared with the rest of the ingestor.
	// The legacy `_migrations` gate is preserved by the async fn so
	// DBs that already completed the sync build stay no-op.

	// #1483: normalize nodes.public_key to lowercase. The server's
	// GetNodeLocationsByKeys lookup dropped LOWER(public_key) for perf
	// (#1481 P0-3) and now relies on stored keys being lowercase. The
	// decoder writes lowercase today, but legacy/admin/API inserts may
	// have left mixed-case rows. Idempotent: counts and lowers any
	// non-lowercase rows on every boot, runs once via _migrations gate
	// for the bulk fix. Re-running stays cheap because subsequent
	// passes match zero rows.
	if r := db.QueryRow("SELECT COUNT(*) FROM nodes WHERE public_key != lower(public_key)"); r != nil {
		var n int64
		_ = r.Scan(&n)
		if n > 0 {
			log.Printf("[migration] Normalizing %d nodes.public_key row(s) to lowercase (#1483)...", n)
			if _, err := db.Exec(`UPDATE nodes SET public_key = lower(public_key) WHERE public_key != lower(public_key)`); err != nil {
				log.Printf("[migration] public_key lowercase normalize failed: %v", err)
			} else {
				log.Printf("[migration] public_key lowercase normalize complete (%d rows)", n)
			}
		}
	}

	// observer_metrics table for RF health dashboard
	row = db.QueryRow("SELECT 1 FROM _migrations WHERE name = 'observer_metrics_v1'")
	if row.Scan(&migDone) != nil {
		log.Println("[migration] Creating observer_metrics table...")
		_, err := db.Exec(`
			CREATE TABLE IF NOT EXISTS observer_metrics (
				observer_id TEXT NOT NULL,
				timestamp TEXT NOT NULL,
				noise_floor REAL,
				tx_air_secs INTEGER,
				rx_air_secs INTEGER,
				recv_errors INTEGER,
				battery_mv INTEGER,
				PRIMARY KEY (observer_id, timestamp)
			)
		`)
		if err != nil {
			return fmt.Errorf("observer_metrics schema: %w", err)
		}
		db.Exec(`INSERT INTO _migrations (name) VALUES ('observer_metrics_v1')`)
		log.Println("[migration] observer_metrics table created")
	}

	// Migration: add timestamp index for cross-observer time-range queries
	row = db.QueryRow("SELECT 1 FROM _migrations WHERE name = 'observer_metrics_ts_idx'")
	if row.Scan(&migDone) != nil {
		log.Println("[migration] Creating observer_metrics timestamp index...")
		_, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_observer_metrics_timestamp ON observer_metrics(timestamp)`)
		if err != nil {
			return fmt.Errorf("observer_metrics timestamp index: %w", err)
		}
		db.Exec(`INSERT INTO _migrations (name) VALUES ('observer_metrics_ts_idx')`)
		log.Println("[migration] observer_metrics timestamp index created")
	}

	// Migration: add inactive column to observers for soft-delete retention
	row = db.QueryRow("SELECT 1 FROM _migrations WHERE name = 'observers_inactive_v1'")
	if row.Scan(&migDone) != nil {
		log.Println("[migration] Adding inactive column to observers...")
		_, err := db.Exec(`ALTER TABLE observers ADD COLUMN inactive INTEGER DEFAULT 0`)
		if err != nil {
			// Column may already exist (e.g. fresh install with schema above)
			log.Printf("[migration] observers.inactive: %v (may already exist)", err)
		}
		db.Exec(`INSERT INTO _migrations (name) VALUES ('observers_inactive_v1')`)
		log.Println("[migration] observers.inactive column added")
	}

	// Migration: add packets_sent and packets_recv columns to observer_metrics
	row = db.QueryRow("SELECT 1 FROM _migrations WHERE name = 'observer_metrics_packets_v1'")
	if row.Scan(&migDone) != nil {
		log.Println("[migration] Adding packets_sent/packets_recv columns to observer_metrics...")
		db.Exec(`ALTER TABLE observer_metrics ADD COLUMN packets_sent INTEGER`)
		db.Exec(`ALTER TABLE observer_metrics ADD COLUMN packets_recv INTEGER`)
		db.Exec(`INSERT INTO _migrations (name) VALUES ('observer_metrics_packets_v1')`)
		log.Println("[migration] packets_sent/packets_recv columns added")
	}

	// Migration: add channel_hash column for fast channel queries (#762)
	row = db.QueryRow("SELECT 1 FROM _migrations WHERE name = 'channel_hash_v1'")
	if row.Scan(&migDone) != nil {
		log.Println("[migration] Adding channel_hash column to transmissions...")
		db.Exec(`ALTER TABLE transmissions ADD COLUMN channel_hash TEXT DEFAULT NULL`)
		db.Exec(`CREATE INDEX IF NOT EXISTS idx_tx_channel_hash ON transmissions(channel_hash) WHERE payload_type = 5`)
		// Backfill: extract channel name for decrypted (CHAN) packets
		res, err := db.Exec(`UPDATE transmissions SET channel_hash = json_extract(decoded_json, '$.channel') WHERE payload_type = 5 AND channel_hash IS NULL AND json_extract(decoded_json, '$.type') = 'CHAN'`)
		if err == nil {
			n, _ := res.RowsAffected()
			log.Printf("[migration] Backfilled channel_hash for %d CHAN packets", n)
		}
		// Backfill: extract channelHashHex for encrypted (GRP_TXT) packets, prefixed with 'enc_'
		res, err = db.Exec(`UPDATE transmissions SET channel_hash = 'enc_' || json_extract(decoded_json, '$.channelHashHex') WHERE payload_type = 5 AND channel_hash IS NULL AND json_extract(decoded_json, '$.type') = 'GRP_TXT'`)
		if err == nil {
			n, _ := res.RowsAffected()
			log.Printf("[migration] Backfilled channel_hash for %d GRP_TXT packets", n)
		}
		db.Exec(`INSERT INTO _migrations (name) VALUES ('channel_hash_v1')`)
		log.Println("[migration] channel_hash column added and backfilled")
	}

	// Migration: dropped_packets table for signature validation failures (#793)
	row = db.QueryRow("SELECT 1 FROM _migrations WHERE name = 'dropped_packets_v1'")
	if row.Scan(&migDone) != nil {
		log.Println("[migration] Creating dropped_packets table...")
		_, err := db.Exec(`
			CREATE TABLE IF NOT EXISTS dropped_packets (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				hash TEXT,
				raw_hex TEXT,
				reason TEXT NOT NULL,
				observer_id TEXT,
				observer_name TEXT,
				node_pubkey TEXT,
				node_name TEXT,
				dropped_at DATETIME DEFAULT CURRENT_TIMESTAMP
			);
			CREATE INDEX IF NOT EXISTS idx_dropped_observer ON dropped_packets(observer_id);
			CREATE INDEX IF NOT EXISTS idx_dropped_node ON dropped_packets(node_pubkey);
		`)
		if err != nil {
			return fmt.Errorf("dropped_packets schema: %w", err)
		}
		db.Exec(`INSERT INTO _migrations (name) VALUES ('dropped_packets_v1')`)
		log.Println("[migration] dropped_packets table created")
	}

	// Migration: observations.raw_hex (#881) is now owned by
	// internal/dbschema/dbschema.go (#1321). The server PRAGMA-detects
	// this column as hasObsRawHex; keeping a single canonical Apply
	// path closes the startup race where the server's detector ran
	// before this ALTER finished.

	// Migration: transmissions.scope_name (#899) is now owned by
	// internal/dbschema/dbschema.go (#1321). See above.

	// Migration: add last_packet_at column to observers (#last-packet-at)
	row = db.QueryRow("SELECT 1 FROM _migrations WHERE name = 'observers_last_packet_at_v1'")
	if row.Scan(&migDone) != nil {
		log.Println("[migration] Adding last_packet_at column to observers...")
		_, alterErr := db.Exec(`ALTER TABLE observers ADD COLUMN last_packet_at TEXT DEFAULT NULL`)
		if alterErr != nil && !strings.Contains(alterErr.Error(), "duplicate column") {
			return fmt.Errorf("observers last_packet_at ALTER: %w", alterErr)
		}
		// Backfill: set last_packet_at = last_seen only for observers that actually have
		// observation rows (packet_count alone is unreliable — UpsertObserver sets it to 1
		// on INSERT even for status-only observers).
		res, err := db.Exec(`UPDATE observers SET last_packet_at = last_seen
			WHERE last_packet_at IS NULL
			AND rowid IN (SELECT DISTINCT observer_idx FROM observations WHERE observer_idx IS NOT NULL)`)
		if err == nil {
			n, _ := res.RowsAffected()
			log.Printf("[migration] Backfilled last_packet_at for %d observers with packets", n)
		}
		db.Exec(`INSERT INTO _migrations (name) VALUES ('observers_last_packet_at_v1')`)
		log.Println("[migration] observers.last_packet_at column added")
	}

	// Migration: per-observer naive-clock skew tracking (#1478).
	// When the ingestor clamps a packet's envelope timestamp because the
	// observer emitted a zone-less local-time string off from UTC by >15min
	// (resolveRxTime in main.go), we record the event here so the UI can
	// surface a ⚠️ chip + banner. Decays after 24h via server-side read sweep.
	row = db.QueryRow("SELECT 1 FROM _migrations WHERE name = 'observers_clock_naive_v1'")
	if row.Scan(&migDone) != nil {
		log.Println("[migration] Adding clock-naive columns to observers (#1478)...")
		// Each ALTER is independent — ignore "duplicate column" so reruns are safe.
		for _, stmt := range []string{
			`ALTER TABLE observers ADD COLUMN clock_skew_seconds INTEGER DEFAULT NULL`,
			`ALTER TABLE observers ADD COLUMN clock_skew_count_24h INTEGER DEFAULT 0`,
			`ALTER TABLE observers ADD COLUMN clock_last_naive_at TEXT DEFAULT NULL`,
		} {
			if _, err := db.Exec(stmt); err != nil && !strings.Contains(err.Error(), "duplicate column") {
				return fmt.Errorf("clock_naive migration: %w", err)
			}
		}
		db.Exec(`INSERT INTO _migrations (name) VALUES ('observers_clock_naive_v1')`)
		log.Println("[migration] observers.clock_naive columns added")
	}

	// Migration: backfill observations.path_json from raw_hex (#888)
	// NOTE: This runs ASYNC via BackfillPathJSONAsync() to avoid blocking MQTT startup.
	// See staging outage where ~502K rows blocked ingest for 15+ hours.

	// One-time cleanup: delete legacy packets with empty hash or empty first_seen (#994)
	row = db.QueryRow("SELECT 1 FROM _migrations WHERE name = 'cleanup_legacy_null_hash_ts'")
	if row.Scan(&migDone) != nil {
		log.Println("[migration] Cleaning up legacy packets with empty hash/timestamp...")
		db.Exec(`DELETE FROM observations WHERE transmission_id IN (SELECT id FROM transmissions WHERE hash = '' OR first_seen = '')`)
		res, err := db.Exec(`DELETE FROM transmissions WHERE hash = '' OR first_seen = ''`)
		if err == nil {
			deleted, _ := res.RowsAffected()
			log.Printf("[migration] deleted %d legacy packets with empty hash/timestamp", deleted)
		}
		db.Exec(`INSERT INTO _migrations (name) VALUES ('cleanup_legacy_null_hash_ts')`)
	}

	// Migration: foreign_advert column on nodes/inactive_nodes (#730)
	// Marks nodes whose ADVERT GPS lies outside the configured geofilter polygon.
	// Default 0; set to 1 by the ingestor when GeoFilter is configured and
	// PassesFilter() returns false. Allows operators to surface bridged/leaked
	// adverts without silently dropping them.
	row = db.QueryRow("SELECT 1 FROM _migrations WHERE name = 'foreign_advert_v1'")
	if row.Scan(&migDone) != nil {
		log.Println("[migration] Adding foreign_advert column to nodes/inactive_nodes...")
		if _, err := db.Exec(`ALTER TABLE nodes ADD COLUMN foreign_advert INTEGER DEFAULT 0`); err != nil {
			log.Printf("[migration] nodes.foreign_advert: %v (may already exist)", err)
		}
		if _, err := db.Exec(`ALTER TABLE inactive_nodes ADD COLUMN foreign_advert INTEGER DEFAULT 0`); err != nil {
			log.Printf("[migration] inactive_nodes.foreign_advert: %v (may already exist)", err)
		}
		db.Exec(`CREATE INDEX IF NOT EXISTS idx_nodes_foreign_advert ON nodes(foreign_advert) WHERE foreign_advert = 1`)
		db.Exec(`INSERT INTO _migrations (name) VALUES ('foreign_advert_v1')`)
		log.Println("[migration] foreign_advert column added")
	}

	// Migration: from_pubkey column on transmissions (#1143).
	// Replaces the unsound `decoded_json LIKE '%pubkey%'` attribution path with
	// an exact-match indexed column. Synchronously adds the column + index;
	// row-level backfill is run by the SERVER asynchronously
	// (cmd/server/from_pubkey_migration.go) so we don't block ingestor boot.
	row = db.QueryRow("SELECT 1 FROM _migrations WHERE name = 'from_pubkey_v1'")
	if row.Scan(&migDone) != nil {
		log.Println("[migration] Adding from_pubkey column + index to transmissions (#1143)...")
		if _, err := db.Exec(`ALTER TABLE transmissions ADD COLUMN from_pubkey TEXT`); err != nil {
			log.Printf("[migration] transmissions.from_pubkey: %v (may already exist)", err)
		}
		if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_transmissions_from_pubkey ON transmissions(from_pubkey)`); err != nil {
			log.Printf("[migration] idx_transmissions_from_pubkey: %v", err)
		}
		db.Exec(`INSERT INTO _migrations (name) VALUES ('from_pubkey_v1')`)
		log.Println("[migration] from_pubkey column + index added")
	}

	// Migration: nodes.default_scope (#899 Feature 3) is now owned by
	// internal/dbschema/dbschema.go (#1321). The server PRAGMA-detects
	// this column as hasDefaultScope; keeping a single canonical Apply
	// path closes the startup race that #1321 documented.

	// Migration: normalize known channel_hash values for existing rows.
	// Before this PR, config key "public" was stored as channel_hash="public".
	// After this PR, new rows use channel_hash="Public". Without backfill,
	// channel grouping queries split into two buckets across the upgrade boundary.
	row = db.QueryRow("SELECT 1 FROM _migrations WHERE name = 'channel_hash_casing_v1'")
	if row.Scan(&migDone) != nil {
		log.Println("[migration] Normalizing known channel_hash values...")
		res, err := db.Exec(`UPDATE transmissions SET channel_hash = 'Public' WHERE channel_hash = 'public' AND payload_type = 5`)
		if err != nil {
			log.Printf("[migration] ERROR: failed to normalize channel_hash: %v", err)
			return fmt.Errorf("migration channel_hash_casing_v1 UPDATE failed: %w", err)
		}
		n, _ := res.RowsAffected()
		log.Printf("[migration] Normalized %d channel_hash rows from 'public' to 'Public'", n)
		if _, err := db.Exec(`INSERT OR IGNORE INTO _migrations (name) VALUES ('channel_hash_casing_v1')`); err != nil {
			log.Printf("[migration] WARNING: failed to record migration: %v", err)
		}
		log.Println("[migration] channel_hash casing normalization complete")
	}

	// Migration: transmissions.code1/code2 — the transport codes were decoded
	// and discarded; storing them makes scope forwarding queryable per repeater.
	row = db.QueryRow("SELECT 1 FROM _migrations WHERE name = 'transport_codes_v1'")
	if row.Scan(new(int)) != nil {
		log.Println("[migration] Adding code1/code2 columns to transmissions...")
		// Each ALTER is independent — ignore "duplicate column" so reruns (and
		// the fresh-database path, where the base schema already created the
		// columns) are safe. Any other failure must stop before the guard row
		// is inserted, or prepareStatements' code1/code2 INSERT fails forever
		// on every subsequent restart with no way to retry the migration.
		for _, stmt := range []string{
			`ALTER TABLE transmissions ADD COLUMN code1 TEXT DEFAULT NULL`,
			`ALTER TABLE transmissions ADD COLUMN code2 TEXT DEFAULT NULL`,
		} {
			if _, err := db.Exec(stmt); err != nil && !strings.Contains(err.Error(), "duplicate column") {
				return fmt.Errorf("transport_codes_v1 migration: %w", err)
			}
		}
		db.Exec(`CREATE INDEX IF NOT EXISTS idx_tx_code1 ON transmissions(code1) WHERE code1 IS NOT NULL`)
		db.Exec(`INSERT INTO _migrations (name) VALUES ('transport_codes_v1')`)
		log.Println("[migration] code1/code2 columns added")
	}

	return nil
}

// Normalize upgrades an offline SQLite working copy before import.
func Normalize(db *sql.DB, logf Logger) error {
	if err := CheckLegacySource(db); err != nil {
		return err
	}
	if err := ApplyBase(db); err != nil {
		return err
	}
	return Apply(db, logf)
}

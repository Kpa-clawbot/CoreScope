-- End-state SQLite schema of a freshly booted ingestor (cmd/ingestor OpenStore),
-- exported as dbschema.SQLiteEndState. TestSQLiteEndStateMatchesIngestor in
-- cmd/ingestor fails when the ingestor's schema drifts from this file.

-- table _async_migrations
CREATE TABLE _async_migrations (
			name       TEXT PRIMARY KEY,
			status     TEXT NOT NULL,             -- pending_async | done | cancelled | failed
			started_at TEXT NOT NULL DEFAULT (datetime('now')),
			ended_at   TEXT,
			error      TEXT
		);

-- table _migrations
CREATE TABLE _migrations (name TEXT PRIMARY KEY);

-- table advert_evidence_backfill
CREATE TABLE advert_evidence_backfill (
			id INTEGER PRIMARY KEY CHECK(id=1),
			tx_cursor INTEGER NOT NULL DEFAULT 0,
			obs_cursor INTEGER NOT NULL DEFAULT 0
		);

-- table advert_route_evidence
CREATE TABLE advert_route_evidence (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			tx_id INTEGER NOT NULL REFERENCES transmissions(id) ON DELETE CASCADE,
			bit INTEGER NOT NULL CHECK(bit IN (1,2)),
			UNIQUE(tx_id,bit)
		);

-- table client_node_names
CREATE TABLE client_node_names (
			id             INTEGER PRIMARY KEY AUTOINCREMENT,
			target         TEXT NOT NULL,
			rx_pubkey      TEXT NOT NULL,
			observed_at    TEXT NOT NULL,
			ingested_at    TEXT NOT NULL,
			name           TEXT NOT NULL,
			lat            REAL,
			lon            REAL,
			pos_acc_m      REAL,
			repeater_clock INTEGER,
			UNIQUE(target, rx_pubkey, observed_at)
		);

-- table client_observers
CREATE TABLE client_observers (
			pubkey    TEXT PRIMARY KEY,
			name      TEXT,
			last_seen TEXT
		);

-- table client_receptions
CREATE TABLE client_receptions (
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

-- table client_rf_samples
CREATE TABLE client_rf_samples (
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

-- table client_rx_observations
CREATE TABLE client_rx_observations (
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

-- table dropped_packets
CREATE TABLE dropped_packets (
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

-- table inactive_nodes
CREATE TABLE inactive_nodes (
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
		, default_scope TEXT DEFAULT NULL, configured_scope TEXT DEFAULT NULL, configured_scope_at TEXT DEFAULT NULL, multibyte_sup INTEGER NOT NULL DEFAULT 0, multibyte_evidence TEXT);

-- table key_backfill_done
CREATE TABLE key_backfill_done (
			kind TEXT NOT NULL,
			key_fp TEXT NOT NULL,
			done_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
			PRIMARY KEY(kind, key_fp)
		) WITHOUT ROWID;

-- table key_backfill_state
CREATE TABLE key_backfill_state (
			kind TEXT PRIMARY KEY CHECK(kind IN ('channel','region')),
			run_fp TEXT NOT NULL,
			cursor INTEGER NOT NULL DEFAULT 0,
			upper INTEGER NOT NULL DEFAULT 0,
			rewritten INTEGER NOT NULL DEFAULT 0,
			started_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER))
		);

-- table neighbor_edges
CREATE TABLE neighbor_edges (
		node_a TEXT NOT NULL,
		node_b TEXT NOT NULL,
		count INTEGER DEFAULT 1,
		last_seen TEXT,
		PRIMARY KEY (node_a, node_b)
	);

-- table node_declared_regions
CREATE TABLE node_declared_regions (
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

-- table nodes
CREATE TABLE nodes (
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
		, default_scope TEXT DEFAULT NULL, configured_scope TEXT DEFAULT NULL, configured_scope_at TEXT DEFAULT NULL, multibyte_sup INTEGER NOT NULL DEFAULT 0, multibyte_evidence TEXT);

-- table observation_radios
CREATE TABLE observation_radios (
			observation_id INTEGER PRIMARY KEY,
			radio_id       TEXT NOT NULL
		);

-- table observations
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
			, resolved_path TEXT, raw_hex TEXT);

-- table observer_metrics
CREATE TABLE observer_metrics (
				observer_id TEXT NOT NULL,
				timestamp TEXT NOT NULL,
				noise_floor REAL,
				tx_air_secs INTEGER,
				rx_air_secs INTEGER,
				recv_errors INTEGER,
				battery_mv INTEGER, packets_sent INTEGER, packets_recv INTEGER,
				PRIMARY KEY (observer_id, timestamp)
			);

-- table observer_neighbor_reports
CREATE TABLE observer_neighbor_reports (
			observer_id        TEXT PRIMARY KEY,
			received_at        TEXT NOT NULL,
			reported_at        TEXT,
			neighbor_count     INTEGER NOT NULL,
			queried_neighbors  INTEGER,
			self_scopes        TEXT,
			self_default_scope TEXT,
			neighbors_json     TEXT NOT NULL
		);

-- table observer_radios
CREATE TABLE observer_radios (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			observer_id TEXT NOT NULL,
			radio_id    TEXT NOT NULL DEFAULT '',
			radio       TEXT NOT NULL,
			first_seen  TEXT NOT NULL,
			last_seen   TEXT NOT NULL
		);

-- table observers
CREATE TABLE observers (
			idx INTEGER PRIMARY KEY AUTOINCREMENT,
			id TEXT NOT NULL UNIQUE,
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

-- table scope_match_totals
CREATE TABLE scope_match_totals (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			since_unix INTEGER NOT NULL,
			unique_matches INTEGER NOT NULL,
			explicit_over_derived INTEGER NOT NULL,
			ambiguous INTEGER NOT NULL,
			none_matches INTEGER NOT NULL,
			updated_unix INTEGER NOT NULL
		);

-- table transmissions
CREATE TABLE transmissions (
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
		, channel_hash TEXT DEFAULT NULL, scope_name TEXT DEFAULT NULL);

-- table tx_rewrite_feed
CREATE TABLE tx_rewrite_feed (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			tx_id INTEGER NOT NULL REFERENCES transmissions(id) ON DELETE CASCADE,
			created_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER))
		);

-- index idx_cnn_prune
CREATE INDEX idx_cnn_prune  ON client_node_names(observed_at);

-- index idx_cnn_target
CREATE INDEX idx_cnn_target ON client_node_names(target, observed_at);

-- index idx_client_recept_heard_geo
CREATE INDEX idx_client_recept_heard_geo ON client_receptions(heard_key, heard_keylen, lat, lon);

-- index idx_client_recept_latlon
CREATE INDEX idx_client_recept_latlon ON client_receptions(lat, lon);

-- index idx_client_recept_rxat
CREATE INDEX idx_client_recept_rxat ON client_receptions(rx_at);

-- index idx_crf_prune
CREATE INDEX idx_crf_prune ON client_rf_samples(sampled_at);

-- index idx_crf_track
CREATE INDEX idx_crf_track ON client_rf_samples(rx_pubkey, sampled_at);

-- index idx_cro_forwarder
CREATE INDEX idx_cro_forwarder ON client_rx_observations(forwarder, rx_at);

-- index idx_cro_hash
CREATE INDEX idx_cro_hash      ON client_rx_observations(pkt_hash, rx_at);

-- index idx_cro_prune
CREATE INDEX idx_cro_prune     ON client_rx_observations(rx_at);

-- index idx_cro_scope
CREATE INDEX idx_cro_scope     ON client_rx_observations(scope_name, rx_at);

-- index idx_dropped_node
CREATE INDEX idx_dropped_node ON dropped_packets(node_pubkey);

-- index idx_dropped_observer
CREATE INDEX idx_dropped_observer ON dropped_packets(observer_id);

-- index idx_inactive_nodes_last_seen
CREATE INDEX idx_inactive_nodes_last_seen ON inactive_nodes(last_seen);

-- index idx_ndr_prune
CREATE INDEX idx_ndr_prune  ON node_declared_regions(observed_at);

-- index idx_ndr_target
CREATE INDEX idx_ndr_target ON node_declared_regions(target, observed_at);

-- index idx_nodes_foreign_advert
CREATE INDEX idx_nodes_foreign_advert ON nodes(foreign_advert) WHERE foreign_advert = 1;

-- index idx_nodes_last_seen
CREATE INDEX idx_nodes_last_seen ON nodes(last_seen);

-- index idx_observations_dedup
CREATE UNIQUE INDEX idx_observations_dedup ON observations(transmission_id, observer_idx, COALESCE(path_json, ''));

-- index idx_observations_observer_idx
CREATE INDEX idx_observations_observer_idx ON observations(observer_idx);

-- index idx_observations_observer_idx_timestamp
CREATE INDEX idx_observations_observer_idx_timestamp ON observations(observer_idx, timestamp);

-- index idx_observations_timestamp
CREATE INDEX idx_observations_timestamp ON observations(timestamp);

-- index idx_observations_transmission_id
CREATE INDEX idx_observations_transmission_id ON observations(transmission_id);

-- index idx_observations_tx_ts
CREATE INDEX idx_observations_tx_ts ON observations(transmission_id, timestamp);

-- index idx_observer_metrics_timestamp
CREATE INDEX idx_observer_metrics_timestamp ON observer_metrics(timestamp);

-- index idx_observer_radios_obs
CREATE INDEX idx_observer_radios_obs
			ON observer_radios(observer_id, radio_id, first_seen);

-- index idx_observers_iata_norm
CREATE INDEX idx_observers_iata_norm ON observers(UPPER(TRIM(iata)));

-- index idx_observers_last_seen
CREATE INDEX idx_observers_last_seen ON observers(last_seen);

-- index idx_transmissions_channel_hash_payload
CREATE INDEX idx_transmissions_channel_hash_payload ON transmissions(channel_hash, payload_type, first_seen);

-- index idx_transmissions_first_seen
CREATE INDEX idx_transmissions_first_seen ON transmissions(first_seen);

-- index idx_transmissions_from_pubkey
CREATE INDEX idx_transmissions_from_pubkey ON transmissions(from_pubkey);

-- index idx_transmissions_hash
CREATE INDEX idx_transmissions_hash ON transmissions(hash);

-- index idx_transmissions_payload_type
CREATE INDEX idx_transmissions_payload_type ON transmissions(payload_type);

-- index idx_tx_channel_hash
CREATE INDEX idx_tx_channel_hash ON transmissions(channel_hash) WHERE payload_type = 5;

-- index idx_tx_code1
CREATE INDEX idx_tx_code1 ON transmissions(code1) WHERE code1 IS NOT NULL;

-- index idx_tx_last_seen_zero
CREATE INDEX idx_tx_last_seen_zero ON transmissions(id) WHERE last_seen=0;

-- index idx_tx_scope_name
CREATE INDEX idx_tx_scope_name ON transmissions(scope_name) WHERE scope_name IS NOT NULL;

-- index idx_tx_rewrite_feed_created
CREATE INDEX idx_tx_rewrite_feed_created ON tx_rewrite_feed(created_at);

-- index idx_tx_rewrite_feed_tx
CREATE INDEX idx_tx_rewrite_feed_tx ON tx_rewrite_feed(tx_id);

-- view packets_v
CREATE VIEW packets_v AS
		SELECT o.id, COALESCE(o.raw_hex, t.raw_hex) AS raw_hex,
			   datetime(o.timestamp, 'unixepoch') AS timestamp,
			   obs.id AS observer_id, obs.name AS observer_name,
			   o.direction, o.snr, o.rssi, o.score, t.hash, t.route_type,
			   t.payload_type, t.payload_version, o.path_json, t.decoded_json,
			   t.created_at
		FROM observations o
		JOIN transmissions t ON t.id = o.transmission_id
		LEFT JOIN observers obs ON obs.idx = o.observer_idx AND (obs.inactive IS NULL OR obs.inactive = 0);


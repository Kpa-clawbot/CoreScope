package dbconfig

import "database/sql"

// ApplyPostgresSchema creates the CoreScope tables, indexes, and compatibility
// view used by both the ingestor and server when Postgres is selected.
func ApplyPostgresSchema(db *sql.DB) error {
	schema := `
		CREATE TABLE IF NOT EXISTS nodes (
			public_key TEXT PRIMARY KEY,
			name TEXT,
			role TEXT,
			lat DOUBLE PRECISION,
			lon DOUBLE PRECISION,
			last_seen TEXT,
			first_seen TEXT,
			advert_count BIGINT DEFAULT 0,
			battery_mv INTEGER,
			temperature_c DOUBLE PRECISION,
			foreign_advert INTEGER DEFAULT 0
		);
		CREATE INDEX IF NOT EXISTS idx_nodes_last_seen ON nodes(last_seen);
		CREATE INDEX IF NOT EXISTS idx_nodes_foreign_advert ON nodes(foreign_advert) WHERE foreign_advert = 1;

		CREATE TABLE IF NOT EXISTS inactive_nodes (
			public_key TEXT PRIMARY KEY,
			name TEXT,
			role TEXT,
			lat DOUBLE PRECISION,
			lon DOUBLE PRECISION,
			last_seen TEXT,
			first_seen TEXT,
			advert_count BIGINT DEFAULT 0,
			battery_mv INTEGER,
			temperature_c DOUBLE PRECISION,
			foreign_advert INTEGER DEFAULT 0
		);
		CREATE INDEX IF NOT EXISTS idx_inactive_nodes_last_seen ON inactive_nodes(last_seen);

		CREATE TABLE IF NOT EXISTS observers (
			rowid BIGSERIAL UNIQUE,
			id TEXT PRIMARY KEY,
			name TEXT,
			iata TEXT,
			last_seen TEXT,
			first_seen TEXT,
			packet_count BIGINT DEFAULT 0,
			model TEXT,
			firmware TEXT,
			client_version TEXT,
			radio TEXT,
			battery_mv INTEGER,
			uptime_secs BIGINT,
			noise_floor DOUBLE PRECISION,
			inactive INTEGER DEFAULT 0,
			last_packet_at TEXT DEFAULT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_observers_last_seen ON observers(last_seen);
		CREATE INDEX IF NOT EXISTS idx_observers_iata ON observers(UPPER(TRIM(iata)));

		CREATE TABLE IF NOT EXISTS transmissions (
			id BIGSERIAL PRIMARY KEY,
			raw_hex TEXT NOT NULL,
			hash TEXT NOT NULL UNIQUE,
			first_seen TEXT NOT NULL,
			route_type INTEGER,
			payload_type INTEGER,
			payload_version INTEGER,
			decoded_json TEXT,
			from_pubkey TEXT,
			channel_hash TEXT,
			created_at TEXT DEFAULT to_char(now() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
		);
		CREATE INDEX IF NOT EXISTS idx_transmissions_hash ON transmissions(hash);
		CREATE INDEX IF NOT EXISTS idx_transmissions_first_seen ON transmissions(first_seen DESC);
		CREATE INDEX IF NOT EXISTS idx_transmissions_payload_type ON transmissions(payload_type);
		CREATE INDEX IF NOT EXISTS idx_transmissions_from_pubkey ON transmissions(from_pubkey);
		CREATE INDEX IF NOT EXISTS idx_tx_channel_hash ON transmissions(channel_hash) WHERE payload_type = 5;

		CREATE TABLE IF NOT EXISTS observations (
			id BIGSERIAL PRIMARY KEY,
			transmission_id BIGINT NOT NULL REFERENCES transmissions(id) ON DELETE CASCADE,
			observer_idx BIGINT REFERENCES observers(rowid),
			direction TEXT,
			snr DOUBLE PRECISION,
			rssi DOUBLE PRECISION,
			score INTEGER,
			path_json TEXT,
			timestamp BIGINT NOT NULL,
			raw_hex TEXT,
			resolved_path TEXT
		);
		CREATE INDEX IF NOT EXISTS idx_observations_transmission_id ON observations(transmission_id);
		CREATE INDEX IF NOT EXISTS idx_observations_observer_idx ON observations(observer_idx);
		CREATE INDEX IF NOT EXISTS idx_observations_timestamp ON observations(timestamp);
		CREATE UNIQUE INDEX IF NOT EXISTS idx_observations_dedup ON observations(transmission_id, observer_idx, (COALESCE(path_json, '')));

		CREATE TABLE IF NOT EXISTS observer_metrics (
			observer_id TEXT NOT NULL,
			timestamp TEXT NOT NULL,
			noise_floor DOUBLE PRECISION,
			tx_air_secs INTEGER,
			rx_air_secs INTEGER,
			recv_errors INTEGER,
			battery_mv INTEGER,
			packets_sent INTEGER,
			packets_recv INTEGER,
			PRIMARY KEY (observer_id, timestamp)
		);
		CREATE INDEX IF NOT EXISTS idx_observer_metrics_timestamp ON observer_metrics(timestamp);

		CREATE TABLE IF NOT EXISTS dropped_packets (
			id BIGSERIAL PRIMARY KEY,
			hash TEXT,
			raw_hex TEXT,
			reason TEXT NOT NULL,
			observer_id TEXT,
			observer_name TEXT,
			node_pubkey TEXT,
			node_name TEXT,
			dropped_at TEXT DEFAULT to_char(now() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
		);
		CREATE INDEX IF NOT EXISTS idx_dropped_observer ON dropped_packets(observer_id);
		CREATE INDEX IF NOT EXISTS idx_dropped_node ON dropped_packets(node_pubkey);

		CREATE TABLE IF NOT EXISTS neighbor_edges (
			node_a TEXT NOT NULL,
			node_b TEXT NOT NULL,
			count BIGINT DEFAULT 1,
			last_seen TEXT,
			PRIMARY KEY (node_a, node_b)
		);

		CREATE TABLE IF NOT EXISTS _migrations (name TEXT PRIMARY KEY);
	`
	if _, err := db.Exec(schema); err != nil {
		return err
	}
	if _, err := db.Exec(`DROP VIEW IF EXISTS packets_v`); err != nil {
		return err
	}
	_, err := db.Exec(`
		CREATE VIEW packets_v AS
			SELECT o.id, COALESCE(o.raw_hex, t.raw_hex) AS raw_hex,
				   to_char(to_timestamp(o.timestamp) AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"') AS timestamp,
				   obs.id AS observer_id, obs.name AS observer_name,
				   o.direction, o.snr, o.rssi, o.score, t.hash, t.route_type,
				   t.payload_type, t.payload_version, o.path_json, t.decoded_json,
				   t.created_at
			FROM observations o
			JOIN transmissions t ON t.id = o.transmission_id
			LEFT JOIN observers obs ON obs.rowid = o.observer_idx AND (obs.inactive IS NULL OR obs.inactive = 0)
	`)
	return err
}

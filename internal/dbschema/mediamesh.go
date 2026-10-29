package dbschema

import (
	"database/sql"
	"fmt"
)

// MediaMesh: a second LoRa profile ("radio2") that Keymind Cascade repeaters,
// Wadamesh TAK standalones and MeshCore Open TAK use for photos and voice.
// Observers tag what used it (Keymind Cascade MQTT_IMPLEMENTATION.md,
// "radio2 (MediaMesh)"):
//
//   - packets: `"mediamesh": true`, with `rx_radio_id` / `tx_radio_id`;
//   - status: `radios: [{id, radio, mode, mediamesh: true, temporary}]`;
//   - neighbors: `self.radio2` and each neighbour's `radio2` {mode, radio}.
//
// observation_mediamesh is sparse like observation_radios: one row per
// observation that came with `mediamesh: true`, keyed by the observation, so
// the observations table and every query that scans it stay untouched.
//
// node_mediamesh holds the newest evidence that a node runs MediaMesh (mode
// rx|rxtx) or not (off): the node's own status, a neighbour's report of it,
// or the node heard sending on radio2 (source "heard", mode rxtx). A later
// row replaces an older one; "off" clears the mark.
//
// Neither is required by AssertReady: a server that comes up before the
// ingestor created them shows no MediaMesh marks and probes again on its next
// refresh, the same rule as observer_radios.
//
// PREFLIGHT: async=false reason="two small tables, no backfill"
func ensureMediaMesh(rw *sql.DB) error {
	if _, err := rw.Exec(`
		CREATE TABLE IF NOT EXISTS observation_mediamesh (
			observation_id INTEGER PRIMARY KEY
		);
		CREATE TABLE IF NOT EXISTS node_mediamesh (
			public_key TEXT PRIMARY KEY,
			mode       TEXT NOT NULL,
			radio      TEXT,
			source     TEXT NOT NULL,
			last_seen  TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_node_mediamesh_last_seen
			ON node_mediamesh(last_seen);
	`); err != nil {
		return fmt.Errorf("create mediamesh tables: %w", err)
	}
	return nil
}

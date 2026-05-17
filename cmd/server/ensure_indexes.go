package main

import (
	"fmt"
	"strings"
)

// ensureServerIndexes creates the indexes that the SQL fallback path in
// QueryPackets / QueryGroupedPackets and the background hot-startup chunk
// loader depend on. Mirrors the indexes the ingestor creates (see
// cmd/ingestor/db.go applySchema). Safe to call on every server start
// because every CREATE INDEX uses IF NOT EXISTS. Needed because DBs
// created by an old server-only build (pre-ingestor) won't have the
// ingestor's indexes, which would cause full table scans on the SQL
// fallback path during hot startup.
func ensureServerIndexes(dbPath string) error {
	rw, err := cachedRW(dbPath)
	if err != nil {
		return fmt.Errorf("open rw for index ensure: %w", err)
	}
	stmts := []string{
		`CREATE INDEX IF NOT EXISTS idx_transmissions_first_seen ON transmissions(first_seen)`,
		`CREATE INDEX IF NOT EXISTS idx_transmissions_hash ON transmissions(hash)`,
		`CREATE INDEX IF NOT EXISTS idx_transmissions_payload_type ON transmissions(payload_type)`,
		// PR #1187 r3: commit 63cc1bc3 restored the RFC3339 since/until path
		// to a SELECT … FROM observations WHERE timestamp >= ? subquery in
		// buildTransmissionWhere. Without these indexes the subquery
		// full-scans observations on legacy server-only DBs (the ingestor
		// already creates them; see cmd/ingestor/db.go applySchema).
		`CREATE INDEX IF NOT EXISTS idx_observations_timestamp ON observations(timestamp)`,
		`CREATE INDEX IF NOT EXISTS idx_observations_transmission_id ON observations(transmission_id)`,
		// nodes table: role and last_seen are both used in GetNodes ORDER BY / WHERE
		// and in GetAllRoleCounts / GetRoleCounts GROUP BY. Without these indexes
		// every query does a full table scan even though the table is small.
		`CREATE INDEX IF NOT EXISTS idx_nodes_last_seen ON nodes(last_seen)`,
		`CREATE INDEX IF NOT EXISTS idx_nodes_role ON nodes(role)`,
		// observers: queried with last_seen > ? filter in GetStoreStats.
		`CREATE INDEX IF NOT EXISTS idx_observers_last_seen ON observers(last_seen)`,
	}
	for _, s := range stmts {
		if _, err := rw.Exec(s); err != nil {
			return fmt.Errorf("ensure index %q: %w", s, err)
		}
	}

	// observer_idx column exists in v3 schema only; observer_id is the
	// v2 equivalent. Probe the schema and create the matching index.
	rows, err := rw.Query(`PRAGMA table_info(observations)`)
	if err != nil {
		return fmt.Errorf("pragma table_info(observations): %w", err)
	}
	var hasObserverIdx, hasObserverID bool
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt interface{}
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			rows.Close()
			return fmt.Errorf("scan table_info: %w", err)
		}
		switch strings.ToLower(name) {
		case "observer_idx":
			hasObserverIdx = true
		case "observer_id":
			hasObserverID = true
		}
	}
	rows.Close()

	if hasObserverIdx {
		if _, err := rw.Exec(`CREATE INDEX IF NOT EXISTS idx_observations_observer_idx ON observations(observer_idx)`); err != nil {
			return fmt.Errorf("ensure idx_observations_observer_idx: %w", err)
		}
	}
	if hasObserverID {
		if _, err := rw.Exec(`CREATE INDEX IF NOT EXISTS idx_observations_observer_id ON observations(observer_id)`); err != nil {
			return fmt.Errorf("ensure idx_observations_observer_id: %w", err)
		}
	}
	return nil
}

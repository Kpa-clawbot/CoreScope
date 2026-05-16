package main

import (
	"fmt"
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
	}
	for _, s := range stmts {
		if _, err := rw.Exec(s); err != nil {
			return fmt.Errorf("ensure index %q: %w", s, err)
		}
	}
	return nil
}

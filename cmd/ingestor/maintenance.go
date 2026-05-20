package main

import (
	"fmt"
	"log"
	"time"
)

// PruneOldPackets deletes transmissions (and their child observations)
// older than `days`. Returns count of transmissions deleted.
//
// Owned by the ingestor per #1283: the writer process is the only one
// allowed to hold the DB write lock; previously this lived in
// cmd/server/db.go and raced ingestor INSERTs (SQLITE_BUSY).
func (s *Store) PruneOldPackets(days int) (int64, error) {
	if days <= 0 {
		return 0, nil
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -days).Format(time.RFC3339)

	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("prune begin: %w", err)
	}
	defer tx.Rollback()

	// Delete child observations first (no CASCADE in SQLite).
	if _, err := tx.Exec(`DELETE FROM observations WHERE transmission_id IN (
		SELECT id FROM transmissions WHERE first_seen < ?
	)`, cutoff); err != nil {
		return 0, fmt.Errorf("prune observations: %w", err)
	}

	res, err := tx.Exec(`DELETE FROM transmissions WHERE first_seen < ?`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("prune transmissions: %w", err)
	}
	n, _ := res.RowsAffected()
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("prune commit: %w", err)
	}
	if n > 0 {
		log.Printf("[prune] deleted %d transmissions older than %d days", n, days)
	}
	return n, nil
}

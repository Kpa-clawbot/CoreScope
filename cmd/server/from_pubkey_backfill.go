package main

import (
	"encoding/json"
	"log"
	"strings"
	"time"
)

// ensureFromPubkeyColumn adds the from_pubkey column and index to the transmissions
// table if they don't already exist. Called at startup before HTTP starts.
func ensureFromPubkeyColumn(dbPath string) error {
	rw, err := openRW(dbPath)
	if err != nil {
		return err
	}
	defer rw.Close()

	rw.Exec(`ALTER TABLE transmissions ADD COLUMN from_pubkey TEXT`)
	rw.Exec(`CREATE INDEX IF NOT EXISTS idx_transmissions_from_pubkey ON transmissions(from_pubkey)`)
	return nil
}

// backfillFromPubkeyAsync fills the from_pubkey column for legacy ADVERT transmissions
// where it is NULL by parsing decoded_json. Runs in the background in chunks to avoid
// blocking the write path.
func backfillFromPubkeyAsync(dbPath string, chunkSize int, yieldDuration time.Duration) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[from_pubkey] backfill panic: %v", r)
		}
	}()

	rw, err := openRW(dbPath)
	if err != nil {
		log.Printf("[from_pubkey] backfill: could not open RW DB: %v", err)
		return
	}
	defer rw.Close()

	// Count pending rows
	var pending int
	rw.QueryRow(`SELECT COUNT(*) FROM transmissions WHERE payload_type = 4 AND from_pubkey IS NULL`).Scan(&pending)
	if pending == 0 {
		log.Printf("[from_pubkey] backfill: nothing to do")
		return
	}
	log.Printf("[from_pubkey] backfill: %d ADVERT rows to fill", pending)

	type row struct {
		id          int64
		decodedJSON string
	}

	processed := 0
	for {
		rows, err := rw.Query(`
			SELECT id, decoded_json FROM transmissions
			WHERE payload_type = 4 AND from_pubkey IS NULL
			LIMIT ?`, chunkSize)
		if err != nil {
			log.Printf("[from_pubkey] backfill query error: %v", err)
			return
		}

		batch := make([]row, 0, chunkSize)
		for rows.Next() {
			var r row
			if rows.Scan(&r.id, &r.decodedJSON) == nil {
				batch = append(batch, r)
			}
		}
		rows.Close()

		if len(batch) == 0 {
			break
		}

		tx, err := rw.Begin()
		if err != nil {
			log.Printf("[from_pubkey] backfill begin tx: %v", err)
			return
		}
		stmt, err := tx.Prepare(`UPDATE transmissions SET from_pubkey = ? WHERE id = ?`)
		if err != nil {
			tx.Rollback()
			log.Printf("[from_pubkey] backfill prepare: %v", err)
			return
		}

		for _, r := range batch {
			var payload struct {
				PubKey string `json:"pubKey"`
			}
			if json.Unmarshal([]byte(r.decodedJSON), &payload) == nil && payload.PubKey != "" {
				stmt.Exec(strings.ToLower(payload.PubKey), r.id)
			} else {
				// Mark as scanned but empty with a sentinel so we don't re-scan it
				stmt.Exec("", r.id)
			}
		}
		stmt.Close()
		if err := tx.Commit(); err != nil {
			log.Printf("[from_pubkey] backfill commit: %v", err)
			return
		}

		processed += len(batch)
		time.Sleep(yieldDuration)
	}

	log.Printf("[from_pubkey] backfill complete: %d rows processed", processed)
}

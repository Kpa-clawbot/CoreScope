package main

// from_pubkey migration (#1143).
//
// Adds the `transmissions.from_pubkey` column + index, and provides an async
// backfill that populates the column from `decoded_json` for ADVERT packets
// whose `from_pubkey` is still NULL.
//
// Why a column at all: the legacy attribution path used
// `WHERE decoded_json LIKE '%pubkey%'` (and `OR LIKE '%name%'`). This is
// structurally unsound (adversarial spoofing + accidental hex-substring
// false positives + full table scan). The column gives us exact match,
// O(log n) lookups, and an explicit, auditable attribution surface.
//
// Backfill is run async (best-effort) so it cannot block server startup
// even on prod-sized DBs (100K+ transmissions). Queries handle NULL
// gracefully (return empty for that pubkey, same as today's behaviour
// for unknown pubkeys).

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"sync/atomic"
	"time"
)

// ensureFromPubkeyColumn adds the from_pubkey column + index to the
// transmissions table if missing. Safe to call repeatedly.
func ensureFromPubkeyColumn(dbPath string) error {
	rw, err := cachedRW(dbPath)
	if err != nil {
		return err
	}

	has, err := tableHasColumn(rw, "transmissions", "from_pubkey")
	if err != nil {
		return fmt.Errorf("inspect transmissions: %w", err)
	}
	if !has {
		if _, err := rw.Exec("ALTER TABLE transmissions ADD COLUMN from_pubkey TEXT"); err != nil {
			return fmt.Errorf("add from_pubkey column: %w", err)
		}
		log.Println("[store] Added from_pubkey column to transmissions (#1143)")
	}

	if _, err := rw.Exec("CREATE INDEX IF NOT EXISTS idx_transmissions_from_pubkey ON transmissions(from_pubkey)"); err != nil {
		return fmt.Errorf("create idx_transmissions_from_pubkey: %w", err)
	}
	return nil
}

// fromPubkeyBackfillProgress reports backfill state for /api/healthz
// (zero-value safe; updated by backfillFromPubkeyAsync).
var (
	fromPubkeyBackfillTotal     atomic.Int64
	fromPubkeyBackfillProcessed atomic.Int64
	fromPubkeyBackfillDone      atomic.Bool
)

// startFromPubkeyBackfill is the production entry point used by main.go to
// launch the backfill so it cannot block startup. It MUST dispatch the
// backfill in a goroutine; the dispatch path is gated by
// TestBackfillFromPubkey_DoesNotBlockBoot — if the `go` keyword below is ever
// removed, that test fails because dispatch becomes synchronous and exceeds
// the 50ms boot budget.
func startFromPubkeyBackfill(dbPath string, chunkSize int, yieldDuration time.Duration) {
	// RED: synchronous — green commit adds `go` to make the test pass.
	backfillFromPubkeyAsync(dbPath, chunkSize, yieldDuration)
}

// backfillFromPubkeyAsync scans transmissions where from_pubkey IS NULL and
// populates from_pubkey by parsing decoded_json. Runs in chunks with a
// short yield between chunks so it can't starve other writers.
//
// Strategy:
//   - ADVERT (payload_type = 4) -> decoded_json.pubKey
//   - other types -> leave NULL (queries handle NULL gracefully)
//
// chunkSize and yieldDuration are tunable for tests.
func backfillFromPubkeyAsync(dbPath string, chunkSize int, yieldDuration time.Duration) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[store] backfillFromPubkeyAsync panic recovered: %v", r)
		}
		fromPubkeyBackfillDone.Store(true)
	}()

	if chunkSize <= 0 {
		chunkSize = 5000
	}

	rw, err := cachedRW(dbPath)
	if err != nil {
		log.Printf("[store] from_pubkey backfill: open rw error: %v", err)
		return
	}

	var total int64
	if err := rw.QueryRow(
		"SELECT COUNT(*) FROM transmissions WHERE from_pubkey IS NULL AND payload_type = 4",
	).Scan(&total); err != nil {
		log.Printf("[store] from_pubkey backfill: count error: %v", err)
		return
	}
	fromPubkeyBackfillTotal.Store(total)
	if total == 0 {
		log.Println("[store] from_pubkey backfill: nothing to do")
		return
	}
	log.Printf("[store] from_pubkey backfill starting: %d ADVERT rows", total)

	updateStmt, err := rw.Prepare("UPDATE transmissions SET from_pubkey = ? WHERE id = ?")
	if err != nil {
		log.Printf("[store] from_pubkey backfill: prepare update: %v", err)
		return
	}
	defer updateStmt.Close()

	var processed int64
	for {
		rows, err := rw.Query(
			"SELECT id, decoded_json FROM transmissions WHERE from_pubkey IS NULL AND payload_type = 4 LIMIT ?",
			chunkSize)
		if err != nil {
			log.Printf("[store] from_pubkey backfill: select error: %v", err)
			return
		}

		type row struct {
			id  int64
			pk  string
		}
		batch := make([]row, 0, chunkSize)
		for rows.Next() {
			var id int64
			var dj sql.NullString
			if err := rows.Scan(&id, &dj); err != nil {
				continue
			}
			pk := extractPubkeyFromAdvertJSON(dj.String)
			batch = append(batch, row{id: id, pk: pk})
		}
		rows.Close()

		if len(batch) == 0 {
			break
		}

		// Apply updates in a single tx for throughput.
		tx, err := rw.Begin()
		if err != nil {
			log.Printf("[store] from_pubkey backfill: begin tx: %v", err)
			return
		}
		txStmt := tx.Stmt(updateStmt)
		for _, b := range batch {
			// Sentinel convention for transmissions.from_pubkey (#1143, m5):
			//   NULL — row has not yet been scanned by this backfill.
			//   ""   — scanned, no extractable pubkey (malformed/legacy ADVERT
			//          decoded_json, or a JSON shape we don't understand).
			//   hex  — scanned, pubkey successfully extracted.
			//
			// The "" sentinel exists ONLY in this backfill path: it's how we
			// avoid the #1119 infinite-rescan loop (the WHERE clause is
			// `from_pubkey IS NULL`, so once we mark a row "" it never matches
			// again). The ingest write path (cmd/ingestor/db.go ~1289) leaves
			// from_pubkey NULL when PubKey is empty; the two states are
			// semantically equivalent ("we have no pubkey for this row") and
			// all attribution call sites query `from_pubkey = ?` with a real
			// pubkey, so neither NULL nor "" matches — no UX divergence.
			var val interface{}
			if b.pk != "" {
				val = b.pk
			} else {
				val = "" // scanned, no extractable pubkey — see comment above
			}
			if _, err := txStmt.Exec(val, b.id); err != nil {
				// non-fatal; log first failure per chunk and keep going
				log.Printf("[store] from_pubkey backfill: update id=%d: %v", b.id, err)
			}
		}
		if err := tx.Commit(); err != nil {
			log.Printf("[store] from_pubkey backfill: commit: %v", err)
			return
		}
		processed += int64(len(batch))
		fromPubkeyBackfillProcessed.Store(processed)

		if len(batch) < chunkSize {
			break
		}
		if yieldDuration > 0 {
			time.Sleep(yieldDuration)
		}
	}
	log.Printf("[store] from_pubkey backfill complete: %d rows processed", processed)
}

// extractPubkeyFromAdvertJSON parses an ADVERT decoded_json blob and returns
// the pubKey field, or "" if absent/invalid. Lenient: any parse error yields
// the empty string rather than a panic.
func extractPubkeyFromAdvertJSON(s string) string {
	if s == "" {
		return ""
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return ""
	}
	if v, ok := m["pubKey"].(string); ok {
		return v
	}
	return ""
}

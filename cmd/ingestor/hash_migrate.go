package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"math"
	"runtime"
)

// Content-hash migration (#786/#787, moved here from cmd/server in #1856).
//
// Hashes written before #787 covered the full header byte, so one logical
// packet heard with different route or version bits got several hashes and
// several transmissions rows. ComputeContentHash now covers only the payload
// type bits. This pass rewrites every stale hash and folds the rows the old
// formula split apart back into one.
//
// It lives in the ingestor because it writes: the server holds a mode=ro
// handle (#1283) and its copy of this pass could never commit. The server
// reads the _async_migrations row to answer /api/stats.hashMigrationComplete.

// contentHashMigrationName is the _async_migrations row the server reads.
const contentHashMigrationName = "content_hash_payload_bits_v1"

// contentHashMigrationBatch bounds how many transmissions one read scans.
// Writes for the stale rows of a batch commit in one transaction, and the
// writer lock is released before the next batch is read.
const contentHashMigrationBatch = 5000

// The deletes for a merged duplicate, with the same child list retention uses.
var hashMergeDeleteRadios, hashMergeDeleteObservations, hashMergeDeleteTransmission = transmissionDeleteStatements("?")

// hashMergeMoveObservations moves the duplicate's observations onto the
// survivor, except those the survivor already has: idx_observations_dedup is
// (transmission_id, observer_idx, path_json with NULL read as empty), and
// moving one of those would violate it. Plain = on observer_idx is deliberate: NULLs never
// collide under the unique index, and NULL = NULL is not true here either, so
// observations without an observer always move. Args: survivor, duplicate,
// survivor.
const hashMergeMoveObservations = `UPDATE observations SET transmission_id = ?
	WHERE transmission_id = ? AND NOT EXISTS (
		SELECT 1 FROM observations s
		WHERE s.transmission_id = ?
		  AND s.observer_idx = observations.observer_idx
		  AND COALESCE(s.path_json, '') = COALESCE(observations.path_json, ''))`

// hashMergeWidenSpan gives the survivor the earlier first_seen and the later
// last_seen of the pair. Args: dup first_seen x2, dup last_seen x2, survivor.
const hashMergeWidenSpan = `UPDATE transmissions SET
	first_seen = CASE WHEN first_seen > ? THEN ? ELSE first_seen END,
	last_seen = CASE WHEN last_seen < ? THEN ? ELSE last_seen END
	WHERE id = ?`

// staleContentHash is a transmission whose stored hash is not its content hash.
type staleContentHash struct {
	id      int64
	newHash string
}

// MigrateContentHashes rewrites every stale content hash and merges the
// transmissions that collide once rewritten. It returns how many rows were
// rewritten in place and how many were merged into another row. Idempotent:
// once every hash is current it changes nothing and returns (0, 0, nil).
//
// On error, the counts cover the batches already committed.
func (s *Store) MigrateContentHashes(ctx context.Context, batchSize int) (rewritten, merged int, err error) {
	return s.migrateContentHashes(ctx, batchSize, math.MaxInt64)
}

// migrateContentHashes is MigrateContentHashes over ids up to maxID. The boot
// schedule passes the highest id at boot: this process writes current hashes,
// so a row inserted after that is never stale.
func (s *Store) migrateContentHashes(ctx context.Context, batchSize int, maxID int64) (rewritten, merged int, err error) {
	if batchSize <= 0 {
		return 0, 0, fmt.Errorf("content-hash migration: batch size %d must be positive", batchSize)
	}
	var cursor int64
	for {
		if err := ctx.Err(); err != nil {
			return rewritten, merged, fmt.Errorf("content-hash migration stopped after id %d: %w", cursor, err)
		}
		stale, last, scanned, err := s.readStaleContentHashes(ctx, cursor, maxID, batchSize)
		if err != nil {
			return rewritten, merged, fmt.Errorf("content-hash migration: read after id %d: %w", cursor, err)
		}
		if len(stale) > 0 {
			var r, m int
			err := s.WriterTx("content_hash_migrate", func(tx *sql.Tx) error {
				r, m = 0, 0
				for _, st := range stale {
					wasRewritten, wasMerged, err := rehashTransmission(tx, st)
					if err != nil {
						return fmt.Errorf("tx %d: %w", st.id, err)
					}
					if wasRewritten {
						r++
					}
					if wasMerged {
						m++
					}
				}
				return nil
			})
			if err != nil {
				return rewritten, merged, fmt.Errorf("content-hash migration: batch after id %d: %w", cursor, err)
			}
			rewritten += r
			merged += m
		}
		// A short read proves nothing is left in range.
		if scanned < batchSize {
			break
		}
		cursor = last
		// Let a queued ingest writer take the lock before the next batch.
		runtime.Gosched()
	}
	if rewritten > 0 || merged > 0 {
		log.Printf("[hash-migrate] rewrote %d content hashes, merged %d duplicate transmissions", rewritten, merged)
	}
	return rewritten, merged, nil
}

// readStaleContentHashes scans the next batch of transmissions after cursor,
// outside the writer lock, and returns the ones whose hash is stale, the last
// id scanned and how many rows were scanned. Rows without raw_hex are not
// content-hashed (companion-relayed messages) and are skipped.
func (s *Store) readStaleContentHashes(ctx context.Context, cursor, maxID int64, batchSize int) (stale []staleContentHash, last int64, scanned int, err error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, hash, raw_hex FROM transmissions WHERE id > ? AND id <= ? ORDER BY id LIMIT ?`,
		cursor, maxID, batchSize)
	if err != nil {
		return nil, 0, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var hash, rawHex string
		if err := rows.Scan(&id, &hash, &rawHex); err != nil {
			return nil, 0, 0, err
		}
		scanned++
		last = id
		if rawHex == "" {
			continue
		}
		if h := ComputeContentHash(rawHex); h != hash {
			stale = append(stale, staleContentHash{id: id, newHash: h})
		}
	}
	return stale, last, scanned, rows.Err()
}

// rehashTransmission gives one stale row its content hash, merging it into the
// row that already holds that hash if there is one. The row is re-read inside
// the transaction: retention may have deleted it since the batch was scanned,
// and an earlier row of the same batch may have taken the hash.
func rehashTransmission(tx *sql.Tx, st staleContentHash) (rewritten, merged bool, err error) {
	var curHash, firstSeen string
	var lastSeen int64
	err = tx.QueryRow(`SELECT hash, first_seen, last_seen FROM transmissions WHERE id = ?`, st.id).
		Scan(&curHash, &firstSeen, &lastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("read row: %w", err)
	}
	if curHash == st.newHash {
		return false, false, nil
	}

	var survivor int64
	err = tx.QueryRow(`SELECT id FROM transmissions WHERE hash = ?`, st.newHash).Scan(&survivor)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := tx.Exec(`UPDATE transmissions SET hash = ? WHERE id = ?`, st.newHash, st.id); err != nil {
			return false, false, fmt.Errorf("rewrite hash: %w", err)
		}
		return true, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("look up survivor: %w", err)
	}

	if _, err := tx.Exec(hashMergeMoveObservations, survivor, st.id, survivor); err != nil {
		return false, false, fmt.Errorf("move observations to %d: %w", survivor, err)
	}
	if _, err := tx.Exec(hashMergeWidenSpan, firstSeen, firstSeen, lastSeen, lastSeen, survivor); err != nil {
		return false, false, fmt.Errorf("widen survivor %d: %w", survivor, err)
	}
	for _, q := range []string{hashMergeDeleteRadios, hashMergeDeleteObservations, hashMergeDeleteTransmission} {
		if _, err := tx.Exec(q, st.id); err != nil {
			return false, false, fmt.Errorf("delete duplicate: %w", err)
		}
	}
	return false, true, nil
}

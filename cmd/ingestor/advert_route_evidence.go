package main

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/meshcore-analyzer/packetpath"
)

// The indexed existence probe avoids consuming an identity on duplicates.
const insertAdvertEvidenceSQL = `INSERT INTO advert_route_evidence(tx_id,bit)
	SELECT $1,$2 WHERE NOT EXISTS (SELECT 1 FROM advert_route_evidence WHERE tx_id=$3 AND bit=$4)`

// Match the UPSERT's exact expression-index key. NULL observer_idx cannot
// conflict, so the caller skips it. Only an uncheckpointed old row needs work.
const legacyAdvertObservationSQL = `SELECT raw_hex FROM observations
	WHERE transmission_id=$1 AND observer_idx=$2 AND COALESCE(path_json,'')=$3
	AND id > COALESCE((SELECT obs_cursor FROM advert_evidence_backfill WHERE id=1),0)`

// Caller holds writerMu, including through the subsequent observation UPSERT.
// The backfill commits evidence and obs_cursor together under that same lock.
func (s *Store) preserveLegacyAdvertObservation(tx *sql.Tx, txID, observerIdx int64, path string) error {
	if s.advertEvidenceComplete.Load() {
		return nil
	}
	var raw sql.NullString
	err := tx.Stmt(s.stmtGetLegacyAdvertObservation).QueryRow(txID, observerIdx, path).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	if bit := packetpath.AdvertRouteEvidence(raw.String); bit != 0 {
		_, err = tx.Stmt(s.stmtInsertAdvertEvidence).Exec(txID, bit, txID, bit)
	}
	return err
}

// backfillAdvertEvidence recovers only evidence still present in canonical and
// observation frames. An overwritten middle frame is unrecoverable. Persisted
// cursors and 500-row transactions make this cancellable/resumable; new traffic
// records evidence synchronously, so rows beyond either scan need no replay.
func (s *Store) backfillAdvertEvidence(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `INSERT INTO advert_evidence_backfill(id) VALUES(1) ON CONFLICT DO NOTHING`); err != nil {
		return err
	}
	for _, source := range []struct{ cursor, table, query string }{
		{"tx_cursor", "transmissions", `SELECT id,id,COALESCE(raw_hex,''),payload_type FROM transmissions WHERE id>$1 AND id<=$2 ORDER BY id LIMIT 500`},
		{"obs_cursor", "observations", `SELECT o.id,o.transmission_id,COALESCE(o.raw_hex,''),t.payload_type FROM observations o JOIN transmissions t ON t.id=o.transmission_id WHERE o.id>$1 AND o.id<=$2 ORDER BY o.id LIMIT 500`},
	} {
		// A finite horizon prevents live traffic from extending this scan.
		var upper int64
		if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(id),0) FROM `+source.table).Scan(&upper); err != nil {
			return err
		}
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			var cursor int64
			if err := db.QueryRowContext(ctx, `SELECT `+source.cursor+` FROM advert_evidence_backfill WHERE id=1`).Scan(&cursor); err != nil {
				return err
			}
			rows, err := db.QueryContext(ctx, source.query, cursor, upper)
			if err != nil {
				return err
			}
			type evidence struct {
				txID int64
				bit  uint8
			}
			batch := make([]evidence, 0, 500)
			lastID := cursor
			for rows.Next() {
				var id, txID int64
				var raw string
				var payloadType sql.NullInt64
				if err := rows.Scan(&id, &txID, &raw, &payloadType); err != nil {
					rows.Close()
					return err
				}
				lastID = id
				if bit := packetpath.AdvertRouteEvidence(raw); bit != 0 && payloadType.Valid && payloadType.Int64 == 4 {
					batch = append(batch, evidence{txID, bit})
				}
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			if lastID == cursor {
				break
			}
			// The read cursor is closed before the single writer is acquired.
			// Retention may have removed a transmission meanwhile; do not
			// resurrect evidence for it or violate its foreign key.
			err = func() error {
				writerMu.Lock()
				defer writerMu.Unlock()
				tx, err := beginWriteContext(ctx, db)
				if err != nil {
					return err
				}
				defer tx.Rollback()
				for _, item := range batch {
					if _, err := tx.ExecContext(ctx, insertAdvertEvidenceSQL+` AND EXISTS(SELECT 1 FROM transmissions WHERE id=$5)`, item.txID, item.bit, item.txID, item.bit, item.txID); err != nil {
						return err
					}
				}
				if _, err := tx.ExecContext(ctx, `UPDATE advert_evidence_backfill SET `+source.cursor+`=$1 WHERE id=1`, lastID); err != nil {
					return err
				}
				return tx.Commit()
			}()
			if err != nil {
				return fmt.Errorf("advert evidence backfill: %w", err)
			}
			// Yield between bounded batches so live ingestion gets the writer.
			timer := time.NewTimer(10 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	// RunAsyncMigration persists "done" after this returns. A crash before
	// that status write simply re-enables protection and resumes the cursors.
	s.advertEvidenceComplete.Store(true)
	return nil
}

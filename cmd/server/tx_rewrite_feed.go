package main

import (
	"database/sql"
	"log"
	"sync"
)

// tx_rewrite_feed (#2107) is the ingestor's change feed for transmissions it
// rewrote in place: a GRP_TXT it can now decrypt, a transport scope it can now
// name (cmd/ingestor/key_backfill.go). IngestNewFromDB only ever looks at
// id > maxTxID, so without this feed a rewritten row stayed stale in memory
// until the next restart.
//
// Same shape as advert_route_evidence: the table is optional (an older
// ingestor never creates it), so absence is re-probed rather than cached, and
// the server tails it with its own cursor captured before Load. A feed row
// carries only the transmission id; the current values are read from
// transmissions, so replaying a row is harmless and the newest write wins.

// txRewriteFeedPresent reports whether the ingestor has created the feed.
func (db *DB) txRewriteFeedPresent() bool {
	if db == nil || db.conn == nil {
		return false
	}
	if db.txRewriteFeedTable.Load() {
		return true
	}
	var exists int
	if err := db.conn.QueryRow(`SELECT 1 FROM sqlite_master WHERE type='table' AND name='tx_rewrite_feed'`).Scan(&exists); err != nil {
		return false
	}
	db.txRewriteFeedTable.Store(true)
	return true
}

// pollTxRewrites applies up to limit feed rows to the in-memory store. A
// transmission the store does not hold (evicted, or outside the loaded
// window) is skipped: if it is ever loaded again it is read fresh from the DB.
// A failed read leaves the cursor where it was, so the next poll retries.
func (s *PacketStore) pollTxRewrites(limit int) error {
	s.txRewriteMu.Lock()
	defer s.txRewriteMu.Unlock()
	if !s.db.txRewriteFeedPresent() {
		return nil
	}
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	scopeCol := "NULL"
	if s.db.hasScopeName {
		scopeCol = "t.scope_name"
	}
	rows, err := s.db.conn.Query(`
		SELECT f.id, f.tx_id, t.decoded_json, `+scopeCol+`
		FROM tx_rewrite_feed f LEFT JOIN transmissions t ON t.id = f.tx_id
		WHERE f.id > ? ORDER BY f.id LIMIT ?`, s.txRewriteCursor, limit)
	if err != nil {
		return err
	}
	type rewrite struct {
		id      int64
		txID    int
		decoded sql.NullString
		scope   sql.NullString
	}
	var batch []rewrite
	for rows.Next() {
		var r rewrite
		if err := rows.Scan(&r.id, &r.txID, &r.decoded, &r.scope); err != nil {
			rows.Close()
			return err
		}
		batch = append(batch, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(batch) == 0 {
		return err
	}

	channelChanged := false
	s.mu.Lock()
	for _, r := range batch {
		s.txRewriteCursor = r.id
		tx := s.byTxID[r.txID]
		if tx == nil {
			continue
		}
		if r.decoded.Valid && r.decoded.String != tx.DecodedJSON {
			tx.DecodedJSON = r.decoded.String
			// ParsedDecoded caches behind a sync.Once; reset both under the
			// write lock so the next reader parses the new JSON.
			tx.decodedOnce = sync.Once{}
			tx.parsedDecoded = nil
			s.trackedBytes += rechargeTx(tx)
			if tx.PayloadType != nil && *tx.PayloadType == PayloadGRP_TXT {
				channelChanged = true
			}
		}
		if s.db.hasScopeName && r.scope.Valid && (tx.ScopeName == nil || *tx.ScopeName != r.scope.String) {
			v := r.scope.String
			tx.ScopeName = &v
		}
	}
	s.mu.Unlock()

	if channelChanged {
		s.invalidateCachesFor(cacheInvalidation{hasChannelData: true})
	}
	return nil
}

func (s *PacketStore) refreshTxRewrites() {
	if err := s.pollTxRewrites(500); err != nil {
		log.Printf("[store] tx rewrite feed poll: %v", err)
	}
}

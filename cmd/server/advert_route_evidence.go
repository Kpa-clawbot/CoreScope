package main

import (
	"fmt"
	"log"
	"strings"
)

// The table is optional for legacy read-only databases. Re-probe while absent
// so a concurrently starting ingestor can create it without a server restart.
func (db *DB) advertEvidencePresent() bool {
	if db == nil || db.conn == nil {
		return false
	}
	if db.advertEvidenceTable.Load() {
		return true
	}
	var exists int
	if err := db.conn.QueryRow(`SELECT 1 FROM information_schema.tables WHERE table_schema=current_schema() AND table_name='advert_route_evidence'`).Scan(&exists); err != nil {
		return false
	}
	db.advertEvidenceTable.Store(true)
	return true
}

// At most two indexed evidence rows per selected transmission, fetched in
// batches rather than per-packet round trips. No raw frames enter the store.
func (db *DB) advertEvidenceForIDs(ids []int) (map[int]uint8, error) {
	result := make(map[int]uint8, len(ids))
	if len(ids) == 0 || !db.advertEvidencePresent() {
		return result, nil
	}
	for start := 0; start < len(ids); start += 500 {
		end := start + 500
		if end > len(ids) {
			end = len(ids)
		}
		args := make([]interface{}, end-start)
		for i, id := range ids[start:end] {
			args[i] = id
		}
		query := `SELECT tx_id,SUM(bit) FROM advert_route_evidence WHERE tx_id IN (` + sqlPlaceholders(len(args)) + `) GROUP BY tx_id`
		if db.advertEvidenceReadHook != nil {
			db.advertEvidenceReadHook()
		}
		rows, err := db.conn.Query(query, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id int
			var mask uint8
			if err := rows.Scan(&id, &mask); err != nil {
				rows.Close()
				return nil, err
			}
			result[id] = mask
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

// Caller holds mu. Unioning evidence is independent of cache invalidation so
// loaders and polls can invalidate once per batch, not once per transmission.
func unionAdvertEvidence(tx *StoreTx, mask uint8) bool {
	if tx == nil || tx.AdvertRouteEvidence|mask == tx.AdvertRouteEvidence {
		return false
	}
	tx.AdvertRouteEvidence |= mask
	return true
}

// Caller holds mu. The revision prevents an in-flight old computation from
// repopulating an entry after the evidence that produced it has changed.
func (s *PacketStore) invalidateAdvertEvidence() {
	s.cacheMu.Lock()
	s.advertEvidenceRevision++
	for key := range s.rfCache {
		if strings.HasPrefix(key, "relay-airtime-share|") {
			delete(s.rfCache, key)
		}
	}
	s.cacheMu.Unlock()
}

func advertTxIDs(txs []*StoreTx) []int {
	ids := make([]int, 0, len(txs))
	for _, tx := range txs {
		if tx != nil && tx.PayloadType != nil && *tx.PayloadType == PayloadADVERT {
			ids = append(ids, tx.ID)
		}
	}
	return ids
}

// Caller holds mu. A transmission evicted during the mask read is skipped;
// its persisted union will be fetched if it is loaded again.
func (s *PacketStore) mergeAdvertEvidence(masks map[int]uint8) {
	changed := false
	for id, mask := range masks {
		if unionAdvertEvidence(s.byTxID[id], mask) {
			changed = true
		}
	}
	if changed {
		s.invalidateAdvertEvidence()
	}
}

func (s *PacketStore) pollAdvertEvidence(limit int) error {
	s.advertEvidenceMu.Lock()
	defer s.advertEvidenceMu.Unlock()
	if !s.db.advertEvidencePresent() {
		return nil
	}
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	rows, err := s.db.conn.Query(`SELECT id,tx_id,bit FROM advert_route_evidence WHERE id>$1 ORDER BY id LIMIT $2`, s.advertEvidenceCursor, limit)
	if err != nil {
		return err
	}
	type event struct {
		id   int64
		txID int
		bit  uint8
	}
	var batch []event
	for rows.Next() {
		var e event
		if err := rows.Scan(&e.id, &e.txID, &e.bit); err != nil {
			rows.Close()
			return err
		}
		if e.bit != 1 && e.bit != 2 {
			rows.Close()
			return fmt.Errorf("invalid advert evidence bit %d", e.bit)
		}
		batch = append(batch, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for _, e := range batch {
		if unionAdvertEvidence(s.byTxID[e.txID], e.bit) {
			changed = true
		}
		s.advertEvidenceCursor = e.id
	}
	if changed {
		s.invalidateAdvertEvidence()
	}
	return nil
}

func (s *PacketStore) refreshAdvertEvidence() {
	if err := s.pollAdvertEvidence(500); err != nil {
		log.Printf("[store] advert evidence poll: %v", err)
	}
}

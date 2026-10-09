package main

import (
	"testing"
	"time"
)

// #2146: resolved_path is fetched from SQLite on demand. The store functions
// that build response maps under s.mu must run that SQL only after releasing
// it, so a slow pool connection never holds the store lock.

// newResolvedPathStore loads one transmission whose shorter-path observation
// carries a resolved_path (the #810 fixture), so both the single-observation
// lookup and the best-of-transmission fallback run SQL.
func newResolvedPathStore(t *testing.T) *PacketStore {
	t.Helper()
	db := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.conn.Exec(query, args...); err != nil {
			t.Fatalf("seed resolved path fixture: %v", err)
		}
	}
	now := time.Now().UTC()
	recent := now.Add(-1 * time.Hour).Format(time.RFC3339)
	recentEpoch := now.Add(-1 * time.Hour).Unix()
	exec(`INSERT INTO observers (id, name, last_seen, first_seen, packet_count) VALUES ('obs1','O1',`+db.parameter(1)+`, '2026-01-01T00:00:00Z', 100)`, recent)
	exec(`INSERT INTO observers (id, name, last_seen, first_seen, packet_count) VALUES ('obs2','O2',`+db.parameter(1)+`, '2026-01-01T00:00:00Z', 100)`, recent)
	exec(`INSERT INTO nodes (public_key, name, role, last_seen, first_seen, advert_count) VALUES ('aabbccdd11223344','R','repeater',`+db.parameter(1)+`, '2026-01-01T00:00:00Z', 1)`, recent)
	exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json) VALUES ('AABB','testhash00000001',`+db.parameter(1)+`,1,4,'{"pubKey":"aabbccdd11223344","type":"ADVERT"}')`, recent)
	exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp) VALUES (1,1,12.5,-90,'["aa","bb","cc"]',`+db.parameter(1)+`)`, recentEpoch)
	exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp, resolved_path) VALUES (1,2,8.0,-95,'["aa","bb"]',`+db.parameter(1)+`,'["aabbccdd11223344","eeff00112233aabb"]')`, recentEpoch-100)

	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	return store
}

// expectNoStoreLockDuringResolvedPathSQL fails the test when a resolved_path
// query runs while the calling goroutine holds s.mu. Background goroutines
// started by Load can hold s.mu briefly, so TryLock is retried: it keeps
// failing for the whole window only when the caller itself holds the lock.
func expectNoStoreLockDuringResolvedPathSQL(t *testing.T, s *PacketStore, caseName *string) (queries *int) {
	t.Helper()
	queries = new(int)
	s.cacheLoadHook = func(kind string) {
		if kind != "resolvedPath" {
			return
		}
		*queries++
		for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
			if s.mu.TryLock() {
				s.mu.Unlock()
				return
			}
		}
		t.Errorf("%s: resolved_path SQL ran while s.mu was held", *caseName)
	}
	return queries
}

func clearResolvedPathLRU(s *PacketStore) {
	s.lruMu.Lock()
	s.apiResolvedPathLRU = make(map[int][]*string)
	s.lruOrder = nil
	s.lruMu.Unlock()
}

func TestResolvedPathSQLRunsWithoutStoreLock(t *testing.T) {
	s := newResolvedPathStore(t)
	var caseName string
	queries := expectNoStoreLockDuringResolvedPathSQL(t, s, &caseName)

	s.mu.RLock()
	tx := s.byHash["testhash00000001"]
	s.mu.RUnlock()

	hasRP := func(m map[string]interface{}) bool { return m != nil && m["resolved_path"] != nil }
	cases := []struct {
		name string
		run  func() bool
	}{
		{"QueryPackets", func() bool {
			r := s.QueryPackets(PacketQuery{Limit: 10, ExpandObservations: true})
			return len(r.Packets) == 1 && hasRP(r.Packets[0])
		}},
		{"QueryMultiNodePackets", func() bool {
			r := s.QueryMultiNodePackets([]string{"aabbccdd11223344"}, 10, 0, "DESC", "", "")
			return len(r.Packets) == 1 && hasRP(r.Packets[0])
		}},
		{"GetTransmissionByID", func() bool { return hasRP(s.GetTransmissionByID(tx.ID)) }},
		{"GetPacketByHash", func() bool { return hasRP(s.GetPacketByHash("testhash00000001")) }},
		{"GetPacketByID", func() bool {
			// the observation carrying the resolved_path
			for _, o := range tx.Observations {
				if m := s.GetPacketByID(o.ID); hasRP(m) {
					return true
				}
			}
			return false
		}},
		{"GetObservationsForHash", func() bool {
			for _, m := range s.GetObservationsForHash("testhash00000001") {
				if hasRP(m) {
					return true
				}
			}
			return false
		}},
		{"GetNodeHealth", func() bool {
			h, err := s.GetNodeHealth("aabbccdd11223344")
			if err != nil || h == nil {
				return false
			}
			rp, _ := h["recentPackets"].([]map[string]interface{})
			return len(rp) == 1 && hasRP(rp[0])
		}},
	}
	for _, c := range cases {
		caseName = c.name
		clearResolvedPathLRU(s)
		before := *queries
		if !c.run() {
			t.Errorf("%s: want resolved_path in the response", c.name)
		}
		if *queries == before {
			t.Errorf("%s: no resolved_path SQL ran, so the lock check proved nothing", c.name)
		}
	}
}

package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func seedUnbackfilledAdvert(t *testing.T, canonical, observed string) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "legacy-advert.db")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertObserver("fixture-observer", "Fixture observer", "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO transmissions(id,hash,raw_hex,first_seen,payload_type,route_type) VALUES(1,'legacy-advert',?,'2026-01-01T00:00:00Z',4,?);
		INSERT INTO observations(id,transmission_id,observer_idx,raw_hex,path_json,timestamp) VALUES(1,1,(SELECT rowid FROM observers WHERE id='fixture-observer'),?,'[]',1)`, canonical, int(canonical[1]-'0')&3, observed); err != nil {
		s.Close()
		t.Fatal(err)
	}
	return s, path
}

func TestAdvertRouteEvidenceLegacyProtectionErrorsDoNotDropIncomingRaw(t *testing.T) {
	for _, failure := range []string{"lookup", "write"} {
		t.Run(failure, func(t *testing.T) {
			s, _ := seedUnbackfilledAdvert(t, "1100aa", "1200aa")
			defer s.Close()
			stmt := `DROP TABLE advert_evidence_backfill`
			if failure == "write" {
				stmt = `CREATE TRIGGER fail_old_evidence BEFORE INSERT ON advert_route_evidence WHEN NEW.bit=2 BEGIN SELECT RAISE(ABORT,'fixture old evidence failure'); END`
			}
			if _, err := s.db.Exec(stmt); err != nil {
				t.Fatal(err)
			}
			_, err := s.InsertTransmission(&PacketData{Hash: "legacy-advert", ObserverID: "fixture-observer", PayloadType: 4, RouteType: 1, RawHex: "1100aa", PathJSON: "[]"})
			if err != nil {
				t.Errorf("analytics preservation failure dropped incoming frame: %v", err)
			}
			var old string
			if err := s.db.QueryRow(`SELECT raw_hex FROM observations WHERE id=1`).Scan(&old); err != nil {
				t.Fatal(err)
			}
			if old != "1100aa" {
				t.Fatalf("failed analytics preservation prevented incoming raw: %q", old)
			}
		})
	}
}

func TestAdvertRouteEvidenceLegacyCoalescedPathAndIndex(t *testing.T) {
	s, _ := seedUnbackfilledAdvert(t, "1100aa", "1200aa")
	defer s.Close()
	if _, err := s.db.Exec(`UPDATE observations SET path_json=NULL WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertTransmission(&PacketData{Hash: "legacy-advert", ObserverID: "fixture-observer", PayloadType: 4, RouteType: 1, RawHex: "1100aa", PathJSON: ""}); err != nil {
		t.Fatal(err)
	}
	var count, mask int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM observations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("NULL and empty path must conflict, got %d observations", count)
	}
	if err := s.db.QueryRow(`SELECT SUM(bit) FROM advert_route_evidence`).Scan(&mask); err != nil {
		t.Fatal(err)
	}
	if mask != 3 {
		t.Fatalf("coalesced path lookup lost old evidence: mask=%d", mask)
	}
	rows, err := s.db.Query(`EXPLAIN QUERY PLAN `+legacyAdvertObservationSQL, 1, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	indexed := false
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(detail, "SEARCH observations USING INDEX idx_observations_dedup") {
			indexed = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !indexed {
		t.Fatal("legacy preservation must use the unique observation conflict index")
	}
}

func TestAdvertRouteEvidenceCompletionSkipsLookupAfterRestart(t *testing.T) {
	s, path := seedUnbackfilledAdvert(t, "1100aa", "1200aa")
	defer func() { s.Close() }()
	if err := s.RunAsyncMigration(context.Background(), "advert_route_evidence_v1", s.backfillAdvertEvidence); err != nil {
		t.Fatal(err)
	}
	s.WaitForAsyncMigrations()
	if !s.advertEvidenceComplete.Load() {
		t.Fatal("backfill did not close preservation window")
	}
	s.Close()
	var err error
	s, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if !s.advertEvidenceComplete.Load() {
		t.Fatal("restart did not restore persisted migration completion")
	}
	// Any accidental legacy lookup now fails. Completed stores must incur
	// no extra SQL on this steady-state overwrite path.
	if _, err := s.db.Exec(`DROP TABLE advert_evidence_backfill`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertTransmission(&PacketData{Hash: "legacy-advert", ObserverID: "fixture-observer", PayloadType: 4, RouteType: 1, RawHex: "1100aa", PathJSON: "[]"}); err != nil {
		t.Fatal(err)
	}
}

func BenchmarkAdvertEvidenceLegacyPreservation(b *testing.B) {
	s, err := OpenStore(filepath.Join(b.TempDir(), "legacy-preservation.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	s.WaitForAsyncMigrations()
	if err := s.UpsertObserver("fixture-observer", "Fixture observer", "", nil); err != nil {
		b.Fatal(err)
	}
	if _, err := s.InsertTransmission(&PacketData{Hash: "bench-legacy", ObserverID: "fixture-observer", PayloadType: 4, RouteType: 1, RawHex: "1100aa", PathJSON: "[]"}); err != nil {
		b.Fatal(err)
	}
	var observerIdx int64
	if err := s.db.QueryRow(`SELECT rowid FROM observers WHERE id='fixture-observer'`).Scan(&observerIdx); err != nil {
		b.Fatal(err)
	}
	for _, state := range []string{"pending", "checkpointed", "complete"} {
		b.Run(state, func(b *testing.B) {
			cursor := 0
			if state != "pending" {
				cursor = 1
			}
			if _, err := s.db.Exec(`INSERT INTO advert_evidence_backfill(id,obs_cursor) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET obs_cursor=excluded.obs_cursor`, cursor); err != nil {
				b.Fatal(err)
			}
			s.advertEvidenceComplete.Store(state == "complete")
			writerMu.Lock()
			defer writerMu.Unlock()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := s.preserveLegacyAdvertObservation(1, observerIdx, "[]"); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestAdvertRouteEvidencePreservesLegacyConflictBeforeBackfill(t *testing.T) {
	for _, tc := range []struct{ name, canonical, oldRaw, incoming string }{
		{"flood_zero_then_flood", "1100aa", "1200aa", "1100aa"},
		{"zero_flood_then_zero", "1200aa", "1100aa", "1200aa"},
		{"flood_zero_then_malformed", "1100aa", "1200aa", "11zzaa"},
		{"zero_flood_then_malformed", "1200aa", "1100aa", "12zzaa"},
	} {
		for _, restart := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/restart=%v", tc.name, restart), func(t *testing.T) {
				s, path := seedUnbackfilledAdvert(t, tc.canonical, tc.oldRaw)
				defer func() { s.Close() }()
				// Do not replay oldRaw: it only exists in the legacy row and
				// this one incoming frame will replace it before backfill.
				data := &PacketData{Hash: "legacy-advert", ObserverID: "fixture-observer", PayloadType: 4, RouteType: int(tc.canonical[1]-'0') & 3, RawHex: tc.incoming, PathJSON: "[]"}
				if _, err := s.InsertTransmission(data); err != nil {
					t.Fatal(err)
				}
				var count, id int
				var surviving string
				if err := s.db.QueryRow(`SELECT COUNT(*),MIN(id),MIN(raw_hex) FROM observations`).Scan(&count, &id, &surviving); err != nil {
					t.Fatal(err)
				}
				if count != 1 || id != 1 || surviving != tc.incoming {
					t.Fatalf("fixture did not replace same observation: count=%d id=%d raw=%s", count, id, surviving)
				}
				if restart {
					s.Close()
					var err error
					s, err = OpenStore(path)
					if err != nil {
						t.Fatal(err)
					}
				}
				if err := s.backfillAdvertEvidence(context.Background(), s.db); err != nil {
					t.Fatal(err)
				}
				var mask int
				if err := s.db.QueryRow(`SELECT COUNT(*),COALESCE(SUM(bit),0) FROM advert_route_evidence WHERE tx_id=1`).Scan(&count, &mask); err != nil {
					t.Fatal(err)
				}
				if count != 2 || mask != 3 {
					t.Fatalf("upgrade overwrote available legacy route evidence: rows=%d mask=%d, want 2/3", count, mask)
				}
			})
		}
	}
}

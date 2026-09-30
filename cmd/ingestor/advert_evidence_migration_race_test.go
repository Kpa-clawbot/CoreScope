package main

import (
	"context"
	"fmt"
	"path/filepath"
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

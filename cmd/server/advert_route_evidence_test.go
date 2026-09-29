package main

import (
	"database/sql"
	"testing"
	"time"

	"github.com/meshcore-analyzer/lora"
)

// Writer-side ingestion is exercised in cmd/ingestor. This fixture represents
// its durable output after F->Z->F (or Z->F->Z), where surviving raw frames
// alone cannot reconstruct the complete set of known route families.
func advertEvidenceFixture(t *testing.T, raw string, bits ...int) (*DB, *sql.DB) {
	t.Helper()
	path := createTestDBWithResolvedPath(t, 1, []string{"fixture-relay"})
	w, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	for _, stmt := range []string{
		`ALTER TABLE transmissions ADD COLUMN from_pubkey TEXT`,
		`CREATE TABLE advert_route_evidence (id INTEGER PRIMARY KEY AUTOINCREMENT, tx_id INTEGER NOT NULL REFERENCES transmissions(id) ON DELETE CASCADE, bit INTEGER NOT NULL CHECK (bit IN (1,2)), UNIQUE(tx_id,bit))`,
	} {
		if _, err := w.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	if _, err := w.Exec(`UPDATE transmissions SET raw_hex=?, route_type=?, first_seen=?, from_pubkey='fixture-origin'; UPDATE observations SET raw_hex=?, timestamp=?`, raw, int(raw[1]-'0')&3, now, raw, now); err != nil {
		t.Fatal(err)
	}
	for _, bit := range bits {
		if _, err := w.Exec(`INSERT INTO advert_route_evidence(tx_id,bit) VALUES(1,?)`, bit); err != nil {
			t.Fatal(err)
		}
	}
	db, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.conn.Close() })
	return db, w
}

func assertAdvertEvidenceViews(t *testing.T, s *PacketStore, want string) {
	t.Helper()
	result := s.GetRelayAirtimeShareWithWindow(TimeWindow{})
	rows := result["rows"].([]map[string]interface{})
	if len(rows) != 1 || rows[0]["advert_kind"] != want || rows[0]["count"] != 1 || result["total_count"] != 1 {
		t.Errorf("relay airtime must count one hash as %s: %v", want, result)
	}
	if len(s.packets) != 1 {
		t.Fatalf("loaded %d transmissions, want 1", len(s.packets))
	}
	tx := s.packets[0]
	// Classification must not change the established airtime formula, even
	// when evidence includes zero-hop and the packet has a resolved relay.
	wantScore := int64(lora.TimeOnAir(len(tx.RawHex)/2, defaultLoRaPreset())) * int64(s.distinctRelayCount(tx))
	if result["total_score"] != wantScore {
		t.Errorf("airtime changed: %v, want %d", result["total_score"], wantScore)
	}
	for _, obs := range tx.Observations {
		if obs.RawHex != "" {
			t.Error("route evidence must not retain raw frames on StoreObs")
		}
	}
	adverts, err := s.db.GetRecentTransmissionsForNode("fixture-origin", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(adverts) != 1 || adverts[0]["advert_kind"] != want {
		t.Errorf("node API must agree on %s: %v", want, adverts)
	}
}

func TestAdvertRouteEvidenceLoadPaths(t *testing.T) {
	for _, raw := range []string{"1100aa", "1200aa", "100102030400aa", "130102030400aa"} {
		for _, mode := range []string{"cold", "chunk", "new_transmission"} {
			t.Run(raw+"/"+mode, func(t *testing.T) {
				db, _ := advertEvidenceFixture(t, raw, 1, 2)
				s := NewPacketStore(db, &PacketStoreConfig{})
				s.useResolvedPathIndex = true
				s.initResolvedPathIndex()
				switch mode {
				case "cold":
					if err := s.Load(); err != nil {
						t.Fatal(err)
					}
				case "chunk":
					if err := s.loadChunk(time.Now().Add(-time.Hour), time.Now()); err != nil {
						t.Fatal(err)
					}
				case "new_transmission":
					s.IngestNewFromDB(0, 100)
				}
				assertAdvertEvidenceViews(t, s, "mixed")
			})
		}
	}
}

func TestAdvertRouteEvidencePollSameObservationID(t *testing.T) {
	for _, tc := range []struct {
		name, first, second, kind string
		firstBit, secondBit       int
	}{
		{"flood_zero_flood", "1100aa", "1200aa", "flood", 1, 2},
		{"zero_flood_zero", "1200aa", "1100aa", "zero_hop", 2, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, w := advertEvidenceFixture(t, tc.first, tc.firstBit)
			s := NewPacketStore(db, &PacketStoreConfig{})
			s.useResolvedPathIndex = true
			s.initResolvedPathIndex()
			s.rfCacheTTL = time.Hour
			if err := s.Load(); err != nil {
				t.Fatal(err)
			}
			initial := s.GetRelayAirtimeShareWithWindow(TimeWindow{})
			if rows := initial["rows"].([]map[string]interface{}); len(rows) != 1 || rows[0]["advert_kind"] != tc.kind {
				t.Fatalf("initial fixture classification: %v", initial)
			}
			unrelated := &cachedResult{expiresAt: time.Now().Add(time.Hour)}
			s.rfCache["unrelated-rf-result"] = unrelated
			maxObs, maxTx := db.GetMaxObservationID(), db.GetMaxTransmissionID()
			// This matches the ingestor's conflict update: neither observation
			// ID nor timestamp advances, and the final raw matches the first.
			if _, err := w.Exec(`UPDATE observations SET raw_hex=? WHERE id=1; INSERT INTO advert_route_evidence(tx_id,bit) VALUES(1,?); UPDATE observations SET raw_hex=? WHERE id=1`, tc.second, tc.secondBit, tc.first); err != nil {
				t.Fatal(err)
			}
			s.IngestNewFromDB(maxTx, 100)
			s.IngestNewObservations(maxObs, 100)
			assertAdvertEvidenceViews(t, s, "mixed")
			if s.rfCache["unrelated-rf-result"] != unrelated {
				t.Error("route evidence invalidated unrelated RF cache")
			}
			if db.GetMaxObservationID() != maxObs || db.GetMaxTransmissionID() != maxTx {
				t.Fatal("fixture unexpectedly created a new observation/transmission")
			}
			// A fresh store must recover the same classification from disk.
			restarted := NewPacketStore(db, &PacketStoreConfig{})
			restarted.useResolvedPathIndex = true
			restarted.initResolvedPathIndex()
			if err := restarted.Load(); err != nil {
				t.Fatal(err)
			}
			assertAdvertEvidenceViews(t, restarted, "mixed")
		})
	}
}

func TestAdvertRouteEvidenceMissingTableIsUnknown(t *testing.T) {
	db, w := advertEvidenceFixture(t, "1100aa")
	if _, err := w.Exec(`DROP TABLE advert_route_evidence`); err != nil {
		t.Fatal(err)
	}
	// Reopen after removing the table to exercise legacy read-only detection.
	legacy, err := OpenDB(db.path)
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.conn.Close()
	s := NewPacketStore(legacy, &PacketStoreConfig{})
	s.useResolvedPathIndex = true
	s.initResolvedPathIndex()
	if err := s.Load(); err != nil {
		t.Fatal(err)
	}
	assertAdvertEvidenceViews(t, s, "other")
	var count int
	if err := w.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='advert_route_evidence'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("read-only server created legacy evidence table")
	}
}

func TestRelayAirtimeShareAdvertDuplicateHashEvidenceUnion(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		flood, direct := RouteFlood, RouteDirect
		first := advertAirtimeTx(1, &flood, "1100aa")
		second := advertAirtimeTx(2, &direct, "1200aa")
		second.Hash = first.Hash
		packets := []*StoreTx{first, second}
		if reverse {
			packets[0], packets[1] = packets[1], packets[0]
		}
		s := newRelayAirtimeShareTestStore(packets)
		// Keep relay evidence identical so only classification changes.
		s.addToResolvedPubkeyIndex(1, []string{"fixture-relay"})
		s.addToResolvedPubkeyIndex(2, []string{"fixture-relay"})
		result := s.computeRelayAirtimeShare(TimeWindow{})
		rows := result["rows"].([]map[string]interface{})
		if len(rows) != 1 || rows[0]["advert_kind"] != "mixed" || rows[0]["count"] != 1 || result["total_count"] != 1 {
			t.Errorf("reverse=%v: duplicate hash must have one mixed bucket: %v", reverse, result)
		}
		if result["total_score"] != int64(lora.TimeOnAir(3, defaultLoRaPreset())) {
			t.Errorf("reverse=%v: duplicate hash inflated score: %v", reverse, result)
		}
	}
}

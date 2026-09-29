package main

import (
	"path/filepath"
	"testing"
)

// These frames have the same advert payload. Firmware hashes payload/type,
// excluding the route header and path, so they belong to one transmission.
func TestAdvertRouteEvidenceSurvivesObservationUpsert(t *testing.T) {
	for _, sequence := range []struct {
		name string
		raws []string
	}{
		{"flood_zero", []string{"1100aa", "1200aa"}},
		{"zero_flood", []string{"1200aa", "1100aa"}},
		{"flood_zero_flood", []string{"1100aa", "1200aa", "1100aa"}},
		{"zero_flood_zero", []string{"1200aa", "1100aa", "1200aa"}},
		{"transport_flood_zero_flood", []string{"100102030400aa", "130102030400aa", "100102030400aa"}},
		{"transport_zero_flood_zero", []string{"130102030400aa", "100102030400aa", "130102030400aa"}},
	} {
		t.Run(sequence.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "evidence.db")
			s, err := OpenStore(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { s.Close() }()
			if err := s.UpsertObserver("test-observer", "Fixture observer", "", nil); err != nil {
				t.Fatal(err)
			}
			data := &PacketData{Hash: "advert-evidence", PayloadType: 4, ObserverID: "test-observer", PathJSON: "[]", DecodedJSON: `{"type":"ADVERT"}`}
			for i, raw := range sequence.raws {
				data.RawHex = raw
				data.RouteType = int(raw[1]-'0') & 3
				// Same second, then an older receive-time: timestamps cannot be
				// used as a reliable cursor for evidence changes.
				data.Timestamp = "2026-01-02T00:00:00Z"
				if i == 2 {
					data.Timestamp = "2026-01-01T00:00:00Z"
				}
				if _, err := s.InsertTransmission(data); err != nil {
					t.Fatal(err)
				}
			}
			var count, obsID int
			var canonical, surviving string
			if err := s.db.QueryRow(`SELECT COUNT(*), MIN(id), MIN(raw_hex) FROM observations`).Scan(&count, &obsID, &surviving); err != nil {
				t.Fatal(err)
			}
			if count != 1 || obsID != 1 || surviving != sequence.raws[len(sequence.raws)-1] {
				t.Fatalf("fixture must overwrite one observation in place: count=%d id=%d raw=%s", count, obsID, surviving)
			}
			if err := s.db.QueryRow(`SELECT raw_hex FROM transmissions`).Scan(&canonical); err != nil {
				t.Fatal(err)
			}
			if canonical != sequence.raws[0] {
				t.Fatalf("canonical raw changed: %s", canonical)
			}
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='advert_route_evidence'`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Fatal("missing durable advert route evidence: expected both route families to survive same-row observation replacement")
			}
			assertMixed := func() int64 {
				t.Helper()
				var rows, mask int
				var maxID int64
				if err := s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(bit),0), COALESCE(MAX(id),0) FROM advert_route_evidence`).Scan(&rows, &mask, &maxID); err != nil {
					t.Fatal(err)
				}
				if rows != 2 || mask != 3 {
					t.Fatalf("durable evidence rows=%d mask=%d, want exactly two rows and mixed mask=3", rows, mask)
				}
				return maxID
			}
			maxID := assertMixed()
			for i := 0; i < 100; i++ {
				data.RawHex = sequence.raws[i%len(sequence.raws)]
				data.RouteType = int(data.RawHex[1]-'0') & 3
				if _, err := s.InsertTransmission(data); err != nil {
					t.Fatal(err)
				}
			}
			if assertMixed() != maxID {
				t.Fatal("duplicate traffic appended route evidence")
			}
			var sequenceID int64
			if err := s.db.QueryRow(`SELECT seq FROM sqlite_sequence WHERE name='advert_route_evidence'`).Scan(&sequenceID); err != nil {
				t.Fatal(err)
			}
			if sequenceID != maxID {
				t.Fatalf("duplicate traffic advanced evidence sequence: %d, want %d", sequenceID, maxID)
			}
			s.Close()
			s, err = OpenStore(path)
			if err != nil {
				t.Fatal(err)
			}
			assertMixed()
			// The evidence lifetime is bounded by the retained transmission.
			if _, err := s.db.Exec(`DELETE FROM observations; DELETE FROM transmissions`); err != nil {
				t.Fatal(err)
			}
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM advert_route_evidence`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("retention left %d orphan evidence rows", count)
			}
		})
	}
}

func TestAdvertRouteEvidenceRejectsUnprovenFrames(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "evidence.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, raw := range []string{"", "12", "1200", "1200a", "12zzaa", "1240aa", "1280aa", "12c0aa", "1201ffaa", "130102030401ffaa", "13zz00000000aa", "1600aa", "1100zz", "1101"} {
		data := &PacketData{Hash: "unproven-" + raw, PayloadType: 4, RouteType: 2, RawHex: raw, Timestamp: "2026-01-01T00:00:00Z", PathJSON: "[]"}
		if _, err := s.InsertTransmission(data); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='advert_route_evidence'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("missing durable advert route evidence table")
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM advert_route_evidence`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("malformed/nonzero-path/unrelated frames contributed %d evidence rows", count)
	}
}

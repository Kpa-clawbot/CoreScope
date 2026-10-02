package main

import (
	"context"
	"errors"
	"fmt"
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

func TestAdvertRouteEvidenceBackfillResumeAndLiveUnion(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "backfill.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.WaitForAsyncMigrations()
	if err := s.UpsertObserver("fixture-observer", "Fixture observer", "", nil); err != nil {
		t.Fatal(err)
	}
	// Simulate pre-upgrade history without going through the new writer.
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for id := 1; id <= 1200; id++ {
		if _, err := tx.Exec(`INSERT INTO transmissions(id,hash,raw_hex,first_seen,payload_type,route_type) VALUES(?,?, '1100aa','2026-01-01T00:00:00Z',4,1)`, id, fmt.Sprintf("history-%d", id)); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO observations(transmission_id,observer_idx,raw_hex,path_json,timestamp) VALUES(?,(SELECT rowid FROM observers WHERE id='fixture-observer'),'1200aa','[]',1)`, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.backfillAdvertEvidence(ctx, s.db); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled backfill returned %v", err)
	}
	// Abort after one committed batch; the failed batch must not move its
	// persisted cursor, so a retry can recover every remaining frame.
	if _, err := s.db.Exec(`CREATE TRIGGER fail_evidence_batch BEFORE INSERT ON advert_route_evidence WHEN NEW.tx_id=501 BEGIN SELECT RAISE(ABORT,'fixture failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.backfillAdvertEvidence(context.Background(), s.db); err == nil {
		t.Fatal("expected injected batch failure")
	}
	var cursor, count int
	if err := s.db.QueryRow(`SELECT tx_cursor FROM advert_evidence_backfill WHERE id=1`).Scan(&cursor); err != nil {
		t.Fatal(err)
	}
	if cursor != 500 {
		t.Fatalf("cursor=%d after failure, want committed batch boundary 500", cursor)
	}
	if _, err := s.db.Exec(`DROP TRIGGER fail_evidence_batch`); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.backfillAdvertEvidence(context.Background(), s.db) }()
	// Live processing can overwrite the only surviving zero-hop raw before
	// the backfill reaches it; its synchronous evidence must preserve it.
	data := &PacketData{Hash: "history-1200", ObserverID: "fixture-observer", PayloadType: 4, RouteType: 1, RawHex: "1100aa", Timestamp: "2026-01-01T00:00:00Z", PathJSON: "[]"}
	if _, err := s.InsertTransmission(data); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM advert_route_evidence`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2400 {
		t.Fatalf("got %d evidence rows, want two per history transmission", count)
	}
	var before, after int64
	if err := s.db.QueryRow(`SELECT seq FROM sqlite_sequence WHERE name='advert_route_evidence'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := s.backfillAdvertEvidence(context.Background(), s.db); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT seq FROM sqlite_sequence WHERE name='advert_route_evidence'`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("idempotent backfill changed evidence sequence %d -> %d", before, after)
	}
}

// Analytics is best effort: failure must not prevent the core observation,
// resolved relay path, relay liveness, or transmission timestamp from landing.
func TestAdvertRouteEvidenceFailureKeepsCoreIngestion(t *testing.T) {
	for _, failure := range []string{"incoming-write", "legacy-read", "legacy-write"} {
		t.Run(failure, func(t *testing.T) {
			s, err := OpenStore(filepath.Join(t.TempDir(), "write-failure.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			const relay = "bbbbbbbbbb"
			seedRelayNode(t, s, relay, "Fixture relay", "2026-01-01T00:00:00Z")
			if err := s.RefreshPrefixIndex(); err != nil {
				t.Fatal(err)
			}
			if err := s.UpsertObserver("fixture-observer", "Fixture observer", "", nil); err != nil {
				t.Fatal(err)
			}
			data := &PacketData{Hash: "write-failure", PayloadType: 4, RouteType: 1, RawHex: "1101bbaa", PathJSON: `["bb"]`, ObserverID: "fixture-observer", Timestamp: "2026-01-01T00:00:00Z"}
			if _, err := s.InsertTransmission(data); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(`DELETE FROM advert_route_evidence; UPDATE observations SET raw_hex='1200aa',resolved_path=NULL`); err != nil {
				t.Fatal(err)
			}
			stmt := `CREATE TRIGGER fail_evidence BEFORE INSERT ON advert_route_evidence WHEN NEW.bit=1 BEGIN SELECT RAISE(ABORT,'fixture evidence failure'); END`
			if failure == "legacy-read" {
				stmt = `DROP TABLE advert_evidence_backfill`
			}
			if failure == "legacy-write" {
				stmt = `CREATE TRIGGER fail_evidence BEFORE INSERT ON advert_route_evidence WHEN NEW.bit=2 BEGIN SELECT RAISE(ABORT,'fixture evidence failure'); END`
			}
			if _, err := s.db.Exec(stmt); err != nil {
				t.Fatal(err)
			}
			before := s.Stats.WriteErrors.Load()
			data.Timestamp = "2026-01-02T00:00:00Z"
			if _, err := s.InsertTransmission(data); err != nil {
				t.Errorf("analytics failure aborted core ingestion: %v", err)
			}
			var raw, resolved string
			var ts, lastSeen int64
			if err := s.db.QueryRow(`SELECT raw_hex,COALESCE(resolved_path,''),timestamp FROM observations`).Scan(&raw, &resolved, &ts); err != nil {
				t.Fatal(err)
			}
			if raw != data.RawHex || resolved != `["bbbbbbbbbb"]` || ts != 1767312000 {
				t.Errorf("core observation not updated: raw=%s resolved=%s timestamp=%d", raw, resolved, ts)
			}
			if err := s.db.QueryRow(`SELECT last_seen FROM transmissions`).Scan(&lastSeen); err != nil {
				t.Fatal(err)
			}
			if lastSeen != 1767312000 || nodeLastSeen(t, s, relay) != data.Timestamp {
				t.Errorf("core liveness not updated: tx=%d relay=%s", lastSeen, nodeLastSeen(t, s, relay))
			}
			if s.Stats.WriteErrors.Load() != before+1 {
				t.Errorf("analytics failure not counted once: before=%d after=%d", before, s.Stats.WriteErrors.Load())
			}
			var mask int
			if err := s.db.QueryRow(`SELECT COALESCE(SUM(bit),0) FROM advert_route_evidence`).Scan(&mask); err != nil {
				t.Fatal(err)
			}
			want := 1
			if failure == "incoming-write" {
				want = 2
			}
			if mask != want {
				t.Errorf("independent evidence not preserved: mask=%d want %d", mask, want)
			}
		})
	}
}

func TestAdvertRouteEvidenceConstraintsAndFeedIndex(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "constraints.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.InsertTransmission(&PacketData{Hash: "constraints", PayloadType: 4, RouteType: 1, RawHex: "1100aa", PathJSON: "[]"}); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`INSERT INTO advert_route_evidence(tx_id,bit) VALUES(1,1)`,
		`INSERT INTO advert_route_evidence(tx_id,bit) VALUES(1,3)`,
		`INSERT INTO advert_route_evidence(tx_id,bit) VALUES(999,2)`,
	} {
		if _, err := s.db.Exec(stmt); err == nil {
			t.Errorf("constraint accepted: %s", stmt)
		}
	}
	var id, parent, unused int
	var detail string
	if err := s.db.QueryRow(`EXPLAIN QUERY PLAN SELECT id,tx_id,bit FROM advert_route_evidence WHERE id>1 ORDER BY id LIMIT 500`).Scan(&id, &parent, &unused, &detail); err != nil {
		t.Fatal(err)
	}
	if detail != "SEARCH advert_route_evidence USING INTEGER PRIMARY KEY (rowid>?)" {
		t.Fatalf("feed must seek by primary key: %s", detail)
	}
	if _, err := s.db.Exec(`DELETE FROM observations; DELETE FROM transmissions`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertTransmission(&PacketData{Hash: "after-retention", PayloadType: 4, RouteType: 1, RawHex: "1100aa", PathJSON: "[]"}); err != nil {
		t.Fatal(err)
	}
	var nextID int
	if err := s.db.QueryRow(`SELECT MIN(id) FROM advert_route_evidence`).Scan(&nextID); err != nil {
		t.Fatal(err)
	}
	if nextID <= 1 {
		t.Fatalf("feed ID reused after retention: %d", nextID)
	}
}

func BenchmarkAdvertEvidenceRepeatedWrite(b *testing.B) {
	s, err := OpenStore(filepath.Join(b.TempDir(), "repeat.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	s.WaitForAsyncMigrations()
	if _, err := s.InsertTransmission(&PacketData{Hash: "benchmark-repeat", PayloadType: 4, RouteType: 1, RawHex: "1100aa", PathJSON: "[]"}); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.stmtInsertAdvertEvidence.Exec(1, 1, 1, 1); err != nil {
			b.Fatal(err)
		}
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

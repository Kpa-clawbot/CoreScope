package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// The action represents eligible ingestion at a cooperative scheduling point.
// It verifies actual native commits, the released writer mutex/pool, and a real
// foreground goroutine. The deadline only detects a leaked lock; no sleep or
// wall-clock speed threshold determines correctness.
func TestPruneYieldsAfterCommittedFullBatches(t *testing.T) {
	s := openPruneStore(t, "yield.db")
	const aged = pruneBatchTransmissions*2 + 7
	seedRetentionFanout(t, s, aged, aged)
	yields := 0
	removed, err := s.pruneOldPackets(5, func() {
		yields++
		if !writerMu.TryLock() {
			t.Error("scheduling action still holds writerMu")
			return
		}
		writerMu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		conn, err := s.db.Conn(ctx)
		if err != nil {
			t.Error("scheduling action still occupies the single DB connection", err)
			return
		}
		var remaining int
		err = conn.QueryRowContext(ctx, `SELECT count(*) FROM transmissions WHERE first_seen < '2021-01-01'`).Scan(&remaining)
		conn.Close()
		if err != nil || remaining != aged-yields*pruneBatchTransmissions {
			t.Error("scheduling action preceded a committed full batch", remaining, err)
			return
		}
		done := make(chan error, 1)
		sequence := yields
		go func() {
			_, err := s.InsertTransmission(&PacketData{
				RawHex: "0A00D69FD7A5A7475DB07337749AE61FA53A4788E976",
				Hash:   fmt.Sprint("scheduled-live-", sequence), Timestamp: time.Now().UTC().Format(time.RFC3339),
				RouteType: 2, PayloadType: 2, PathJSON: "[]", DecodedJSON: "{}",
			})
			done <- err
		}()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-ctx.Done():
			t.Error("foreground ingestion could not finish between committed batches")
		}
	})
	if err != nil || removed != aged {
		t.Fatalf("removed=%d err=%v", removed, err)
	}
	if yields != 2 {
		t.Errorf("scheduling action called %d times; want once after each of two full committed batches", yields)
	}
	if countRows(t, s, "transmissions") != 2 || countRows(t, s, "observations") != 2 {
		t.Error("foreground rows were not ingested and preserved between batches")
	}
}

func TestPruneDoesNotYieldForDisabledEmptyOrFailedWork(t *testing.T) {
	for _, days := range []int{0, -1, 5} {
		t.Run(fmt.Sprint(days), func(t *testing.T) {
			s := openPruneStore(t, "noop.db")
			yields := 0
			n, err := s.pruneOldPackets(days, func() { yields++ })
			if err != nil || n != 0 || yields != 0 {
				t.Fatalf("n=%d yields=%d err=%v", n, yields, err)
			}
		})
	}
	t.Run("failed batch", func(t *testing.T) {
		s := openPruneStore(t, "failed.db")
		seedRetentionFanout(t, s, 1, 1)
		if _, err := s.db.Exec(`CREATE TRIGGER fail_prune BEFORE DELETE ON observations BEGIN SELECT RAISE(ABORT,'blocked prune'); END`); err != nil {
			t.Fatal(err)
		}
		yields := 0
		n, err := s.pruneOldPackets(5, func() { yields++ })
		if err == nil || n != 0 || yields != 0 {
			t.Fatalf("n=%d yields=%d err=%v", n, yields, err)
		}
		if countRows(t, s, "transmissions") != 1 || countRows(t, s, "observations") != 1 {
			t.Fatal("failed batch changed rows")
		}
	})
}

func TestPrunePreservesLateFreshArrivalsBetweenBatches(t *testing.T) {
	s := openPruneStore(t, "late-arrival.db")
	seedRetentionFanout(t, s, 251, 251)
	yields := 0
	n, err := s.pruneOldPackets(5, func() {
		yields++
		// Hash 1 was removed by the previous batch. Its new reception must get
		// a fresh transmission identity and remain outside this fixed cutoff.
		if _, err := s.InsertTransmission(&PacketData{Hash: "retention-1", RawHex: "00", Timestamp: time.Now().UTC().Format(time.RFC3339), PathJSON: "[]", DecodedJSON: "{}"}); err != nil {
			t.Error(err)
		}
	})
	if err != nil || n != 251 || yields != 1 {
		t.Fatalf("n=%d yields=%d err=%v", n, yields, err)
	}
	var id int64
	if err := s.db.QueryRow(`SELECT id FROM transmissions WHERE hash='retention-1'`).Scan(&id); err != nil || id <= 251 {
		t.Fatal("late reception reused an expired identity or was lost", id, err)
	}
	var linked int
	if err := s.db.QueryRow(`SELECT count(*) FROM observations WHERE transmission_id=?`, id).Scan(&linked); err != nil || linked != 1 {
		t.Fatal("late observation lost/orphaned", linked, err)
	}
}

func TestPruneSkewedChildrenAndEmptyParentsPreservesFreshEvidence(t *testing.T) {
	s := openPruneStore(t, "skewed.db")
	seedRetentionFanout(t, s, 1, 2501)
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for id := 2; id <= 601; id++ {
		if _, err := tx.Exec(`INSERT INTO transmissions(id,raw_hex,hash,first_seen) VALUES(?,'00',?,'2020-01-01T00:00:00Z')`, id, fmt.Sprint("empty-", id)); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`INSERT INTO transmissions(id,raw_hex,hash,first_seen) VALUES(701,'00','keep-fresh','2099-01-01T00:00:00Z')`,
		`INSERT INTO observations(transmission_id,timestamp) VALUES(701,1),(701,2)`,
		`INSERT INTO advert_route_evidence(tx_id,bit) VALUES(1,1),(1,2),(601,1),(701,1),(701,2)`,
	} {
		if _, err := s.db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := s.PruneOldPackets(5)
	if err != nil || removed != 601 {
		t.Fatalf("removed=%d err=%v", removed, err)
	}
	if countRows(t, s, "transmissions") != 1 || countRows(t, s, "observations") != 2 || countRows(t, s, "advert_route_evidence") != 2 {
		t.Fatal("skewed retention lost fresh rows or left expired rows/evidence")
	}
	var evidence int
	if err := s.db.QueryRow(`SELECT count(*) FROM advert_route_evidence WHERE tx_id=701`).Scan(&evidence); err != nil || evidence != 2 {
		t.Fatal("fresh route evidence changed", err)
	}
}

func TestPruneFailureRollsBackOnlyCurrentBatch(t *testing.T) {
	s := openPruneStore(t, "rollback.db")
	seedRetentionFanout(t, s, 500, 1000)
	if _, err := s.db.Exec(`CREATE TRIGGER fail_prune BEFORE DELETE ON observations WHEN OLD.transmission_id=251 BEGIN SELECT RAISE(ABORT,'retention test failure'); END`); err != nil {
		t.Fatal(err)
	}
	removed, err := s.PruneOldPackets(5)
	if removed != 250 || err == nil || !strings.Contains(err.Error(), "retention test failure") {
		t.Fatalf("removed=%d err=%v", removed, err)
	}
	if countRows(t, s, "transmissions") != 250 || countRows(t, s, "observations") != 500 {
		t.Fatal("failed batch was only partly rolled back or earlier progress lost")
	}
	if _, err := s.db.Exec(`DROP TRIGGER fail_prune`); err != nil {
		t.Fatal(err)
	}
	if removed, err := s.PruneOldPackets(5); err != nil || removed != 250 {
		t.Fatalf("retry removed=%d err=%v", removed, err)
	}
}

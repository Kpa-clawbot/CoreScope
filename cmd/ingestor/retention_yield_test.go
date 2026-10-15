package main

import (
	"context"
	"fmt"
	"testing"
	"time"
)

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

func seedRetentionFanout(tb testing.TB, s *Store, transmissions, observations int) {
	tb.Helper()
	s.WaitForAsyncMigrations()
	tx, err := s.db.Begin()
	if err != nil {
		tb.Fatal(err)
	}
	defer tx.Rollback()
	parents, err := tx.Prepare(`INSERT INTO transmissions(id,raw_hex,hash,first_seen,last_seen) VALUES($1,'00',$2,'2020-01-01T00:00:00Z',1)`)
	if err != nil {
		tb.Fatal(err)
	}
	defer parents.Close()
	children, err := tx.Prepare(`INSERT INTO observations(transmission_id,observer_idx,path_json,timestamp) VALUES($1,$2,'[]',1)`)
	if err != nil {
		tb.Fatal(err)
	}
	defer children.Close()
	for i := 1; i <= transmissions; i++ {
		if _, err := parents.Exec(i, fmt.Sprint("retention-", i)); err != nil {
			tb.Fatal(err)
		}
	}
	for i := 0; i < observations; i++ {
		if _, err := children.Exec(i%transmissions+1, i/transmissions); err != nil {
			tb.Fatal(err)
		}
	}

	if err := tx.Commit(); err != nil {
		tb.Fatal(err)
	}
	if s.Backend() == "postgres" {
		if _, err := testAdmin(tb, s).Exec(`SELECT setval('transmissions_id_seq',$1,true)`, transmissions); err != nil {
			tb.Fatal(err)
		}
	}
}

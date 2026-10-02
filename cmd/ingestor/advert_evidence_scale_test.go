//go:build advert_evidence_scale

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// Opt-in disk benchmark: a new database with 1M transmissions and 11M
// observations (10% adverts, 11 observations per transmission, 120-byte frames).
// ADVERT_SCALE_DB retains the fixture for the server catch-up benchmark.
func TestAdvertBackfillScale(t *testing.T) {
	path := os.Getenv("ADVERT_SCALE_DB")
	if path == "" {
		path = filepath.Join(t.TempDir(), "advert-scale.db")
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("ADVERT_SCALE_DB must be a new temporary database")
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.WaitForAsyncMigrations()
	var existing int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM observations`).Scan(&existing); err != nil {
		t.Fatal(err)
	}
	if existing != 0 {
		t.Fatal("scale benchmark requires a new empty database")
	}
	started := time.Now()
	{
		raw := "1100" + strings.Repeat("ab", 118)
		for start := 1; start <= 1000000; start += 10000 {
			_, err = s.db.Exec(`WITH RECURSIVE ids(n) AS (VALUES(?) UNION ALL SELECT n+1 FROM ids WHERE n<?)
    INSERT INTO transmissions(id,hash,raw_hex,first_seen,payload_type,route_type,last_seen)
    SELECT n,'scale-'||n,?,'2026-10-01T00:00:00Z',CASE WHEN n%10=0 THEN 4 ELSE 3 END,1,1790812800 FROM ids`, start, start+9999, raw)
			if err != nil {
				t.Fatal(err)
			}
		}
		for start := 1; start <= 11000000; start += 10000 {
			_, err = s.db.Exec(`WITH RECURSIVE ids(n) AS (VALUES(?) UNION ALL SELECT n+1 FROM ids WHERE n<?)
    INSERT INTO observations(id,transmission_id,raw_hex,path_json,timestamp)
    SELECT n,((n-1)%1000000)+1,CASE WHEN ((n-1)/1000000)%2=0 THEN ? ELSE ? END,'[]',1790812800 FROM ids`, start, start+9999, raw, "12"+raw[2:])
			if err != nil {
				t.Fatal(err)
			}
			if start%1000000 == 1 {
				t.Logf("seeded observations=%d elapsed=%s", start+9999, time.Since(started))
			}
		}
		t.Logf("seed complete elapsed=%s", time.Since(started))
	}
	if _, err := s.db.Exec(`DELETE FROM advert_route_evidence;DELETE FROM advert_evidence_backfill`); err != nil {
		t.Fatal(err)
	}
	s.advertEvidenceComplete.Store(false)
	stop := make(chan struct{})
	done := make(chan struct{})
	var latencies []time.Duration
	var failures int
	go func() {
		defer close(done)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				at := time.Now()
				_, err := s.InsertTransmission(&PacketData{Hash: "scale-live", RawHex: "1100aa", PayloadType: 4, RouteType: 1, PathJSON: "[]", Timestamp: time.Now().UTC().Format(time.RFC3339)})
				latencies = append(latencies, time.Since(at))
				if err != nil {
					failures++
				}
			}
		}
	}()
	started = time.Now()
	err = s.backfillAdvertEvidence(context.Background(), s.db)
	elapsed := time.Since(started)
	close(stop)
	<-done
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	var rows, txCursor, obsCursor int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM advert_route_evidence`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT tx_cursor,obs_cursor FROM advert_evidence_backfill`).Scan(&txCursor, &obsCursor); err != nil {
		t.Fatal(err)
	}
	t.Logf("BACKFILL transmissions=1000000 observations=11000000 advert_fraction=10%% frame_bytes=120 elapsed=%s evidence_rows=%d cursors=%d/%d live_samples=%d failures=%d p50=%s p95=%s p99=%s max=%s", elapsed, rows, txCursor, obsCursor, len(latencies), failures, latencies[len(latencies)/2], latencies[len(latencies)*95/100], latencies[len(latencies)*99/100], latencies[len(latencies)-1])
	if rows < 200000 || failures != 0 {
		t.Fatal(fmt.Sprint("incomplete result", rows, failures))
	}
}

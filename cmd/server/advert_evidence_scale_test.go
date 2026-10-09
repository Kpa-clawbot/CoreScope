//go:build advert_evidence_scale

package main

import (
	"fmt"
	"os"
	"testing"
	"time"
)

// Run after the ingestor's TestAdvertBackfillScale with the same ADVERT_SCALE_DB.
// Models a server starting just before backfill: its cursor is zero and retained
// packets initially have no evidence. Uses the default 1-second poll cadence,
// with one real cached analytics request per tick and a 300K-packet retention.
func TestAdvertEvidenceCatchupScale(t *testing.T) {
	path := os.Getenv("ADVERT_SCALE_DB")
	if path == "" {
		t.Fatal("ADVERT_SCALE_DB must name the completed ingestor scale fixture")
	}
	db, err := openFixtureReader(t, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.conn.Close()
	var horizon int64
	var evidenceCount int
	if err := db.conn.QueryRow(`SELECT MAX(id),COUNT(*) FROM advert_route_evidence`).Scan(&horizon, &evidenceCount); err != nil {
		t.Fatal(err)
	}
	if evidenceCount < 200000 {
		t.Fatal("fixture must contain the completed 11M-observation evidence set")
	}
	const size = 300000
	packets := make([]*StoreTx, size)
	for i := range packets {
		pt := PayloadGRP_TXT
		if (i+1)%10 == 0 {
			pt = PayloadADVERT
		}
		tx := makeRelayAirtimeTx(i+1, pt, 120, 0, fmt.Sprintf("scale-%d", i+1))
		tx.FirstSeen = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * (7 * 24 * time.Hour / size)).Format(time.RFC3339)
		packets[i] = tx
	}
	s := newRelayAirtimeShareTestStore(packets)
	s.db = db
	s.rfCacheTTL = time.Hour
	window := TimeWindow{Since: "2026-01-07T23:00:00Z", Until: "2026-01-08T00:00:00Z", Label: "1h"}
	initial := s.GetRelayAirtimeShareWithWindow(window)
	if initial["total_count"] != 1785 {
		t.Fatalf("window fixture selected %v, want1785", initial["total_count"])
	}
	var initialAdverts int
	for _, row := range initial["rows"].([]map[string]interface{}) {
		if row["advert_kind"] == "other" {
			initialAdverts = row["count"].(int)
		}
	}
	if initialAdverts != 179 {
		t.Fatalf("initial unknown adverts=%d want179", initialAdverts)
	}
	started := time.Now()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	ticks := 0
	var computeTotal, timeMax, retainedReady time.Duration
	for s.advertEvidenceCursor < horizon {
		<-ticker.C
		ticks++
		if err := s.pollAdvertEvidence(500); err != nil {
			t.Fatal(err)
		}
		if retainedReady == 0 && s.byTxID[size].AdvertRouteEvidence == 3 {
			retainedReady = time.Since(started)
		}
		at := time.Now()
		s.GetRelayAirtimeShareWithWindow(window)
		d := time.Since(at)
		computeTotal += d
		if d > timeMax {
			timeMax = d
		}
	}
	mixed := 0
	for _, tx := range packets {
		if tx.AdvertRouteEvidence == 3 {
			mixed++
		}
	}
	final := s.GetRelayAirtimeShareWithWindow(window)
	var finalAdverts int
	for _, row := range final["rows"].([]map[string]interface{}) {
		if row["advert_kind"] == "mixed" {
			finalAdverts = row["count"].(int)
		}
	}
	if finalAdverts != 179 || final["total_count"] != 1785 {
		t.Fatalf("final classification/window changed: %v", final)
	}
	t.Logf("eligible=1785 eligible_adverts=179 final_cursor=%d horizon=%d", s.advertEvidenceCursor, horizon)
	t.Logf("CATCHUP evidence_rows=%d retained_packets=%d adverts=30000 poll_rows=500 cadence=1s elapsed=%s ticks=%d cache_misses=%d revisions=%d request_total=%s request_max=%s retained_ready=%s mixed_retained=%d", evidenceCount, size, time.Since(started), ticks, s.cacheMisses, s.advertEvidenceRevision, computeTotal, timeMax, retainedReady, mixed)
	if mixed != 30000 {
		t.Fatalf("lost retained evidence: mixed=%d want30000", mixed)
	}
}

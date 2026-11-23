package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

// nodeAnalyticsSignal is the part of the analytics response these tests read.
type nodeAnalyticsSignal struct {
	HeardBy       []HeardByObserver `json:"heardBy"`
	SnrTrend      []SnrTrendEntry   `json:"snrTrend"`
	ComputedStats ComputedNodeStats `json:"computedStats"`
}

// heardByTestServer seeds two repeaters and direct-heard evidence, then
// publishes the direct-heard snapshot the analytics endpoint reads.
func heardByTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv, router := setupTestServer(t)
	srv.db.conn.Exec(`INSERT INTO nodes (public_key, name, role) VALUES
		('c7d1000000000000000000000000000000000000000000000000000000000001', 'TwoByte', 'repeater'),
		('e5a9000000000000000000000000000000000000000000000000000000000002', 'OneByteOnly', 'repeater')`)
	srv.store.InvalidateNodeCache()

	at := time.Now().UTC().Add(-2 * time.Hour)
	add := func(path, observer string, snr float64) {
		tx := dhTx(RouteFlood, PayloadTXT_MSG, "")
		v := snr
		tx.Observations = []*StoreObs{{ObserverID: observer, ObserverName: observer + "-name", PathJSON: path, SNR: &v, Timestamp: at.Format(time.RFC3339)}}
		at = at.Add(time.Minute)
		srv.store.packets = append(srv.store.packets, tx)
	}
	srv.store.mu.Lock()
	add(`["C7D1"]`, "obsA", 5)
	add(`["C7D1"]`, "obsA", 6)
	add(`["C7D1"]`, "obsA", 7)
	add(`["C7"]`, "obsB", 20)          // 1-byte hop: not proof of who sent it
	add(`["C7D1","99AA"]`, "obsC", 15) // relayed onward: obsC heard 99AA
	add(`["E5"]`, "obsA", 12)
	srv.store.mu.Unlock()
	srv.store.publishDirectHeard(srv.store.computeDirectHeard())

	ts := httptest.NewServer(router)
	t.Cleanup(ts.Close)
	return ts
}

func getNodeAnalyticsSignal(t *testing.T, ts *httptest.Server, pk string) nodeAnalyticsSignal {
	t.Helper()
	res, err := ts.Client().Get(ts.URL + "/api/nodes/" + pk + "/analytics?days=7")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status %d", res.StatusCode)
	}
	var body nodeAnalyticsSignal
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body
}

func TestNodeAnalytics_HeardByDirectOnly(t *testing.T) {
	ts := heardByTestServer(t)

	body := getNodeAnalyticsSignal(t, ts, "c7d1000000000000000000000000000000000000000000000000000000000001")
	if len(body.HeardBy) != 1 || body.HeardBy[0].ObserverID != "obsA" || *body.HeardBy[0].MedianSNR != 6 || body.HeardBy[0].Count != 3 {
		t.Fatalf("heardBy = %+v, want only obsA, 3 receptions, median 6", body.HeardBy)
	}
	if body.HeardBy[0].ObserverName != "obsA-name" {
		t.Fatalf("observer name = %q", body.HeardBy[0].ObserverName)
	}
	if s := body.ComputedStats; s.UniqueObservers != 1 || s.SnrMean != 6 {
		t.Fatalf("uniqueObservers %d, snrMean %v; want 1, 6", s.UniqueObservers, s.SnrMean)
	}
	if len(body.SnrTrend) != 3 {
		t.Fatalf("snrTrend has %d entries, want 3", len(body.SnrTrend))
	}
	for _, e := range body.SnrTrend {
		if e.ObserverID != "obsA" {
			t.Fatalf("snrTrend entry from %v, want only obsA", e.ObserverID)
		}
	}

	body = getNodeAnalyticsSignal(t, ts, "e5a9000000000000000000000000000000000000000000000000000000000002")
	if len(body.HeardBy) != 0 || len(body.SnrTrend) != 0 {
		t.Fatalf("1-byte only: heardBy %+v, snrTrend %d entries; want both empty", body.HeardBy, len(body.SnrTrend))
	}
	if s := body.ComputedStats; s.UniqueObservers != 0 || s.SignalGrade != "D" || s.SnrMean != 0 {
		t.Fatalf("1-byte only: stats %+v; want 0 observers, grade D, snrMean 0", s)
	}
}

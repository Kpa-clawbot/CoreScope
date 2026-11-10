package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// #2270 / #2271: the latest /neighbors report per observer.

const (
	nbKeyA = "aa00000000000000000000000000000000000000000000000000000000000001"
	nbKeyB = "bb00000000000000000000000000000000000000000000000000000000000002"
)

func neighborsMsg(t *testing.T, payload string) map[string]interface{} {
	t.Helper()
	var msg map[string]interface{}
	if err := json.Unmarshal([]byte(payload), &msg); err != nil {
		t.Fatal(err)
	}
	return msg
}

func TestSanitizeNeighbors(t *testing.T) {
	msg := neighborsMsg(t, `{"neighbors":[
		{"pubkey":"`+strings.ToUpper(nbKeyA)+`","snr":7.256,"heard_secs_ago":42,"status":"responded","scopes":"#cz,#cz-ulk"},
		{"pubkey":"not-a-key","snr":1},
		{"pubkey":"`+nbKeyB+`","snr":"NaN","heard_secs_ago":-5,"status":"<img src=x>","scopes":"`+strings.Repeat("x", 400)+`"},
		"garbage"]}`)
	got := sanitizeNeighbors(msg["neighbors"].([]interface{}))
	if len(got) != 2 {
		t.Fatalf("kept %d entries, want 2: %+v", len(got), got)
	}
	a := got[0]
	if a.Pubkey != nbKeyA || a.SNR == nil || *a.SNR != 7.26 || a.HeardSecsAgo == nil || *a.HeardSecsAgo != 42 ||
		a.Status != "responded" || a.Scopes != "#cz,#cz-ulk" {
		t.Errorf("first entry = %+v", a)
	}
	b := got[1]
	if b.SNR != nil || b.HeardSecsAgo != nil || b.Status != "" || len(b.Scopes) > 256 {
		t.Errorf("out-of-range fields kept: %+v (scopes %d chars)", b, len(b.Scopes))
	}
}

func TestSanitizeNeighborsCap(t *testing.T) {
	raw := make([]interface{}, maxStoredNeighbors+50)
	for i := range raw {
		raw[i] = map[string]interface{}{"pubkey": nbKeyA}
	}
	if n := len(sanitizeNeighbors(raw)); n != maxStoredNeighbors {
		t.Errorf("kept %d, want %d", n, maxStoredNeighbors)
	}
}

type storedReport struct {
	receivedAt, reportedAt string
	count                  int
	neighbors              []StoredNeighbor
}

func loadReport(t *testing.T, s *Store, observer string) *storedReport {
	t.Helper()
	var r storedReport
	var reported *string
	var body string
	err := s.db.QueryRow(`SELECT received_at, reported_at, neighbor_count, neighbors_json
		FROM observer_neighbor_reports WHERE observer_id = ?`, observer).Scan(&r.receivedAt, &reported, &r.count, &body)
	if err != nil {
		return nil
	}
	if reported != nil {
		r.reportedAt = *reported
	}
	if err := json.Unmarshal([]byte(body), &r.neighbors); err != nil {
		t.Fatal(err)
	}
	return &r
}

func TestUpsertNeighborReportLiveReplaces(t *testing.T) {
	s := newTestStore(t)
	t0 := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	first := neighborsMsg(t, `{"timestamp":"2026-10-25T09:59:58Z","total_neighbors":2,
		"neighbors":[{"pubkey":"`+nbKeyA+`"},{"pubkey":"`+nbKeyB+`"}]}`)
	if err := s.UpsertNeighborReport("OBS1", first, false, t0); err != nil {
		t.Fatal(err)
	}
	second := neighborsMsg(t, `{"timestamp":"2026-10-25T16:00:00Z","total_neighbors":40,"neighbors":[{"pubkey":"`+nbKeyB+`"}]}`)
	if err := s.UpsertNeighborReport("OBS1", second, false, t0.Add(6*time.Hour)); err != nil {
		t.Fatal(err)
	}
	r := loadReport(t, s, "OBS1")
	if r == nil || r.receivedAt != "2026-10-25T16:00:00Z" || r.count != 40 || len(r.neighbors) != 1 || r.neighbors[0].Pubkey != nbKeyB {
		t.Errorf("after second report: %+v", r)
	}
}

// total_neighbors below what arrived is not believed.
func TestUpsertNeighborReportCountNotBelowList(t *testing.T) {
	s := newTestStore(t)
	msg := neighborsMsg(t, `{"total_neighbors":0,"neighbors":[{"pubkey":"`+nbKeyA+`"},{"pubkey":"`+nbKeyB+`"}]}`)
	if err := s.UpsertNeighborReport("OBS1", msg, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	if r := loadReport(t, s, "OBS1"); r == nil || r.count != 2 {
		t.Errorf("count = %+v, want 2", r)
	}
}

// A retained replay fills a missing row, dated by the report, and never
// overwrites a live one.
func TestUpsertNeighborReportRetained(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	old := neighborsMsg(t, `{"timestamp":"2026-10-20T08:00:00.123Z","neighbors":[{"pubkey":"`+nbKeyA+`"}]}`)
	if err := s.UpsertNeighborReport("OBS1", old, true, now); err != nil {
		t.Fatal(err)
	}
	if r := loadReport(t, s, "OBS1"); r == nil || r.receivedAt != "2026-10-20T08:00:00Z" {
		t.Errorf("retained into empty: %+v, want dated 2026-10-20T08:00:00Z", r)
	}

	live := neighborsMsg(t, `{"timestamp":"2026-10-25T11:59:59Z","neighbors":[{"pubkey":"`+nbKeyB+`"}]}`)
	if err := s.UpsertNeighborReport("OBS2", live, false, now); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertNeighborReport("OBS2", old, true, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if r := loadReport(t, s, "OBS2"); r == nil || r.receivedAt != "2026-10-25T12:00:00Z" || r.neighbors[0].Pubkey != nbKeyB {
		t.Errorf("retained replaced a live report: %+v", r)
	}

	undated := neighborsMsg(t, `{"neighbors":[{"pubkey":"`+nbKeyA+`"}]}`)
	if err := s.UpsertNeighborReport("OBS3", undated, true, now); err != nil {
		t.Fatal(err)
	}
	if r := loadReport(t, s, "OBS3"); r != nil {
		t.Errorf("undated retained report stored: %+v", r)
	}
}

// End to end through the MQTT handler; the scope side of the report (#1865)
// keeps working.
func TestNeighborsTopicStoresReport(t *testing.T) {
	s := newTestStore(t)
	msg := &mockMessage{topic: "meshcore/TST/OBS9/neighbors",
		payload: []byte(`{"timestamp":"2026-10-25T10:00:00Z","origin":"o","origin_id":"OBS9",
			"total_neighbors":1,"queried_neighbors":1,"self":{"scopes":"#tst","default_scope":"*"},
			"neighbors":[{"pubkey":"` + nbKeyA + `","snr":5.5,"heard_secs_ago":30,"status":"responded","scopes":"#tst"}]}`)}
	handleMessage(s, "test", MQTTSource{Name: "test"}, msg, nil, nil, &Config{})
	r := loadReport(t, s, "OBS9")
	if r == nil || r.count != 1 || len(r.neighbors) != 1 || r.neighbors[0].Status != "responded" {
		t.Fatalf("stored report = %+v", r)
	}
	var selfScopes, def string
	if err := s.db.QueryRow(`SELECT self_scopes, self_default_scope FROM observer_neighbor_reports WHERE observer_id='OBS9'`).
		Scan(&selfScopes, &def); err != nil || selfScopes != "#tst" || def != "*" {
		t.Errorf("self = %q %q (%v)", selfScopes, def, err)
	}
}

// #2330: a bridge republishes retained reports with RETAIN cleared, so an
// old payload is a replay whatever the flag says. It never refreshes or
// replaces a live row, and into an empty row it goes dated by itself.
func TestUpsertNeighborReportOldUnretainedIsReplay(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	old := neighborsMsg(t, `{"timestamp":"2026-10-20T08:00:00Z","neighbors":[{"pubkey":"`+nbKeyA+`"}]}`)

	if err := s.UpsertNeighborReport("OBS1", old, false, now); err != nil {
		t.Fatal(err)
	}
	if r := loadReport(t, s, "OBS1"); r == nil || r.receivedAt != "2026-10-20T08:00:00Z" {
		t.Errorf("old report into empty row: %+v, want dated 2026-10-20T08:00:00Z", r)
	}

	live := neighborsMsg(t, `{"timestamp":"2026-10-25T11:59:59Z","neighbors":[{"pubkey":"`+nbKeyB+`"}]}`)
	if err := s.UpsertNeighborReport("OBS2", live, false, now); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertNeighborReport("OBS2", old, false, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if r := loadReport(t, s, "OBS2"); r == nil || r.receivedAt != "2026-10-25T12:00:00Z" || r.neighbors[0].Pubkey != nbKeyB {
		t.Errorf("old unretained report replaced a live one: %+v", r)
	}
}

// #2330: a replay newer than the stored row still replaces it; one older
// than the stored row does not.
func TestUpsertNeighborReportReplayKeepsNewest(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	older := neighborsMsg(t, `{"timestamp":"2026-10-18T08:00:00Z","neighbors":[{"pubkey":"`+nbKeyA+`"}]}`)
	newer := neighborsMsg(t, `{"timestamp":"2026-10-20T08:00:00Z","neighbors":[{"pubkey":"`+nbKeyB+`"}]}`)

	for _, m := range []map[string]interface{}{older, newer, older} {
		if err := s.UpsertNeighborReport("OBS1", m, true, now); err != nil {
			t.Fatal(err)
		}
	}
	if r := loadReport(t, s, "OBS1"); r == nil || r.receivedAt != "2026-10-20T08:00:00Z" || r.neighbors[0].Pubkey != nbKeyB {
		t.Errorf("after older, newer, older replay: %+v, want the 2026-10-20 report", r)
	}
}

// #2330: a retained report with a zone-less timestamp (#1478) is stored and
// keeps its own age instead of being dropped.
func TestUpsertNeighborReportRetainedNaiveTimestamp(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	for id, ts := range map[string]string{"OBS1": "2026-10-20T08:00:00", "OBS2": "2026-10-20T08:00:00.123456"} {
		msg := neighborsMsg(t, `{"timestamp":"`+ts+`","neighbors":[{"pubkey":"`+nbKeyA+`"}]}`)
		if err := s.UpsertNeighborReport(id, msg, true, now); err != nil {
			t.Fatal(err)
		}
		if r := loadReport(t, s, id); r == nil || r.receivedAt != "2026-10-20T08:00:00Z" {
			t.Errorf("%s: retained %q stored as %+v, want dated 2026-10-20T08:00:00Z", id, ts, r)
		}
	}
}

// #2330 end to end: an old report arriving without RETAIN (as through a
// bridge) does not refresh the observer's report.
func TestNeighborsTopicOldReportDoesNotRefresh(t *testing.T) {
	s := newTestStore(t)
	payload := func(ts string) []byte {
		return []byte(`{"timestamp":"` + ts + `","neighbors":[{"pubkey":"` + nbKeyA + `"}]}`)
	}
	live := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	handleMessage(s, "test", MQTTSource{Name: "test"},
		&mockMessage{topic: "meshcore/TST/OBS9/neighbors", payload: payload(live)}, nil, nil, &Config{})
	before := loadReport(t, s, "OBS9")
	if before == nil {
		t.Fatal("live report not stored")
	}
	handleMessage(s, "test", MQTTSource{Name: "test"},
		&mockMessage{topic: "meshcore/TST/OBS9/neighbors", payload: payload("2026-01-01T00:00:00Z")}, nil, nil, &Config{})
	if after := loadReport(t, s, "OBS9"); after == nil || after.receivedAt != before.receivedAt || after.reportedAt != live {
		t.Errorf("old report changed the row: before %+v, after %+v", before, after)
	}
}

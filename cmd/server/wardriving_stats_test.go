package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

// TestHandleWardrivingStats covers the three sections item-by-item:
// activity (total + top senders), entry points (path[0] tally), and
// observer coverage (joined against the static iataCoords table).
func TestHandleWardrivingStats(t *testing.T) {
	srv, router := setupTestServer(t)
	if _, err := srv.db.conn.Exec(`DELETE FROM transmissions`); err != nil {
		t.Fatalf("clear transmissions: %v", err)
	}
	if _, err := srv.db.conn.Exec(`DELETE FROM observations`); err != nil {
		t.Fatalf("clear observations: %v", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)

	// Two senders, three messages: Alice sends 2, Bob sends 1.
	insertTx := func(hash, decodedJSON string) int64 {
		res, err := srv.db.conn.Exec(
			`INSERT INTO transmissions (raw_hex,hash,first_seen,route_type,payload_type,channel_hash,decoded_json) VALUES (?,?,?,1,5,'#wardriving',?)`,
			"aa", hash, now, decodedJSON,
		)
		if err != nil {
			t.Fatalf("insert tx %s: %v", hash, err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	tx1 := insertTx("wd1", `{"sender":"Alice","text":"Alice: MM:abc123"}`)
	tx2 := insertTx("wd2", `{"sender":"Alice","text":"Alice: MM:def456"}`)
	tx3 := insertTx("wd3", `{"sender":"Bob","text":"Bob: MM:ghi789"}`)

	// A non-wardriving channel message must never leak into the results.
	insertTx2 := func(hash, channel string) {
		if _, err := srv.db.conn.Exec(
			`INSERT INTO transmissions (raw_hex,hash,first_seen,route_type,payload_type,channel_hash,decoded_json) VALUES (?,?,?,1,5,?,?)`,
			"aa", hash, now, channel, `{"sender":"Eve","text":"Eve: hi"}`,
		); err != nil {
			t.Fatalf("insert other-channel tx: %v", err)
		}
	}
	insertTx2("other1", "#test")

	// Seed observers: one with a known IATA (coordinates resolvable), one without.
	// Schema is v3 (observations.observer_idx references observers.rowid, NOT
	// the TEXT id column) — capture each insert's rowid via LastInsertId.
	insertObserver := func(id, name, iata string) int64 {
		res, err := srv.db.conn.Exec(`INSERT INTO observers (id, name, iata) VALUES (?,?,?)`, id, name, iata)
		if err != nil {
			t.Fatalf("insert observer %s: %v", id, err)
		}
		rowid, _ := res.LastInsertId()
		return rowid
	}
	seaIdx := insertObserver("obsSEA", "SeattleObs", "SEA")
	zzzIdx := insertObserver("obsXXX", "UnknownObs", "ZZZ")

	insertObs := func(txID int64, observerIdx int64, pathJSON string) {
		if _, err := srv.db.conn.Exec(
			`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp) VALUES (?,?,1.0,-90,?,?)`,
			txID, observerIdx, pathJSON, time.Now().Unix(),
		); err != nil {
			t.Fatalf("insert observation: %v", err)
		}
	}
	// tx1: two observations, both via entry prefix "AAAA", one from each observer.
	insertObs(tx1, seaIdx, `["AAAA","1111"]`)
	insertObs(tx1, zzzIdx, `["AAAA","2222"]`)
	// tx2: entry prefix "BBBB", heard only by SEA.
	insertObs(tx2, seaIdx, `["BBBB"]`)
	// tx3: entry prefix "AAAA" again (same prefix as tx1 — tallies together), heard by SEA.
	insertObs(tx3, seaIdx, `["AAAA","3333"]`)

	req := httptest.NewRequest("GET", "/api/analytics/wardriving?window=24h", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp WardrivingStatsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}

	if resp.Channel != "#wardriving" {
		t.Errorf("Channel = %q, want #wardriving", resp.Channel)
	}
	if resp.TotalMessages != 3 {
		t.Errorf("TotalMessages = %d, want 3 (the #test message must not count)", resp.TotalMessages)
	}

	// Top senders: Alice (2) before Bob (1).
	if len(resp.TopSenders) != 2 {
		t.Fatalf("TopSenders = %+v, want 2 entries", resp.TopSenders)
	}
	if resp.TopSenders[0].Sender != "Alice" || resp.TopSenders[0].Count != 2 {
		t.Errorf("TopSenders[0] = %+v, want {Alice 2}", resp.TopSenders[0])
	}
	if resp.TopSenders[1].Sender != "Bob" || resp.TopSenders[1].Count != 1 {
		t.Errorf("TopSenders[1] = %+v, want {Bob 1}", resp.TopSenders[1])
	}

	// Entry points: "AAAA" appears in 3 observations (tx1 x2 + tx3 x1) across
	// 2 distinct messages (tx1, tx3); "BBBB" appears in 1 observation, 1 message.
	if len(resp.EntryPoints) != 2 {
		t.Fatalf("EntryPoints = %+v, want 2 prefixes", resp.EntryPoints)
	}
	if resp.EntryPoints[0].Prefix != "AAAA" || resp.EntryPoints[0].ObservationCount != 3 || resp.EntryPoints[0].MessageCount != 2 {
		t.Errorf("EntryPoints[0] = %+v, want {AAAA obs=3 msgs=2}", resp.EntryPoints[0])
	}
	if resp.EntryPoints[1].Prefix != "BBBB" || resp.EntryPoints[1].ObservationCount != 1 || resp.EntryPoints[1].MessageCount != 1 {
		t.Errorf("EntryPoints[1] = %+v, want {BBBB obs=1 msgs=1}", resp.EntryPoints[1])
	}

	// Observer coverage: SEA heard 3 observations across all 3 messages;
	// ZZZ heard 1 observation from 1 message. SEA's IATA resolves to real
	// coordinates; ZZZ's unknown IATA leaves Lat/Lon nil.
	if len(resp.Observers) != 2 {
		t.Fatalf("Observers = %+v, want 2 entries", resp.Observers)
	}
	sea := resp.Observers[0]
	if sea.ObserverName != "SeattleObs" || sea.ObservationCount != 3 || sea.MessageCount != 3 {
		t.Errorf("Observers[0] = %+v, want {SeattleObs obs=3 msgs=3}", sea)
	}
	if sea.Lat == nil || sea.Lon == nil {
		t.Error("SEA observer should resolve to known coordinates via iataCoords")
	} else if *sea.Lat < 47 || *sea.Lat > 48 {
		t.Errorf("SEA lat = %v, want ~47.45 (Seattle)", *sea.Lat)
	}
	zzz := resp.Observers[1]
	if zzz.ObserverName != "UnknownObs" || zzz.ObservationCount != 1 {
		t.Errorf("Observers[1] = %+v, want {UnknownObs obs=1}", zzz)
	}
	if zzz.Lat != nil || zzz.Lon != nil {
		t.Errorf("ZZZ has an unrecognized IATA — Lat/Lon should stay nil, got lat=%v lon=%v", zzz.Lat, zzz.Lon)
	}

	// Time series should sum back to TotalMessages.
	sum := 0
	for _, pt := range resp.TimeSeries {
		sum += pt.Count
	}
	if sum != 3 {
		t.Errorf("TimeSeries sums to %d, want 3", sum)
	}
}

// TestHandleWardrivingStats_InvalidWindow mirrors the existing scope-stats
// window validation.
func TestHandleWardrivingStats_InvalidWindow(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/analytics/wardriving?window=bogus", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Fatalf("status=%d, want 400 for an invalid window", w.Code)
	}
}

// TestHandleWardrivingStats_EmptyChannel confirms an empty/quiet
// #wardriving channel returns well-formed empty slices, not nulls or an
// error — the frontend always expects arrays it can iterate.
func TestHandleWardrivingStats_EmptyChannel(t *testing.T) {
	srv, router := setupTestServer(t)
	if _, err := srv.db.conn.Exec(`DELETE FROM transmissions`); err != nil {
		t.Fatalf("clear transmissions: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/analytics/wardriving?window=24h", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp WardrivingStatsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.TotalMessages != 0 {
		t.Errorf("TotalMessages = %d, want 0", resp.TotalMessages)
	}
	if resp.TopSenders == nil || resp.EntryPoints == nil || resp.Observers == nil || resp.TimeSeries == nil {
		t.Errorf("expected empty (non-nil) slices, got TopSenders=%v EntryPoints=%v Observers=%v TimeSeries=%v",
			resp.TopSenders, resp.EntryPoints, resp.Observers, resp.TimeSeries)
	}
}

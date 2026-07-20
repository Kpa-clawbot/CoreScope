package main

import (
	"encoding/base64"
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

	insertObs := func(txID int64, observerIdx int64, pathJSON string, snr, rssi float64) {
		if _, err := srv.db.conn.Exec(
			`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp) VALUES (?,?,?,?,?,?)`,
			txID, observerIdx, snr, rssi, pathJSON, time.Now().Unix(),
		); err != nil {
			t.Fatalf("insert observation: %v", err)
		}
	}
	// tx1: two observations, both via entry prefix "AAAA", one from each observer.
	// Signal values are distinct so the avg-SNR/avg-RSSI math is verifiable:
	// avg SNR = (2+4+6+8)/4 = 5.0, avg RSSI = (-80-100-70-60)/4 = -77.5.
	insertObs(tx1, seaIdx, `["AAAA","1111"]`, 2.0, -80)
	insertObs(tx1, zzzIdx, `["AAAA","2222"]`, 4.0, -100)
	// tx2: entry prefix "BBBB", heard only by SEA.
	insertObs(tx2, seaIdx, `["BBBB"]`, 6.0, -70)
	// tx3: entry prefix "AAAA" again (same prefix as tx1 — tallies together), heard by SEA.
	insertObs(tx3, seaIdx, `["AAAA","3333"]`, 8.0, -60)

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

	// Signal quality: all 4 observations land in the same hourly bucket
	// (inserted back-to-back "now"), so there's exactly one signal point
	// averaging all 4 readings: avg SNR = 5.0, avg RSSI = -77.5.
	if len(resp.SignalTimeSeries) != 1 {
		t.Fatalf("SignalTimeSeries = %+v, want 1 bucket", resp.SignalTimeSeries)
	}
	sig := resp.SignalTimeSeries[0]
	if sig.ObservationCount != 4 {
		t.Errorf("SignalTimeSeries[0].ObservationCount = %d, want 4", sig.ObservationCount)
	}
	if sig.AvgSNR != 5.0 {
		t.Errorf("SignalTimeSeries[0].AvgSNR = %v, want 5.0", sig.AvgSNR)
	}
	if sig.AvgRSSI != -77.5 {
		t.Errorf("SignalTimeSeries[0].AvgRSSI = %v, want -77.5", sig.AvgRSSI)
	}
	if resp.AvgSNR == nil || *resp.AvgSNR != 5.0 {
		t.Errorf("AvgSNR = %v, want 5.0", resp.AvgSNR)
	}
	if resp.AvgRSSI == nil || *resp.AvgRSSI != -77.5 {
		t.Errorf("AvgRSSI = %v, want -77.5", resp.AvgRSSI)
	}
}

// TestHandleWardrivingStats_Sessions covers session/run grouping:
// consecutive messages within wardrivingSessionGapMinutes (15) from the
// same sender merge into one session; a bigger gap starts a new one.
func TestHandleWardrivingStats_Sessions(t *testing.T) {
	srv, router := setupTestServer(t)
	if _, err := srv.db.conn.Exec(`DELETE FROM transmissions`); err != nil {
		t.Fatalf("clear transmissions: %v", err)
	}
	if _, err := srv.db.conn.Exec(`DELETE FROM observations`); err != nil {
		t.Fatalf("clear observations: %v", err)
	}

	base := time.Now().UTC().Add(-2 * time.Hour)
	t0 := base                       // Alice, session A, msg 1
	t1 := base.Add(5 * time.Minute)  // Alice, session A, msg 2 (5min gap — same session)
	t2 := base.Add(40 * time.Minute) // Alice, session B, msg 3 (35min gap from t1 — new session)
	t3 := base.Add(10 * time.Minute) // Bob, single-message session

	insertTx := func(hash, sender string, ts time.Time) int64 {
		res, err := srv.db.conn.Exec(
			`INSERT INTO transmissions (raw_hex,hash,first_seen,route_type,payload_type,channel_hash,decoded_json) VALUES (?,?,?,1,5,'#wardriving',?)`,
			"aa", hash, ts.Format(time.RFC3339), `{"sender":"`+sender+`","text":"`+sender+`: MM:x"}`,
		)
		if err != nil {
			t.Fatalf("insert tx %s: %v", hash, err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	tx0 := insertTx("s1", "Alice", t0)
	tx1 := insertTx("s2", "Alice", t1)
	tx2 := insertTx("s3", "Alice", t2)
	tx3 := insertTx("s4", "Bob", t3)

	insertObserver := func(id, name string) int64 {
		res, err := srv.db.conn.Exec(`INSERT INTO observers (id, name) VALUES (?,?)`, id, name)
		if err != nil {
			t.Fatalf("insert observer %s: %v", id, err)
		}
		rowid, _ := res.LastInsertId()
		return rowid
	}
	o1 := insertObserver("obsO1", "ObsOne")
	o2 := insertObserver("obsO2", "ObsTwo")

	insertObs := func(txID, observerIdx int64, pathJSON string) {
		if _, err := srv.db.conn.Exec(
			`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp) VALUES (?,?,1.0,-90,?,?)`,
			txID, observerIdx, pathJSON, time.Now().Unix(),
		); err != nil {
			t.Fatalf("insert observation: %v", err)
		}
	}
	insertObs(tx0, o1, `["EEEE"]`)
	insertObs(tx1, o1, `["FFFF"]`)
	insertObs(tx2, o2, `["EEEE"]`)
	insertObs(tx3, o1, `["GGGG"]`)

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

	if len(resp.Sessions) != 3 {
		t.Fatalf("Sessions = %+v, want 3 (Alice session A, Alice session B, Bob session)", resp.Sessions)
	}

	// Ordered most-recent-first by StartTime: Alice-B (t2), Bob (t3), Alice-A (t0).
	aliceB, bob, aliceA := resp.Sessions[0], resp.Sessions[1], resp.Sessions[2]

	if aliceA.Sender != "Alice" || aliceA.MessageCount != 2 {
		t.Errorf("Alice session A = %+v, want {Alice, 2 messages}", aliceA)
	}
	if aliceA.DurationMinutes < 4.9 || aliceA.DurationMinutes > 5.1 {
		t.Errorf("Alice session A DurationMinutes = %v, want ~5.0", aliceA.DurationMinutes)
	}
	if aliceA.EntryPointCount != 2 {
		t.Errorf("Alice session A EntryPointCount = %d, want 2 (EEEE, FFFF)", aliceA.EntryPointCount)
	}
	if aliceA.ObserverCount != 1 {
		t.Errorf("Alice session A ObserverCount = %d, want 1 (only ObsOne heard it)", aliceA.ObserverCount)
	}

	if aliceB.Sender != "Alice" || aliceB.MessageCount != 1 {
		t.Errorf("Alice session B = %+v, want {Alice, 1 message}", aliceB)
	}
	if aliceB.EntryPointCount != 1 || aliceB.ObserverCount != 1 {
		t.Errorf("Alice session B = %+v, want {1 entry point, 1 observer}", aliceB)
	}

	if bob.Sender != "Bob" || bob.MessageCount != 1 {
		t.Errorf("Bob session = %+v, want {Bob, 1 message}", bob)
	}

	// The 35-minute gap between t1 and t2 must NOT merge into one session —
	// this is the core behavior under test.
	if aliceA.MessageCount+aliceB.MessageCount != 3 {
		t.Errorf("Alice's 3 messages should split into two sessions (2 + 1), got %d + %d", aliceA.MessageCount, aliceB.MessageCount)
	}
}

// TestHandleWardrivingStats_Anomalies covers payload-anomaly detection:
// standard 7-byte tokens count toward StandardPayloadCount, non-standard
// lengths and undecodable base64 are grouped per sender into Anomalies,
// and messages with no "MM:" prefix at all (plain chat) are ignored
// entirely rather than polluting either bucket.
func TestHandleWardrivingStats_Anomalies(t *testing.T) {
	srv, router := setupTestServer(t)
	if _, err := srv.db.conn.Exec(`DELETE FROM transmissions`); err != nil {
		t.Fatalf("clear transmissions: %v", err)
	}

	// insertTx mirrors decodeGrpTxt's "<sender>: <message>" convention —
	// text is NOT a bare "MM:<base64>", it's sender-prefixed, and
	// detectWardrivingAnomalies matches on that exact "<sender>: MM:" prefix.
	insertTx := func(hash, sender, mmPayload string, tsOffset time.Duration) {
		ts := time.Now().UTC().Add(tsOffset).Format(time.RFC3339)
		text := sender + ": " + mmPayload
		if _, err := srv.db.conn.Exec(
			`INSERT INTO transmissions (raw_hex,hash,first_seen,route_type,payload_type,channel_hash,decoded_json) VALUES (?,?,?,1,5,'#wardriving',?)`,
			"aa", hash, ts, `{"sender":"`+sender+`","text":"`+text+`"}`,
		); err != nil {
			t.Fatalf("insert tx %s: %v", hash, err)
		}
	}

	standardToken := base64.RawURLEncoding.EncodeToString([]byte{1, 2, 3, 4, 5, 6, 7})
	longPayload := base64.RawURLEncoding.EncodeToString(make([]byte, 12))

	insertTx("std1", "Alice", "MM:"+standardToken, -3*time.Hour)
	insertTx("std2", "Bob", "MM:"+standardToken, -2*time.Hour)
	// Suspect1: two messages with a consistent non-standard 12-byte payload.
	insertTx("anom1", "Suspect1", "MM:"+longPayload, -90*time.Minute)
	insertTx("anom2", "Suspect1", "MM:"+longPayload, -80*time.Minute)
	// Suspect2: one genuinely undecodable payload (invalid base64 chars).
	insertTx("anom3", "Suspect2", "MM:!!!not-valid-b64!!!", -70*time.Minute)
	// Plain chat on the same channel — must be ignored entirely, not
	// counted as standard or anomalous.
	insertTx("chat1", "Eve", "hey is anyone home", -60*time.Minute)

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

	if resp.StandardPayloadCount != 2 {
		t.Errorf("StandardPayloadCount = %d, want 2 (Alice + Bob)", resp.StandardPayloadCount)
	}
	if len(resp.Anomalies) != 2 {
		t.Fatalf("Anomalies = %+v, want 2 senders (Suspect1, Suspect2)", resp.Anomalies)
	}

	// Sorted by MessageCount desc: Suspect1 (2 messages) before Suspect2 (1).
	s1, s2 := resp.Anomalies[0], resp.Anomalies[1]
	if s1.Sender != "Suspect1" || s1.MessageCount != 2 {
		t.Errorf("Anomalies[0] = %+v, want {Suspect1, 2 messages}", s1)
	}
	if len(s1.PayloadBytes) != 1 || s1.PayloadBytes[0] != 12 {
		t.Errorf("Suspect1 PayloadBytes = %v, want [12]", s1.PayloadBytes)
	}
	if s2.Sender != "Suspect2" || s2.MessageCount != 1 {
		t.Errorf("Anomalies[1] = %+v, want {Suspect2, 1 message}", s2)
	}
	if len(s2.PayloadBytes) != 1 || s2.PayloadBytes[0] != -1 {
		t.Errorf("Suspect2 PayloadBytes = %v, want [-1] (undecodable)", s2.PayloadBytes)
	}

	// Eve's plain-chat message must not appear anywhere in either bucket.
	for _, a := range resp.Anomalies {
		if a.Sender == "Eve" {
			t.Error("Eve's plain-chat message (no MM: prefix) must not be counted as an anomaly")
		}
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
	if resp.TopSenders == nil || resp.EntryPoints == nil || resp.Observers == nil || resp.TimeSeries == nil || resp.SignalTimeSeries == nil || resp.Sessions == nil || resp.Anomalies == nil {
		t.Errorf("expected empty (non-nil) slices, got TopSenders=%v EntryPoints=%v Observers=%v TimeSeries=%v SignalTimeSeries=%v Sessions=%v Anomalies=%v",
			resp.TopSenders, resp.EntryPoints, resp.Observers, resp.TimeSeries, resp.SignalTimeSeries, resp.Sessions, resp.Anomalies)
	}
	if resp.AvgSNR != nil || resp.AvgRSSI != nil {
		t.Errorf("expected nil AvgSNR/AvgRSSI for a channel with no observations, got AvgSNR=%v AvgRSSI=%v", resp.AvgSNR, resp.AvgRSSI)
	}
	if resp.StandardPayloadCount != 0 {
		t.Errorf("StandardPayloadCount = %d, want 0", resp.StandardPayloadCount)
	}
}

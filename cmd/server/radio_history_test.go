package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// #1869: radio setting per observation, from the observer's radio history.

// withRadioHistory installs a snapshot and observation radio ids for one test.
func withRadioHistory(t testing.TB, snap *radioHistorySnapshot, ids map[int]string) {
	t.Helper()
	prev := radioHistory.Load()
	obsRadioMu.Lock()
	prevIDs := obsRadioIDs
	if ids == nil {
		ids = map[int]string{}
	}
	obsRadioIDs = ids
	obsRadioMu.Unlock()
	radioHistory.Store(snap)
	t.Cleanup(func() {
		radioHistory.Store(prev)
		obsRadioMu.Lock()
		obsRadioIDs = prevIDs
		obsRadioMu.Unlock()
	})
}

func TestRadioAt(t *testing.T) {
	h := &radioHistorySnapshot{observers: map[string]*observerRadios{
		"OBS": {
			main: []RadioStint{
				{Radio: "869.432,62.5,7,5", FirstSeen: "2026-10-01T00:00:00Z"},
				{Radio: "869.618,62.5,8,8", FirstSeen: "2026-10-25T09:30:00Z"},
				{Radio: "869.432,62.5,7,5", FirstSeen: "2026-10-28T00:05:00Z"},
			},
			byID:  map[string][]RadioStint{"b": {{RadioID: "b", Radio: "868.1,125,9,5", FirstSeen: "2026-10-26T00:00:00Z"}}},
			multi: true,
		},
		"ONE": {main: []RadioStint{{Radio: "869.432,62.5,7,5", FirstSeen: "2026-10-01T00:00:00Z"}}},
	}}
	withRadioHistory(t, h, map[int]string{7: "b", 8: "nope"})
	for _, c := range []struct {
		observer string
		obsID    int
		ts, want string
	}{
		{"OBS", 1, "2026-09-01T00:00:00.000Z", "869.432,62.5,7,5"}, // before history: first stint
		{"OBS", 1, "2026-10-25T09:20:00.000Z", "869.432,62.5,7,5"}, // between status messages: previous stint
		{"OBS", 1, "2026-10-26T00:00:00.000Z", "869.618,62.5,8,8"},
		{"OBS", 1, "2026-10-29T00:00:00.000Z", "869.432,62.5,7,5"},
		{"OBS", 7, "2026-10-26T12:00:00.000Z", "868.1,125,9,5"},    // its own radio
		{"OBS", 8, "2026-10-26T12:00:00.000Z", "869.618,62.5,8,8"}, // unknown radio id: main radio
		{"ONE", 7, "2020-01-01T00:00:00.000Z", "869.432,62.5,7,5"}, // single stint, ids ignored
		{"OTHER", 1, "2026-10-26T00:00:00Z", ""},
	} {
		if got := h.radioAt(c.observer, c.obsID, c.ts); got != c.want {
			t.Errorf("radioAt(%s, %d, %s) = %q, want %q", c.observer, c.obsID, c.ts, got, c.want)
		}
	}
	var nilSnap *radioHistorySnapshot
	if got := nilSnap.radioAt("OBS", 1, "2026-10-26T00:00:00Z"); got != "" {
		t.Errorf("nil snapshot = %q", got)
	}
}

func TestTxMatchesRadioObserverSet(t *testing.T) {
	h := &radioHistorySnapshot{observers: map[string]*observerRadios{
		"A": {main: []RadioStint{{Radio: "869.432,62.5,7,5", FirstSeen: "2026-01-01T00:00:00Z"}}},
		"B": {main: []RadioStint{{Radio: "869.618,62.5,8,8", FirstSeen: "2026-01-01T00:00:00Z"}}},
	}}
	tx := &StoreTx{Observations: []*StoreObs{
		{ID: 1, ObserverID: "A", Timestamp: "2026-10-25T00:00:00.000Z"},
		{ID: 2, ObserverID: "B", Timestamp: "2026-10-25T00:00:00.000Z"},
	}}
	if !txMatchesRadio(h, tx, "869.618,62.5,8,8", nil) {
		t.Error("any observer: B heard it on EU")
	}
	if txMatchesRadio(h, tx, "869.618,62.5,8,8", map[string]bool{"A": true}) {
		t.Error("observer A did not hear it on EU")
	}
}

// seedRadioHistory gives the test DB the ingestor's tables: obs1 on the CZ
// preset all along, obs2 on EU narrow.
func seedRadioHistory(t *testing.T, srv *Server) {
	t.Helper()
	for _, s := range []string{
		`CREATE TABLE IF NOT EXISTS observer_radios (id INTEGER PRIMARY KEY AUTOINCREMENT,
			observer_id TEXT NOT NULL, radio_id TEXT NOT NULL DEFAULT '', radio TEXT NOT NULL,
			first_seen TEXT NOT NULL, last_seen TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS observation_radios (observation_id INTEGER PRIMARY KEY, radio_id TEXT NOT NULL)`,
		`INSERT INTO observer_radios (observer_id, radio, first_seen, last_seen) VALUES
			('obs1', '869.4320068,62.5,7,5', '2026-01-01T00:00:00Z', '2099-01-01T00:00:00Z'),
			('obs2', '869.618,62.5,8,8',     '2026-01-01T00:00:00Z', '2099-01-01T00:00:00Z')`,
	} {
		if _, err := srv.db.conn.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	withRadioHistory(t, nil, nil)
	refreshRadioHistory(srv.db.conn, true)
}

func getJSON(t *testing.T, router http.Handler, url string, into interface{}) {
	t.Helper()
	req := httptest.NewRequest("GET", url, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("%s: status %d: %s", url, w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), into); err != nil {
		t.Fatalf("%s: %v", url, err)
	}
}

type packetsBody struct {
	Packets []map[string]interface{} `json:"packets"`
	Total   int                      `json:"total"`
}

func TestPacketsRadioFilter(t *testing.T) {
	srv, router := setupTestServer(t)
	seedRadioHistory(t, srv)

	var eu packetsBody
	getJSON(t, router, "/api/packets?radio=869.618,62.50,8,8&expand=observations", &eu)
	if eu.Total != 1 || len(eu.Packets) != 1 || eu.Packets[0]["hash"] != "abc123def4567890" {
		t.Fatalf("EU filter: total %d, packets %v", eu.Total, eu.Packets)
	}
	obs, _ := eu.Packets[0]["observations"].([]interface{})
	radios := map[string]string{}
	for _, o := range obs {
		m := o.(map[string]interface{})
		r, _ := m["radio"].(string)
		radios[m["observer_id"].(string)] = r
	}
	if radios["obs1"] != "869.432,62.5,7,5" || radios["obs2"] != "869.618,62.5,8,8" {
		t.Errorf("observation radios = %v", radios)
	}

	var cz packetsBody
	getJSON(t, router, "/api/packets?radio=869.4320068,62.5,7,5", &cz)
	if cz.Total != 3 {
		t.Errorf("CZ filter total = %d, want 3", cz.Total)
	}
	// obs2 heard tx 1, but on EU: "obs2 on CZ" is nothing.
	var obs2cz packetsBody
	getJSON(t, router, "/api/packets?observer=obs2&radio=869.432,62.5,7,5", &obs2cz)
	if obs2cz.Total != 0 {
		t.Errorf("observer=obs2&radio=CZ total = %d, want 0", obs2cz.Total)
	}
	var none packetsBody
	getJSON(t, router, "/api/packets?radio=868.1,125,9,5&groupByHash=true", &none)
	if none.Total != 0 {
		t.Errorf("unheard setting total = %d, want 0", none.Total)
	}
}

// The packets page shows grouped rows by default; they carry the setting
// of their header observation, which the client-side filter reads.
func TestGroupedPacketsCarryRadio(t *testing.T) {
	srv, router := setupTestServer(t)
	seedRadioHistory(t, srv)
	var grouped packetsBody
	getJSON(t, router, "/api/packets?groupByHash=true", &grouped)
	if len(grouped.Packets) == 0 {
		t.Fatal("no grouped rows")
	}
	for _, p := range grouped.Packets {
		want := map[string]string{"obs1": "869.432,62.5,7,5", "obs2": "869.618,62.5,8,8"}[p["observer_id"].(string)]
		if p["radio"] != want {
			t.Errorf("grouped row %v (header %v): radio = %v, want %s", p["hash"], p["observer_id"], p["radio"], want)
		}
	}
}

func TestRadiosEndpointAndObserverHistory(t *testing.T) {
	srv, router := setupTestServer(t)
	seedRadioHistory(t, srv)

	var list RadiosResponse
	getJSON(t, router, "/api/radios", &list)
	got := map[string][]string{}
	for _, r := range list.Radios {
		got[r.Radio] = r.Observers
	}
	if len(got) != 2 || len(got["869.432,62.5,7,5"]) != 1 || got["869.618,62.5,8,8"][0] != "obs2" {
		t.Errorf("/api/radios = %+v", list.Radios)
	}

	var detail struct {
		RadioHistory []RadioStint `json:"radio_history"`
	}
	getJSON(t, router, "/api/observers/obs2", &detail)
	if len(detail.RadioHistory) != 1 || detail.RadioHistory[0].Radio != "869.618,62.5,8,8" {
		t.Errorf("radio_history = %+v", detail.RadioHistory)
	}
}

// The per-tick refresh sees a retune and new observation radio ids without
// waiting for the full reload.
func TestRefreshRadioHistoryIncremental(t *testing.T) {
	srv, _ := setupTestServer(t)
	seedRadioHistory(t, srv)
	if _, err := srv.db.conn.Exec(`INSERT INTO observer_radios (observer_id, radio_id, radio, first_seen, last_seen) VALUES
		('obs1', '', '869.618,62.5,8,8', '2098-01-01T00:00:00Z', '2098-01-01T00:00:00Z'),
		('obs1', 'b', '868.1,125,9,5', '2026-01-01T00:00:00Z', '2099-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.db.conn.Exec(`INSERT INTO observation_radios (observation_id, radio_id) VALUES (42, 'b')`); err != nil {
		t.Fatal(err)
	}
	refreshRadioHistory(srv.db.conn, false)
	if got := observationRadio(1, "obs1", "2098-06-01T00:00:00.000Z"); got != "869.618,62.5,8,8" {
		t.Errorf("after retune = %q", got)
	}
	if got := observationRadio(42, "obs1", "2026-06-01T00:00:00.000Z"); got != "868.1,125,9,5" {
		t.Errorf("radio id of a new observation = %q", got)
	}
}

func TestLoadRadioHistoryWithoutTables(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	snap, err := loadRadioStints(db.conn)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.observers) != 0 {
		t.Errorf("empty DB gave %+v", snap)
	}
	if ids, _, err := loadObservationRadios(db.conn, 0); err != nil || len(ids) != 0 {
		t.Errorf("observation radios = %v, %v", ids, err)
	}
}

// BenchmarkTxMatchesRadio: the radio filter over a 30K-packet store with
// ~5 observations each, 20 observers of which 2 retuned once.
func BenchmarkTxMatchesRadio(b *testing.B) {
	h := &radioHistorySnapshot{observers: map[string]*observerRadios{}}
	for i := 0; i < 20; i++ {
		o := &observerRadios{main: []RadioStint{{Radio: "869.432,62.5,7,5", FirstSeen: "2026-01-01T00:00:00Z"}}}
		if i < 2 {
			o.main = append(o.main, RadioStint{Radio: "869.618,62.5,8,8", FirstSeen: "2026-10-25T00:00:00Z"})
		}
		h.observers[fmt.Sprintf("obs%d", i)] = o
	}
	withRadioHistory(b, h, nil)
	txs := make([]*StoreTx, 30000)
	id := 0
	for i := range txs {
		tx := &StoreTx{}
		for j := 0; j < 5; j++ {
			id++
			tx.Observations = append(tx.Observations, &StoreObs{ID: id, ObserverID: fmt.Sprintf("obs%d", (i+j)%20), Timestamp: "2026-10-24T12:00:00.000Z"})
		}
		txs[i] = tx
	}
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		snap := radioHistory.Load()
		matched := 0
		for _, tx := range txs {
			if txMatchesRadio(snap, tx, "869.618,62.5,8,8", nil) {
				matched++
			}
		}
		if matched != 0 {
			b.Fatalf("matched %d, want 0 (retune is later)", matched)
		}
	}
}

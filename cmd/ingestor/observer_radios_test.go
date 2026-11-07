package main

import (
	"encoding/json"
	"testing"
	"time"
)

// #1869: the status message's radio settings become a history, and a
// multi-radio node's rx_radio_id is kept per observation.

func TestExtractRadioReports(t *testing.T) {
	const payload = `{"origin":"ob","radio":" 869.618,62.5,8,8 ",
		"radios":[{"id":"narrow","radio":"869.618,62.5,8,8","tx_power":22},
		          {"id":"cz","radio":"869.432,62.5,7,5"},
		          {"id":"","radio":"868.1,125,9,5"},
		          {"id":"empty"},
		          "garbage"]}`
	var msg map[string]interface{}
	if err := json.Unmarshal([]byte(payload), &msg); err != nil {
		t.Fatal(err)
	}
	got := extractRadioReports(msg)
	want := []RadioReport{
		{ID: "", Radio: "869.618,62.5,8,8"},
		{ID: "narrow", Radio: "869.618,62.5,8,8"},
		{ID: "cz", Radio: "869.432,62.5,7,5"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("report %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if r := extractRadioReports(map[string]interface{}{"origin": "x"}); len(r) != 0 {
		t.Errorf("status without radio gave %+v", r)
	}
}

func TestRecordObserverRadiosStints(t *testing.T) {
	s := newTestStore(t)
	t0 := time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC)
	cz := []RadioReport{{Radio: "869.432,62.5,7,5"}}
	eu := []RadioReport{{Radio: "869.618,62.5,8,8"}}

	for i, step := range []struct {
		at      time.Duration
		reports []RadioReport
	}{
		{0, cz}, {5 * time.Minute, cz}, {10 * time.Minute, eu}, {15 * time.Minute, eu}, {20 * time.Minute, cz},
	} {
		if err := s.RecordObserverRadios("OBS1", step.reports, t0.Add(step.at)); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
	}

	rows, err := s.db.Query(`SELECT radio, first_seen, last_seen FROM observer_radios
		WHERE observer_id = 'OBS1' ORDER BY first_seen, id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type stint struct{ radio, first, last string }
	var got []stint
	for rows.Next() {
		var st stint
		if err := rows.Scan(&st.radio, &st.first, &st.last); err != nil {
			t.Fatal(err)
		}
		got = append(got, st)
	}
	want := []stint{
		{"869.432,62.5,7,5", "2026-10-25T09:00:00Z", "2026-10-25T09:05:00Z"},
		{"869.618,62.5,8,8", "2026-10-25T09:10:00Z", "2026-10-25T09:15:00Z"},
		{"869.432,62.5,7,5", "2026-10-25T09:20:00Z", "2026-10-25T09:20:00Z"},
	}
	if len(got) != len(want) {
		t.Fatalf("stints = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("stint %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestRecordObserverRadiosKeepsRadioIDsApart(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC)
	reports := []RadioReport{
		{Radio: "869.432,62.5,7,5"},
		{ID: "a", Radio: "869.432,62.5,7,5"},
		{ID: "b", Radio: "869.618,62.5,8,8"},
	}
	for i := 0; i < 2; i++ {
		if err := s.RecordObserverRadios("OBS1", reports, now.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM observer_radios WHERE observer_id = 'OBS1'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("rows = %d, want 3 (one stint per radio id)", n)
	}
}

func TestInsertTransmissionKeepsRxRadioID(t *testing.T) {
	s := newTestStore(t)
	if err := s.UpsertObserver("OBS1", "multi", "", nil); err != nil {
		t.Fatal(err)
	}
	for _, d := range []*PacketData{
		{Hash: "with-id", ObserverID: "OBS1", PayloadType: 4, RouteType: 1, RawHex: "1100aa", PathJSON: "[]", RxRadioID: "narrow"},
		{Hash: "without-id", ObserverID: "OBS1", PayloadType: 4, RouteType: 1, RawHex: "1100bb", PathJSON: "[]"},
	} {
		if _, err := s.InsertTransmission(d); err != nil {
			t.Fatal(err)
		}
	}
	if got := obsRadioIDs(t, s, "with-id"); len(got) != 1 || got["[]"] != "narrow" {
		t.Errorf("with-id radio ids = %v, want [] -> narrow", got)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM observation_radios`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("observation_radios rows = %d, want 1 (single-radio packets write nothing)", n)
	}
}

// One node, two radios, the same transmission heard twice over different
// paths: each observation keeps the radio that heard it. Keyed by
// (transmission, observer) the second write overwrote the first.
func TestObservationRadioPerObservation(t *testing.T) {
	s := newTestStore(t)
	if err := s.UpsertObserver("OBS1", "multi", "", nil); err != nil {
		t.Fatal(err)
	}
	for _, d := range []*PacketData{
		{Hash: "two-radios", ObserverID: "OBS1", PayloadType: 4, RouteType: 1, RawHex: "1100aa", PathJSON: "[]", RxRadioID: "cz"},
		{Hash: "two-radios", ObserverID: "OBS1", PayloadType: 4, RouteType: 1, RawHex: "1101aaaa", PathJSON: `["aa"]`, RxRadioID: "eu"},
		// A duplicate delivery of the first observation: same row, upserted.
		{Hash: "two-radios", ObserverID: "OBS1", PayloadType: 4, RouteType: 1, RawHex: "1100aa", PathJSON: "[]", RxRadioID: "cz"},
	} {
		if _, err := s.InsertTransmission(d); err != nil {
			t.Fatal(err)
		}
	}
	got := obsRadioIDs(t, s, "two-radios")
	if len(got) != 2 || got["[]"] != "cz" || got[`["aa"]`] != "eu" {
		t.Errorf("radio per observation = %v, want [] -> cz, [\"aa\"] -> eu", got)
	}
}

// obsRadioIDs maps path_json -> radio_id for the observations of one hash.
func obsRadioIDs(t *testing.T, s *Store, hash string) map[string]string {
	t.Helper()
	rows, err := s.db.Query(`SELECT o.path_json, r.radio_id FROM observation_radios r
		JOIN observations o ON o.id = r.observation_id
		JOIN transmissions t ON t.id = o.transmission_id
		WHERE t.hash = ?`, hash)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var path, id string
		if err := rows.Scan(&path, &id); err != nil {
			t.Fatal(err)
		}
		out[path] = id
	}
	return out
}

// Live status messages write the history, retained replays do not.
func TestStatusMessageRadioHistory(t *testing.T) {
	s := newTestStore(t)
	source := MQTTSource{Name: "test"}
	now := time.Now().UTC().Format(time.RFC3339)
	live := &mockMessage{topic: "meshcore/TST/obs1/status",
		payload: []byte(`{"origin":"o","timestamp":"` + now + `","radio":"869.6180054,62.5,8,8",
			"radios":[{"id":"cz","radio":"869.432,62.50,7,5"}]}`)}
	handleMessage(s, "test", source, live, nil, nil, &Config{})
	retained := &mockMessage{topic: "meshcore/TST/obs1/status", retained: true,
		payload: []byte(`{"origin":"o","timestamp":"` + now + `","radio":"868.1,125,9,5"}`)}
	handleMessage(s, "test", source, retained, nil, nil, &Config{})

	rows, err := s.db.Query(`SELECT radio_id, radio FROM observer_radios WHERE observer_id = 'obs1' ORDER BY radio_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var id, radio string
		if err := rows.Scan(&id, &radio); err != nil {
			t.Fatal(err)
		}
		got = append(got, id+"="+radio)
	}
	want := []string{"=869.618,62.5,8,8", "cz=869.432,62.5,7,5"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("history = %v, want %v (canonical, retained replay ignored)", got, want)
	}
}

// Two spellings of one setting extend the same stint.
func TestRecordObserverRadiosCanonicalSpellings(t *testing.T) {
	s := newTestStore(t)
	t0 := time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC)
	for i, raw := range []string{"869.4320068,62.5,7,5", "869.432,62.50,7,5", "869.4320068,62.5,7,5"} {
		msg := map[string]interface{}{"radio": raw}
		if err := s.RecordObserverRadios("OBS1", extractRadioReports(msg), t0.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM observer_radios WHERE observer_id = 'OBS1'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("stints = %d, want 1", n)
	}
}

func TestPruneOldPacketsDropsObservationRadios(t *testing.T) {
	s := newTestStore(t)
	if err := s.UpsertObserver("OBS1", "multi", "", nil); err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().AddDate(0, 0, -40).Format(time.RFC3339)
	if _, err := s.InsertTransmission(&PacketData{Hash: "old", ObserverID: "OBS1", PayloadType: 4, RouteType: 1,
		RawHex: "1100aa", PathJSON: "[]", RxRadioID: "narrow", Timestamp: old}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE transmissions SET first_seen = ? WHERE hash = 'old'`, old); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PruneOldPackets(30); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM observation_radios`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("observation_radios rows after prune = %d, want 0", n)
	}
}

func TestPacketMessageCarriesRxRadioID(t *testing.T) {
	msg := &MQTTPacketMessage{Raw: "1100aa", RxRadioID: "narrow"}
	decoded := &DecodedPacket{}
	pd := BuildPacketData(msg, decoded, "OBS1", "", nil)
	if pd.RxRadioID != "narrow" {
		t.Errorf("PacketData.RxRadioID = %q, want narrow", pd.RxRadioID)
	}
}

// A purged observer takes its radio history along (#1869).
func TestPurgeStaleObserversDropsRadioHistory(t *testing.T) {
	s := newTestStore(t)
	staleObserver(t, s, "obs-gone")
	if err := s.UpsertObserver("obs-kept", "kept", "LAX", nil); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, id := range []string{"obs-gone", "obs-kept"} {
		if err := s.RecordObserverRadios(id, []RadioReport{{Radio: "869.432,62.5,7,5"}}, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.PurgeStaleObservers(30); err != nil {
		t.Fatal(err)
	}
	var left []string
	rows, err := s.db.Query(`SELECT observer_id FROM observer_radios`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		left = append(left, id)
	}
	if len(left) != 1 || left[0] != "obs-kept" {
		t.Errorf("observer_radios after purge = %v, want [obs-kept]", left)
	}
}

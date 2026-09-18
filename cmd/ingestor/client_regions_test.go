package main

import (
	"strings"
	"testing"
	"time"
)

const testRegionsTarget = "ba5e00011bc884f7d9ac06f5e93a4cec6e21822c4362b533e6ad3e88a11430aa"

// clientRegionsMsg builds a /regions message in the shape CoreDrive RX
// publishes (src/publisher.js buildRegionsPayload), as captured off a live
// broker.
func clientRegionsMsg(body string) *mockMessage {
	return &mockMessage{topic: "meshcore/client/" + testCompanionPK + "/regions", payload: []byte(body)}
}

func liveRegionsBody(ts, regions string) string {
	return `{"origin_id":"` + testCompanionPK + `","origin":"RandomCitizenT","timestamp":"` + ts +
		`","type":"REGIONS","target":"` + strings.ToUpper(testRegionsTarget) + `","regions":` + regions +
		`,"truncated":false,"repeater_clock":1789722571,"gps":{}}`
}

type declaredRow struct {
	target, rx, at, csv string
	truncated           int
}

func declaredRows(t *testing.T, s *Store) []declaredRow {
	t.Helper()
	rows, err := s.db.Query(`SELECT target, rx_pubkey, observed_at, regions_csv, truncated FROM node_declared_regions ORDER BY observed_at`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []declaredRow
	for rows.Next() {
		var r declaredRow
		if err := rows.Scan(&r.target, &r.rx, &r.at, &r.csv, &r.truncated); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func regionsCfg() *Config {
	return &Config{ClientRxCoverage: &ClientRxCoverageConfig{Enabled: true}}
}

func sendRegions(t *testing.T, s *Store, cfg *Config, body string) {
	t.Helper()
	handleMessage(s, "test", MQTTSource{Name: "test"}, clientRegionsMsg(body), nil, nil, cfg)
}

// recentTS returns an RFC3339 instant hours before now. resolveRxTimeCore
// replaces anything more than 30 days old with the ingest time, so fixed
// calendar dates would make ordering assertions depend on when the test runs.
func recentTS(hoursAgo int) string {
	return time.Now().UTC().Add(-time.Duration(hoursAgo) * time.Hour).Format(time.RFC3339)
}

func TestClientRegionsStoresAnswer(t *testing.T) {
	store := newTestStore(t)
	ts := recentTS(1)
	sendRegions(t, store, regionsCfg(), liveRegionsBody(strings.Replace(ts, "Z", ".120Z", 1), `["*","hu"]`))

	got := declaredRows(t, store)
	if len(got) != 1 {
		t.Fatalf("expected 1 node_declared_regions row, got %d", len(got))
	}
	r := got[0]
	if r.target != testRegionsTarget {
		t.Errorf("target = %q, want the lowercased repeater key", r.target)
	}
	if r.rx != testCompanionPK {
		t.Errorf("rx_pubkey = %q, want the topic pubkey", r.rx)
	}
	// Canonical RFC3339 (milliseconds dropped), so the server's newest-wins
	// compare against configured_scope_at is a plain string compare.
	if r.at != ts {
		t.Errorf("observed_at = %q, want the answer's own instant %q", r.at, ts)
	}
	if r.csv != "*,hu" || r.truncated != 0 {
		t.Errorf("regions = %q truncated=%d, want \"*,hu\" untruncated", r.csv, r.truncated)
	}

	var name string
	store.db.QueryRow(`SELECT name FROM client_observers WHERE pubkey = ?`, testCompanionPK).Scan(&name)
	if name != "RandomCitizenT" {
		t.Errorf("client_observers name = %q, want the origin", name)
	}
}

// An empty list is a real answer ("declares nothing") and is stored; a
// missing list is not an answer at all and must not be minted into one.
func TestClientRegionsEmptyVsMissing(t *testing.T) {
	store := newTestStore(t)
	ts := recentTS(1)

	sendRegions(t, store, regionsCfg(), `{"timestamp":"`+ts+`","type":"REGIONS","target":"`+testRegionsTarget+`"}`)
	if n := len(declaredRows(t, store)); n != 0 {
		t.Fatalf("missing regions array stored %d rows, want 0", n)
	}
	sendRegions(t, store, regionsCfg(), `{"timestamp":"`+ts+`","type":"REGIONS","target":"`+testRegionsTarget+`","regions":"hu"}`)
	if n := len(declaredRows(t, store)); n != 0 {
		t.Fatalf("non-array regions stored %d rows, want 0", n)
	}
	sendRegions(t, store, regionsCfg(), liveRegionsBody(ts, `[]`))
	got := declaredRows(t, store)
	if len(got) != 1 || got[0].csv != "" {
		t.Fatalf("empty regions array: got %+v, want one row with an empty list", got)
	}
}

func TestClientRegionsRejectsBadTargetAndType(t *testing.T) {
	store := newTestStore(t)
	ts := recentTS(1)
	for _, target := range []string{"", "ba5e0001", strings.Repeat("zz", 32)} {
		sendRegions(t, store, regionsCfg(), `{"timestamp":"`+ts+`","type":"REGIONS","target":"`+target+`","regions":["hu"]}`)
	}
	sendRegions(t, store, regionsCfg(), `{"timestamp":"`+ts+`","type":"PACKET","target":"`+testRegionsTarget+`","regions":["hu"]}`)
	if n := len(declaredRows(t, store)); n != 0 {
		t.Fatalf("stored %d rows for invalid messages, want 0", n)
	}
}

// Only the newest answer per repeater is kept, and an older answer arriving
// late (a drive buffered offline) must not displace it.
func TestClientRegionsNewestAnswerWins(t *testing.T) {
	store := newTestStore(t)
	newer, older := recentTS(2), recentTS(5)

	sendRegions(t, store, regionsCfg(), liveRegionsBody(newer, `["hu","eu"]`))
	sendRegions(t, store, regionsCfg(), liveRegionsBody(older, `["stale"]`))
	got := declaredRows(t, store)
	if len(got) != 1 || got[0].csv != "hu,eu" || got[0].at != newer {
		t.Fatalf("after a late older answer: got %+v, want only the newer answer", got)
	}

	newest := recentTS(1)
	sendRegions(t, store, regionsCfg(), liveRegionsBody(newest, `["hu"]`))
	got = declaredRows(t, store)
	if len(got) != 1 || got[0].csv != "hu" || got[0].at != newest {
		t.Fatalf("after a newer answer: got %+v, want it to replace the old one", got)
	}
}

// Names that cannot be stored faithfully are dropped and the answer flagged
// truncated, rather than split on the comma or kept unbounded.
func TestClientRegionsDropsUnstorableNamesAndFlagsTruncated(t *testing.T) {
	store := newTestStore(t)
	long := strings.Repeat("x", maxDeclaredRegionLen+1)
	sendRegions(t, store, regionsCfg(), liveRegionsBody(recentTS(1), `["hu"," eu ","a,b","","`+long+`",7]`))
	got := declaredRows(t, store)
	if len(got) != 1 || got[0].csv != "hu,eu" || got[0].truncated != 1 {
		t.Fatalf("got %+v, want \"hu,eu\" flagged truncated", got)
	}

	store2 := newTestStore(t)
	many := make([]string, maxDeclaredRegions+5)
	for i := range many {
		many[i] = `"r` + strings.Repeat("a", i%5+1) + `"`
	}
	sendRegions(t, store2, regionsCfg(), liveRegionsBody(recentTS(1), "["+strings.Join(many, ",")+"]"))
	got = declaredRows(t, store2)
	if len(got) != 1 {
		t.Fatalf("oversized list: got %d rows, want 1", len(got))
	}
	if n := len(strings.Split(got[0].csv, ",")); n != maxDeclaredRegions || got[0].truncated != 1 {
		t.Fatalf("oversized list: kept %d names truncated=%d, want %d and truncated", n, got[0].truncated, maxDeclaredRegions)
	}
}

// With client uploads disabled the message is dropped outright and never falls
// through to the observer path (same dispatch-shape guard as /packets and /rf).
func TestClientRegionsGateOff(t *testing.T) {
	store := newTestStore(t)
	sendRegions(t, store, &Config{}, liveRegionsBody(recentTS(1), `["hu"]`))
	if n := len(declaredRows(t, store)); n != 0 {
		t.Fatalf("feature OFF: stored %d rows, want 0", n)
	}
	var observerRows int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM observers WHERE id = ? OR iata = 'client'`, testCompanionPK).Scan(&observerRows); err != nil {
		t.Fatal(err)
	}
	if observerRows != 0 {
		t.Fatalf("feature OFF: a /regions message registered %d observer rows", observerRows)
	}
}

// The derived region-key tier reads the same table, so an answer stored here
// must count as a declaration there.
func TestClientRegionsFeedDeclaredRegionSources(t *testing.T) {
	store := newTestStore(t)
	sendRegions(t, store, regionsCfg(), liveRegionsBody(recentTS(1), `["*","hu"]`))
	stats, err := store.declaredRegionSources()
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range stats {
		if strings.TrimPrefix(st.Name, "#") == "hu" && st.Declarers == 1 {
			return
		}
	}
	t.Fatalf("declaredRegionSources = %+v, want hu declared once", stats)
}

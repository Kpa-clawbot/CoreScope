package main

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// allClientFeatures turns on every client sub-topic and the linked-only
// setting; whether the filter runs depends only on store.linkedCompanions.
func allClientFeatures() *Config {
	return &Config{
		ClientRxCoverage: &ClientRxCoverageConfig{Enabled: true, RequireLinkedCompanion: true},
		ClientRfSamples:  &ClientRfSamplesConfig{Enabled: true},
		ClientRegions:    &ClientRegionsConfig{Enabled: true},
		UserManagement:   &UserManagementConfig{Enabled: true},
	}
}

// linkedFilterStore is a test store with the filter installed over pks.
func linkedFilterStore(t *testing.T, pks ...string) *Store {
	t.Helper()
	store := newTestStore(t)
	s, _, _ := newTestLinkedSet(t, pks...)
	s.refresh()
	store.linkedCompanions = s
	return store
}

func clientTopicMsg(pk, sub, payload string) *mockMessage {
	return &mockMessage{topic: "meshcore/client/" + pk + "/" + sub, payload: []byte(payload)}
}

func rfSampleCount(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM client_rf_samples`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestLinkedOnlyFilterOffChangesNothing(t *testing.T) {
	store := newTestStore(t) // no filter installed
	handleMessage(store, "test", MQTTSource{Name: "test"}, clientCoverageMsg(), nil, nil, allClientFeatures())
	if n := clientReceptionCount(t, store); n != 1 {
		t.Fatalf("filter off: %d client_receptions rows, want 1", n)
	}
	if d := store.Stats.ClientUnlinkedDropped.Load(); d != 0 {
		t.Fatalf("filter off: %d drops counted", d)
	}
}

func TestLinkedOnlyFilterPassesLinked(t *testing.T) {
	store := linkedFilterStore(t, testCompanionPK)
	handleMessage(store, "test", MQTTSource{Name: "test"}, clientCoverageMsg(), nil, nil, allClientFeatures())
	if n := clientReceptionCount(t, store); n != 1 {
		t.Fatalf("linked: %d client_receptions rows, want 1", n)
	}
	if d := store.Stats.ClientUnlinkedDropped.Load(); d != 0 {
		t.Fatalf("linked: %d drops counted", d)
	}
}

func TestLinkedOnlyFilterDropsUnlinkedBeforeEveryHandler(t *testing.T) {
	store := linkedFilterStore(t) // nothing linked
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	cfg := allClientFeatures()
	src := MQTTSource{Name: "test"}
	rf := `{"type":"RF_SAMPLE","timestamp":"` + rfFixtureTime(0) + `","gps":{"lat":51.2,"lon":4.4,"acc_m":8},"stationary":false,"uptime_secs":84213,"noise_floor":-119,"rx_air_secs":20877}`

	handleMessage(store, "test", src, clientCoverageMsg(), nil, nil, cfg)
	handleMessage(store, "test", src, clientTopicMsg(testCompanionPK, "rf", rf), nil, nil, cfg)
	handleMessage(store, "test", src, clientTopicMsg(testCompanionPK, "regions", `{}`), nil, nil, cfg)

	if n := clientReceptionCount(t, store); n != 0 {
		t.Fatalf("unlinked packets wrote %d client_receptions rows", n)
	}
	if n := rfSampleCount(t, store); n != 0 {
		t.Fatalf("unlinked rf wrote %d client_rf_samples rows", n)
	}
	if d := store.Stats.ClientUnlinkedDropped.Load(); d != 3 {
		t.Fatalf("%d drops counted, want 3 (packets, rf, regions)", d)
	}
	if buf.Len() != 0 {
		t.Fatalf("a drop was logged per message:\n%s", buf.String())
	}
}

func TestStatsFileCarriesClientUnlinkedDropped(t *testing.T) {
	statsPath := filepath.Join(t.TempDir(), "ingestor-stats.json")
	t.Setenv("CORESCOPE_INGESTOR_STATS", statsPath)
	store := newTestStore(t)
	store.Stats.ClientUnlinkedDropped.Add(3)
	stop := StartStatsFileWriter(store, 20*time.Millisecond)
	defer stop()
	deadline := time.Now().Add(3 * time.Second)
	for {
		b, err := os.ReadFile(statsPath)
		if err == nil && strings.Contains(string(b), `"client_unlinked_dropped":3`) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("stats file never showed client_unlinked_dropped=3: %s (err %v)", b, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

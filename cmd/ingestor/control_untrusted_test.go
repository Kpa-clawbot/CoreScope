package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/meshcore-analyzer/dbx/dbxtest"
	"github.com/meshcore-analyzer/mbcapqueue"
)

// The test binary runs many isolated CoreScope schemas in one database, which
// production refuses (TestPostgresRefusesSharedDatabase).
func init() { allowSharedDatabaseForTests = true }

// Outside the test seam, a database that already holds another CoreScope
// install is refused at startup.
func TestPostgresRefusesSharedDatabase(t *testing.T) {
	// Another install: a CoreScope schema in the same database.
	openPostgresTestStore(t, filepath.Join(t.TempDir(), "other.db"))

	pg := dbxtest.OpenPostgres(t)
	allowSharedDatabaseForTests = false
	t.Cleanup(func() { allowSharedDatabaseForTests = true })
	s, err := openPostgresStoreDB(pg, filepath.Join(t.TempDir(), "meshcore.db"), 300)
	if err == nil {
		s.Close()
		t.Fatal("started on a database shared with another CoreScope install")
	}
	if !strings.HasPrefix(err.Error(), "refusing to start: ") {
		t.Errorf("error = %v", err)
	}
}

// A malformed newest snapshot on Postgres is discarded with the older ones
// instead of blocking the prune forever; the next good one is applied.
func TestPostgresMalformedMultibyteSnapshotDiscarded(t *testing.T) {
	s := openPostgresTestStore(t, filepath.Join(t.TempDir(), "meshcore.db"))
	ctl := isolateControlTables(t, s)
	if _, err := s.db.Exec(`INSERT INTO nodes (public_key, name, role, last_seen, multibyte_sup) VALUES ('dd44', 'Delta', 'repeater', '2026-01-01T00:00:00Z', 0)`); err != nil {
		t.Fatal(err)
	}
	server := mbcapqueue.NewPG(s.db, ctl)
	for i := 0; i < 5; i++ {
		if err := server.WriteSnapshot(mbcapqueue.Snapshot{Entries: []mbcapqueue.Entry{{PublicKey: "dd44", Status: "suspected"}}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(`INSERT INTO "` + ctl + `".mbcap_snapshots (id, created_at, payload) VALUES ('ffffffffffffffffffffffffffffffff', '2999-01-01T00:00:00.000000000Z', 'not-json{{{')`); err != nil {
		t.Fatal(err)
	}
	logs := captureLogs(t)
	if stats, err := s.RunMultibyteCapPersist(); err != nil || stats.ReadEntries != 0 {
		t.Fatalf("malformed snapshot: %+v %v", stats, err)
	}
	if !logContains(logs, "malformed snapshot") {
		t.Errorf("not logged: %s", logs.String())
	}
	var rows int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM "` + ctl + `".mbcap_snapshots`).Scan(&rows); err != nil || rows != 0 {
		t.Errorf("%d snapshots left after a malformed newest one (%v), want 0", rows, err)
	}
	if err := server.WriteSnapshot(mbcapqueue.Snapshot{Entries: []mbcapqueue.Entry{{PublicKey: "dd44", Status: "confirmed"}}}); err != nil {
		t.Fatal(err)
	}
	if stats, err := s.RunMultibyteCapPersist(); err != nil || stats.UpdatedActive != 1 {
		t.Errorf("good snapshot after the bad one: %+v %v", stats, err)
	}
}

// Snapshot entries are untrusted: only existing nodes are touched, each
// pubkey once, and no more entries are looked at than there are nodes.
func TestMultibyteSnapshotEntriesValidated(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.db.Exec(`INSERT INTO nodes (public_key, name, role, last_seen, multibyte_sup) VALUES ('aa11', 'A', 'repeater', '2026-01-01T00:00:00Z', 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO inactive_nodes (public_key, name, role, last_seen, multibyte_sup) VALUES ('bb22', 'B', 'repeater', '2025-01-01T00:00:00Z', 0)`); err != nil {
		t.Fatal(err)
	}
	entries := []mbcapqueue.Entry{
		{PublicKey: "aa11", Status: "suspected", Evidence: "path"},
		{PublicKey: "aa11", Status: "confirmed", Evidence: "repeat"}, // a repeated pubkey
		{PublicKey: "bb22", Status: "suspected", Evidence: "path"},   // past the node count (2)
	}
	for i := 0; i < 50; i++ {
		entries = append(entries, mbcapqueue.Entry{PublicKey: "ghost", Status: "confirmed"})
	}
	if err := mbcapqueue.WriteSnapshot(dbPath, mbcapqueue.Snapshot{Entries: entries}); err != nil {
		t.Fatal(err)
	}
	stats, err := s.RunMultibyteCapPersist()
	if err != nil {
		t.Fatal(err)
	}
	if stats.UpdatedActive != 1 || stats.UpdatedInactive != 0 || stats.Ignored != len(entries)-1 {
		t.Errorf("stats = %+v, want 1 active update and %d ignored", stats, len(entries)-1)
	}
	var sup int
	if err := s.db.QueryRow(`SELECT multibyte_sup FROM nodes WHERE public_key = 'aa11'`).Scan(&sup); err != nil || sup != 1 {
		t.Errorf("aa11 sup = %d (%v), want the first entry's 1", sup, err)
	}

	// Within the bound, a pubkey that is no node is ignored, not applied.
	if err := mbcapqueue.WriteSnapshot(dbPath, mbcapqueue.Snapshot{Entries: []mbcapqueue.Entry{
		{PublicKey: "ghost", Status: "confirmed"},
		{PublicKey: "bb22", Status: "confirmed", Evidence: "advert"},
	}}); err != nil {
		t.Fatal(err)
	}
	stats, err = s.RunMultibyteCapPersist()
	if err != nil || stats.UpdatedInactive != 1 || stats.Ignored != 1 {
		t.Errorf("stats = %+v %v, want 1 inactive update and 1 ignored", stats, err)
	}
}

func TestMaskURLSecrets(t *testing.T) {
	cases := []struct{ name, in, want string }{
		// The server's maskBrokerURL cases, same output.
		{"plain mqtt no creds", "mqtt://broker.example.com:1883", "mqtt://broker.example.com:1883"},
		{"mqtt with creds", "mqtt://u:secret@broker.example.com:1883", "mqtt://u:****@broker.example.com:1883"},
		{"mqtts with creds", "mqtts://u:secret@broker.example.com:8883", "mqtts://u:****@broker.example.com:8883"},
		{"tcp with creds", "tcp://u:secret@host:1883", "tcp://u:****@host:1883"},
		{"ssl with creds", "ssl://u:secret@host:8883", "ssl://u:****@host:8883"},
		{"ws with creds", "ws://u:secret@host:8080/mqtt", "ws://u:****@host:8080/mqtt"},
		{"wss with creds", "wss://u:secret@host:443/mqtt", "wss://u:****@host:443/mqtt"},
		{"uppercase scheme", "MQTT://u:secret@host:1883", "MQTT://u:****@host:1883"},
		{"empty", "", ""},
		{"no scheme bare host", "host:1883", "host:1883"},
		{"password with @", "mqtt://user:p@ss@wo@secret@host:1883", "mqtt://user:****@host:1883"},
		{"username only", "mqtt://user@host:1883", "mqtt://user@host:1883"},
		// Beyond the server's copy.
		{"password with slash", "mqtt://user:sec/ret@host:1883", "mqtt://user:****@host:1883"},
		{"other scheme", "https://u:secret@example.com/x", "https://u:****@example.com/x"},
		{"query token", "wss://host/mqtt?token=secret&x=1", "wss://host/mqtt?token=****&x=1"},
		{"quoted in error", `dial "mqtt://u:secret@host:1883": connection refused`, `dial "mqtt://u:****@host:1883": connection refused`},
		{"two urls", "a mqtt://u:secret@h1 b tcp://v:secret@h2:1", "a mqtt://u:****@h1 b tcp://v:****@h2:1"},
		{"no url", "network is unreachable", "network is unreachable"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := maskURLSecrets(c.in)
			if got != c.want {
				t.Errorf("maskURLSecrets(%q) = %q, want %q", c.in, got, c.want)
			}
			if strings.Contains(got, "secret") {
				t.Errorf("output still contains the secret: %q", got)
			}
		})
	}
}

// What the ingestor publishes about its MQTT sources (the stats file here,
// the same JSON in ingestor_stats on Postgres) carries no broker password:
// not in the broker, not in an unnamed source's name, not in lastError.
func TestStatsPublishNoBrokerCredentials(t *testing.T) {
	resetSourceStatusRegistry()
	t.Cleanup(resetSourceStatusRegistry)
	const raw = "mqtt://obs:hunter2@broker.example.com:1883"
	RegisterSourceStatus("named", raw).MarkDisconnect(time.Now(), errors.New(`connect "`+raw+`": not authorized`))
	RegisterSourceStatus(raw, raw) // unnamed: the tag is the URL

	dir := t.TempDir()
	statsPath := filepath.Join(dir, "ingestor-stats.json")
	t.Setenv("CORESCOPE_INGESTOR_STATS", statsPath)
	s, err := OpenStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	stop := StartStatsFileWriter(s, 20*time.Millisecond)
	defer stop()
	deadline := time.Now().Add(5 * time.Second)
	var b []byte
	for {
		if b, err = os.ReadFile(statsPath); err == nil && len(b) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no stats file")
		}
		time.Sleep(20 * time.Millisecond)
	}
	stop()
	if strings.Contains(string(b), "hunter2") {
		t.Fatalf("stats publish the broker password: %s", b)
	}
	var snap IngestorStatsSnapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		t.Fatal(err)
	}
	masked := "mqtt://obs:****@broker.example.com:1883"
	var sawErr bool
	for _, st := range snap.SourceStatuses {
		if st.Broker != masked {
			t.Errorf("broker = %q, want %q", st.Broker, masked)
		}
		if st.LastError != "" {
			sawErr = strings.Contains(st.LastError, masked)
		}
	}
	if len(snap.SourceStatuses) != 2 || !sawErr {
		t.Errorf("source statuses = %+v", snap.SourceStatuses)
	}
}

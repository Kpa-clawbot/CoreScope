package main

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// The columns cmd/server's users.db (internal/users schema v4) has; the
// ingestor reads kind, subject, status, decided_at and id.
const testProposalsDDL = `CREATE TABLE IF NOT EXISTS proposals (
	id INTEGER PRIMARY KEY,
	kind TEXT NOT NULL,
	subject TEXT NOT NULL,
	status TEXT NOT NULL,
	proposer_id INTEGER,
	reviewer_id INTEGER,
	note TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	decided_at INTEGER
)`

type testProposal struct {
	subject, status string
	decidedAt       int64
}

func execUsersDB(t *testing.T, path string, stmts ...string) {
	t.Helper()
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}

func writeUsersDB(t *testing.T, path string, rows ...testProposal) {
	t.Helper()
	stmts := []string{testProposalsDDL, `DELETE FROM proposals`}
	for i, r := range rows {
		stmts = append(stmts, fmt.Sprintf(`INSERT INTO proposals (kind, subject, status, created_at, decided_at) VALUES ('hashtag_channel', '%s', '%s', %d, %d)`,
			r.subject, r.status, i, r.decidedAt))
	}
	execUsersDB(t, path, stmts...)
}

func configuredKeys() map[string]string {
	return map[string]string{"Public": "8b3387e9c5cdea6ac9e5edbaa115cd72", "#mesh": deriveHashtagChannelKey("#mesh")}
}

func TestApprovedChannelsQueryMatchesUsersPackage(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "internal", "users", "proposals_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), approvedChannelsQuery) {
		t.Fatalf("internal/users/proposals_test.go no longer pins %q; keep the two in step", approvedChannelsQuery)
	}
}

func TestChannelKeySetOffReturnsConfiguredMap(t *testing.T) {
	cfgd := configuredKeys()
	s := newChannelKeySet(cfgd, filepath.Join(t.TempDir(), "users.db"), 128)
	if reflect.ValueOf(s.Snapshot()).Pointer() != reflect.ValueOf(cfgd).Pointer() {
		t.Fatal("before any refresh the decoder must get the very map loadChannelKeys built")
	}
}

func TestChannelKeySetLoadsApproved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.db")
	writeUsersDB(t, path,
		testProposal{"#mycity", "approved", 10}, testProposal{"#waiting", "pending", 0},
		testProposal{"#nope", "rejected", 11}, testProposal{"#gone", "revoked", 12})
	s := newChannelKeySet(configuredKeys(), path, 128)
	defer s.Close()
	s.refresh()
	snap := s.Snapshot()
	if snap["#mycity"] != deriveHashtagChannelKey("#mycity") {
		t.Fatalf("#mycity key = %q", snap["#mycity"])
	}
	for _, n := range []string{"#waiting", "#nope", "#gone"} {
		if _, ok := snap[n]; ok {
			t.Errorf("%s decrypted without approval", n)
		}
	}
	if snap["Public"] == "" || snap["#mesh"] == "" || len(snap) != 3 {
		t.Fatalf("snapshot = %v; want the configured keys plus #mycity", snap)
	}
}

func TestChannelKeySetConfiguredNameWins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.db")
	writeUsersDB(t, path, testProposal{"#mesh", "approved", 1})
	cfgd := configuredKeys()
	cfgd["#mesh"] = "00112233445566778899aabbccddeeff"
	s := newChannelKeySet(cfgd, path, 128)
	defer s.Close()
	s.refresh()
	if got := s.Snapshot()["#mesh"]; got != "00112233445566778899aabbccddeeff" {
		t.Fatalf("#mesh = %q; the configured key must win", got)
	}
	// Revoking the proposal leaves the configured channel decrypted.
	execUsersDB(t, path, `UPDATE proposals SET status = 'revoked'`)
	s.refresh()
	if got := s.Snapshot()["#mesh"]; got != "00112233445566778899aabbccddeeff" {
		t.Fatalf("after revoke #mesh = %q", got)
	}
}

func TestChannelKeySetRevokeRemovesKeyOnNextRefresh(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.db")
	writeUsersDB(t, path, testProposal{"#mycity", "approved", 1})
	s := newChannelKeySet(configuredKeys(), path, 128)
	defer s.Close()
	s.refresh()
	before := s.Snapshot()
	execUsersDB(t, path, `UPDATE proposals SET status = 'revoked' WHERE subject = '#mycity'`)
	s.refresh()
	if _, ok := s.Snapshot()["#mycity"]; ok {
		t.Fatal("#mycity still decrypted after revoke")
	}
	if _, ok := before["#mycity"]; !ok {
		t.Fatal("an earlier snapshot must stay unchanged (decoders may still hold it)")
	}
}

func TestChannelKeySetMissingFileKeepsConfiguredAndLogsOnce(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	path := filepath.Join(t.TempDir(), "users.db")
	s := newChannelKeySet(configuredKeys(), path, 128)
	defer s.Close()
	s.refresh()
	s.refresh()
	if len(s.Snapshot()) != 2 {
		t.Fatalf("snapshot = %v; want the configured keys", s.Snapshot())
	}
	if n := strings.Count(buf.String(), "approved channels unavailable"); n != 1 {
		t.Fatalf("logged %d times; want once:\n%s", n, buf.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("the ingestor created users.db")
	}
	writeUsersDB(t, path, testProposal{"#mycity", "approved", 1})
	s.refresh()
	if _, ok := s.Snapshot()["#mycity"]; !ok {
		t.Fatal("users.db appearing later is picked up")
	}
}

func TestChannelKeySetReadErrorKeepsLastGoodSet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.db")
	writeUsersDB(t, path, testProposal{"#mycity", "approved", 1})
	s := newChannelKeySet(configuredKeys(), path, 128)
	defer s.Close()
	s.refresh()
	execUsersDB(t, path, `DROP TABLE proposals`)
	s.refresh()
	if _, ok := s.Snapshot()["#mycity"]; !ok {
		t.Fatal("a failing read removed an approved key")
	}
}

func TestChannelKeySetSkipsNamesThatFailTheRules(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.db")
	writeUsersDB(t, path,
		testProposal{"#public", "approved", 1}, testProposal{"nohash", "approved", 2},
		testProposal{"#" + strings.Repeat("a", 31), "approved", 3}, testProposal{"#ok", "approved", 4})
	s := newChannelKeySet(configuredKeys(), path, 128)
	defer s.Close()
	s.refresh()
	snap := s.Snapshot()
	if len(snap) != 3 || snap["#ok"] == "" {
		t.Fatalf("snapshot = %v; want configured + #ok only", snap)
	}
}

func TestChannelKeySetCapKeepsOldestApprovals(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.db")
	writeUsersDB(t, path, testProposal{"#c", "approved", 30}, testProposal{"#a", "approved", 10}, testProposal{"#b", "approved", 20})
	s := newChannelKeySet(configuredKeys(), path, 2)
	defer s.Close()
	s.refresh()
	snap := s.Snapshot()
	if snap["#a"] == "" || snap["#b"] == "" || snap["#c"] != "" {
		t.Fatalf("snapshot = %v; want #a and #b (oldest decisions) only", snap)
	}
}

func TestChannelKeySetIsReadOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.db")
	writeUsersDB(t, path, testProposal{"#mycity", "approved", 1})
	s := newChannelKeySet(configuredKeys(), path, 128)
	defer s.Close()
	s.refresh()
	_, err := s.db.Exec(`INSERT INTO proposals (kind, subject, status, created_at) VALUES ('hashtag_channel', '#x', 'approved', 1)`)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "readonly") {
		t.Fatalf("write through the ingestor's users.db handle: err = %v; want a read-only refusal", err)
	}
	if !strings.Contains(usersDBReadOnlyDSN(path), "mode=ro") {
		t.Fatal("DSN without mode=ro")
	}
}

// The server keeps users.db in WAL mode with a writer connection open.
func TestChannelKeySetReadsWALWithOpenWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.db")
	writeUsersDB(t, path, testProposal{"#mycity", "approved", 1})
	w, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	w.SetMaxOpenConns(1)
	if _, err := w.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Exec(`INSERT INTO proposals (kind, subject, status, created_at, decided_at) VALUES ('hashtag_channel', '#walcity', 'approved', 9, 9)`); err != nil {
		t.Fatal(err)
	}
	s := newChannelKeySet(configuredKeys(), path, 128)
	defer s.Close()
	s.refresh()
	if s.Snapshot()["#walcity"] == "" || s.Snapshot()["#mycity"] == "" {
		t.Fatalf("snapshot = %v; want both approved names", s.Snapshot())
	}
}

func grpTxtFor(t testing.TB, channelName, text string) []byte {
	key := deriveHashtagChannelKey(channelName)
	inner := append([]byte{1, 0, 0, 0, 0}, []byte(text)...)
	ctHex, macHex := buildChannelEncrypted(key, inner)
	kb, _ := hex.DecodeString(key)
	ct, _ := hex.DecodeString(ctHex)
	mac, _ := hex.DecodeString(macHex)
	h := sha256.Sum256(kb)
	return append(append([]byte{h[0]}, mac...), ct...)
}

// Run with -race: decoders read snapshots while refresh swaps them.
func TestChannelKeySetConcurrentDecodeWhileSwapping(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.db")
	writeUsersDB(t, path, testProposal{"#mycity", "approved", 1})
	s := newChannelKeySet(configuredKeys(), path, 128)
	defer s.Close()
	s.refresh()
	buf := grpTxtFor(t, "#mycity", "alice: hi")
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if p := decodeGrpTxt(buf, s.Snapshot()); p.Type == "CHAN" && p.Channel != "#mycity" {
					t.Errorf("decoded as %q", p.Channel)
					return
				}
			}
		}()
	}
	for i := 0; i < 50; i++ {
		status := "revoked"
		if i%2 == 1 {
			status = "approved"
		}
		execUsersDB(t, path, `UPDATE proposals SET status = '`+status+`'`)
		s.refresh()
	}
	close(stop)
	wg.Wait()
	if p := decodeGrpTxt(buf, s.Snapshot()); p.Type != "CHAN" || p.Channel != "#mycity" {
		t.Fatalf("final decode = %+v; want #mycity (last state approved)", p)
	}
}

// Perf evidence for the PR: worst case (no key opens the packet), a
// realistic configured set (320 keys, the size of channel-rainbow.json plus
// Public) alone vs plus 128 approved keys (the maxApproved default).
func BenchmarkDecodeGrpTxtApprovedKeys(b *testing.B) {
	buf := grpTxtFor(b, "#nobody-has-this", "x: y")
	base := configuredKeys()
	for i := len(base); i < 320; i++ {
		name := fmt.Sprintf("#rainbow%03d", i)
		base[name] = deriveHashtagChannelKey(name)
	}
	names := make([]string, 128)
	for i := range names {
		names[i] = fmt.Sprintf("#bench%03d", i)
	}
	withApproved, _ := mergeApprovedKeys(base, names)
	for _, c := range []struct {
		name string
		keys map[string]string
	}{{"configured", base}, {"plus128approved", withApproved}} {
		b.Run(c.name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				decodeGrpTxt(buf, c.keys)
			}
		})
	}
}

func TestApprovedChannelsConfig(t *testing.T) {
	t.Setenv("DB_PATH", "")
	dir := t.TempDir()
	dbPath := filepath.ToSlash(filepath.Join(dir, "data", "meshcore.db"))
	load := func(um string) *Config {
		t.Helper()
		p := filepath.Join(dir, "config.json")
		if err := os.WriteFile(p, []byte(`{"dbPath": "`+dbPath+`"`+um+`}`), 0o644); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadConfig(p)
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	off := load(``)
	if off.ApprovedChannelsEnabled() || off.ApprovedChannelsMax() != 128 || off.UsersDBPath() != filepath.Join(dir, "data", "users.db") {
		t.Fatalf("no block: enabled=%v max=%d path=%q", off.ApprovedChannelsEnabled(), off.ApprovedChannelsMax(), off.UsersDBPath())
	}
	on := load(`, "userManagement": {"enabled": true, "adminEmails": ["a@example.org"], "mail": {"provider": "brevo"}, "channelProposals": {"enabled": true}}`)
	if !on.ApprovedChannelsEnabled() || on.ApprovedChannelsMax() != 128 {
		t.Fatalf("on: enabled=%v max=%d", on.ApprovedChannelsEnabled(), on.ApprovedChannelsMax())
	}
	for _, um := range []string{
		`, "userManagement": {"enabled": false, "channelProposals": {"enabled": true}}`,
		`, "userManagement": {"enabled": true}`,
		`, "userManagement": {"enabled": true, "channelProposals": {"enabled": false}}`,
	} {
		if load(um).ApprovedChannelsEnabled() {
			t.Errorf("%s resolved as on", um)
		}
	}
	custom := load(`, "userManagement": {"enabled": true, "dbPath": "accounts/users.db", "channelProposals": {"enabled": true, "maxApproved": 7}}`)
	if custom.UsersDBPath() != "accounts/users.db" || custom.ApprovedChannelsMax() != 7 {
		t.Fatalf("custom: path=%q max=%d", custom.UsersDBPath(), custom.ApprovedChannelsMax())
	}
	if load(`, "userManagement": {"enabled": true, "channelProposals": {"enabled": true, "maxApproved": -3}}`).ApprovedChannelsMax() != 128 {
		t.Fatal("negative maxApproved must fall back to 128")
	}
}

// The startup line shows the absolute users.db path, so a relative path
// resolved against another working directory than the server's is visible.
func TestApprovedChannelsSourceLogIsAbsolute(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)
	logApprovedChannelsSource("users.db")
	want, err := filepath.Abs("users.db")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "[proposals] reading approved channels from "+want+"\n") {
		t.Fatalf("log line = %q, want the absolute path %q", buf.String(), want)
	}
}

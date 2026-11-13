package mbcapqueue

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func countSnapshots(t *testing.T, env pgEnv) int {
	t.Helper()
	var n int
	if err := env.owner.QueryRow(`SELECT count(*) FROM "` + env.schema + `".mbcap_snapshots`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPGPruneKeepsNewestN(t *testing.T) {
	env := newPGEnv(t)
	srv, ing := NewPG(env.server, env.schema), NewPG(env.owner, env.schema)
	for _, k := range []string{"k1", "k2", "k3", "k4"} {
		if err := srv.WriteSnapshot(Snapshot{Entries: []Entry{{PublicKey: k, Status: "confirmed"}}}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	if err := ing.Prune(2); err != nil {
		t.Fatal(err)
	}
	if n := countSnapshots(t, env); n != 2 {
		t.Fatalf("rows after Prune(2) = %d", n)
	}
	if snap, err := ing.ReadSnapshot(); err != nil || snap.Entries[0].PublicKey != "k4" {
		t.Fatalf("newest after Prune(2): %+v %v", snap, err)
	}
	if err := ing.Prune(0); err != nil {
		t.Fatal(err)
	}
	if _, err := ing.ReadSnapshot(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("after Prune(0): want ErrNotExist, got %v", err)
	}
	// The server role cannot prune.
	if err := srv.Prune(0); err == nil {
		t.Fatal("server role pruned snapshots")
	}
}

func TestPGOversizeSnapshotRejectedBeforeSend(t *testing.T) {
	env := newPGEnv(t)
	ents := make([]Entry, 40_000)
	for i := range ents {
		ents[i] = Entry{PublicKey: strings.Repeat("a", 64), Status: "confirmed", Evidence: "x"}
	}
	err := NewPG(env.server, env.schema).WriteSnapshot(Snapshot{Entries: ents})
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("want size error, got %v", err)
	}
	if n := countSnapshots(t, env); n != 0 {
		t.Fatalf("rows after rejected write: %d", n)
	}
}

func TestPGMalformedSnapshotIsUnmarshalError(t *testing.T) {
	env := newPGEnv(t)
	if _, err := env.owner.Exec(`INSERT INTO "` + env.schema + `".mbcap_snapshots VALUES ('00000000000000000000000000000000', '2026-01-01T00:00:00.000000000Z', '{not json')`); err != nil {
		t.Fatal(err)
	}
	// The ingestor classifies this by the "unmarshal" prefix.
	if _, err := NewPG(env.owner, env.schema).ReadSnapshot(); err == nil || !strings.HasPrefix(err.Error(), "unmarshal:") {
		t.Fatalf("want unmarshal error, got %v", err)
	}
}

func TestPGSchemaDefaultAndValidation(t *testing.T) {
	if got := NewPG(nil, "").Location(); got != "postgres:corescope_control" {
		t.Fatalf("default Location = %q", got)
	}
	for _, bad := range []string{`x"; DROP TABLE y; --`, "Upper", "1abc", strings.Repeat("a", 64)} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("NewPG accepted schema %q", bad)
				}
			}()
			NewPG(nil, bad)
		}()
	}
}

func TestPGServerCannotChooseCreatedAt(t *testing.T) {
	env := newPGEnv(t)
	_, err := env.server.Exec(`INSERT INTO "` + env.schema + `".mbcap_snapshots (id, created_at, payload) VALUES ('00000000000000000000000000000001', '2999-01-01T00:00:00.000000000Z', '{}')`)
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("explicit created_at from the server role: want permission denied, got %v", err)
	}
	if err := NewPG(env.server, env.schema).WriteSnapshot(Snapshot{}); err != nil {
		t.Fatal(err)
	}
}

func TestPGNewestIsByDatabaseCreatedAt(t *testing.T) {
	env := newPGEnv(t)
	srv, ing := NewPG(env.server, env.schema), NewPG(env.owner, env.schema)
	for _, k := range []string{"k1", "k2"} {
		if err := srv.WriteSnapshot(Snapshot{Entries: []Entry{{PublicKey: k, Status: "confirmed"}}}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if snap, err := ing.ReadSnapshot(); err != nil || snap.Entries[0].PublicKey != "k2" {
		t.Fatalf("newest: %+v %v", snap, err)
	}
	// A far-future row (only the owner could write one) is then the newest,
	// proving the order follows created_at and not the random id.
	if _, err := env.owner.Exec(`UPDATE "` + env.schema + `".mbcap_snapshots SET created_at = '2999-01-01T00:00:00.000000000Z' WHERE payload LIKE '%k1%'`); err != nil {
		t.Fatal(err)
	}
	if snap, err := ing.ReadSnapshot(); err != nil || snap.Entries[0].PublicKey != "k1" {
		t.Fatalf("newest after reorder: %+v %v", snap, err)
	}
}

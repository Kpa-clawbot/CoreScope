package prunequeue

import (
	"strings"
	"testing"
	"time"
)

func TestPGServerRoleIsRestricted(t *testing.T) {
	env := newPGEnv(t)
	// The conformance tests only mean something if the server role really
	// lacks these privileges.
	for _, stmt := range []string{
		`DELETE FROM "` + env.schema + `".prune_requests`,
		`UPDATE "` + env.schema + `".prune_requests SET payload = ''`,
		`INSERT INTO "` + env.schema + `".prune_results (id, created_at, payload) VALUES ('0000000000000000', '', '')`,
		`DELETE FROM "` + env.schema + `".mbcap_snapshots`,
	} {
		if _, err := env.server.Exec(stmt); err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("%s: want permission denied, got %v", stmt, err)
		}
	}
	// The ingestor-side calls are refused too, so the server cannot complete
	// or prune by mistake.
	q := NewPG(env.server, env.schema)
	if err := q.Complete(Result{ID: NewID()}); err == nil {
		t.Error("server role completed a request")
	}
	if err := q.PruneResults(0); err == nil {
		t.Error("server role pruned results")
	}
}

func TestPGOversizePayloadRejectedBeforeSend(t *testing.T) {
	env := newPGEnv(t)
	q := NewPG(env.server, env.schema)
	// ~70 bytes of indented JSON per pubkey pushes this past 1 MiB.
	keys := make([]string, 20_000)
	for i := range keys {
		keys[i] = strings.Repeat("a", 64)
	}
	err := q.WriteRequest(Request{ID: NewID(), RequestedAt: time.Now().UTC(), Pubkeys: keys})
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("want size error, got %v", err)
	}
	var n int
	if err := env.owner.QueryRow(`SELECT count(*) FROM "` + env.schema + `".prune_requests`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rows after rejected write: %d %v", n, err)
	}
}

func TestPGCreatedAtFormat(t *testing.T) {
	env := newPGEnv(t)
	id := NewID()
	if err := NewPG(env.server, env.schema).WriteRequest(Request{ID: id}); err != nil {
		t.Fatal(err)
	}
	var s string
	if err := env.owner.QueryRow(`SELECT created_at FROM "`+env.schema+`".prune_requests WHERE id = $1`, id).Scan(&s); err != nil {
		t.Fatal(err)
	}
	ts, err := time.Parse(time.RFC3339Nano, s)
	if err != nil || !strings.HasSuffix(s, "Z") || len(s) != len("2006-01-02T15:04:05.000000000Z") {
		t.Fatalf("created_at %q: %v", s, err)
	}
	if d := time.Since(ts); d < -time.Minute || d > time.Minute {
		t.Fatalf("created_at %q is %s from now", s, d)
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
	_, err := env.server.Exec(`INSERT INTO "`+env.schema+`".prune_requests (id, created_at, payload) VALUES ($1, '1999-01-01T00:00:00.000000000Z', '{}')`, NewID())
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("explicit created_at from the server role: want permission denied, got %v", err)
	}
	// The default fills it in for the permitted column list.
	if err := NewPG(env.server, env.schema).WriteRequest(Request{ID: NewID()}); err != nil {
		t.Fatal(err)
	}
}

func TestPGOrderIsByDatabaseCreatedAt(t *testing.T) {
	env := newPGEnv(t)
	srv, ing := NewPG(env.server, env.schema), NewPG(env.owner, env.schema)
	// Written first, but with the larger id: only created_at can order it first.
	first, second := "ffffffffffffffff", "0000000000000001"
	for _, id := range []string{first, second} {
		if err := srv.WriteRequest(Request{ID: id}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if ids, err := ing.ListPending(); err != nil || len(ids) != 2 || ids[0] != first || ids[1] != second {
		t.Fatalf("ListPending: %v %v", ids, err)
	}
	// Moving the first row's database-assigned time later flips the order.
	if _, err := env.owner.Exec(`UPDATE "`+env.schema+`".prune_requests SET created_at = '2999-01-01T00:00:00.000000000Z' WHERE id = $1`, first); err != nil {
		t.Fatal(err)
	}
	if ids, err := ing.ListPending(); err != nil || len(ids) != 2 || ids[0] != second || ids[1] != first {
		t.Fatalf("ListPending after reorder: %v %v", ids, err)
	}
}

package mbcapqueue

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// storePair is one implementation seen from both processes. On Postgres the
// server Store runs as a role holding only the server's grants, so the
// conformance steps also prove the server path needs no UPDATE/DELETE.
type storePair struct {
	server, ingestor Store
}

func forEachImpl(t *testing.T, fn func(t *testing.T, p storePair)) {
	impls := map[string]func(t *testing.T) storePair{
		"fs": func(t *testing.T) storePair {
			s := NewFS(filepath.Join(t.TempDir(), "x.db"))
			return storePair{server: s, ingestor: s}
		},
		"pg": func(t *testing.T) storePair {
			env := newPGEnv(t)
			return storePair{server: NewPG(env.server, env.schema), ingestor: NewPG(env.owner, env.schema)}
		},
	}
	for name, mk := range impls {
		t.Run(name, func(t *testing.T) { fn(t, mk(t)) })
	}
}

func TestConformanceSnapshots(t *testing.T) {
	forEachImpl(t, func(t *testing.T, p storePair) {
		srv, ing := p.server, p.ingestor

		snap, err := ing.ReadSnapshot()
		if !errors.Is(err, os.ErrNotExist) || snap.WrittenAt != (time.Time{}) || snap.Entries != nil {
			t.Fatalf("empty store: %+v %v", snap, err)
		}
		if err := ing.Prune(1); err != nil {
			t.Fatalf("Prune on empty store: %v", err)
		}

		at := time.Date(2026, 3, 4, 5, 6, 7, 8, time.UTC)
		if err := srv.WriteSnapshot(Snapshot{WrittenAt: at, Entries: []Entry{{PublicKey: "aa", Status: "confirmed", Evidence: "e"}}}); err != nil {
			t.Fatal(err)
		}
		snap, err = ing.ReadSnapshot()
		if err != nil || !snap.WrittenAt.Equal(at) || len(snap.Entries) != 1 || snap.Entries[0] != (Entry{PublicKey: "aa", Status: "confirmed", Evidence: "e"}) {
			t.Fatalf("round trip: %+v %v", snap, err)
		}

		// The newest write wins; a zero WrittenAt is stamped.
		time.Sleep(5 * time.Millisecond)
		if err := srv.WriteSnapshot(Snapshot{Entries: []Entry{{PublicKey: "bb", Status: "suspected"}}}); err != nil {
			t.Fatal(err)
		}
		snap, err = ing.ReadSnapshot()
		if err != nil || len(snap.Entries) != 1 || snap.Entries[0].PublicKey != "bb" || snap.WrittenAt.IsZero() {
			t.Fatalf("newest wins: %+v %v", snap, err)
		}

		if err := ing.Prune(1); err != nil {
			t.Fatal(err)
		}
		if snap, err = ing.ReadSnapshot(); err != nil || snap.Entries[0].PublicKey != "bb" {
			t.Fatalf("after Prune(1): %+v %v", snap, err)
		}
		if err := ing.Prune(-1); err == nil {
			t.Fatal("negative keep accepted")
		}
		if srv.Location() == "" {
			t.Fatal("empty Location")
		}
	})
}

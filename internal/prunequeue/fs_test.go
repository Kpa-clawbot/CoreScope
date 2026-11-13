package prunequeue

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestFSListPendingSkipsInvalidNamesAndOrdersByMtime(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "x.db")
	q := NewFS(dbPath)
	for _, id := range []string{idOld, idNew} {
		if err := q.WriteRequest(Request{ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	// Explicit mtimes, so the order does not depend on timestamp granularity.
	base := time.Now().Add(-time.Hour)
	for id, at := range map[string]time.Time{idOld: base, idNew: base.Add(time.Second)} {
		p, _ := RequestPath(dbPath, id)
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(QueueDir(dbPath), "request-not-hex.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if ids, err := q.ListPending(); err != nil || !reflect.DeepEqual(ids, []string{idOld, idNew}) {
		t.Fatalf("ListPending: %v %v", ids, err)
	}
}

func TestFSPruneResultsMissingDir(t *testing.T) {
	q := NewFS(filepath.Join(t.TempDir(), "x.db"))
	if err := q.PruneResults(0); err != nil {
		t.Fatal(err)
	}
	if ids, err := q.ListPending(); err != nil || len(ids) != 0 {
		t.Fatalf("ListPending missing dir: %v %v", ids, err)
	}
}

func TestFSLocation(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "x.db")
	if got := NewFS(dbPath).Location(); got != QueueDir(dbPath) {
		t.Fatalf("Location = %q", got)
	}
}

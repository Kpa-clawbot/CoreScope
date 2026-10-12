package users

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSnapshotCopiesRows(t *testing.T) {
	st, _ := newTestStore(t)
	u := mustCreate(t, st, "a@example.org", "Aaa")
	uid := u.ID
	if err := st.Audit(&uid, "user.register", &uid, nil); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "snap.db")
	if err := st.Snapshot(path); err != nil {
		t.Fatal(err)
	}
	cp, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer cp.Close()
	got, err := cp.GetByEmail("a@example.org")
	if err != nil || got.ID != u.ID || got.DisplayName != "Aaa" {
		t.Fatalf("user in snapshot = %+v, %v", got, err)
	}
	if v, err := cp.SchemaVersion(); err != nil || v != len(migrations) {
		t.Fatalf("snapshot schema version = %d, %v; want %d", v, err, len(migrations))
	}
	if e, err := cp.AuditAllFor(u.ID); err != nil || len(e) != 1 {
		t.Fatalf("audit in snapshot = %d, %v", len(e), err)
	}
}

func TestSnapshotFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix file modes")
	}
	st, _ := newTestStore(t)
	path := filepath.Join(t.TempDir(), "snap.db")
	if err := st.Snapshot(path); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v; want 0600", fi.Mode().Perm())
	}
}

func TestSnapshotRefusesExistingTarget(t *testing.T) {
	st, _ := newTestStore(t)
	path := filepath.Join(t.TempDir(), "snap.db")
	// An empty file: VACUUM INTO alone would accept and fill it.
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err := st.Snapshot(path)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("err = %v; want already exists", err)
	}
	if fi, _ := os.Stat(path); fi == nil || fi.Size() != 0 {
		t.Fatal("the existing file was changed")
	}
}

func TestSnapshotFailureLeavesNoFile(t *testing.T) {
	st, _ := newTestStore(t)
	st.Close()
	path := filepath.Join(t.TempDir(), "snap.db")
	if err := st.Snapshot(path); err == nil {
		t.Fatal("snapshot of a closed store succeeded")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("partial snapshot left behind: %v", err)
	}
}

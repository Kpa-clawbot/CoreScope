package mbcapqueue

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	db := filepath.Join(t.TempDir(), "x.db")
	if _, err := ReadSnapshot(db); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("want ErrNotExist, got %v", err)
	}
	in := Snapshot{Entries: []Entry{{PublicKey: "aa", Status: "confirmed", Evidence: "e"}}}
	if err := WriteSnapshot(db, in); err != nil {
		t.Fatal(err)
	}
	out, err := ReadSnapshot(db)
	if err != nil || len(out.Entries) != 1 || out.Entries[0].PublicKey != "aa" || out.WrittenAt.IsZero() {
		t.Fatalf("round trip: %+v %v", out, err)
	}
	fi, _ := os.Lstat(SnapshotPath(db))
	if fi.Mode().Perm() != 0o644 {
		t.Errorf("mode %v", fi.Mode().Perm())
	}
	ents, _ := os.ReadDir(QueueDir(db))
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover temp %s", e.Name())
		}
	}
}

func TestWriteDoesNotFollowPlantedSymlinks(t *testing.T) {
	db := filepath.Join(t.TempDir(), "x.db")
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("precious"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := EnsureDir(db); err != nil {
		t.Fatal(err)
	}
	for _, l := range []string{SnapshotPath(db) + ".tmp", SnapshotPath(db)} {
		if err := os.Symlink(victim, l); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}
	if err := WriteSnapshot(db, Snapshot{}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "precious" {
		t.Fatalf("symlink target written through: %q", b)
	}
	if fi, err := os.Lstat(SnapshotPath(db)); err != nil || !fi.Mode().IsRegular() {
		t.Fatalf("snapshot not regular: %v %v", fi, err)
	}
}

func TestReadRejectsSymlinkAndOversize(t *testing.T) {
	db := filepath.Join(t.TempDir(), "x.db")
	if err := EnsureDir(db); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "t.json")
	if err := os.WriteFile(target, []byte(`{"entries":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, SnapshotPath(db)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := ReadSnapshot(db); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("symlink snapshot should be rejected, got %v", err)
	}
	_ = os.Remove(SnapshotPath(db))
	if err := os.WriteFile(SnapshotPath(db), bytes.Repeat([]byte(" "), maxFileBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSnapshot(db); err == nil {
		t.Fatal("oversize snapshot should be rejected")
	}
}

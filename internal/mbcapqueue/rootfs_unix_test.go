//go:build unix

package mbcapqueue

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestQueueDirSwappedForSymlinkIsNeverFollowed(t *testing.T) {
	db := filepath.Join(t.TempDir(), "x.db")
	if err := WriteSnapshot(db, Snapshot{Entries: []Entry{{PublicKey: "aa", Status: "confirmed"}}}); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, SnapshotFileName), []byte(`{"entries":[{"public_key":"evil","status":"confirmed"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	qd := QueueDir(db)
	if err := os.Rename(qd, qd+".orig"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, qd); err != nil {
		t.Fatal(err)
	}
	if err := WriteSnapshot(db, Snapshot{}); err == nil {
		t.Error("WriteSnapshot should fail through a symlinked queue dir")
	}
	if err := EnsureDir(db); err == nil {
		t.Error("EnsureDir should refuse a symlinked queue dir")
	}
	if _, err := ReadSnapshot(db); err == nil {
		t.Error("ReadSnapshot must not read through a symlinked queue dir")
	}
	ents, _ := os.ReadDir(other)
	if len(ents) != 1 {
		t.Errorf("other dir was modified: %v", ents)
	}
}

func TestFIFOSnapshotReturnsPromptly(t *testing.T) {
	db := filepath.Join(t.TempDir(), "x.db")
	if err := EnsureDir(db); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(SnapshotPath(db), 0o644); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	done := make(chan error, 1)
	go func() { _, err := ReadSnapshot(db); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("ReadSnapshot on FIFO should error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ReadSnapshot blocked on FIFO")
	}
}

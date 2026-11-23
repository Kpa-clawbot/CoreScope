//go:build unix

package prunequeue

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// within fails the test if fn does not return within a short deadline (a FIFO
// open without O_NONBLOCK would hang forever).
func within(t *testing.T, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); fn() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("operation blocked")
	}
}

func TestQueueDirSwappedForSymlinkIsNeverFollowed(t *testing.T) {
	base := t.TempDir()
	dbPath := filepath.Join(base, "x.db")
	id := NewID()
	if err := WriteRequest(dbPath, Request{ID: id, Pubkeys: []string{"aa"}}); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "result-"+id+".json"), []byte(`{"id":"`+id+`","deleted":9}`), 0o644); err != nil {
		t.Fatal(err)
	}
	qd := QueueDir(dbPath)
	if err := os.Rename(qd, qd+".orig"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, qd); err != nil {
		t.Fatal(err)
	}

	if err := WriteRequest(dbPath, Request{ID: NewID()}); err == nil {
		t.Error("WriteRequest should fail through a symlinked queue dir")
	}
	if err := WriteResult(dbPath, Result{ID: id}); err == nil {
		t.Error("WriteResult should fail through a symlinked queue dir")
	}
	if _, err := ReadResult(dbPath, id); err == nil {
		t.Error("ReadResult must not read through a symlinked queue dir")
	}
	if ok, err := RequestExists(dbPath, id); ok || err == nil {
		t.Errorf("RequestExists: %v %v", ok, err)
	}
	if l, err := ListPending(dbPath); len(l) != 0 || err == nil {
		t.Errorf("ListPending: %v %v", l, err)
	}
	if _, err := ReadRequest(filepath.Join(qd, "request-"+id+".json")); err == nil {
		t.Error("ReadRequest must not read through a symlinked queue dir")
	}
	ents, _ := os.ReadDir(other)
	if len(ents) != 1 {
		t.Errorf("other dir was modified: %v", ents)
	}
	if _, err := os.Stat(filepath.Join(qd+".orig", "request-"+id+".json")); err != nil {
		t.Errorf("original request should be untouched: %v", err)
	}
}

func TestFIFOReturnsPromptly(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "x.db")
	id := NewID()
	dir, err := EnsureDir(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"request-" + id + ".json", "result-" + id + ".json"} {
		if err := syscall.Mkfifo(filepath.Join(dir, n), 0o644); err != nil {
			t.Skipf("mkfifo: %v", err)
		}
	}
	within(t, func() {
		if _, err := ReadRequest(filepath.Join(dir, "request-"+id+".json")); err == nil {
			t.Error("ReadRequest on FIFO should error")
		}
		if _, err := ReadResult(dbPath, id); err == nil {
			t.Error("ReadResult on FIFO should error")
		}
		if ok, _ := RequestExists(dbPath, id); ok {
			t.Error("FIFO is not a pending request")
		}
	})
}

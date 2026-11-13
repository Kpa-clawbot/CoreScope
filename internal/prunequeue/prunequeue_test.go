package prunequeue

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "x.db")

	id := NewID()
	req := Request{ID: id, RequestedAt: time.Now().UTC(), Reason: "test", Pubkeys: []string{"aa", "bb"}}
	if err := WriteRequest(dbPath, req); err != nil {
		t.Fatal(err)
	}

	pending, err := RequestExists(dbPath, id)
	if err != nil || !pending {
		t.Fatalf("RequestExists: pending=%v err=%v", pending, err)
	}

	list, err := ListPending(dbPath)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListPending: %v / %v", list, err)
	}
	parsed, err := ReadRequest(list[0])
	if err != nil || parsed.ID != id || len(parsed.Pubkeys) != 2 {
		t.Fatalf("ReadRequest: %+v / %v", parsed, err)
	}

	// Writing the result removes the request marker.
	if err := WriteResult(dbPath, Result{ID: id, RequestedAt: req.RequestedAt, CompletedAt: time.Now().UTC(), Deleted: 2}); err != nil {
		t.Fatal(err)
	}
	pending, _ = RequestExists(dbPath, id)
	if pending {
		t.Error("request marker should be gone after WriteResult")
	}
	res, err := ReadResult(dbPath, id)
	if err != nil || res == nil || res.Deleted != 2 {
		t.Fatalf("ReadResult: %+v / %v", res, err)
	}
}

func TestRejectsBadIDs(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "x.db")
	for _, bad := range []string{"", "../escape", "abcg", "0123456789ABCDEFG/extra", "../../etc/passwd"} {
		if _, err := RequestPath(dbPath, bad); err == nil {
			t.Errorf("RequestPath should reject %q", bad)
		}
		if _, err := ResultPath(dbPath, bad); err == nil {
			t.Errorf("ResultPath should reject %q", bad)
		}
	}
}

func TestReadResultMissing(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "x.db")
	res, err := ReadResult(dbPath, "abcdef0123456789")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res != nil {
		t.Errorf("expected nil result, got %+v", res)
	}
}

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
}

func readFileStr(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestWriteDoesNotFollowPlantedSymlinks(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "x.db")
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("precious"), 0o600); err != nil {
		t.Fatal(err)
	}
	id := NewID()
	dir, _ := EnsureDir(dbPath)
	rp, _ := RequestPath(dbPath, id)
	resp, _ := ResultPath(dbPath, id)
	// Old predictable temp names and the final names.
	for _, l := range []string{rp + ".tmp", resp + ".tmp", rp, resp} {
		mustSymlink(t, victim, l)
	}
	if err := WriteRequest(dbPath, Request{ID: id, Pubkeys: []string{"aa"}}); err != nil {
		t.Fatal(err)
	}
	if err := WriteResult(dbPath, Result{ID: id, Deleted: 1}); err != nil {
		t.Fatal(err)
	}
	if got := readFileStr(t, victim); got != "precious" {
		t.Fatalf("symlink target was written through: %q", got)
	}
	fi, err := os.Lstat(resp)
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o644 {
		t.Fatalf("result not a regular 0644 file: %v %v", fi, err)
	}
	// No stray random temp files left behind.
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.Contains(e.Name(), ".tmp") && e.Type().IsRegular() {
			t.Errorf("leftover temp %s", e.Name())
		}
	}
}

func TestSymlinksSkippedOnRead(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "x.db")
	dir, _ := EnsureDir(dbPath)
	target := filepath.Join(t.TempDir(), "t.json")
	if err := os.WriteFile(target, []byte(`{"id":"abcdef0123456789","pubkeys":["aa"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	id := "abcdef0123456789"
	rp, _ := RequestPath(dbPath, id)
	resp, _ := ResultPath(dbPath, id)
	mustSymlink(t, target, rp)
	mustSymlink(t, target, resp)

	list, err := ListPending(dbPath)
	if err != nil || len(list) != 0 {
		t.Fatalf("ListPending should skip symlink: %v %v", list, err)
	}
	if ok, err := RequestExists(dbPath, id); ok || err != nil {
		t.Fatalf("RequestExists on symlink: %v %v", ok, err)
	}
	if _, err := ReadRequest(rp); err == nil {
		t.Error("ReadRequest should reject symlink")
	}
	if r, err := ReadResult(dbPath, id); err == nil || r != nil {
		t.Errorf("ReadResult should reject symlink: %v %v", r, err)
	}
	_ = dir
}

func TestOversizeRejected(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "x.db")
	dir, _ := EnsureDir(dbPath)
	p := filepath.Join(dir, "request-abcdef0123456789.json")
	if err := os.WriteFile(p, bytes.Repeat([]byte(" "), maxFileBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRequest(p); err == nil {
		t.Error("oversize request should be rejected")
	}
}

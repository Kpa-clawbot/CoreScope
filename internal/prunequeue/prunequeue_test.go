package prunequeue

import (
	"path/filepath"
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

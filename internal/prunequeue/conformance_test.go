package prunequeue

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// queuePair is one implementation seen from both processes. On Postgres the
// server Queue runs as a role holding only the server's grants, so every
// conformance step also proves the server path needs no UPDATE/DELETE.
type queuePair struct {
	server, ingestor Queue
}

func implementations(t *testing.T) map[string]func(t *testing.T) queuePair {
	return map[string]func(t *testing.T) queuePair{
		"fs": func(t *testing.T) queuePair {
			q := NewFS(filepath.Join(t.TempDir(), "x.db"))
			return queuePair{server: q, ingestor: q}
		},
		"pg": func(t *testing.T) queuePair {
			env := newPGEnv(t)
			return queuePair{server: NewPG(env.server, env.schema), ingestor: NewPG(env.owner, env.schema)}
		},
	}
}

func forEachImpl(t *testing.T, fn func(t *testing.T, p queuePair)) {
	for name, mk := range implementations(t) {
		t.Run(name, func(t *testing.T) { fn(t, mk(t)) })
	}
}

// Ids chosen so lexicographic order is the reverse of write order.
const (
	idOld = "ffffffffffffffff"
	idNew = "0000000000000001"
)

func TestConformanceLifecycle(t *testing.T) {
	forEachImpl(t, func(t *testing.T, p queuePair) {
		srv, ing := p.server, p.ingestor

		if ids, err := ing.ListPending(); err != nil || len(ids) != 0 {
			t.Fatalf("empty ListPending: %v %v", ids, err)
		}
		if r, err := srv.ReadResult(idOld); r != nil || err != nil {
			t.Fatalf("ReadResult unknown id: %v %v", r, err)
		}
		if ok, err := srv.RequestExists(idOld); ok || err != nil {
			t.Fatalf("RequestExists unknown id: %v %v", ok, err)
		}
		if _, err := ing.ReadRequest(idOld); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("ReadRequest unknown id: want ErrNotExist, got %v", err)
		}

		reqAt := time.Date(2026, 1, 2, 3, 4, 5, 6, time.UTC)
		old := Request{ID: idOld, RequestedAt: reqAt, Reason: "geo-prune", Pubkeys: []string{"aa", "bb"}}
		if err := srv.WriteRequest(old); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
		if err := srv.WriteRequest(Request{ID: idNew, RequestedAt: reqAt, Pubkeys: []string{"cc"}}); err != nil {
			t.Fatal(err)
		}

		if ids, err := ing.ListPending(); err != nil || !reflect.DeepEqual(ids, []string{idOld, idNew}) {
			t.Fatalf("ListPending oldest first: %v %v", ids, err)
		}
		if ok, err := srv.RequestExists(idOld); !ok || err != nil {
			t.Fatalf("RequestExists pending: %v %v", ok, err)
		}
		if r, err := srv.ReadResult(idOld); r != nil || err != nil {
			t.Fatalf("ReadResult before completion: %v %v", r, err)
		}
		got, err := ing.ReadRequest(idOld)
		if err != nil {
			t.Fatal(err)
		}
		if got.ID != old.ID || !got.RequestedAt.Equal(reqAt) || got.Reason != old.Reason || !reflect.DeepEqual(got.Pubkeys, old.Pubkeys) {
			t.Fatalf("ReadRequest round trip: %+v", got)
		}

		done := time.Date(2026, 1, 2, 3, 5, 0, 0, time.UTC)
		if err := ing.Complete(Result{ID: idOld, RequestedAt: reqAt, CompletedAt: done, Deleted: 2}); err != nil {
			t.Fatal(err)
		}
		if ok, err := srv.RequestExists(idOld); ok || err != nil {
			t.Fatalf("RequestExists after Complete: %v %v", ok, err)
		}
		res, err := srv.ReadResult(idOld)
		if err != nil || res == nil || res.ID != idOld || res.Deleted != 2 || !res.CompletedAt.Equal(done) || res.Error != "" {
			t.Fatalf("ReadResult after Complete: %+v %v", res, err)
		}
		if ids, err := ing.ListPending(); err != nil || !reflect.DeepEqual(ids, []string{idNew}) {
			t.Fatalf("ListPending after Complete: %v %v", ids, err)
		}

		// Completing again replaces the result (an error result here).
		if err := ing.Complete(Result{ID: idOld, Error: "boom"}); err != nil {
			t.Fatal(err)
		}
		if res, err := srv.ReadResult(idOld); err != nil || res == nil || res.Error != "boom" || res.Deleted != 0 {
			t.Fatalf("ReadResult after second Complete: %+v %v", res, err)
		}

		// Results younger than the age survive; older ones go. Requests are
		// never touched.
		if err := ing.PruneResults(time.Hour); err != nil {
			t.Fatal(err)
		}
		if res, _ := srv.ReadResult(idOld); res == nil {
			t.Fatal("PruneResults(1h) removed a fresh result")
		}
		time.Sleep(5 * time.Millisecond)
		if err := ing.PruneResults(time.Millisecond); err != nil {
			t.Fatal(err)
		}
		if res, err := srv.ReadResult(idOld); res != nil || err != nil {
			t.Fatalf("PruneResults(1ms) kept an old result: %+v %v", res, err)
		}
		if ok, _ := srv.RequestExists(idNew); !ok {
			t.Fatal("PruneResults removed a pending request")
		}
		if err := ing.PruneResults(-time.Second); err == nil {
			t.Fatal("negative PruneResults age accepted")
		}
		if srv.Location() == "" {
			t.Fatal("empty Location")
		}
	})
}

func TestConformanceRejectsBadIDs(t *testing.T) {
	forEachImpl(t, func(t *testing.T, p queuePair) {
		for _, bad := range []string{"", "../escape", "abcg", "0123456789abcdefg", strings.Repeat("a", 65), "../../etc/passwd"} {
			isInvalid := func(op string, err error) {
				t.Helper()
				// The server's status handler matches this text to answer 400.
				if err == nil || !strings.Contains(err.Error(), "invalid prune request id") {
					t.Errorf("%s(%q): want invalid id error, got %v", op, bad, err)
				}
			}
			isInvalid("WriteRequest", p.server.WriteRequest(Request{ID: bad}))
			_, err := p.server.RequestExists(bad)
			isInvalid("RequestExists", err)
			_, err = p.server.ReadResult(bad)
			isInvalid("ReadResult", err)
			_, err = p.ingestor.ReadRequest(bad)
			isInvalid("ReadRequest", err)
			isInvalid("Complete", p.ingestor.Complete(Result{ID: bad}))
		}
	})
}

func TestConformanceListPendingBounded(t *testing.T) {
	forEachImpl(t, func(t *testing.T, p queuePair) {
		const total = MaxPendingList + 7
		want := make([]string, 0, total)
		for i := 0; i < total; i++ {
			id := fmt.Sprintf("%016x", total-i) // id order is the reverse of write order
			want = append(want, id)
			if err := p.server.WriteRequest(Request{ID: id}); err != nil {
				t.Fatal(err)
			}
			// Distinct write times on both clocks (file mtime and database).
			time.Sleep(2 * time.Millisecond)
		}
		ids, err := p.ingestor.ListPending()
		if err != nil || !reflect.DeepEqual(ids, want[:MaxPendingList]) {
			t.Fatalf("ListPending: %d ids, err %v (want the oldest %d)", len(ids), err, MaxPendingList)
		}
		if n, err := p.ingestor.PendingCount(); err != nil || n != total {
			t.Fatalf("PendingCount = %d, %v; want %d", n, err, total)
		}
		for _, id := range ids {
			if err := p.ingestor.Complete(Result{ID: id}); err != nil {
				t.Fatal(err)
			}
		}
		rest, err := p.ingestor.ListPending()
		if err != nil || !reflect.DeepEqual(rest, want[MaxPendingList:]) {
			t.Fatalf("ListPending after draining: %v %v", rest, err)
		}
		if n, _ := p.ingestor.PendingCount(); n != total-MaxPendingList {
			t.Fatalf("PendingCount after draining = %d", n)
		}
	})
}

func TestConformanceOversizeRejected(t *testing.T) {
	forEachImpl(t, func(t *testing.T, p queuePair) {
		keys := make([]string, 20_000) // ~1.4 MiB of indented JSON
		for i := range keys {
			keys[i] = strings.Repeat("a", 64)
		}
		err := p.server.WriteRequest(Request{ID: NewID(), Pubkeys: keys})
		if err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("WriteRequest: want size error, got %v", err)
		}
		err = p.ingestor.Complete(Result{ID: NewID(), Error: strings.Repeat("x", maxFileBytes)})
		if err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("Complete: want size error, got %v", err)
		}
		if n, err := p.ingestor.PendingCount(); err != nil || n != 0 {
			t.Fatalf("PendingCount after rejected writes: %d %v", n, err)
		}
	})
}

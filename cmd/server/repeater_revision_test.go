package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func editAt(base int64, fields map[string]any) map[string]any {
	out := map[string]any{"baseRevision": base}
	for k, v := range fields {
		out[k] = v
	}
	return out
}

func TestRepeaterRevisionOnReads(t *testing.T) {
	f := repeaterFixture(t)
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	if got := decode[repeaterInfoJSON](t, f.do("GET", repPath(rpkA, "info"), nil)); got.Revision != 0 {
		t.Fatalf("revision without details = %d, want 0", got.Revision)
	}
	got := decode[repeaterInfoJSON](t, f.do("PUT", repPath(rpkA, "info"), map[string]any{"notes": "a"}, as(alice)))
	if got.Revision != 1 {
		t.Fatalf("revision after the first save = %d", got.Revision)
	}
	// Claims are not details: they leave the revision alone.
	got = decode[repeaterInfoJSON](t, f.do("POST", repPath(rpkA, "claim"), nil, as(alice)))
	if got.Revision != 1 {
		t.Fatalf("revision after a claim = %d", got.Revision)
	}
	list := decode[repeaterInfoListJSON](t, f.do("GET", "/api/repeaters/info", nil))
	if len(list.Repeaters) != 1 || list.Repeaters[0].Revision != 1 {
		t.Fatalf("list = %+v", list)
	}
}

// Two clients start from the same copy: the first save wins, the second gets
// 409 with what is stored now and changes nothing.
func TestRepeaterEditBaseRevisionConflict(t *testing.T) {
	f := repeaterFixture(t)
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	f.registerAndActivate(t, "bob@example.org", "Bob", pw)
	bob := f.scopedToken(t, "bob@example.org", "Bob", deviceScopeRepeaterEdit)

	// First edit of a repeater with no details yet.
	w := f.do("PUT", repPath(rpkA, "info"), editAt(0, map[string]any{"notes": "from Alice"}), as(alice))
	expectStatus(t, w, 200)
	if got := decode[repeaterInfoJSON](t, w); got.Revision != 1 || got.Notes != "from Alice" {
		t.Fatalf("Alice's save = %+v", got)
	}
	w = f.do("PUT", repPath(rpkA, "info"), editAt(0, map[string]any{"notes": "from Bob"}), bearer(bob))
	expectStatus(t, w, http.StatusConflict)
	got := decode[repeaterInfoJSON](t, w)
	if got.Revision != 1 || got.Notes != "from Alice" || got.CanEdit == nil || !*got.CanEdit {
		t.Fatalf("409 body = %+v, want the stored details with caller fields", got)
	}
	// Bob merges and retries from the revision the 409 gave him.
	w = f.do("PUT", repPath(rpkA, "info"), editAt(got.Revision, map[string]any{"notes": "from Alice\nand Bob"}), bearer(bob))
	expectStatus(t, w, 200)
	if got := decode[repeaterInfoJSON](t, w); got.Revision != 2 || got.UpdatedBy == nil || *got.UpdatedBy != "Bob" {
		t.Fatalf("Bob's retry = %+v", got)
	}
	// Alice's copy is now stale, even for a body that would change nothing.
	expectStatus(t, f.do("PUT", repPath(rpkA, "info"), editAt(1, map[string]any{"notes": "from Alice\nand Bob"}), as(alice)), http.StatusConflict)
	// One audit row per accepted save; the refused ones wrote none.
	if rows := auditOf(t, f, alice.me.ID, "repeater.edit"); len(rows) != 1 {
		t.Fatalf("Alice's edit audit rows = %d, want 1", len(rows))
	}
}

// Without baseRevision a save is unconditional, as the browser editor sends it.
func TestRepeaterEditWithoutBaseRevision(t *testing.T) {
	f := repeaterFixture(t)
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	for i, notes := range []string{"one", "two"} {
		w := f.do("PUT", repPath(rpkA, "info"), map[string]any{"notes": notes}, as(alice))
		expectStatus(t, w, 200)
		if got := decode[repeaterInfoJSON](t, w); got.Revision != int64(i+1) {
			t.Fatalf("save %d revision = %d", i+1, got.Revision)
		}
	}
	// An unchanged save writes nothing, so the revision stays.
	if got := decode[repeaterInfoJSON](t, f.do("PUT", repPath(rpkA, "info"), map[string]any{"notes": "two"}, as(alice))); got.Revision != 2 {
		t.Fatalf("revision after an unchanged save = %d", got.Revision)
	}
	if got := decode[repeaterInfoJSON](t, f.do("PUT", repPath(rpkA, "info"), editAt(2, map[string]any{"notes": "two"}), as(alice))); got.Revision != 2 {
		t.Fatalf("revision after an unchanged conditional save = %d", got.Revision)
	}
}

func TestRepeaterEditBaseRevisionValidation(t *testing.T) {
	f := repeaterFixture(t)
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	w := f.do("PUT", repPath(rpkA, "info"), editAt(-1, map[string]any{"notes": "x"}), as(alice))
	expectStatus(t, w, 400)
	if !strings.Contains(w.Body.String(), "baseRevision") {
		t.Fatalf("body = %s", w.Body.String())
	}
	// Permission is checked before the revision: no 409 leaks the details.
	alicePK := rpkA
	expectStatus(t, f.do("POST", repPath(alicePK, "claim"), nil, as(alice)), 200)
	bob := f.registerAndActivate(t, "bob@example.org", "Bob", pw)
	expectStatus(t, f.do("PUT", repPath(rpkA, "info"), editAt(7, map[string]any{"notes": "y"}), as(bob)), 403)
}

// Two requests from the same revision at the same time: one 200, one 409.
func TestRepeaterEditConcurrentSaves(t *testing.T) {
	f := repeaterFixture(t)
	f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	f.registerAndActivate(t, "bob@example.org", "Bob", pw)
	tokens := []string{
		f.scopedToken(t, "alice@example.org", "Alice", deviceScopeRepeaterEdit),
		f.scopedToken(t, "bob@example.org", "Bob", deviceScopeRepeaterEdit),
	}
	for _, base := range []int64{0, 1} {
		codes := make([]int, len(tokens))
		var wg sync.WaitGroup
		for i, tok := range tokens {
			wg.Add(1)
			go func(i int, tok string) {
				defer wg.Done()
				body := editAt(base, map[string]any{"notes": "client " + string(rune('A'+i)) + " at " + string(rune('0'+base))})
				codes[i] = f.serve("PUT", repPath(rpkA, "info"), body, bearer(tok)).Code
			}(i, tok)
		}
		wg.Wait()
		f.srv.auth.waitAudits()
		ok, conflict := 0, 0
		for _, c := range codes {
			switch c {
			case 200:
				ok++
			case http.StatusConflict:
				conflict++
			}
		}
		if ok != 1 || conflict != 1 {
			t.Fatalf("base %d: codes %v, want one 200 and one 409", base, codes)
		}
	}
	if info, err := f.st.GetRepeaterInfo(rpkA); err != nil || info.Revision != 2 {
		t.Fatalf("stored = %+v, %v", info, err)
	}
}

func TestClientConfigAnnouncesRepeaterTokens(t *testing.T) {
	srv, router := setupTestServer(t)
	get := func() string {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", "/api/config/client", nil))
		return w.Body.String()
	}
	if strings.Contains(get(), "repeaterTokens") {
		t.Fatal("repeaterTokens present with user management off")
	}
	a, _ := newTestAuthService(t)
	srv.auth = a
	if !strings.Contains(get(), `"repeaterTokens":true`) {
		t.Fatalf("repeaterTokens missing with user management on: %s", get())
	}
}

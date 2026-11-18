package main

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/meshcore-analyzer/users"
)

const rpkGone = "dd44dd44dd44dd44dd44dd44dd44dd44dd44dd44dd44dd44dd44dd44dd44dd44"

func TestMyRepeatersEmpty(t *testing.T) {
	f := repeaterFixture(t)
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	w := f.do("GET", "/api/account/repeaters", nil, as(alice))
	expectStatus(t, w, http.StatusOK)
	if strings.TrimSpace(w.Body.String()) != `{"repeaters":[]}` {
		t.Fatalf("body = %s", w.Body.String())
	}
}

func TestMyRepeatersList(t *testing.T) {
	f := repeaterFixture(t)
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	bob := f.registerAndActivate(t, "bob@example.org", "Bob", pw)
	carol := f.registerAndActivate(t, "carol@example.org", "Carol", pw)
	const rpkNamed = "ee55ee55ee55ee55ee55ee55ee55ee55ee55ee55ee55ee55ee55ee55ee55ee55"
	if _, err := f.srv.db.conn.Exec(`UPDATE nodes SET name = 'Zulu Hill', last_seen = '2026-11-01T10:00:00Z' WHERE public_key = ?`, rpkA); err != nil {
		t.Fatal(err)
	}
	if _, err := f.srv.db.conn.Exec(`INSERT INTO nodes (public_key, name, role, last_seen) VALUES (?, 'alpha peak', 'repeater', NULL)`, rpkNamed); err != nil {
		t.Fatal(err)
	}
	// Alice claims three: two known (one co-maintained, one with details),
	// and one that has left the analyzer database since.
	for _, pk := range []string{rpkA, rpkNamed, rpkGone} {
		if _, err := f.st.ClaimRepeater(alice.me.ID, pk); err != nil {
			t.Fatal(err)
		}
	}
	for _, uid := range []int64{bob.me.ID, carol.me.ID} {
		if _, err := f.st.ClaimRepeater(uid, rpkA); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.st.SetStatus(carol.me.ID, users.StatusDisabled); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.SaveRepeaterInfo(alice.me.ID, rpkA, users.RepeaterEdit{Status: users.RepeaterStatusMaintenance}); err != nil {
		t.Fatal(err)
	}

	w := f.do("GET", "/api/account/repeaters", nil, as(alice))
	expectStatus(t, w, http.StatusOK)
	got := decode[myRepeatersJSON](t, w).Repeaters
	var order []string
	for _, r := range got {
		order = append(order, r.Pubkey)
	}
	// Named first, by name ignoring case; then unnamed by pubkey.
	if want := []string{rpkNamed, rpkA, rpkGone}; !slices.Equal(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	named, zulu, gone := got[0], got[1], got[2]
	if !named.Known || named.Name != "alpha peak" || named.LastHeard != nil || named.Status != nil || len(named.Comaintainers) != 0 {
		t.Errorf("unheard repeater = %+v", named)
	}
	if !zulu.Known || zulu.Role != "repeater" || zulu.LastHeard == nil || *zulu.LastHeard != "2026-11-01T10:00:00Z" {
		t.Errorf("heard repeater = %+v", zulu)
	}
	if zulu.Status == nil || *zulu.Status != "maintenance" {
		t.Errorf("status = %v", zulu.Status)
	}
	// Bob is listed; Alice herself and the disabled Carol are not.
	if !slices.Equal(zulu.Comaintainers, []string{"Bob"}) {
		t.Errorf("comaintainers = %v", zulu.Comaintainers)
	}
	if gone.Known || gone.Name != "" || gone.ClaimedAt == "" {
		t.Errorf("departed repeater = %+v", gone)
	}
	if strings.Contains(w.Body.String(), "@") {
		t.Errorf("body leaks an email: %s", w.Body.String())
	}

	// Bob sees only his own claim, with Alice as the other maintainer.
	b := decode[myRepeatersJSON](t, f.do("GET", "/api/account/repeaters", nil, as(bob))).Repeaters
	if len(b) != 1 || b[0].Pubkey != rpkA || !slices.Equal(b[0].Comaintainers, []string{"Alice"}) {
		t.Fatalf("Bob's list = %+v", b)
	}
	// Unclaiming through the existing route takes it off the list.
	expectStatus(t, f.do("DELETE", repPath(rpkA, "claim"), nil, as(bob)), http.StatusOK)
	if b := decode[myRepeatersJSON](t, f.do("GET", "/api/account/repeaters", nil, as(bob))).Repeaters; len(b) != 0 {
		t.Fatalf("after unclaim = %+v", b)
	}
}

func TestMyRepeatersNeedsBrowserSession(t *testing.T) {
	f := repeaterFixture(t)
	f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	expectStatus(t, f.do("GET", "/api/account/repeaters", nil), http.StatusUnauthorized)
	tok := f.deviceToken(t, "alice@example.org", pw, "Pixel").Token
	expectStatus(t, f.do("GET", "/api/account/repeaters", nil, bearer(tok)), http.StatusForbidden)
}

// Without the analyzer database (auth-only fixture) the claims still list,
// as not known.
func TestMyRepeatersWithoutAnalyzerDB(t *testing.T) {
	f := newAuthFixture(t)
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	if _, err := f.st.ClaimRepeater(alice.me.ID, rpkA); err != nil {
		t.Fatal(err)
	}
	got := decode[myRepeatersJSON](t, f.do("GET", "/api/account/repeaters", nil, as(alice))).Repeaters
	if len(got) != 1 || got[0].Known || got[0].LastHeard != nil {
		t.Fatalf("list = %+v", got)
	}
}

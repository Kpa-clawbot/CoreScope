package main

import (
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

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
	if gone.Known || gone.Inactive || gone.Name != "" || gone.ClaimedAt == "" {
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

// A browser session or a device token with a repeater scope lists the
// caller's claims; an rx-only token and no login do not.
func TestMyRepeatersAccess(t *testing.T) {
	f := repeaterFixture(t)
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	f.registerAndActivate(t, "bob@example.org", "Bob", pw)
	if _, err := f.st.ClaimRepeater(alice.me.ID, rpkA); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, f.do("GET", "/api/account/repeaters", nil), http.StatusUnauthorized)
	rx := f.deviceToken(t, "alice@example.org", pw, "Pixel").Token
	expectStatus(t, f.do("GET", "/api/account/repeaters", nil, bearer(rx)), http.StatusForbidden)
	for _, scope := range []string{deviceScopeRepeaterClaim, deviceScopeRepeaterEdit} {
		tok := f.scopedToken(t, "alice@example.org", scope, scope)
		got := decode[myRepeatersJSON](t, f.do("GET", "/api/account/repeaters", nil, bearer(tok))).Repeaters
		if len(got) != 1 || got[0].Pubkey != rpkA {
			t.Errorf("%s token: got %+v, want alice's one claim", scope, got)
		}
	}
	// Bob's repeater token lists Bob's claims, not Alice's.
	bobTok := f.scopedToken(t, "bob@example.org", "bob", deviceScopeRepeaterClaim)
	if got := decode[myRepeatersJSON](t, f.do("GET", "/api/account/repeaters", nil, bearer(bobTok))).Repeaters; len(got) != 0 {
		t.Errorf("bob's token listed %+v", got)
	}
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

// A repeater silent past retention.nodeDays sits in inactive_nodes, not
// nodes: it still lists by name with its last advert, marked inactive.
func TestMyRepeatersInactiveNode(t *testing.T) {
	f := repeaterFixture(t)
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	const rpkOld = "ff66ff66ff66ff66ff66ff66ff66ff66ff66ff66ff66ff66ff66ff66ff66ff66"
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS inactive_nodes (public_key TEXT PRIMARY KEY, name TEXT, role TEXT, last_seen TEXT)`,
		`INSERT INTO inactive_nodes (public_key, name, role, last_seen) VALUES ('` + rpkOld + `', 'Old Tower', 'Repeater', '2026-10-01T08:00:00Z')`,
	} {
		if _, err := f.srv.db.conn.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	// The claim route already accepts it (RepeaterKnown reads inactive_nodes).
	expectStatus(t, f.do("POST", repPath(rpkOld, "claim"), nil, as(alice)), http.StatusOK)
	if _, err := f.st.ClaimRepeater(alice.me.ID, rpkGone); err != nil {
		t.Fatal(err)
	}
	got := decode[myRepeatersJSON](t, f.do("GET", "/api/account/repeaters", nil, as(alice))).Repeaters
	if len(got) != 2 {
		t.Fatalf("list = %+v", got)
	}
	old, gone := got[0], got[1]
	if old.Pubkey != rpkOld || !old.Known || !old.Inactive || old.Name != "Old Tower" || old.Role != "repeater" ||
		old.LastHeard == nil || *old.LastHeard != "2026-10-01T08:00:00Z" {
		t.Errorf("inactive repeater = %+v", old)
	}
	if gone.Pubkey != rpkGone || gone.Known || gone.Inactive {
		t.Errorf("unknown repeater = %+v", gone)
	}
}

// The production path: a packet store whose certain-heard entry is newer
// than the node's last advert, and a relay entry for its prefix.
func TestMyRepeatersUsesPacketStore(t *testing.T) {
	f := repeaterFixture(t)
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	if _, err := f.srv.db.conn.Exec(`UPDATE nodes SET name = 'Zulu Hill', last_seen = '2026-11-01T10:00:00Z' WHERE public_key = ?`, rpkA); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.ClaimRepeater(alice.me.ID, rpkA); err != nil {
		t.Fatal(err)
	}
	ps := NewPacketStore(nil, nil)
	heard := time.Date(2026, 11, 3, 12, 30, 0, 0, time.UTC)
	ps.certainHeard = map[string]string{rpkA: heard.Format(time.RFC3339)}
	ps.repeaterRelayCache = map[string]RepeaterRelayInfo{rpkA: {LastRelayed: "2026-11-05T09:15:00Z", RelayActive: true, WindowHours: 24}}
	f.srv.store = ps

	got := decode[myRepeatersJSON](t, f.do("GET", "/api/account/repeaters", nil, as(alice))).Repeaters
	if len(got) != 1 {
		t.Fatalf("list = %+v", got)
	}
	r := got[0]
	if r.LastHeard == nil || *r.LastHeard != "2026-11-03T12:30:00Z" {
		t.Errorf("lastHeard = %v, want the certain-heard time over the older advert", r.LastHeard)
	}
	if r.LastRelayed == nil || *r.LastRelayed != "2026-11-05T09:15:00Z" {
		t.Errorf("lastRelayed = %v", r.LastRelayed)
	}

	// An advert newer than the certain-heard entry wins.
	if _, err := f.srv.db.conn.Exec(`UPDATE nodes SET last_seen = '2026-11-04T00:00:00Z' WHERE public_key = ?`, rpkA); err != nil {
		t.Fatal(err)
	}
	got = decode[myRepeatersJSON](t, f.do("GET", "/api/account/repeaters", nil, as(alice))).Repeaters
	if got[0].LastHeard == nil || *got[0].LastHeard != "2026-11-04T00:00:00Z" {
		t.Errorf("lastHeard = %v, want the newer advert", got[0].LastHeard)
	}
}

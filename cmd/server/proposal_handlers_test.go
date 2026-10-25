package main

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/meshcore-analyzer/users"
)

func defaultProposalSettings() proposalSettings {
	return proposalSettings{enabled: true, maxPending: 100, maxApproved: 128, perUserPerDay: 5}
}

// newProposalFixture is newAuthFixture with channel proposals set to ps
// before the routes are registered. admin@example.org is a config admin.
func newProposalFixture(t *testing.T, ps proposalSettings) *authFixture {
	t.Helper()
	return newProposalFixtureWithConfig(t, ps, &Config{APIKey: testAPIKey})
}

func newProposalFixtureWithConfig(t *testing.T, ps proposalSettings, cfg *Config) *authFixture {
	t.Helper()
	a, fake, ownerURL := newTestAuthServiceWithURL(t, postgresTestDSN(t), "admin@example.org")
	a.set.proposals = ps
	srv := &Server{cfg: cfg, perfStats: NewPerfStats(), auth: a}
	r := mux.NewRouter()
	srv.registerAuthRoutes(r)
	return &authFixture{srv: srv, router: r, fake: fake, st: a.st, ownerURL: ownerURL}
}

func proposalPeople(t *testing.T, f *authFixture) (admin, user *client) {
	t.Helper()
	return f.registerAndActivate(t, "admin@example.org", "Admin", pw), f.registerAndActivate(t, "pat@example.org", "Pat", pw)
}

func postProposal(t *testing.T, f *authFixture, c *client, subject string) *httptest.ResponseRecorder {
	t.Helper()
	return f.do("POST", "/api/proposals", proposalRequest{Kind: users.KindHashtagChannel, Subject: subject}, as(c))
}

func TestProposalRoutesAbsentWhenProposalsOff(t *testing.T) {
	f := newProposalFixture(t, proposalSettings{})
	for _, rt := range []struct{ method, path string }{
		{"POST", "/api/proposals"}, {"GET", "/api/account/proposals"},
		{"GET", "/api/admin/proposals"}, {"POST", "/api/admin/proposals/1/approve"},
	} {
		if w := f.do(rt.method, rt.path, nil); w.Code != 404 {
			t.Errorf("%s %s = %d with proposals off; want 404", rt.method, rt.path, w.Code)
		}
	}
}

func TestProposeAndListOwn(t *testing.T) {
	f := newProposalFixture(t, defaultProposalSettings())
	_, user := proposalPeople(t, f)
	w := postProposal(t, f, user, "  mycity ")
	expectStatus(t, w, 201)
	p := decode[proposalJSON](t, w)
	if p.Subject != "#mycity" || p.Status != users.ProposalPending || p.Kind != users.KindHashtagChannel || p.DecidedAt != nil {
		t.Fatalf("created = %+v", p)
	}
	w = f.do("GET", "/api/account/proposals", nil, as(user))
	expectStatus(t, w, 200)
	if list := decode[[]proposalJSON](t, w); len(list) != 1 || list[0].ID != p.ID {
		t.Fatalf("own list = %+v", list)
	}
	entries, err := f.st.AuditList(users.AuditFilter{Actions: []string{"proposal.create"}})
	if err != nil || len(entries) != 1 || *entries[0].ActorUserID != user.me.ID ||
		entries[0].Detail["subject"] != "#mycity" || entries[0].Detail["kind"] != users.KindHashtagChannel {
		t.Fatalf("audit = %+v, %v", entries, err)
	}
}

func TestProposeRefusals(t *testing.T) {
	f := newProposalFixture(t, defaultProposalSettings())
	admin, user := proposalPeople(t, f)
	ok := proposalRequest{Kind: users.KindHashtagChannel, Subject: "#a"}
	cases := []struct {
		name string
		body any
		mods []reqMod
		code int
		msg  string
	}{
		{"logged out", ok, nil, 401, "not logged in"},
		{"no csrf", ok, []reqMod{as(&client{cookie: user.cookie})}, 403, "CSRF check failed"},
		{"kind", proposalRequest{Kind: "other", Subject: "#a"}, []reqMod{as(user)}, 400, "unsupported proposal kind"},
		{"public", proposalRequest{Kind: users.KindHashtagChannel, Subject: "Public"}, []reqMod{as(user)}, 400, "Public is the built-in channel"},
		{"too long", proposalRequest{Kind: users.KindHashtagChannel, Subject: "#" + strings.Repeat("a", 31)}, []reqMod{as(user)}, 400, "at most 31 bytes"},
		{"unknown field", map[string]string{"kind": users.KindHashtagChannel, "subject": "#a", "x": "1"}, []reqMod{as(user)}, 400, "invalid request body"},
	}
	for _, c := range cases {
		w := f.do("POST", "/api/proposals", c.body, c.mods...)
		if w.Code != c.code || !strings.Contains(w.Body.String(), c.msg) {
			t.Errorf("%s: %d %s; want %d containing %q", c.name, w.Code, w.Body.String(), c.code, c.msg)
		}
	}
	a := decode[proposalJSON](t, postProposal(t, f, user, "#a"))
	if w := postProposal(t, f, admin, "#a"); w.Code != 409 || !strings.Contains(w.Body.String(), "already proposed") {
		t.Errorf("pending duplicate: %d %s", w.Code, w.Body.String())
	}
	if _, err := f.st.Decide(a.ID, users.ProposalApprove, admin.me.ID, "", 0); err != nil {
		t.Fatal(err)
	}
	if w := postProposal(t, f, admin, "#a"); w.Code != 409 || !strings.Contains(w.Body.String(), "already approved") {
		t.Errorf("approved duplicate: %d %s", w.Code, w.Body.String())
	}
	b := decode[proposalJSON](t, postProposal(t, f, user, "#b"))
	if _, err := f.st.Decide(b.ID, users.ProposalReject, admin.me.ID, "", 0); err != nil {
		t.Fatal(err)
	}
	if w := postProposal(t, f, user, "#b"); w.Code != 409 || !strings.Contains(w.Body.String(), "was rejected") {
		t.Errorf("rejected: %d %s", w.Code, w.Body.String())
	}
	if entries, _ := f.st.AuditList(users.AuditFilter{Actions: []string{"proposal.create"}}); len(entries) != 2 {
		t.Fatalf("proposal.create rows = %d; want 2 (refusals are not audited)", len(entries))
	}
}

func TestProposeLimitsAnswer429(t *testing.T) {
	ps := defaultProposalSettings()
	ps.perUserPerDay, ps.maxPending = 1, 2
	f := newProposalFixture(t, ps)
	admin, user := proposalPeople(t, f)
	expectStatus(t, postProposal(t, f, user, "#a"), 201)
	if w := postProposal(t, f, user, "#b"); w.Code != 429 || !strings.Contains(w.Body.String(), "daily proposal limit") {
		t.Fatalf("per-user limit: %d %s", w.Code, w.Body.String())
	}
	expectStatus(t, postProposal(t, f, admin, "#b"), 201)
	sam := f.registerAndActivate(t, "sam@example.org", "Sam", pw)
	if w := postProposal(t, f, sam, "#c"); w.Code != 429 || !strings.Contains(w.Body.String(), "wait for review") {
		t.Fatalf("pending cap: %d %s", w.Code, w.Body.String())
	}
}

func TestAdminProposalList(t *testing.T) {
	f := newProposalFixture(t, defaultProposalSettings())
	admin, user := proposalPeople(t, f)
	expectStatus(t, postProposal(t, f, user, "#a"), 201)
	w := f.do("GET", "/api/admin/proposals?status=pending&kind=hashtag_channel", nil, as(admin))
	expectStatus(t, w, 200)
	list := decode[[]adminProposalJSON](t, w)
	if len(list) != 1 || list[0].Subject != "#a" || list[0].Proposer == nil || list[0].Proposer.DisplayName != "Pat" || list[0].Reviewer != nil {
		t.Fatalf("admin list = %+v", list)
	}
	if got := decode[[]adminProposalJSON](t, f.do("GET", "/api/admin/proposals?status=approved", nil, as(admin))); len(got) != 0 {
		t.Fatalf("approved list = %+v", got)
	}
	for _, q := range []string{"?status=bogus", "?kind=other"} {
		if w := f.do("GET", "/api/admin/proposals"+q, nil, as(admin)); w.Code != 400 {
			t.Errorf("%s = %d; want 400", q, w.Code)
		}
	}
	if w := f.do("GET", "/api/admin/proposals", nil, as(user)); w.Code != 403 {
		t.Errorf("non-admin = %d; want 403", w.Code)
	}
}

func TestAdminDecideFlow(t *testing.T) {
	f := newProposalFixture(t, defaultProposalSettings())
	admin, user := proposalPeople(t, f)
	p := decode[proposalJSON](t, postProposal(t, f, user, "#a"))
	path := fmt.Sprintf("/api/admin/proposals/%d/", p.ID)
	if w := f.do("POST", path+"approve", decideRequest{Note: "ok"}, as(user)); w.Code != 403 {
		t.Fatalf("non-admin approve = %d", w.Code)
	}
	w := f.do("POST", path+"approve", decideRequest{Note: "  welcome  "}, as(admin))
	expectStatus(t, w, 200)
	got := decode[adminProposalJSON](t, w)
	if got.Status != users.ProposalApproved || got.Note != "welcome" || got.Reviewer == nil || got.Reviewer.ID != admin.me.ID ||
		got.Proposer == nil || got.Proposer.ID != user.me.ID || got.DecidedAt == nil {
		t.Fatalf("approved = %+v", got)
	}
	if w := f.do("POST", path+"reject", decideRequest{}, as(admin)); w.Code != 409 {
		t.Fatalf("reject an approved proposal = %d; want 409", w.Code)
	}
	expectStatus(t, f.do("POST", path+"revoke", decideRequest{}, as(admin)), 200)
	entries, _ := f.st.AuditList(users.AuditFilter{Actions: []string{"proposal.approve", "proposal.revoke"}})
	if len(entries) != 2 {
		t.Fatalf("decision audit rows = %d; want 2", len(entries))
	}
	for _, e := range entries {
		if *e.ActorUserID != admin.me.ID || e.TargetUserID == nil || *e.TargetUserID != user.me.ID || e.Detail["subject"] != "#a" || e.Detail["kind"] != users.KindHashtagChannel {
			t.Fatalf("audit entry = %+v", e)
		}
	}
	if mine := decode[[]proposalJSON](t, f.do("GET", "/api/account/proposals", nil, as(user))); mine[0].Status != users.ProposalRevoked {
		t.Fatalf("proposer sees %+v", mine)
	}
	for _, c := range []struct {
		path string
		code int
	}{{"/api/admin/proposals/999/approve", 404}, {"/api/admin/proposals/x/approve", 400}, {"/api/admin/proposals/0/approve", 400}} {
		if w := f.do("POST", c.path, decideRequest{}, as(admin)); w.Code != c.code {
			t.Errorf("%s = %d; want %d", c.path, w.Code, c.code)
		}
	}
}

func TestAdminApproveRespectsMaxApproved(t *testing.T) {
	ps := defaultProposalSettings()
	ps.maxApproved = 1
	f := newProposalFixture(t, ps)
	admin, user := proposalPeople(t, f)
	a := decode[proposalJSON](t, postProposal(t, f, user, "#a"))
	b := decode[proposalJSON](t, postProposal(t, f, user, "#b"))
	expectStatus(t, f.do("POST", fmt.Sprintf("/api/admin/proposals/%d/approve", a.ID), decideRequest{}, as(admin)), 200)
	w := f.do("POST", fmt.Sprintf("/api/admin/proposals/%d/approve", b.ID), decideRequest{}, as(admin))
	if w.Code != 409 || !strings.Contains(w.Body.String(), "limit of approved channels") {
		t.Fatalf("approve over the cap: %d %s", w.Code, w.Body.String())
	}
}

func TestAdminDecideNoteValidation(t *testing.T) {
	f := newProposalFixture(t, defaultProposalSettings())
	admin, user := proposalPeople(t, f)
	p := decode[proposalJSON](t, postProposal(t, f, user, "#a"))
	path := fmt.Sprintf("/api/admin/proposals/%d/reject", p.ID)
	for _, c := range []struct{ note, msg string }{
		{strings.Repeat("a", 501), "at most 500 characters"},
		{"a\u202eb", "invisible or control characters"},
	} {
		if w := f.do("POST", path, decideRequest{Note: c.note}, as(admin)); w.Code != 400 || !strings.Contains(w.Body.String(), c.msg) {
			t.Errorf("note %q: %d %s", c.note, w.Code, w.Body.String())
		}
	}
	expectStatus(t, f.do("POST", path, decideRequest{Note: "line one\nline two"}, as(admin)), 200)
}

func TestValidateProposalNote(t *testing.T) {
	for in, want := range map[string]string{"": "", "  fine  ": "fine", "a\nb": "a\nb", "👩\u200d💻": "👩\u200d💻", strings.Repeat("é", 500): strings.Repeat("é", 500)} {
		if got, err := validateProposalNote(in); err != nil || got != want {
			t.Errorf("validateProposalNote(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{strings.Repeat("é", 501), "a\tb", "a\u200bb", "a\u2029b"} {
		if _, err := validateProposalNote(in); err == nil {
			t.Errorf("validateProposalNote(%q) accepted", in)
		}
	}
}

func TestJanitorPrunesOldDecisions(t *testing.T) {
	f := newProposalFixture(t, defaultProposalSettings())
	admin, user := proposalPeople(t, f)
	p := decode[proposalJSON](t, postProposal(t, f, user, "#a"))
	expectStatus(t, f.do("POST", fmt.Sprintf("/api/admin/proposals/%d/reject", p.ID), decideRequest{}, as(admin)), 200)
	f.st.SetClock(func() time.Time { return time.Now().Add(91 * 24 * time.Hour) })
	f.srv.auth.prune()
	if list, _ := f.st.ListProposals(users.ProposalFilter{}); len(list) != 0 {
		t.Fatalf("after prune = %+v; want the 91-day-old rejection gone", list)
	}
}

const testChannelKeyHex = "0123456789abcdef0123456789abcdef"

func configuredChannelsConfig(t *testing.T) *Config {
	t.Helper()
	var cfg Config
	raw := `{"apiKey":"` + testAPIKey + `","hashChannels":[" mesh","#Club",""],"channelKeys":{"#secret":"` + testChannelKeyHex + `","public":"` + testChannelKeyHex + `"}}`
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	return &cfg
}

// The server reads channelKeys for its names only; the key values are
// dropped while unmarshalling, so the server never holds key material.
func TestChannelKeyNamesKeepsNoKeyMaterial(t *testing.T) {
	cfg := configuredChannelsConfig(t)
	got := append([]string(nil), cfg.ChannelKeys...)
	if len(got) != 2 || !((got[0] == "#secret" && got[1] == "public") || (got[1] == "#secret" && got[0] == "public")) {
		t.Fatalf("channelKeys names = %v", got)
	}
	out, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), testChannelKeyHex) {
		t.Fatalf("server config retains a channel key: %s", out)
	}
	var none Config
	if err := json.Unmarshal([]byte(`{"channelKeys":null}`), &none); err != nil || len(none.ChannelKeys) != 0 {
		t.Fatalf("null channelKeys = %v, %v", none.ChannelKeys, err)
	}
	// A non-object channelKeys is ignored, never a parse error: LoadConfig
	// would otherwise drop the whole config and start on defaults.
	for _, bad := range []string{`["#x"]`, `"#x"`, `42`} {
		var c Config
		raw := `{"port":8123,"apiKey":"` + testAPIKey + `","channelKeys":` + bad + `,"hashChannels":["mesh"]}`
		if err := json.Unmarshal([]byte(raw), &c); err != nil {
			t.Fatalf("channelKeys %s: %v", bad, err)
		}
		if c.Port != 8123 || c.APIKey != testAPIKey || len(c.HashChannels) != 1 || len(c.ChannelKeys) != 0 {
			t.Fatalf("channelKeys %s: config = port %d, apiKey %q, hashChannels %v, channelKeys %v", bad, c.Port, c.APIKey, c.HashChannels, c.ChannelKeys)
		}
	}
}

func TestConfiguredChannelNames(t *testing.T) {
	got := configuredChannelsConfig(t).configuredChannelNames()
	want := map[string]bool{"#mesh": true, "#Club": true, "#secret": true, "Public": true}
	if len(got) != len(want) {
		t.Fatalf("configured names = %v; want %v", got, want)
	}
	for n := range want {
		if !got[n] {
			t.Fatalf("configured names = %v; missing %q", got, n)
		}
	}
	var nilCfg *Config
	if len(nilCfg.configuredChannelNames()) != 0 {
		t.Fatal("nil config has configured names")
	}
}

func TestProposeRefusesConfiguredChannels(t *testing.T) {
	f := newProposalFixtureWithConfig(t, defaultProposalSettings(), configuredChannelsConfig(t))
	_, user := proposalPeople(t, f)
	for _, subject := range []string{"mesh", "#mesh", " #Club ", "#secret"} {
		if w := postProposal(t, f, user, subject); w.Code != 409 || !strings.Contains(w.Body.String(), "already decrypted on this instance") {
			t.Errorf("%q: %d %s; want 409 already decrypted", subject, w.Code, w.Body.String())
		}
	}
	expectStatus(t, postProposal(t, f, user, "#club"), 201) // case-sensitive, like the ingestor's key map
	if entries, _ := f.st.AuditList(users.AuditFilter{Actions: []string{"proposal.create"}}); len(entries) != 1 {
		t.Fatalf("proposal.create rows = %d; want 1 (refusals are not audited)", len(entries))
	}
}

// Every decision reloads the approved-name snapshot from users.db under a
// mutex, so concurrent decisions cannot leave an older read in place. Run
// with -race.
func TestConcurrentDecisionsKeepApprovedSnapshotFresh(t *testing.T) {
	ps := defaultProposalSettings()
	ps.perUserPerDay = 100
	f := newProposalFixture(t, ps)
	admin, user := proposalPeople(t, f)
	f.srv.auth.refreshApproved()
	if got := f.srv.auth.approvedChannels(); got == nil || len(got) != 0 {
		t.Fatalf("initial snapshot = %#v; want empty", got)
	}
	const n = 8
	ids := make([]int64, n)
	for i := range ids {
		ids[i] = decode[proposalJSON](t, postProposal(t, f, user, fmt.Sprintf("#c%d", i))).ID
	}
	decideAll := func(action string) {
		var wg sync.WaitGroup
		codes := make([]int, n)
		for i, id := range ids {
			wg.Add(1)
			go func(i int, id int64) {
				defer wg.Done()
				codes[i] = f.serve("POST", fmt.Sprintf("/api/admin/proposals/%d/%s", id, action), decideRequest{}, as(admin)).Code
			}(i, id)
		}
		wg.Wait()
		f.srv.auth.waitAudits()
		for i, c := range codes {
			if c != 200 {
				t.Fatalf("%s #%d = %d", action, ids[i], c)
			}
		}
	}
	decideAll("approve")
	want, err := f.st.ApprovedSubjects(users.KindHashtagChannel, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.srv.auth.approvedChannels(); len(want) != n || strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("snapshot after concurrent approvals = %v; users.db has %v", got, want)
	}
	decideAll("revoke")
	if got := f.srv.auth.approvedChannels(); got == nil || len(got) != 0 {
		t.Fatalf("snapshot after concurrent revocations = %#v; want empty", got)
	}
}

// The concurrency test above cannot force the bad interleaving (users.db
// serialises the reads), so this pins the serialisation itself: a refresh
// waits for one already in progress.
func TestRefreshApprovedIsSerialised(t *testing.T) {
	f := newProposalFixture(t, defaultProposalSettings())
	a := f.srv.auth
	a.approvedMu.Lock()
	done := make(chan struct{})
	go func() {
		a.refreshApproved()
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("refreshApproved ran while another refresh held the lock")
	case <-time.After(50 * time.Millisecond):
	}
	a.approvedMu.Unlock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("refreshApproved did not finish after the lock was released")
	}
}

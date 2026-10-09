package users

import (
	"errors"
	"github.com/meshcore-analyzer/dbconfig"
	"strings"
	"testing"
	"time"
)

// cmd/ingestor/approved_channels.go reads the approved names with this exact
// literal (it does not import this package). Its test checks the literal is
// still in this file; TestApprovedSubjects checks it still agrees with
// ApprovedSubjects.
const ingestorApprovedQuery = `SELECT subject FROM approved_channels WHERE kind = 'hashtag_channel' ORDER BY decided_at, id LIMIT $1`

func mustPropose(t *testing.T, st *Store, subject string, uid int64) *Proposal {
	t.Helper()
	p, err := st.Propose(KindHashtagChannel, subject, uid, ProposalLimits{})
	if err != nil {
		t.Fatalf("Propose(%s): %v", subject, err)
	}
	return p
}

func mustDecide(t *testing.T, st *Store, id int64, a ProposalAction, reviewer int64) *Proposal {
	t.Helper()
	p, err := st.Decide(id, a, reviewer, "", 0)
	if err != nil {
		t.Fatalf("Decide(%d, %s): %v", id, a, err)
	}
	return p
}

// reach moves a fresh pending proposal to status.
func reach(t *testing.T, st *Store, id int64, status ProposalStatus, reviewer int64) {
	t.Helper()
	switch status {
	case ProposalApproved:
		mustDecide(t, st, id, ProposalApprove, reviewer)
	case ProposalRejected:
		mustDecide(t, st, id, ProposalReject, reviewer)
	case ProposalRevoked:
		mustDecide(t, st, id, ProposalApprove, reviewer)
		mustDecide(t, st, id, ProposalRevoke, reviewer)
	}
}

func TestProposeCreatesPending(t *testing.T) {
	st, clk := newTestStore(t)
	u := mustCreate(t, st, "pat@example.org", "Pat")
	p := mustPropose(t, st, "#mycity", u.ID)
	if p.ID == 0 || p.Kind != KindHashtagChannel || p.Subject != "#mycity" || p.Status != ProposalPending ||
		p.ProposerID == nil || *p.ProposerID != u.ID || p.ReviewerID != nil || p.Note != "" ||
		p.DecidedAt != nil || !p.CreatedAt.Equal(clk.Now()) {
		t.Fatalf("unexpected proposal: %+v", p)
	}
}

func TestProposeRefusesDuplicatesByState(t *testing.T) {
	st, _ := newTestStore(t)
	u := mustCreate(t, st, "pat@example.org", "Pat")
	admin := mustCreate(t, st, "ada@example.org", "Ada")
	a := mustPropose(t, st, "#a", u.ID)
	if _, err := st.Propose(KindHashtagChannel, "#a", admin.ID, ProposalLimits{}); !errors.Is(err, ErrProposalPending) {
		t.Fatalf("pending duplicate err = %v; want ErrProposalPending", err)
	}
	mustDecide(t, st, a.ID, ProposalApprove, admin.ID)
	if _, err := st.Propose(KindHashtagChannel, "#a", admin.ID, ProposalLimits{}); !errors.Is(err, ErrProposalApproved) {
		t.Fatalf("approved duplicate err = %v; want ErrProposalApproved", err)
	}
	b := mustPropose(t, st, "#b", u.ID)
	mustDecide(t, st, b.ID, ProposalReject, admin.ID)
	if _, err := st.Propose(KindHashtagChannel, "#b", u.ID, ProposalLimits{}); !errors.Is(err, ErrProposalRejected) {
		t.Fatalf("rejected re-proposal err = %v; want ErrProposalRejected", err)
	}
	// The same subject under another kind is another row.
	if _, err := st.Propose("other_kind", "#a", u.ID, ProposalLimits{}); err != nil {
		t.Fatalf("other kind: %v", err)
	}
	// Case matters: the key is derived from the exact name.
	if _, err := st.Propose(KindHashtagChannel, "#A", u.ID, ProposalLimits{}); err != nil {
		t.Fatalf("#A next to #a: %v", err)
	}
}

func TestRevokedCanBeProposedAgain(t *testing.T) {
	st, clk := newTestStore(t)
	u := mustCreate(t, st, "pat@example.org", "Pat")
	v := mustCreate(t, st, "val@example.org", "Val")
	admin := mustCreate(t, st, "ada@example.org", "Ada")
	p := mustPropose(t, st, "#a", u.ID)
	clk.Advance(time.Hour)
	mustDecide(t, st, p.ID, ProposalApprove, admin.ID)
	if _, err := st.Decide(p.ID, ProposalRevoke, admin.ID, "spam", 0); err != nil {
		t.Fatal(err)
	}
	clk.Advance(time.Hour)
	again, err := st.Propose(KindHashtagChannel, "#a", v.ID, ProposalLimits{})
	if err != nil || again.ID != p.ID || again.Status != ProposalPending || again.ProposerID == nil || *again.ProposerID != v.ID ||
		again.ReviewerID != nil || again.Note != "" || again.DecidedAt != nil || !again.CreatedAt.Equal(clk.Now()) {
		t.Fatalf("re-proposal = %+v, %v; want the same row pending under Val", again, err)
	}
}

func TestDecideTransitions(t *testing.T) {
	cases := []struct {
		from   ProposalStatus
		action ProposalAction
		to     ProposalStatus // "" = refused
	}{
		{ProposalPending, ProposalApprove, ProposalApproved},
		{ProposalPending, ProposalReject, ProposalRejected},
		{ProposalPending, ProposalRevoke, ""},
		{ProposalApproved, ProposalRevoke, ProposalRevoked},
		{ProposalApproved, ProposalApprove, ""},
		{ProposalApproved, ProposalReject, ""},
		{ProposalRejected, ProposalApprove, ""},
		{ProposalRejected, ProposalRevoke, ""},
		{ProposalRevoked, ProposalApprove, ""},
		{ProposalRevoked, ProposalReject, ""},
	}
	for _, c := range cases {
		st, clk := newTestStore(t)
		u := mustCreate(t, st, "pat@example.org", "Pat")
		admin := mustCreate(t, st, "ada@example.org", "Ada")
		p := mustPropose(t, st, "#x", u.ID)
		reach(t, st, p.ID, c.from, admin.ID)
		clk.Advance(time.Minute)
		got, err := st.Decide(p.ID, c.action, admin.ID, "because", 0)
		if c.to == "" {
			if !errors.Is(err, ErrProposalTransition) {
				t.Errorf("%s from %s: err = %v; want ErrProposalTransition", c.action, c.from, err)
			}
			continue
		}
		if err != nil || got.Status != c.to || got.ReviewerID == nil || *got.ReviewerID != admin.ID ||
			got.Note != "because" || got.DecidedAt == nil || !got.DecidedAt.Equal(clk.Now()) {
			t.Errorf("%s from %s = %+v, %v; want %s", c.action, c.from, got, err, c.to)
			continue
		}
		list, err := st.ListProposals(ProposalFilter{})
		if err != nil || len(list) != 1 || list[0].Status != c.to || list[0].Note != "because" {
			t.Errorf("%s from %s: stored %+v, %v", c.action, c.from, list, err)
		}
	}
}

func TestDecideUnknownID(t *testing.T) {
	st, _ := newTestStore(t)
	admin := mustCreate(t, st, "ada@example.org", "Ada")
	if _, err := st.Decide(999, ProposalApprove, admin.ID, "", 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Decide(missing) err = %v; want ErrNotFound", err)
	}
}

func TestDecideApproveRespectsMaxApproved(t *testing.T) {
	st, _ := newTestStore(t)
	u := mustCreate(t, st, "pat@example.org", "Pat")
	admin := mustCreate(t, st, "ada@example.org", "Ada")
	for _, s := range []string{"#a", "#b"} {
		mustDecide(t, st, mustPropose(t, st, s, u.ID).ID, ProposalApprove, admin.ID)
	}
	c := mustPropose(t, st, "#c", u.ID)
	if _, err := st.Decide(c.ID, ProposalApprove, admin.ID, "", 2); !errors.Is(err, ErrProposalApprovedLimit) {
		t.Fatalf("third approve err = %v; want ErrProposalApprovedLimit", err)
	}
	if got, _ := st.ListProposals(ProposalFilter{Status: ProposalPending}); len(got) != 1 || got[0].ID != c.ID {
		t.Fatalf("refused approve changed the row: %+v", got)
	}
	// Other kinds do not count, and reject is never limited.
	o, err := st.Propose("other_kind", "#a", u.ID, ProposalLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Decide(o.ID, ProposalApprove, admin.ID, "", 2); err != nil {
		t.Fatalf("other kind approve: %v", err)
	}
	if _, err := st.Decide(c.ID, ProposalReject, admin.ID, "", 2); err != nil {
		t.Fatalf("reject at the cap: %v", err)
	}
}

func TestProposeLimits(t *testing.T) {
	st, clk := newTestStore(t)
	u := mustCreate(t, st, "pat@example.org", "Pat")
	v := mustCreate(t, st, "val@example.org", "Val")
	day := ProposalLimits{PerUserPerDay: 2}
	for _, s := range []string{"#a", "#b"} {
		if _, err := st.Propose(KindHashtagChannel, s, u.ID, day); err != nil {
			t.Fatalf("Propose(%s): %v", s, err)
		}
	}
	if _, err := st.Propose(KindHashtagChannel, "#c", u.ID, day); !errors.Is(err, ErrProposalUserLimit) {
		t.Fatalf("third in a day err = %v; want ErrProposalUserLimit", err)
	}
	if _, err := st.Propose(KindHashtagChannel, "#c", v.ID, day); err != nil {
		t.Fatalf("another user has its own budget: %v", err)
	}
	clk.Advance(24*time.Hour + time.Second)
	if _, err := st.Propose(KindHashtagChannel, "#d", u.ID, day); err != nil {
		t.Fatalf("next day: %v", err)
	}
	// Four pending now: #a #b #c #d.
	capped := ProposalLimits{MaxPending: 4}
	if _, err := st.Propose(KindHashtagChannel, "#e", v.ID, capped); !errors.Is(err, ErrProposalPendingLimit) {
		t.Fatalf("pending cap err = %v; want ErrProposalPendingLimit", err)
	}
	// A duplicate is reported as such, not as a limit.
	if _, err := st.Propose(KindHashtagChannel, "#a", v.ID, capped); !errors.Is(err, ErrProposalPending) {
		t.Fatalf("duplicate at the cap err = %v; want ErrProposalPending", err)
	}
}

func TestListProposalsFilterAndOrder(t *testing.T) {
	st, clk := newTestStore(t)
	u := mustCreate(t, st, "pat@example.org", "Pat")
	admin := mustCreate(t, st, "ada@example.org", "Ada")
	a := mustPropose(t, st, "#a", u.ID)
	clk.Advance(time.Minute)
	b := mustPropose(t, st, "#b", u.ID)
	clk.Advance(time.Minute)
	mustDecide(t, st, a.ID, ProposalApprove, admin.ID)
	all, err := st.ListProposals(ProposalFilter{})
	if err != nil || len(all) != 2 || all[0].ID != b.ID || all[1].ID != a.ID {
		t.Fatalf("ListProposals(all) = %+v, %v; want #b then #a", all, err)
	}
	if pend, _ := st.ListProposals(ProposalFilter{Status: ProposalPending}); len(pend) != 1 || pend[0].ID != b.ID {
		t.Fatalf("pending = %+v", pend)
	}
	if other, _ := st.ListProposals(ProposalFilter{Kind: "other_kind"}); len(other) != 0 {
		t.Fatalf("other kind = %+v", other)
	}
}

func TestProposalsByUser(t *testing.T) {
	st, clk := newTestStore(t)
	u := mustCreate(t, st, "pat@example.org", "Pat")
	v := mustCreate(t, st, "val@example.org", "Val")
	a := mustPropose(t, st, "#a", u.ID)
	clk.Advance(time.Minute)
	b := mustPropose(t, st, "#b", u.ID)
	mustPropose(t, st, "#c", v.ID)
	list, err := st.ProposalsByUser(u.ID)
	if err != nil || len(list) != 2 || list[0].ID != b.ID || list[1].ID != a.ID {
		t.Fatalf("ProposalsByUser = %+v, %v", list, err)
	}
}

func TestApprovedSubjects(t *testing.T) {
	st, clk := newTestStore(t)
	u := mustCreate(t, st, "pat@example.org", "Pat")
	admin := mustCreate(t, st, "ada@example.org", "Ada")
	for _, s := range []string{"#c", "#a", "#b"} {
		p := mustPropose(t, st, s, u.ID)
		clk.Advance(time.Minute)
		mustDecide(t, st, p.ID, ProposalApprove, admin.ID)
	}
	mustPropose(t, st, "#pending", u.ID)
	o, err := st.Propose("other_kind", "#x", u.ID, ProposalLimits{})
	if err != nil {
		t.Fatal(err)
	}
	mustDecide(t, st, o.ID, ProposalApprove, admin.ID)

	got, err := st.ApprovedSubjects(KindHashtagChannel, 0)
	if err != nil || strings.Join(got, ",") != "#c,#a,#b" {
		t.Fatalf("ApprovedSubjects = %v, %v; want decision order #c,#a,#b", got, err)
	}
	if capped, _ := st.ApprovedSubjects(KindHashtagChannel, 2); strings.Join(capped, ",") != "#c,#a" {
		t.Fatalf("capped = %v", capped)
	}
	if empty, err := st.ApprovedSubjects("nothing", 0); err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("no rows = %#v, %v; want a non-nil empty slice", empty, err)
	}
	ingestorQuery := ingestorApprovedQuery
	if st.backend == dbconfig.SQLite {
		ingestorQuery = `SELECT subject FROM proposals WHERE kind = 'hashtag_channel' AND status = 'approved' ORDER BY decided_at, id LIMIT ?1`
	}
	rows, err := st.db.Query(ingestorQuery, 2)
	if err != nil {
		t.Fatalf("ingestor query: %v", err)
	}
	defer rows.Close()
	var viaIngestor []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		viaIngestor = append(viaIngestor, s)
	}
	if strings.Join(viaIngestor, ",") != "#c,#a" {
		t.Fatalf("ingestor query = %v; want #c,#a (same order and cap as ApprovedSubjects)", viaIngestor)
	}
}

func TestPruneProposals(t *testing.T) {
	st, clk := newTestStore(t)
	u := mustCreate(t, st, "pat@example.org", "Pat")
	admin := mustCreate(t, st, "ada@example.org", "Ada")
	old := map[string]ProposalStatus{"#rej": ProposalRejected, "#rev": ProposalRevoked, "#app": ProposalApproved, "#pen": ProposalPending}
	for s, status := range old {
		reach(t, st, mustPropose(t, st, s, u.ID).ID, status, admin.ID)
	}
	clk.Advance(91 * 24 * time.Hour)
	reach(t, st, mustPropose(t, st, "#young", u.ID).ID, ProposalRejected, admin.ID)
	n, err := st.PruneProposals(90 * 24 * time.Hour)
	if err != nil || n != 2 {
		t.Fatalf("PruneProposals = %d, %v; want 2", n, err)
	}
	left, _ := st.ListProposals(ProposalFilter{})
	got := map[string]bool{}
	for _, p := range left {
		got[p.Subject] = true
	}
	if len(left) != 3 || !got["#app"] || !got["#pen"] || !got["#young"] {
		t.Fatalf("left after prune = %v", got)
	}
	// A pruned rejection can be proposed again.
	if _, err := st.Propose(KindHashtagChannel, "#rej", u.ID, ProposalLimits{}); err != nil {
		t.Fatalf("re-propose after prune: %v", err)
	}
}

func TestProposalSurvivesProposerDeletion(t *testing.T) {
	st, _ := newTestStore(t)
	u := mustCreate(t, st, "pat@example.org", "Pat")
	mustPropose(t, st, "#a", u.ID)
	if err := st.Delete(u.ID); err != nil {
		t.Fatal(err)
	}
	list, err := st.ListProposals(ProposalFilter{})
	if err != nil || len(list) != 1 || list[0].ProposerID != nil {
		t.Fatalf("after deleting the proposer = %+v, %v; want the row with no proposer", list, err)
	}
}

// A native account schema at v3 binary gains the proposals table and keeps its rows.
func TestMigrateV3DatabaseToV4(t *testing.T) {
	st := migratedTestStore(t, 3, `INSERT INTO users (email,display_name,password_hash,created_at) VALUES ('old@example.org','Old','x',1)`)
	if v, err := st.SchemaVersion(); err != nil || v != schemaVersion(t) {
		t.Fatalf("SchemaVersion = %d, %v; want %d", v, err, schemaVersion(t))
	}
	if !hasIndex(t, st, "proposals_kind_subject") || !hasIndex(t, st, "proposals_status") {
		t.Fatal("proposals indexes missing after migration")
	}
	u, err := st.GetByEmail("old@example.org")
	if err != nil {
		t.Fatalf("v3 user lost: %v", err)
	}
	if _, err := st.Propose(KindHashtagChannel, "#after", u.ID, ProposalLimits{}); err != nil {
		t.Fatalf("Propose after migration: %v", err)
	}
}

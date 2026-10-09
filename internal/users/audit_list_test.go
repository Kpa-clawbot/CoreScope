package users

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func mustAudit(t *testing.T, st *Store, actor *int64, action string, target *int64, detail map[string]string) {
	t.Helper()
	if err := st.Audit(actor, action, target, detail); err != nil {
		t.Fatalf("Audit(%s): %v", action, err)
	}
}

func actionsOf(list []AuditEntry) string {
	parts := make([]string, len(list))
	for i, e := range list {
		parts[i] = e.Action
	}
	return strings.Join(parts, ",")
}

func mustList(t *testing.T, st *Store, f AuditFilter) []AuditEntry {
	t.Helper()
	list, err := st.AuditList(f)
	if err != nil {
		t.Fatalf("AuditList(%+v): %v", f, err)
	}
	return list
}

func TestAuditListFiltersAndPages(t *testing.T) {
	st, clk := newTestStore(t)
	base := clk.Now()
	at := func(d time.Duration) { clk.t = base.Add(d) }
	a := mustCreate(t, st, "a@example.org", "A")
	b := mustCreate(t, st, "b@example.org", "B")
	aid, bid := a.ID, b.ID
	at(-48 * time.Hour)
	mustAudit(t, st, nil, "user.register", &aid, nil)
	at(-30 * time.Hour)
	mustAudit(t, st, nil, "user.login", &aid, nil)
	at(-2 * time.Hour)
	mustAudit(t, st, nil, "user.login.failed", &bid, map[string]string{"reason": "wrong_password"})
	at(-1 * time.Hour)
	mustAudit(t, st, &aid, "user.disable", &bid, nil)
	at(0)
	mustAudit(t, st, nil, "user.loginx", &aid, nil) // not in the user.login group

	from3h, from31h, to2h := base.Add(-3*time.Hour), base.Add(-31*time.Hour), base.Add(-2*time.Hour)
	type listCase struct {
		name string
		f    AuditFilter
		want string
	}
	for _, c := range []listCase{
		{"all, newest first", AuditFilter{}, "user.loginx,user.disable,user.login.failed,user.login,user.register"},
		{"group", AuditFilter{Actions: []string{"user.login.*"}}, "user.login.failed,user.login"},
		{"exact", AuditFilter{Actions: []string{"user.login"}}, "user.login"},
		{"two actions", AuditFilter{Actions: []string{"user.disable", "user.register"}}, "user.disable,user.register"},
		{"user b as actor or target", AuditFilter{UserID: &bid}, "user.disable,user.login.failed"},
		{"user a as actor or target", AuditFilter{UserID: &aid}, "user.loginx,user.disable,user.login,user.register"},
		{"from", AuditFilter{From: &from3h}, "user.loginx,user.disable,user.login.failed"},
		{"from and inclusive to", AuditFilter{From: &from31h, To: &to2h}, "user.login.failed,user.login"},
	} {
		if got := actionsOf(mustList(t, st, c.f)); got != c.want {
			t.Errorf("%s: got %s; want %s", c.name, got, c.want)
		}
	}

	failed := mustList(t, st, AuditFilter{Actions: []string{"user.login.failed"}})
	if failed[0].Detail["reason"] != "wrong_password" || failed[0].TargetUserID == nil || *failed[0].TargetUserID != bid {
		t.Fatalf("failed entry = %+v", failed[0])
	}

	p1 := mustList(t, st, AuditFilter{Limit: 2})
	p2 := mustList(t, st, AuditFilter{Limit: 2, BeforeID: p1[1].ID})
	p3 := mustList(t, st, AuditFilter{Limit: 2, BeforeID: p2[1].ID})
	if actionsOf(p1) != "user.loginx,user.disable" || actionsOf(p2) != "user.login.failed,user.login" || actionsOf(p3) != "user.register" {
		t.Fatalf("pages = %s | %s | %s", actionsOf(p1), actionsOf(p2), actionsOf(p3))
	}
}

func TestAuditListGroupEscapesLike(t *testing.T) {
	st, _ := newTestStore(t)
	mustAudit(t, st, nil, "aXb.c", nil, nil)
	mustAudit(t, st, nil, "a_b.c", nil, nil)
	if got := actionsOf(mustList(t, st, AuditFilter{Actions: []string{"a_b.*"}})); got != "a_b.c" {
		t.Fatalf("group a_b.* matched %s", got)
	}
}

func TestAuditListLimitBounds(t *testing.T) {
	st, _ := newTestStore(t)
	for i := 0; i < AuditListMax+1; i++ {
		mustAudit(t, st, nil, fmt.Sprintf("x.n%d", i), nil, nil)
	}
	if n := len(mustList(t, st, AuditFilter{})); n != 100 {
		t.Fatalf("default page = %d; want 100", n)
	}
	if n := len(mustList(t, st, AuditFilter{Limit: 100000})); n != AuditListMax {
		t.Fatalf("capped page = %d; want %d", n, AuditListMax)
	}
}

func TestPruneAuditRemovesOnlyListedOldActions(t *testing.T) {
	st, clk := newTestStore(t)
	base := clk.Now()
	u := mustCreate(t, st, "u@example.org", "U")
	uid := u.ID
	clk.t = base.Add(-91 * 24 * time.Hour)
	mustAudit(t, st, nil, "user.login", &uid, nil)
	mustAudit(t, st, nil, "user.login.failed", &uid, nil)
	mustAudit(t, st, nil, "user.register", &uid, nil)
	clk.t = base.Add(-89 * 24 * time.Hour)
	mustAudit(t, st, nil, "user.login", &uid, nil)
	clk.t = base
	n, err := st.PruneAudit([]string{"user.login", "user.login.failed"}, 90*24*time.Hour)
	if err != nil || n != 2 {
		t.Fatalf("PruneAudit = %d, %v; want 2", n, err)
	}
	if got := actionsOf(mustList(t, st, AuditFilter{})); got != "user.login,user.register" {
		t.Fatalf("left = %s", got)
	}
}

func TestUsersByID(t *testing.T) {
	st, _ := newTestStore(t)
	a := mustCreate(t, st, "a@example.org", "A")
	b := mustCreate(t, st, "b@example.org", "B")
	if err := st.Delete(b.ID); err != nil {
		t.Fatal(err)
	}
	got, err := st.UsersByID([]int64{a.ID, b.ID, 999})
	if err != nil || len(got) != 1 || got[a.ID].Email != "a@example.org" {
		t.Fatalf("UsersByID = %+v, %v", got, err)
	}
	if empty, err := st.UsersByID(nil); err != nil || len(empty) != 0 {
		t.Fatalf("UsersByID(nil) = %+v, %v", empty, err)
	}
}

func hasIndex(t *testing.T, st *Store, name string) bool {
	t.Helper()
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM pg_indexes WHERE schemaname=current_schema() AND indexname=$1`, name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

func TestFreshDatabaseHasAuditTimeIndex(t *testing.T) {
	st, _ := newTestStore(t)
	if !hasIndex(t, st, "audit_at") {
		t.Fatal("audit_at index missing on a fresh database")
	}
}

// A native account schema at v2 binary gains the audit_at index and keeps its rows.
func TestMigrateV2DatabaseToV3(t *testing.T) {
	st := migratedTestStore(t, 2, `INSERT INTO audit_log (at,action,detail) VALUES (1,'user.register','{}')`)
	if v, err := st.SchemaVersion(); err != nil || v != len(migrations) {
		t.Fatalf("SchemaVersion = %d, %v; want %d", v, err, len(migrations))
	}
	if !hasIndex(t, st, "audit_at") {
		t.Fatal("audit_at index missing after migration")
	}
	if got := actionsOf(mustList(t, st, AuditFilter{})); got != "user.register" {
		t.Fatalf("v2 audit rows after migration = %q", got)
	}
}

package main

import (
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/meshcore-analyzer/users"
)

func auditPage(t *testing.T, f *authFixture, c *client, query string) auditListJSON {
	t.Helper()
	w := f.do("GET", "/api/admin/audit"+query, nil, as(c))
	expectStatus(t, w, 200)
	return decode[auditListJSON](t, w)
}

func entryActions(p auditListJSON) string {
	parts := make([]string, len(p.Entries))
	for i, e := range p.Entries {
		parts[i] = e.Action
	}
	return strings.Join(parts, ",")
}

func TestAdminAuditRequiresAdmin(t *testing.T) {
	f, _, uma := adminFixture(t)
	expectStatus(t, f.do("GET", "/api/admin/audit", nil), 401)
	expectStatus(t, f.do("GET", "/api/admin/audit", nil, as(uma)), 403)
}

func TestAdminAuditFiltersAndUsers(t *testing.T) {
	f, boss, uma := adminFixture(t)
	for i := 0; i < 2; i++ {
		expectStatus(t, f.do("POST", "/api/auth/login", loginRequest{Email: "uma@example.org", Password: "wrong password!"}), 401)
	}
	f.login(t, "uma@example.org", pw)

	p := auditPage(t, f, boss, "?action=user.login.failed")
	if len(p.Entries) != 2 || p.Next != nil {
		t.Fatalf("failed logins = %+v", p)
	}
	e := p.Entries[0]
	if e.Actor != nil || e.Target == nil || e.Target.ID != uma.me.ID || e.Target.DisplayName != "Uma" ||
		e.Target.Email != "uma@example.org" || e.Target.Deleted || e.Detail["reason"] != "wrong_password" || e.At == "" {
		t.Fatalf("entry = %+v (target %+v)", e, e.Target)
	}
	if got := entryActions(auditPage(t, f, boss, "?action=user.login.*")); got != "user.login,user.login.failed,user.login.failed" {
		t.Fatalf("group = %s", got)
	}
	if got := entryActions(auditPage(t, f, boss, fmt.Sprintf("?user=%d", boss.me.ID))); got != "user.activate,user.register" {
		t.Fatalf("user filter = %s", got)
	}
	future := url.QueryEscape("2100-01-01T00:00:00Z")
	if p := auditPage(t, f, boss, "?from="+future); len(p.Entries) != 0 || p.Next != nil {
		t.Fatalf("future period = %+v", p)
	}
}

func TestAdminAuditPaginates(t *testing.T) {
	f, boss, _ := adminFixture(t) // 4 rows: register and activate for each user
	first := auditPage(t, f, boss, "?limit=3")
	if len(first.Entries) != 3 || first.Next == nil || *first.Next != first.Entries[2].ID {
		t.Fatalf("first page = %+v", first)
	}
	second := auditPage(t, f, boss, fmt.Sprintf("?limit=3&before=%d", *first.Next))
	if len(second.Entries) != 1 || second.Next != nil || second.Entries[0].ID >= *first.Next {
		t.Fatalf("second page = %+v", second)
	}
}

func TestAdminAuditDeletedUser(t *testing.T) {
	f, boss, uma := adminFixture(t)
	expectStatus(t, f.do("DELETE", userPath(uma.me.ID, ""), nil, as(boss)), 200)
	p := auditPage(t, f, boss, fmt.Sprintf("?user=%d&action=user.register", uma.me.ID))
	if len(p.Entries) != 1 || p.Entries[0].Target == nil || !p.Entries[0].Target.Deleted ||
		p.Entries[0].Target.ID != uma.me.ID || p.Entries[0].Target.Email != "" || p.Entries[0].Target.DisplayName != "" {
		t.Fatalf("deleted target = %+v", p)
	}
	del := auditPage(t, f, boss, "?action=user.delete")
	if len(del.Entries) != 1 || del.Entries[0].Actor == nil || del.Entries[0].Actor.ID != boss.me.ID || del.Entries[0].Actor.Deleted {
		t.Fatalf("delete row = %+v", del)
	}
}

func TestAdminAuditRejectsBadParameters(t *testing.T) {
	f, boss, _ := adminFixture(t)
	for _, q := range []string{
		"?action=DROP%20TABLE", "?action=user.*.x", "?action=*", "?user=abc", "?user=0",
		"?from=yesterday", "?to=2026-13-01T00:00:00Z", "?before=-1", "?before=x", "?limit=0", "?limit=ten",
		"?from=2026-10-02T00:00:00Z&to=2026-10-01T00:00:00Z",
	} {
		if w := f.do("GET", "/api/admin/audit"+q, nil, as(boss)); w.Code != 400 {
			t.Errorf("%s = %d; want 400", q, w.Code)
		}
	}
}

func TestParseAuditFilterDefaultsAndCap(t *testing.T) {
	flt, err := parseAuditFilter(url.Values{"limit": {"100000"}})
	if err != nil || flt.Limit != users.AuditListMax {
		t.Fatalf("limit 100000 = %+v, %v; want %d", flt, err, users.AuditListMax)
	}
	flt, err = parseAuditFilter(url.Values{})
	if err != nil || flt.Limit != 100 || flt.Actions != nil || flt.UserID != nil || flt.From != nil || flt.BeforeID != 0 {
		t.Fatalf("defaults = %+v, %v", flt, err)
	}
}

func TestAdminAuditStoreErrorIs500(t *testing.T) {
	f, boss, _ := adminFixture(t)
	f.breakTable(t, "audit_log")
	expectStatus(t, f.do("GET", "/api/admin/audit", nil, as(boss)), 500)
}

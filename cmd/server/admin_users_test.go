package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/meshcore-analyzer/mailer"
	"github.com/meshcore-analyzer/users"
)

// adminFixture: config admin "boss" plus regular user "uma".
func adminFixture(t *testing.T) (*authFixture, *client, *client) {
	f := newAuthFixture(t, "boss@example.org")
	return f, f.registerAndActivate(t, "boss@example.org", "Boss", pw), f.registerAndActivate(t, "uma@example.org", "Uma", pw)
}

func userPath(id int64, suffix string) string {
	return fmt.Sprintf("/api/admin/users/%d%s", id, suffix)
}

func TestAdminUsersRequiresAdmin(t *testing.T) {
	f, _, uma := adminFixture(t)
	expectStatus(t, f.do("GET", "/api/admin/users", nil), 401)
	expectStatus(t, f.do("GET", "/api/admin/users", nil, as(uma)), 403)
}

func TestAdminListAndDetail(t *testing.T) {
	f, boss, uma := adminFixture(t)
	f.do("POST", "/api/auth/register", registerRequest{Email: "pending@example.org", DisplayName: "Pen", Password: pw})
	w := f.do("GET", "/api/admin/users?status=pending", nil, as(boss))
	expectStatus(t, w, 200)
	rows := decode[[]adminUserJSON](t, w)
	if len(rows) != 1 || rows[0].Email != "pending@example.org" || rows[0].LastMail == nil || rows[0].LastMail.Purpose != "activate" {
		t.Fatalf("pending rows = %+v", rows)
	}
	expectStatus(t, f.do("GET", "/api/admin/users?status=bogus", nil, as(boss)), 400)
	w = f.do("GET", userPath(uma.me.ID, ""), nil, as(boss))
	expectStatus(t, w, 200)
	d := decode[adminUserDetailJSON](t, w)
	if d.User.Email != "uma@example.org" || len(d.Sessions) != 1 || len(d.Mail) != 1 || len(d.Audit) == 0 {
		t.Fatalf("detail = %+v", d)
	}
	all := decode[[]adminUserJSON](t, f.do("GET", "/api/admin/users", nil, as(boss)))
	for _, r := range all {
		if r.Email == "boss@example.org" && !r.ConfigAdmin {
			t.Fatal("config admin not marked")
		}
	}
}

func TestAdminDisableEnable(t *testing.T) {
	f, boss, uma := adminFixture(t)
	expectStatus(t, f.do("POST", userPath(uma.me.ID, "/disable"), nil, as(boss)), 200)
	expectStatus(t, f.do("GET", "/api/auth/me", nil, as(uma)), 401)
	expectStatus(t, f.do("POST", "/api/auth/login", loginRequest{Email: "uma@example.org", Password: pw}), 401)
	expectStatus(t, f.do("POST", userPath(uma.me.ID, "/disable"), nil, as(boss)), 409)
	expectStatus(t, f.do("POST", userPath(uma.me.ID, "/enable"), nil, as(boss)), 200)
	f.login(t, "uma@example.org", pw)
}

func TestAdminGuards(t *testing.T) {
	f, boss, uma := adminFixture(t)
	expectStatus(t, f.do("POST", userPath(boss.me.ID, "/disable"), nil, as(boss)), 409) // self
	expectStatus(t, f.do("POST", userPath(uma.me.ID, "/role"), roleRequest{Role: users.RoleAdmin}, as(boss)), 200)
	umaAdmin := f.login(t, "uma@example.org", pw)
	expectStatus(t, f.do("POST", userPath(boss.me.ID, "/disable"), nil, as(umaAdmin)), 409)                            // config admin
	expectStatus(t, f.do("DELETE", userPath(boss.me.ID, ""), nil, as(umaAdmin)), 409)                                  // config admin
	expectStatus(t, f.do("POST", userPath(boss.me.ID, "/role"), roleRequest{Role: users.RoleUser}, as(umaAdmin)), 409) // config admin
	expectStatus(t, f.do("POST", userPath(uma.me.ID, "/role"), roleRequest{Role: "root"}, as(boss)), 400)
	// uma may demote herself: boss remains.
	expectStatus(t, f.do("POST", userPath(uma.me.ID, "/role"), roleRequest{Role: users.RoleUser}, as(umaAdmin)), 200)
}

func TestAdminLastAdminCannotDemoteSelf(t *testing.T) {
	f := newAuthFixture(t)
	solo := f.registerAndActivate(t, "solo@example.org", "Solo", pw)
	f.st.SetRole(solo.me.ID, users.RoleAdmin) // UI-promoted, not a config admin
	expectStatus(t, f.do("POST", userPath(solo.me.ID, "/role"), roleRequest{Role: users.RoleUser}, as(solo)), 409)
}

func TestAdminManualActivate(t *testing.T) {
	f, boss, _ := adminFixture(t)
	f.do("POST", "/api/auth/register", registerRequest{Email: "late@example.org", DisplayName: "Late", Password: pw})
	link := f.lastToken(t)
	late, _ := f.st.GetByEmail("late@example.org")
	expectStatus(t, f.do("POST", userPath(late.ID, "/activate"), nil, as(boss)), 200)
	got, _ := f.st.GetByID(late.ID)
	if got.Status != users.StatusActive || got.ActivatedBy == nil || *got.ActivatedBy != boss.me.ID {
		t.Fatalf("after manual activate: %+v", got)
	}
	expectStatus(t, f.do("POST", "/api/auth/activate", activateRequest{Token: link, Password: pw}), 410)
	expectStatus(t, f.do("POST", userPath(late.ID, "/activate"), nil, as(boss)), 409)
	if !hasAudit(t, f, late.ID, "user.activate.manual") {
		t.Fatal("no user.activate.manual audit row")
	}
	f.login(t, "late@example.org", pw)
}

func TestAdminResendActivation(t *testing.T) {
	f, boss, uma := adminFixture(t)
	f.do("POST", "/api/auth/register", registerRequest{Email: "re@example.org", DisplayName: "Re", Password: pw})
	old := f.lastToken(t)
	re, _ := f.st.GetByEmail("re@example.org")
	expectStatus(t, f.do("POST", userPath(re.ID, "/resend-activation"), nil, as(boss)), 200)
	fresh := f.lastToken(t)
	if fresh == old {
		t.Fatal("no new link sent")
	}
	expectStatus(t, f.do("POST", "/api/auth/activate", activateRequest{Token: old, Password: pw}), 410)
	expectStatus(t, f.do("POST", "/api/auth/activate", activateRequest{Token: fresh, Password: pw}), 200)
	expectStatus(t, f.do("POST", userPath(uma.me.ID, "/resend-activation"), nil, as(boss)), 409)
}

func TestAdminDelete(t *testing.T) {
	f, boss, uma := adminFixture(t)
	expectStatus(t, f.do("DELETE", userPath(uma.me.ID, ""), nil, as(boss)), 200)
	expectStatus(t, f.do("GET", userPath(uma.me.ID, ""), nil, as(boss)), 404)
}

func TestAdminMailRefresh(t *testing.T) {
	f, boss, uma := adminFixture(t)
	mails, _ := f.st.MailForUser(uma.me.ID, 10)
	m := mails[0]
	f.fake.SetEvents(m.ProviderMessageID, []mailer.Event{
		{MessageID: m.ProviderMessageID, Event: mailer.EventHardBounce, Reason: "user unknown", At: m.SentAt.Add(60e9)},
	})
	w := f.do("POST", userPath(uma.me.ID, fmt.Sprintf("/mail/%d/refresh", m.ID)), nil, as(boss))
	expectStatus(t, w, 200)
	if got := decode[mailJSON](t, w); got.LastEvent != mailer.EventHardBounce || got.LastReason != "user unknown" {
		t.Fatalf("refreshed = %+v", got)
	}
	if !hasAudit(t, f, uma.me.ID, "user.mail.refresh") {
		t.Fatal("no user.mail.refresh audit row")
	}
	if u, _ := f.st.GetByID(uma.me.ID); !u.EmailBouncing {
		t.Fatal("hard bounce did not flag the address")
	}
	expectStatus(t, f.do("POST", userPath(boss.me.ID, fmt.Sprintf("/mail/%d/refresh", m.ID)), nil, as(boss)), 404) // wrong owner
}

func hasAudit(t *testing.T, f *authFixture, userID int64, action string) bool {
	t.Helper()
	entries, err := f.st.AuditFor(userID, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Action == action {
			return true
		}
	}
	return false
}

// A pending row squatting on a config-admin address is not an admin yet and
// stays deletable.
func TestAdminDeletePendingSquatterOnConfigAddress(t *testing.T) {
	f, boss, _ := adminFixture(t)
	f.do("POST", "/api/auth/register", registerRequest{Email: "squat@example.org", DisplayName: "Sq", Password: pw})
	f.srv.auth.set.adminEmails["squat@example.org"] = true
	sq, _ := f.st.GetByEmail("squat@example.org")
	expectStatus(t, f.do("DELETE", userPath(sq.ID, ""), nil, as(boss)), 200)
	if _, err := f.st.GetByID(sq.ID); err == nil {
		t.Fatal("pending squatter still present")
	}
}

func TestAdminUnknownAndBadID(t *testing.T) {
	f, boss, _ := adminFixture(t)
	expectStatus(t, f.do("POST", userPath(9999, "/disable"), nil, as(boss)), 404)
	expectStatus(t, f.do("GET", "/api/admin/users/abc", nil, as(boss)), 400)
}

// A pending account must go through activation; disable then enable must not
// be a way around it.
func TestAdminCannotDisablePending(t *testing.T) {
	f, boss, _ := adminFixture(t)
	f.do("POST", "/api/auth/register", registerRequest{Email: "pend@example.org", DisplayName: "Pe", Password: pw})
	p, _ := f.st.GetByEmail("pend@example.org")
	expectStatus(t, f.do("POST", userPath(p.ID, "/disable"), nil, as(boss)), 409)
	got, _ := f.st.GetByID(p.ID)
	if got.Status != users.StatusPending {
		t.Fatalf("status = %s, want pending", got.Status)
	}
	expectStatus(t, f.do("POST", userPath(p.ID, "/enable"), nil, as(boss)), 409)
}

func TestAdminDisableKillsPendingLinks(t *testing.T) {
	f, boss, uma := adminFixture(t)
	confirm, reset := pendingLinks(t, f, uma, "uma@example.org", "attacker@example.org")
	expectStatus(t, f.do("POST", userPath(uma.me.ID, "/disable"), nil, as(boss)), 200)
	expectStatus(t, f.do("POST", userPath(uma.me.ID, "/enable"), nil, as(boss)), 200)
	expectStatus(t, f.do("POST", "/api/account/confirm-email", tokenRequest{Token: confirm}), 410)
	expectStatus(t, f.do("POST", "/api/auth/reset", resetRequest{Token: reset, Password: "another new secret"}), 410)
}

func TestAdminDisableTokenStoreFailureIs500(t *testing.T) {
	f, boss, uma := adminFixture(t)
	f.breakTable(t, "tokens")
	expectStatus(t, f.do("POST", userPath(uma.me.ID, "/disable"), nil, as(boss)), 500)
}

func TestAdminRoleChangeOnPendingIs409(t *testing.T) {
	f, boss, _ := adminFixture(t)
	f.do("POST", "/api/auth/register", registerRequest{Email: "pend@example.org", DisplayName: "Pend", Password: pw})
	p, _ := f.st.GetByEmail("pend@example.org")
	w := f.do("POST", userPath(p.ID, "/role"), roleRequest{Role: users.RoleAdmin}, as(boss))
	expectStatus(t, w, 409)
	if !strings.Contains(w.Body.String(), "activate the account first") {
		t.Fatalf("body = %s", w.Body.String())
	}
	if got, _ := f.st.GetByID(p.ID); got.Role != users.RoleUser {
		t.Fatalf("role changed on a pending user: %+v", got)
	}
}

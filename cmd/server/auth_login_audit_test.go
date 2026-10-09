package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/meshcore-analyzer/users"
)

func TestLoginIsAudited(t *testing.T) {
	f := newAuthFixture(t)
	dave := f.registerAndActivate(t, "dave@example.org", "Dave", pw)
	expectStatus(t, f.do("POST", "/api/auth/register", registerRequest{Email: "pen@example.org", DisplayName: "Pen", Password: pw}), 200)
	pen, err := f.st.GetByEmail("pen@example.org")
	if err != nil {
		t.Fatal(err)
	}

	f.login(t, "dave@example.org", pw)
	expectStatus(t, f.do("POST", "/api/auth/login", loginRequest{Email: "dave@example.org", Password: "wrong password!"}), 401)
	expectStatus(t, f.do("POST", "/api/auth/login", loginRequest{Email: "nobody@example.org", Password: pw}), 401)
	expectStatus(t, f.do("POST", "/api/auth/login", loginRequest{Email: "pen@example.org", Password: pw}), 401)
	if err := f.st.SetStatus(dave.me.ID, users.StatusDisabled); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, f.do("POST", "/api/auth/login", loginRequest{Email: "dave@example.org", Password: pw}), 401)

	type row struct {
		action string
		target int64
		reason string
	}
	want := []row{
		{"user.login.failed", dave.me.ID, "disabled"},
		{"user.login.failed", pen.ID, "pending"},
		{"user.login.failed", dave.me.ID, "wrong_password"},
		{"user.login", dave.me.ID, ""},
	}
	got, err := f.st.AuditList(users.AuditFilter{Actions: []string{"user.login.*"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("login rows = %+v; want %d (the unknown address writes none)", got, len(want))
	}
	for i, e := range got {
		if e.Action != want[i].action || e.ActorUserID != nil || e.TargetUserID == nil ||
			*e.TargetUserID != want[i].target || e.Detail["reason"] != want[i].reason {
			t.Errorf("row %d = %+v; want %+v", i, e, want[i])
		}
	}
	all, _ := f.st.AuditList(users.AuditFilter{})
	for _, e := range all {
		for _, v := range e.Detail {
			if strings.Contains(v, "nobody") {
				t.Fatalf("an unknown address reached the audit log: %+v", e)
			}
		}
	}
}

func TestLoginFailureResponsesAreIdentical(t *testing.T) {
	f := newAuthFixture(t)
	dis := f.registerAndActivate(t, "dis@example.org", "Dis", pw)
	if err := f.st.SetStatus(dis.me.ID, users.StatusDisabled); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, f.do("POST", "/api/auth/register", registerRequest{Email: "pen@example.org", DisplayName: "Pen", Password: pw}), 200)
	bodies := map[string]string{}
	for name, req := range map[string]loginRequest{
		"unknown":  {Email: "nobody@example.org", Password: pw},
		"wrong":    {Email: "dis@example.org", Password: "wrong password!"},
		"pending":  {Email: "pen@example.org", Password: pw},
		"disabled": {Email: "dis@example.org", Password: pw},
	} {
		w := f.do("POST", "/api/auth/login", req)
		if w.Code != 401 {
			t.Errorf("%s: status %d; want 401", name, w.Code)
		}
		bodies[name] = w.Body.String()
	}
	for name, b := range bodies {
		if b != bodies["unknown"] {
			t.Errorf("%s body %q differs from the unknown-address body %q", name, b, bodies["unknown"])
		}
	}
}

func TestLoginAuditFailureDoesNotChangeTheAnswer(t *testing.T) {
	f := newAuthFixture(t)
	f.registerAndActivate(t, "dave@example.org", "Dave", pw)
	ref := f.do("POST", "/api/auth/login", loginRequest{Email: "nobody@example.org", Password: pw}).Body.String()
	f.breakTable(t, "audit_log")
	w := f.do("POST", "/api/auth/login", loginRequest{Email: "dave@example.org", Password: "wrong password!"})
	if w.Code != 401 || w.Body.String() != ref {
		t.Fatalf("with a broken audit log: %d %q; want 401 %q", w.Code, w.Body.String(), ref)
	}
	f.login(t, "dave@example.org", pw)
}

func TestJanitorPrunesOldLoginAudit(t *testing.T) {
	f := newAuthFixture(t)
	dave := f.registerAndActivate(t, "dave@example.org", "Dave", pw)
	id := dave.me.ID
	old := time.Now().Add(-91 * 24 * time.Hour)
	f.st.SetClock(func() time.Time { return old })
	for _, a := range []string{"user.login", "user.login.failed", "user.password.change"} {
		if err := f.st.Audit(nil, a, &id, nil); err != nil {
			t.Fatal(err)
		}
	}
	f.st.SetClock(time.Now)
	f.srv.auth.prune()
	list, err := f.st.AuditList(users.AuditFilter{UserID: &id})
	if err != nil {
		t.Fatal(err)
	}
	var acts []string
	for _, e := range list {
		acts = append(acts, e.Action)
	}
	if got := strings.Join(acts, ","); got != "user.password.change,user.activate,user.register" {
		t.Fatalf("after prune: %s", got)
	}
}

// A locked users.db must not hold the login answer: the audit row is
// written in the background and lands once the lock is gone. A synchronous
// write would block on the lock and then fail, leaving no row.
func TestLoginAnswersBeforeTheAuditWrite(t *testing.T) {
	f := newAuthFixture(t)
	dave := f.registerAndActivate(t, "dave@example.org", "Dave", pw)

	db, err := openFixtureSQL(f.ownerURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), "BEGIN; LOCK TABLE audit_log IN SHARE MODE"); err != nil {
		t.Fatal(err)
	}

	w := f.serve("POST", "/api/auth/login", loginRequest{Email: "dave@example.org", Password: "wrong password!"})
	if w.Code != 401 {
		t.Fatalf("status %d; want 401", w.Code)
	}
	if _, err := conn.ExecContext(context.Background(), "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	f.srv.auth.waitAudits()
	got, err := f.st.AuditList(users.AuditFilter{Actions: []string{"user.login.failed"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].TargetUserID == nil || *got[0].TargetUserID != dave.me.ID {
		t.Fatalf("login rows after the lock = %+v; want one for dave", got)
	}
}

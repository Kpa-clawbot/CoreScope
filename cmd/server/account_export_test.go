package main

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/meshcore-analyzer/users"
)

const exportPubkey = "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"

var exportFilenameRE = regexp.MustCompile(`^attachment; filename="corescope-account-\d{4}-\d{2}-\d{2}\.json"$`)

func TestAccountExportRequiresSession(t *testing.T) {
	f := newAuthFixture(t)
	expectStatus(t, f.do("GET", "/api/account/export", nil), 401)
}

func TestAccountExportSections(t *testing.T) {
	f, boss, uma := adminFixture(t)
	st, uid, bossID := f.st, uma.me.ID, boss.me.ID
	if _, err := st.PutSettings(uid, users.SettingsVersion{}, `{"v":1,"keys":{"meshcore-theme":"dark"}}`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Propose(users.KindHashtagChannel, "#export-test", uid, users.ProposalLimits{}); err != nil {
		t.Fatal(err)
	}
	prefs, err := st.SetNotifyPrefs(uid, true, []string{users.NotifyNodeOffline})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddWatch(uid, exportPubkey, 10); err != nil {
		t.Fatal(err)
	}
	if err := st.Audit(&bossID, "user.role", &uid, map[string]string{"role": "user"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.LogMail(&uid, "uma@example.org", "reset", "<export-1>"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.RecordMailEvent("<export-1>", "delivered", time.Now(), ""); err != nil {
		t.Fatal(err)
	}
	reset, err := st.IssueToken(uid, users.PurposeReset, time.Hour, "")
	if err != nil {
		t.Fatal(err)
	}
	change, err := st.IssueToken(uid, users.PurposeEmailChange, time.Hour, "uma-new@example.org")
	if err != nil {
		t.Fatal(err)
	}
	full, err := st.GetByID(uid)
	if err != nil {
		t.Fatal(err)
	}

	w := f.do("GET", "/api/account/export", nil, as(uma))
	expectStatus(t, w, 200)
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q", cc)
	}
	if cd := w.Header().Get("Content-Disposition"); !exportFilenameRE.MatchString(cd) {
		t.Errorf("Content-Disposition = %q", cd)
	}
	body := w.Body.String()
	x := decode[accountExport](t, w)

	if x.FormatVersion != 1 || x.Instance != testBase || x.ExportedAt == "" {
		t.Fatalf("header fields = %d %q %q", x.FormatVersion, x.Instance, x.ExportedAt)
	}
	p := x.Profile
	if p.ID != uid || p.Email != "uma@example.org" || p.DisplayName != "Uma" || p.Role != users.RoleUser ||
		p.Status != users.StatusActive || p.CreatedAt == "" || p.EmailBouncing {
		t.Fatalf("profile = %+v", p)
	}
	if p.PendingEmail == nil || *p.PendingEmail != "uma-new@example.org" {
		t.Fatalf("pendingEmail = %v; want the unconfirmed address", p.PendingEmail)
	}
	if full.ActivatedAt == nil || p.ActivatedAt == nil || *p.ActivatedAt != rfc3339(*full.ActivatedAt) || p.ActivatedBy != nil {
		t.Fatalf("activation (link): profile = %+v, stored at %v", p, full.ActivatedAt)
	}
	if len(x.Sessions) != 1 || x.Sessions[0].CreatedAt == "" || x.Sessions[0].LastSeenAt == "" {
		t.Fatalf("sessions = %+v", x.Sessions)
	}
	if x.Settings == nil || x.Settings.Revision != 1 || x.Settings.UpdatedAt == "" || x.Settings.Doc.Keys["meshcore-theme"] != "dark" {
		t.Fatalf("settings = %+v", x.Settings)
	}
	if len(x.Proposals) != 1 || x.Proposals[0].Subject != "#export-test" || x.Proposals[0].Kind != users.KindHashtagChannel ||
		x.Proposals[0].Status != users.ProposalPending || x.Proposals[0].DecidedAt != nil {
		t.Fatalf("proposals = %+v", x.Proposals)
	}
	n := x.Notifications
	if n.Prefs == nil || !n.Prefs.Enabled || len(n.Prefs.Events) != 1 || n.Prefs.Events[0] != users.NotifyNodeOffline ||
		len(n.Watches) != 1 || n.Watches[0].Pubkey != exportPubkey || n.Watches[0].CreatedAt == "" {
		t.Fatalf("notifications = %+v", n)
	}
	var role *exportAuditEntry
	for i := range x.Audit {
		switch x.Audit[i].Action {
		case "user.role":
			role = &x.Audit[i]
		case "user.export":
			t.Fatal("the export contains its own audit row")
		}
	}
	if role == nil || role.ActorUserID == nil || *role.ActorUserID != bossID || role.TargetUserID == nil ||
		*role.TargetUserID != uid || role.Detail["role"] != "user" || role.At == "" {
		t.Fatalf("user.role audit entry = %+v", role)
	}
	var resetMail *exportMail
	for i := range x.Mail {
		if x.Mail[i].Purpose == "reset" {
			resetMail = &x.Mail[i]
		}
	}
	if resetMail == nil || resetMail.To != "uma@example.org" || resetMail.Status != "delivered" || resetMail.SentAt == "" ||
		len(resetMail.Events) != 1 || resetMail.Events[0].Event != "delivered" {
		t.Fatalf("reset mail = %+v", resetMail)
	}

	secrets := map[string]string{
		"password hash":      full.PasswordHash,
		"session cookie":     uma.cookie.Value,
		"session token hash": users.HashToken(uma.cookie.Value),
		"csrf token":         uma.csrf,
		"unsubscribe token":  prefs.UnsubToken,
		"reset token":        reset,
		"reset token hash":   users.HashToken(reset),
		"email change token": change,
		"email change hash":  users.HashToken(change),
	}
	for name, v := range secrets {
		if v == "" {
			t.Fatalf("%s is empty: the check below would prove nothing", name)
		}
		if strings.Contains(body, v) {
			t.Errorf("export contains the %s", name)
		}
	}
	for _, other := range []string{"boss@example.org", `"Boss"`} {
		if strings.Contains(body, other) {
			t.Errorf("export contains another account's %s", other)
		}
	}
}

func TestAccountExportWritesAuditRowAfterwards(t *testing.T) {
	f := newAuthFixture(t)
	uma := f.registerAndActivate(t, "uma@example.org", "Uma", pw)
	expectStatus(t, f.do("GET", "/api/account/export", nil, as(uma)), 200)
	entries, err := f.st.AuditList(users.AuditFilter{Actions: []string{"user.export"}})
	if err != nil || len(entries) != 1 {
		t.Fatalf("user.export rows = %d, %v", len(entries), err)
	}
	e := entries[0]
	if e.ActorUserID == nil || *e.ActorUserID != uma.me.ID || e.TargetUserID == nil || *e.TargetUserID != uma.me.ID {
		t.Fatalf("user.export row = %+v", e)
	}
	second := decode[accountExport](t, f.do("GET", "/api/account/export", nil, as(uma)))
	seen := 0
	for _, a := range second.Audit {
		if a.Action == "user.export" {
			seen++
		}
	}
	if seen != 1 {
		t.Fatalf("second export lists %d user.export rows; want the 1 from the first export", seen)
	}
}

func TestAccountExportEmptySectionsAndNoWrites(t *testing.T) {
	f := newAuthFixture(t)
	uma := f.registerAndActivate(t, "uma@example.org", "Uma", pw)
	w := f.do("GET", "/api/account/export", nil, as(uma))
	expectStatus(t, w, 200)
	body := w.Body.String()
	for _, want := range []string{`"settings":null`, `"proposals":[]`, `"prefs":null`, `"watches":[]`, `"pendingEmail":null`} {
		if !strings.Contains(body, want) {
			t.Errorf("export lacks %s: %s", want, body)
		}
	}
	if p, err := f.st.StoredNotifyPrefs(uma.me.ID); err != nil || p != nil {
		t.Fatalf("the export created notification prefs: %+v, %v", p, err)
	}
}

func TestAccountExportNoEventsIsEmptyList(t *testing.T) {
	f := newAuthFixture(t)
	uma := f.registerAndActivate(t, "uma@example.org", "Uma", pw)
	if _, err := f.st.SetNotifyPrefs(uma.me.ID, false, nil); err != nil {
		t.Fatal(err)
	}
	w := f.do("GET", "/api/account/export", nil, as(uma))
	expectStatus(t, w, 200)
	if body := w.Body.String(); !strings.Contains(body, `"prefs":{"enabled":false,"events":[]}`) {
		t.Fatalf("prefs without events: %s", body)
	}
}

func TestAccountExportStoreFailure(t *testing.T) {
	f := newAuthFixture(t)
	uma := f.registerAndActivate(t, "uma@example.org", "Uma", pw)
	f.breakTable(t, "proposals")
	w := f.do("GET", "/api/account/export", nil, as(uma))
	expectStatus(t, w, 500)
	if cd := w.Header().Get("Content-Disposition"); cd != "" {
		t.Fatalf("Content-Disposition on a failed export: %q", cd)
	}
	if entries, _ := f.st.AuditList(users.AuditFilter{Actions: []string{"user.export"}}); len(entries) != 0 {
		t.Fatal("user.export recorded for a failed export")
	}
}

func TestAccountExportManualActivation(t *testing.T) {
	f, boss, _ := adminFixture(t)
	f.do("POST", "/api/auth/register", registerRequest{Email: "late@example.org", DisplayName: "Late", Password: pw})
	late, err := f.st.GetByEmail("late@example.org")
	if err != nil {
		t.Fatal(err)
	}
	expectStatus(t, f.do("POST", userPath(late.ID, "/activate"), nil, as(boss)), 200)
	lateC := f.login(t, "late@example.org", pw)
	x := decode[accountExport](t, f.do("GET", "/api/account/export", nil, as(lateC)))
	if x.Profile.ActivatedBy == nil || *x.Profile.ActivatedBy != boss.me.ID || x.Profile.ActivatedAt == nil {
		t.Fatalf("activation (manual): profile = %+v", x.Profile)
	}
	var activate *exportMail
	for i := range x.Mail {
		if x.Mail[i].Purpose == "activate" {
			activate = &x.Mail[i]
		}
	}
	if activate == nil || activate.To != "late@example.org" {
		t.Fatalf("activation mail = %+v", activate)
	}
}

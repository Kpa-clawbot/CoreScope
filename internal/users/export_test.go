package users

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestAuditAllForHasNoCap(t *testing.T) {
	st, clk := newTestStore(t)
	u := mustCreate(t, st, "a@example.org", "Aaa")
	uid := u.ID
	for i := 0; i < 120; i++ {
		if err := st.Audit(&uid, "user.login", &uid, nil); err != nil {
			t.Fatal(err)
		}
		clk.Advance(time.Second)
	}
	capped, err := st.AuditFor(uid, 0)
	if err != nil || len(capped) != 100 {
		t.Fatalf("AuditFor default = %d, %v; want 100", len(capped), err)
	}
	all, err := st.AuditAllFor(uid)
	if err != nil || len(all) != 120 {
		t.Fatalf("AuditAllFor = %d, %v; want 120", len(all), err)
	}
	if !all[0].At.After(all[119].At) {
		t.Fatal("AuditAllFor is not newest first")
	}
}

func TestMailAllForUserHasNoCap(t *testing.T) {
	st, clk := newTestStore(t)
	u := mustCreate(t, st, "a@example.org", "Aaa")
	uid := u.ID
	for i := 0; i < 60; i++ {
		if _, err := st.LogMail(&uid, "a@example.org", "reset", ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.LogMail(&uid, "a@example.org", "activate", "<m1>"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.RecordMailEvent("<m1>", "delivered", clk.Now(), ""); err != nil {
		t.Fatal(err)
	}
	capped, err := st.MailForUser(uid, 0)
	if err != nil || len(capped) != 50 {
		t.Fatalf("MailForUser default = %d, %v; want 50", len(capped), err)
	}
	all, err := st.MailAllForUser(uid)
	if err != nil || len(all) != 61 {
		t.Fatalf("MailAllForUser = %d, %v; want 61", len(all), err)
	}
	if all[0].Purpose != "activate" || len(all[0].Events) != 1 || all[0].Events[0].Event != "delivered" {
		t.Fatalf("newest mail = %+v", all[0])
	}
}

func TestAllProposalsByUserHasNoCap(t *testing.T) {
	st, clk := newTestStore(t)
	u := mustCreate(t, st, "a@example.org", "Aaa")
	tx, err := st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i <= ProposalListMax; i++ {
		if _, err := tx.Exec(`INSERT INTO proposals (kind, subject, status, proposer_id, created_at) VALUES (?, ?, 'rejected', ?, ?)`,
			KindHashtagChannel, fmt.Sprintf("#p%d", i), u.ID, unix(clk.Now())); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	capped, err := st.ProposalsByUser(u.ID)
	if err != nil || len(capped) != ProposalListMax {
		t.Fatalf("ProposalsByUser = %d, %v; want %d", len(capped), err, ProposalListMax)
	}
	all, err := st.AllProposalsByUser(u.ID)
	if err != nil || len(all) != ProposalListMax+1 {
		t.Fatalf("AllProposalsByUser = %d, %v; want %d", len(all), err, ProposalListMax+1)
	}
}

func TestSettingsRecordFor(t *testing.T) {
	st, clk := newTestStore(t)
	u := mustCreate(t, st, "a@example.org", "Aaa")
	if r, err := st.SettingsRecordFor(u.ID); err != nil || r != nil {
		t.Fatalf("without a document = %+v, %v; want nil, nil", r, err)
	}
	doc := `{"v":1,"keys":{"meshcore-theme":"dark"}}`
	if _, err := st.PutSettings(u.ID, SettingsVersion{}, doc); err != nil {
		t.Fatal(err)
	}
	r, err := st.SettingsRecordFor(u.ID)
	if err != nil || r == nil {
		t.Fatalf("SettingsRecordFor = %+v, %v", r, err)
	}
	if r.Doc != doc || r.Version.Revision != 1 || r.Version.Generation == "" || !r.UpdatedAt.Equal(clk.Now()) {
		t.Fatalf("record = %+v", r)
	}
}

func TestStoredNotifyPrefsNeverCreates(t *testing.T) {
	st, _ := newTestStore(t)
	u := mustCreate(t, st, "a@example.org", "Aaa")
	if p, err := st.StoredNotifyPrefs(u.ID); err != nil || p != nil {
		t.Fatalf("before first use = %+v, %v; want nil, nil", p, err)
	}
	if _, err := st.getPrefs(u.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("StoredNotifyPrefs created a row: %v", err)
	}
	want, err := st.NotifyPrefsFor(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	p, err := st.StoredNotifyPrefs(u.ID)
	if err != nil || p == nil || p.UnsubToken != want.UnsubToken || p.Enabled != want.Enabled {
		t.Fatalf("after NotifyPrefsFor = %+v, %v; want %+v", p, err, want)
	}
}

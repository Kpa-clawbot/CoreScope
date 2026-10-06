package users

import (
	"errors"
	"testing"
	"time"
)

func TestMailEventsSummaryFollowsNewest(t *testing.T) {
	st, clk := newTestStore(t)
	u := mustCreate(t, st, "m@example.org", "Mai")
	uid := u.ID
	id, err := st.LogMail(&uid, "m@example.org", "activate", "<msg-1@brevo>")
	if err != nil {
		t.Fatal(err)
	}
	t0 := clk.Now()
	if _, found, err := st.RecordMailEvent("<msg-1@brevo>", "delivered", t0.Add(time.Minute), ""); err != nil || !found {
		t.Fatalf("delivered: %v %v", found, err)
	}
	gotUID, found, err := st.RecordMailEvent("<msg-1@brevo>", "hard_bounce", t0.Add(3*time.Minute), "mailbox full")
	if err != nil || !found || gotUID == nil || *gotUID != uid {
		t.Fatalf("hard_bounce: %v %v %v", gotUID, found, err)
	}
	// Older event arriving late does not overwrite the summary.
	st.RecordMailEvent("<msg-1@brevo>", "opened", t0.Add(2*time.Minute), "")
	// Duplicate is ignored.
	st.RecordMailEvent("<msg-1@brevo>", "delivered", t0.Add(time.Minute), "")

	rec, err := st.MailByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if rec.LastEvent != "hard_bounce" || rec.LastReason != "mailbox full" || len(rec.Events) != 3 {
		t.Fatalf("record = %+v", rec)
	}
	if rec.Events[0].Event != "delivered" { // chronological
		t.Fatalf("events not chronological: %+v", rec.Events)
	}
	if _, found, _ := st.RecordMailEvent("<unknown@brevo>", "delivered", t0, ""); found {
		t.Fatal("unknown message id reported as found")
	}
}

func TestMailForUserAndLatest(t *testing.T) {
	st, clk := newTestStore(t)
	a := mustCreate(t, st, "a@example.org", "Aaa")
	b := mustCreate(t, st, "b@example.org", "Bbb")
	aid, bid := a.ID, b.ID
	st.LogMail(&aid, "a@example.org", "activate", "<a1>")
	clk.Advance(time.Minute)
	st.LogMail(&aid, "a@example.org", "reset", "<a2>")
	st.LogMail(&bid, "b@example.org", "activate", "")

	list, err := st.MailForUser(a.ID, 10)
	if err != nil || len(list) != 2 || list[0].Purpose != "reset" {
		t.Fatalf("MailForUser = %+v, %v", list, err)
	}
	latest, err := st.LatestMailByUser()
	if err != nil || latest[a.ID].Purpose != "reset" || latest[b.ID].Purpose != "activate" {
		t.Fatalf("LatestMailByUser = %+v, %v", latest, err)
	}
}

func TestPruneMail(t *testing.T) {
	st, clk := newTestStore(t)
	st.LogMail(nil, "x@example.org", "activate", "<old>")
	clk.Advance(91 * 24 * time.Hour)
	st.LogMail(nil, "y@example.org", "activate", "<new>")
	n, err := st.PruneMail(90 * 24 * time.Hour)
	if err != nil || n != 1 {
		t.Fatalf("PruneMail = %d, %v", n, err)
	}
}

func TestDeleteHashesMailAddresses(t *testing.T) {
	st, _ := newTestStore(t)
	u := mustCreate(t, st, "gone@example.org", "Gone")
	uid := u.ID
	mailID, err := st.LogMail(&uid, "gone@example.org", "activate", "<m1@x>")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Delete(u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetByID(u.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("user still present: %v", err)
	}
	rec, err := st.MailByID(mailID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.UserID != nil || rec.ToEmail != HashedEmail("gone@example.org") {
		t.Fatalf("mail record after delete: %+v", rec)
	}
	if err := st.Delete(u.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second Delete = %v", err)
	}
}

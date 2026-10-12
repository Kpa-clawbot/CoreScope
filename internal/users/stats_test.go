package users

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

func TestStatsCountsEveryFigure(t *testing.T) {
	st, clk := newTestStore(t)
	now := clk.Now() // 2026-10-06 12:00 UTC
	at := func(d time.Duration) { clk.t = now.Add(d) }
	day := 24 * time.Hour
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	at(-48 * time.Hour)
	mustCreate(t, st, "stuck@example.org", "Stuck") // pending for more than 24 hours
	at(-2 * time.Hour)
	mustCreate(t, st, "fresh@example.org", "Fresh") // pending, recent
	at(-40 * day)
	act := mustCreate(t, st, "act@example.org", "Act")
	adm := mustCreate(t, st, "adm@example.org", "Adm")
	dis := mustCreate(t, st, "dis@example.org", "Dis")
	for _, u := range []*User{act, adm, dis} {
		must(st.Activate(u.ID, RoleUser, nil))
	}
	must(st.SetRole(adm.ID, RoleAdmin))
	must(st.SetStatus(dis.ID, StatusDisabled))
	must(st.SetEmailBouncing(dis.ID, true))
	at(-3 * day)
	must(st.TouchLogin(act.ID)) // active within 7 days
	at(-10 * day)
	_, _, err := st.CreateSession(adm.ID, 30*day, "") // active within 30 days only
	must(err)

	for _, d := range []time.Duration{-time.Hour, -3 * day, -20 * day, -40 * day} {
		at(d)
		mustAudit(t, st, nil, "user.register", nil, nil)
	}
	aid, mid := act.ID, adm.ID
	at(-30 * time.Hour)
	mustAudit(t, st, nil, "user.login", &aid, nil)
	at(-25 * time.Hour)
	mustAudit(t, st, nil, "user.login.failed", &mid, nil)
	at(-time.Hour)
	mustAudit(t, st, nil, "user.login", &aid, nil)
	at(-2 * time.Hour)
	for i := 0; i < 5; i++ {
		mustAudit(t, st, nil, "user.login.failed", &mid, map[string]string{"reason": "wrong_password"})
	}
	for i := 0; i < 4; i++ {
		mustAudit(t, st, nil, "user.login.failed", &aid, map[string]string{"reason": "wrong_password"})
	}

	at(-day)
	for i, ev := range []string{"delivered", "opened", "hard_bounce", "soft_bounce", "blocked", "spam", "", "deferred", "error"} {
		id := fmt.Sprintf("m%d", i)
		_, err := st.LogMail(&aid, "act@example.org", "activate", id)
		must(err)
		if ev != "" {
			_, _, err := st.RecordMailEvent(id, ev, clk.Now(), "")
			must(err)
		}
	}
	at(-8 * day)
	_, err = st.LogMail(&aid, "act@example.org", "activate", "old")
	must(err)
	_, _, err = st.RecordMailEvent("old", "delivered", clk.Now(), "")
	must(err)
	at(0)

	got, err := st.Stats(now)
	if err != nil {
		t.Fatal(err)
	}
	perDay := got.NewPerDay
	got.NewPerDay = nil
	want := Stats{Total: 5, Active: 2, Pending: 2, Disabled: 1, Admins: 1, StuckPending: 1, Bouncing: 1,
		New7d: 2, New30d: 3, Active7d: 1, Active30d: 2, Logins24h: 1, FailedLogins24h: 9,
		Mail7d:   MailCounts{Delivered: 2, Bounced: 2, Blocked: 1, Spam: 1, Pending: 2, Other: 1},
		Guessing: []GuessedAccount{{UserID: adm.ID, DisplayName: "Adm", Failed: 5}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Stats =\n%+v\nwant\n%+v", got, want)
	}
	if len(perDay) != 30 || perDay[0].Day != "2026-09-07" || perDay[29].Day != "2026-10-06" {
		t.Fatalf("NewPerDay bounds = %d entries, %+v .. %+v", len(perDay), perDay[0], perDay[len(perDay)-1])
	}
	nonZero := map[string]int{}
	for _, d := range perDay {
		if d.Count != 0 {
			nonZero[d.Day] = d.Count
		}
	}
	if !reflect.DeepEqual(nonZero, map[string]int{"2026-09-16": 1, "2026-10-03": 1, "2026-10-06": 1}) {
		t.Fatalf("NewPerDay non-zero days = %v", nonZero)
	}
}

func TestStatsOnEmptyDatabase(t *testing.T) {
	st, clk := newTestStore(t)
	got, err := st.Stats(clk.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got.Total != 0 || got.New30d != 0 || got.Active30d != 0 || got.FailedLogins24h != 0 || got.Mail7d != (MailCounts{}) {
		t.Fatalf("empty Stats = %+v", got)
	}
	if got.Guessing == nil || len(got.Guessing) != 0 {
		t.Fatalf("Guessing = %#v; want an empty, non-nil slice", got.Guessing)
	}
	if len(got.NewPerDay) != 30 {
		t.Fatalf("NewPerDay has %d entries; want 30", len(got.NewPerDay))
	}
	for _, d := range got.NewPerDay {
		if d.Count != 0 {
			t.Fatalf("NewPerDay = %+v; want zeros", got.NewPerDay)
		}
	}
}

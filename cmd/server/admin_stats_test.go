package main

import (
	"strings"
	"testing"
)

func TestAdminStatsRequiresAdmin(t *testing.T) {
	f, _, uma := adminFixture(t)
	expectStatus(t, f.do("GET", "/api/admin/stats", nil), 401)
	expectStatus(t, f.do("GET", "/api/admin/stats", nil, as(uma)), 403)
}

func TestAdminStats(t *testing.T) {
	f, boss, uma := adminFixture(t)
	expectStatus(t, f.do("POST", "/api/auth/register", registerRequest{Email: "pending@example.org", DisplayName: "Pen", Password: pw}), 200)
	for i := 0; i < 5; i++ {
		expectStatus(t, f.do("POST", "/api/auth/login", loginRequest{Email: "uma@example.org", Password: "wrong password!"}), 401)
	}
	w := f.do("GET", "/api/admin/stats", nil, as(boss))
	expectStatus(t, w, 200)
	st := decode[adminStatsJSON](t, w)
	if st.Total != 3 || st.Active != 2 || st.Pending != 1 || st.Admins != 1 || st.StuckPending != 0 ||
		st.New7d != 3 || st.New30d != 3 || len(st.NewPerDay) != 30 || st.Active7d != 2 ||
		st.Logins24h != 0 || st.FailedLogins24h != 5 || st.Mail7d.Pending != 3 {
		t.Fatalf("stats = %+v", st)
	}
	if len(st.Guessing) != 1 || st.Guessing[0].UserID != uma.me.ID || st.Guessing[0].DisplayName != "Uma" || st.Guessing[0].Failed != 5 {
		t.Fatalf("guessing = %+v", st.Guessing)
	}
}

func TestAdminStatsEmptyListsAreArrays(t *testing.T) {
	f, boss, _ := adminFixture(t)
	w := f.do("GET", "/api/admin/stats", nil, as(boss))
	expectStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), `"guessing":[]`) {
		t.Fatalf("body = %s; want \"guessing\":[]", w.Body.String())
	}
}

func TestAdminStatsStoreErrorIs500(t *testing.T) {
	f, boss, _ := adminFixture(t)
	f.breakTable(t, "mail_log")
	expectStatus(t, f.do("GET", "/api/admin/stats", nil, as(boss)), 500)
}

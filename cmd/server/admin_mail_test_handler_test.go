package main

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
)

const mailTestPath = "/api/admin/mail/test"

func testMailRows(t *testing.T, f *authFixture, uid int64) int {
	t.Helper()
	recs, err := f.st.MailForUser(uid, 100)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range recs {
		if r.Purpose == "test" {
			n++
		}
	}
	return n
}

func TestAdminMailTestSendsToOwnAddress(t *testing.T) {
	f, boss, _ := adminFixture(t)
	w := f.do("POST", mailTestPath, map[string]string{"to": "x@evil.test"}, as(boss))
	expectStatus(t, w, 200)
	got := decode[mailTestJSON](t, w)
	if !got.OK || got.Provider != "fake" || got.MessageID == "" || got.Error != "" {
		t.Fatalf("answer = %+v", got)
	}
	m, id, ok := f.fake.Last()
	if !ok || m.To != "boss@example.org" || !strings.Contains(m.Subject, "Test mail") || id != got.MessageID {
		t.Fatalf("last mail = %+v (id %q)", m, id)
	}
	if !strings.Contains(m.Text, "scope.example.org") || !strings.Contains(m.Text, "fake") {
		t.Fatalf("mail text does not name host and provider: %q", m.Text)
	}
	if n := testMailRows(t, f, boss.me.ID); n != 1 {
		t.Fatalf("test mail log rows = %d, want 1", n)
	}
	rows := auditOf(t, f, boss.me.ID, "mail.test")
	if len(rows) != 1 || rows[0].Detail["provider"] != "fake" || rows[0].Detail["result"] != "ok" || len(rows[0].Detail) != 2 {
		t.Fatalf("audit rows = %+v", rows)
	}
}

func TestAdminMailTestFailureReturnsProviderError(t *testing.T) {
	f, boss, _ := adminFixture(t)
	f.fake.SetSendErr(errors.New("535 5.7.8 authentication failed"))
	w := f.do("POST", mailTestPath, nil, as(boss))
	expectStatus(t, w, 502)
	got := decode[mailTestJSON](t, w)
	if got.OK || got.Provider != "fake" || !strings.Contains(got.Error, "535") || got.MessageID != "" {
		t.Fatalf("answer = %+v", got)
	}
	rows := auditOf(t, f, boss.me.ID, "mail.test")
	if len(rows) != 1 || rows[0].Detail["result"] != "failed" || rows[0].Detail["provider"] != "fake" {
		t.Fatalf("audit rows = %+v", rows)
	}
	if n := testMailRows(t, f, boss.me.ID); n != 0 {
		t.Fatalf("test mail log rows = %d, want 0", n)
	}
}

func TestAdminMailTestRedactsAddressesInError(t *testing.T) {
	f, boss, _ := adminFixture(t)
	f.fake.SetSendErr(errors.New("550 rejected boss@example.org"))
	w := f.do("POST", mailTestPath, nil, as(boss))
	expectStatus(t, w, 502)
	if got := decode[mailTestJSON](t, w); strings.Contains(got.Error, "boss@example.org") || !strings.Contains(got.Error, "550") {
		t.Fatalf("error not redacted: %q", got.Error)
	}
}

func TestAdminMailTestRateLimited(t *testing.T) {
	f, boss, _ := adminFixture(t)
	for i := 0; i < 3; i++ {
		expectStatus(t, f.do("POST", mailTestPath, nil, as(boss)), 200)
	}
	sent := len(f.fake.Sent())
	w := f.do("POST", mailTestPath, nil, as(boss))
	expectStatus(t, w, 429)
	if ra, err := strconv.Atoi(w.Header().Get("Retry-After")); err != nil || ra < 1 {
		t.Fatalf("Retry-After = %q, want >= 1", w.Header().Get("Retry-After"))
	}
	if n := len(auditOf(t, f, boss.me.ID, "mail.test")); n != 3 {
		t.Fatalf("mail.test audit rows = %d, want 3", n)
	}
	if n := testMailRows(t, f, boss.me.ID); n != 3 {
		t.Fatalf("test mail log rows = %d, want 3", n)
	}
	if n := len(f.fake.Sent()); n != sent {
		t.Fatalf("sent = %d after the 429, want %d", n, sent)
	}
}

func TestAdminMailTestNonAdmin(t *testing.T) {
	f, boss, uma := adminFixture(t)
	expectStatus(t, f.do("POST", mailTestPath, nil, as(uma)), 403)
	expectStatus(t, f.do("POST", mailTestPath, nil), 401)
	expectStatus(t, f.do("POST", mailTestPath, nil, as(&client{cookie: boss.cookie})), 403)
	if n := len(auditOf(t, f, boss.me.ID, "mail.test")) + len(auditOf(t, f, uma.me.ID, "mail.test")); n != 0 {
		t.Fatalf("mail.test audit rows = %d, want 0", n)
	}
}

func TestAdminStatsMailDeliveryStatus(t *testing.T) {
	f, boss, _ := adminFixture(t)
	w := f.do("GET", "/api/admin/stats", nil, as(boss))
	expectStatus(t, w, 200)
	if !decode[adminStatsJSON](t, w).MailDeliveryStatus {
		t.Fatal("fake provider: mailDeliveryStatus = false, want true")
	}
	if !strings.Contains(w.Body.String(), `"mailDeliveryStatus":true`) {
		t.Fatalf("field missing from JSON: %s", w.Body.String())
	}
	f.srv.auth.set.provider = "smtp"
	w = f.do("GET", "/api/admin/stats", nil, as(boss))
	expectStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), `"mailDeliveryStatus":false`) {
		t.Fatalf("smtp provider: %s", w.Body.String())
	}
}

func TestAdminUserDetailMailDeliveryStatus(t *testing.T) {
	f, boss, uma := adminFixture(t)
	w := f.do("GET", userPath(uma.me.ID, ""), nil, as(boss))
	expectStatus(t, w, 200)
	if !decode[adminUserDetailJSON](t, w).MailDeliveryStatus {
		t.Fatal("fake provider: mailDeliveryStatus = false, want true")
	}
	f.srv.auth.set.provider = "smtp"
	w = f.do("GET", userPath(uma.me.ID, ""), nil, as(boss))
	expectStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), `"mailDeliveryStatus":false`) {
		t.Fatalf("smtp provider: %s", w.Body.String())
	}
}

func TestOpenAPIListsMailTest(t *testing.T) {
	f := newAuthFixture(t)
	raw, err := json.Marshal(buildOpenAPISpec(f.router, "test"))
	if err != nil {
		t.Fatal(err)
	}
	var spec map[string]interface{}
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	item := asMap(t, asMap(t, spec["paths"], "paths")[mailTestPath], mailTestPath)
	op := asMap(t, item["post"], "post")
	if s, _ := op["summary"].(string); s == "" || s == mailTestPath {
		t.Fatalf("no summary for POST %s: %v", mailTestPath, op)
	}
	if _, ok := routeDescriptions()["POST "+mailTestPath]; !ok {
		t.Fatalf("routeDescriptions has no POST %s", mailTestPath)
	}
}

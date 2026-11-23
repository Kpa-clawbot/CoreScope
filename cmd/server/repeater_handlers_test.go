package main

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/meshcore-analyzer/users"
)

const (
	rpkA = "aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11"
	rpkB = "bb22bb22bb22bb22bb22bb22bb22bb22bb22bb22bb22bb22bb22bb22bb22bb22"
)

func TestRepeaterInfoGetDefaults(t *testing.T) {
	f := newAuthFixture(t)
	w := f.do("GET", "/api/repeaters/"+rpkA+"/info", nil)
	expectStatus(t, w, 200)
	body := w.Body.String()
	for _, want := range []string{`"heightM":null`, `"gainDbi":0`, `"status":null`, `"power":null`,
		`"notes":""`, `"claimers":[]`, `"updatedAt":null`, `"updatedBy":null`} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %s: %s", want, body)
		}
	}
	if strings.Contains(body, "canEdit") || strings.Contains(body, "claimedByMe") {
		t.Errorf("anonymous body has caller fields: %s", body)
	}
}

func seedAliceRepeater(t *testing.T, f *authFixture) *client {
	t.Helper()
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	h := 12.5
	if _, err := f.st.SaveRepeaterInfo(alice.me.ID, rpkA, users.RepeaterEdit{HeightM: &h, GainDBi: 3, Status: users.RepeaterStatusMaintenance}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.ClaimRepeater(alice.me.ID, rpkA); err != nil {
		t.Fatal(err)
	}
	return alice
}

func TestRepeaterInfoGetSaved(t *testing.T) {
	f := newAuthFixture(t)
	seedAliceRepeater(t, f)
	w := f.do("GET", "/api/repeaters/"+rpkA+"/info", nil)
	expectStatus(t, w, 200)
	got := decode[repeaterInfoJSON](t, w)
	if got.HeightM == nil || *got.HeightM != 12.5 || got.Status == nil || *got.Status != "maintenance" || got.GainDBi != 3 {
		t.Errorf("saved fields wrong: %+v", got)
	}
	if len(got.Claimers) != 1 || got.Claimers[0].DisplayName != "Alice" || got.Claimers[0].ClaimedAt == "" {
		t.Errorf("claimers = %+v", got.Claimers)
	}
	if got.UpdatedBy == nil || *got.UpdatedBy != "Alice" || got.UpdatedAt == nil {
		t.Errorf("updatedBy/At = %v %v", got.UpdatedBy, got.UpdatedAt)
	}
	if strings.Contains(w.Body.String(), "@") {
		t.Errorf("body leaks an email: %s", w.Body.String())
	}
}

func TestRepeaterInfoGetCallerFields(t *testing.T) {
	f := newAuthFixture(t)
	alice := seedAliceRepeater(t, f)
	bob := f.registerAndActivate(t, "bob@example.org", "Bob", pw)
	for _, c := range []struct {
		name string
		cl   *client
		want bool
	}{{"bob", bob, false}, {"alice", alice, true}} {
		got := decode[repeaterInfoJSON](t, f.do("GET", "/api/repeaters/"+rpkA+"/info", nil, as(c.cl)))
		if got.CanEdit == nil || *got.CanEdit != c.want || got.ClaimedByMe == nil || *got.ClaimedByMe != c.want {
			t.Errorf("%s: canEdit=%v claimedByMe=%v, want %v", c.name, got.CanEdit, got.ClaimedByMe, c.want)
		}
	}
}

func TestRepeaterInfoGetUpperCase(t *testing.T) {
	f := newAuthFixture(t)
	seedAliceRepeater(t, f)
	w := f.do("GET", "/api/repeaters/"+strings.ToUpper(rpkA)+"/info", nil)
	expectStatus(t, w, 200)
	got := decode[repeaterInfoJSON](t, w)
	if got.Pubkey != rpkA || got.HeightM == nil || *got.HeightM != 12.5 {
		t.Errorf("uppercase did not hit the lowercase row: %+v", got)
	}
}

func TestRepeaterInfoGetBadPubkey(t *testing.T) {
	f := newAuthFixture(t)
	w := f.do("GET", "/api/repeaters/xyz/info", nil)
	expectStatus(t, w, 400)
	if !strings.Contains(w.Body.String(), "pubkey must be 64 hex characters") {
		t.Errorf("body = %s", w.Body.String())
	}
}

func TestRepeaterInfoList(t *testing.T) {
	f := newAuthFixture(t)
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	if _, err := f.st.SaveRepeaterInfo(alice.me.ID, rpkA, users.RepeaterEdit{Status: users.RepeaterStatusActive}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.ClaimRepeater(alice.me.ID, rpkB); err != nil {
		t.Fatal(err)
	}
	w := f.do("GET", "/api/repeaters/info", nil)
	expectStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), `"claimers":[]`) {
		t.Errorf("first entry claimers not []: %s", w.Body.String())
	}
	got := decode[repeaterInfoListJSON](t, w)
	if len(got.Repeaters) != 2 || got.Repeaters[0].Pubkey != rpkA || got.Repeaters[1].Pubkey != rpkB {
		t.Fatalf("list = %+v", got.Repeaters)
	}
	if len(got.Repeaters[1].Claimers) != 1 || got.Repeaters[1].Claimers[0].DisplayName != "Alice" {
		t.Errorf("claim-only entry = %+v", got.Repeaters[1])
	}
}

func TestRepeaterInfoListEmpty(t *testing.T) {
	f := newAuthFixture(t)
	w := f.do("GET", "/api/repeaters/info", nil)
	expectStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), `"repeaters":[]`) {
		t.Errorf("body = %s", w.Body.String())
	}
}

func TestRepeaterRoutesOffWithoutUserManagement(t *testing.T) {
	_, router := setupTestServer(t)
	for _, p := range []string{"/api/repeaters/info", "/api/repeaters/" + rpkA + "/info"} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", p, nil))
		if w.Code != 404 {
			t.Errorf("GET %s = %d with user management off, want 404", p, w.Code)
		}
	}
}

func seedNode(t *testing.T, db *DB, pk, role string) {
	t.Helper()
	if _, err := db.conn.Exec(`INSERT INTO nodes (public_key, role) VALUES (?, ?)`, pk, role); err != nil {
		t.Fatal(err)
	}
}

const rpkComp = "cc33cc33cc33cc33cc33cc33cc33cc33cc33cc33cc33cc33cc33cc33cc33cc33"

func repeaterFixture(t *testing.T, adminEmails ...string) *authFixture {
	t.Helper()
	f := newAuthFixture(t, adminEmails...)
	f.srv.db = setupTestDB(t)
	seedNode(t, f.srv.db, rpkA, "repeater")
	seedNode(t, f.srv.db, rpkComp, "companion")
	return f
}

func auditOf(t *testing.T, f *authFixture, uid int64, action string) []users.AuditEntry {
	t.Helper()
	entries, err := f.st.AuditFor(uid, 100)
	if err != nil {
		t.Fatal(err)
	}
	var out []users.AuditEntry
	for _, e := range entries {
		if e.Action == action {
			out = append(out, e)
		}
	}
	return out
}

func repPath(pk, what string) string { return "/api/repeaters/" + pk + "/" + what }

func TestRepeaterEditUnclaimedByAnyUser(t *testing.T) {
	f := repeaterFixture(t)
	bob := f.registerAndActivate(t, "bob@example.org", "Bob", pw)
	w := f.do("PUT", repPath(rpkA, "info"), map[string]any{"heightM": 20}, as(bob))
	expectStatus(t, w, 200)
	got := decode[repeaterInfoJSON](t, w)
	if got.HeightM == nil || *got.HeightM != 20 || got.UpdatedBy == nil || *got.UpdatedBy != "Bob" {
		t.Errorf("response = %+v", got)
	}
	if got.CanEdit == nil || !*got.CanEdit {
		t.Errorf("caller fields missing: %+v", got)
	}
	rows := auditOf(t, f, bob.me.ID, "repeater.edit")
	if len(rows) != 1 || rows[0].Detail["height_m.old"] != "" || rows[0].Detail["height_m.new"] != "20" || rows[0].Detail["pubkey"] != rpkA {
		t.Fatalf("audit = %+v", rows)
	}
}

func TestRepeaterEditClaimedOnlyByClaimers(t *testing.T) {
	f := repeaterFixture(t, "boss@example.org")
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	bob := f.registerAndActivate(t, "bob@example.org", "Bob", pw)
	boss := f.registerAndActivate(t, "boss@example.org", "Boss", pw)
	expectStatus(t, f.do("POST", repPath(rpkA, "claim"), nil, as(alice)), 200)
	body := map[string]any{"heightM": 5}
	w := f.do("PUT", repPath(rpkA, "info"), body, as(bob))
	expectStatus(t, w, 403)
	if !strings.Contains(w.Body.String(), "only this repeater's maintainers can edit it") {
		t.Errorf("body = %s", w.Body.String())
	}
	expectStatus(t, f.do("PUT", repPath(rpkA, "info"), body, as(alice)), 200)
	expectStatus(t, f.do("PUT", repPath(rpkA, "info"), map[string]any{"heightM": 6}, as(boss)), 200)
}

func TestRepeaterEditValidation(t *testing.T) {
	f := repeaterFixture(t)
	bob := f.registerAndActivate(t, "bob@example.org", "Bob", pw)
	w := f.do("PUT", repPath(rpkA, "info"), map[string]any{"gainDbi": 31}, as(bob))
	expectStatus(t, w, 400)
	if !strings.Contains(w.Body.String(), "gain") {
		t.Errorf("body = %s", w.Body.String())
	}
	expectStatus(t, f.do("PUT", repPath(rpkA, "info"), map[string]any{"status": "planned"}, as(bob)), 400)
}

func TestRepeaterEditUnknownAndNonRepeater(t *testing.T) {
	f := repeaterFixture(t)
	bob := f.registerAndActivate(t, "bob@example.org", "Bob", pw)
	expectStatus(t, f.do("PUT", repPath(rpkB, "info"), map[string]any{"heightM": 1}, as(bob)), 404)
	w := f.do("PUT", repPath(rpkComp, "info"), map[string]any{"heightM": 1}, as(bob))
	expectStatus(t, w, 400)
	if !strings.Contains(w.Body.String(), "not a repeater") {
		t.Errorf("body = %s", w.Body.String())
	}
	expectStatus(t, f.do("POST", repPath(rpkComp, "claim"), nil, as(bob)), 400)
	expectStatus(t, f.do("POST", repPath(rpkB, "claim"), nil, as(bob)), 404)
	if rows, _ := f.st.ListRepeaterInfo(); len(rows) != 0 {
		t.Errorf("stored rows = %+v", rows)
	}
	if claims, _ := f.st.AllRepeaterClaims(); len(claims) != 0 {
		t.Errorf("stored claims = %+v", claims)
	}
}

func TestRepeaterEditAnonymous(t *testing.T) {
	f := repeaterFixture(t)
	bob := f.registerAndActivate(t, "bob@example.org", "Bob", pw)
	body := map[string]any{"heightM": 1}
	expectStatus(t, f.do("PUT", repPath(rpkA, "info"), body), 401)
	expectStatus(t, f.do("PUT", repPath(rpkA, "info"), body, func(r *http.Request) { r.AddCookie(bob.cookie) }), 403)
}

func TestRepeaterClaimIdempotent(t *testing.T) {
	f := repeaterFixture(t)
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	for i := 0; i < 2; i++ {
		w := f.do("POST", repPath(rpkA, "claim"), nil, as(alice))
		expectStatus(t, w, 200)
		got := decode[repeaterInfoJSON](t, w)
		if len(got.Claimers) != 1 || got.ClaimedByMe == nil || !*got.ClaimedByMe {
			t.Fatalf("claim %d: %+v", i, got)
		}
	}
	rows := auditOf(t, f, alice.me.ID, "repeater.claim")
	if len(rows) != 1 || rows[0].Detail["pubkey"] != rpkA || rows[0].TargetUserID == nil || *rows[0].TargetUserID != alice.me.ID {
		t.Fatalf("audit = %+v", rows)
	}
}

func TestRepeaterUnclaim(t *testing.T) {
	f := repeaterFixture(t)
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	expectStatus(t, f.do("POST", repPath(rpkA, "claim"), nil, as(alice)), 200)
	w := f.do("DELETE", repPath(rpkA, "claim"), nil, as(alice))
	expectStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), `"claimers":[]`) {
		t.Errorf("body = %s", w.Body.String())
	}
	if rows := auditOf(t, f, alice.me.ID, "repeater.unclaim"); len(rows) != 1 || rows[0].Detail["pubkey"] != rpkA {
		t.Fatalf("audit = %+v", rows)
	}
	expectStatus(t, f.do("DELETE", repPath(rpkA, "claim"), nil, as(alice)), 404)
}

func TestRepeaterEditNoChangeNoAudit(t *testing.T) {
	f := repeaterFixture(t)
	bob := f.registerAndActivate(t, "bob@example.org", "Bob", pw)
	body := map[string]any{"heightM": 20, "gainDbi": 3, "status": "active", "notes": "roof"}
	expectStatus(t, f.do("PUT", repPath(rpkA, "info"), body, as(bob)), 200)
	expectStatus(t, f.do("PUT", repPath(rpkA, "info"), body, as(bob)), 200)
	if rows := auditOf(t, f, bob.me.ID, "repeater.edit"); len(rows) != 1 {
		t.Fatalf("audit rows = %d, want 1", len(rows))
	}
}

func TestRepeaterInfoGetPaddedPubkey(t *testing.T) {
	f := newAuthFixture(t)
	seedAliceRepeater(t, f)
	w := f.do("GET", "/api/repeaters/%20"+rpkA+"%20/info", nil)
	expectStatus(t, w, 200)
	got := decode[repeaterInfoJSON](t, w)
	if got.Pubkey != rpkA || got.HeightM == nil || *got.HeightM != 12.5 {
		t.Errorf("padded pubkey did not hit the lowercase row: %+v", got)
	}
}

func TestRepeaterEditIdenticalIsNoOp(t *testing.T) {
	f := repeaterFixture(t)
	bob := f.registerAndActivate(t, "bob@example.org", "Bob", pw)
	cy := f.registerAndActivate(t, "cy@example.org", "Cy", pw)
	body := map[string]any{"heightM": 20, "notes": "roof"}
	first := decode[repeaterInfoJSON](t, f.do("PUT", repPath(rpkA, "info"), body, as(bob)))
	second := decode[repeaterInfoJSON](t, f.do("PUT", repPath(rpkA, "info"), body, as(cy)))
	if second.UpdatedBy == nil || *second.UpdatedBy != "Bob" || second.UpdatedAt == nil || *second.UpdatedAt != *first.UpdatedAt {
		t.Errorf("no-op PUT changed updatedBy/At: %+v vs %+v", second, first)
	}
	if rows := auditOf(t, f, cy.me.ID, "repeater.edit"); len(rows) != 0 {
		t.Errorf("no-op PUT wrote audit: %+v", rows)
	}
}

func TestRepeaterEditPaddedNotesNoSecondAudit(t *testing.T) {
	f := repeaterFixture(t)
	bob := f.registerAndActivate(t, "bob@example.org", "Bob", pw)
	expectStatus(t, f.do("PUT", repPath(rpkA, "info"), map[string]any{"notes": "roof"}, as(bob)), 200)
	expectStatus(t, f.do("PUT", repPath(rpkA, "info"), map[string]any{"notes": " roof "}, as(bob)), 200)
	if rows := auditOf(t, f, bob.me.ID, "repeater.edit"); len(rows) != 1 {
		t.Fatalf("audit rows = %d, want 1", len(rows))
	}
}

func TestRepeaterEditDeviceTokenForbidden(t *testing.T) {
	f := repeaterFixture(t)
	f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	tok := f.deviceToken(t, "alice@example.org", pw, "Pixel").Token
	expectStatus(t, f.do("PUT", repPath(rpkA, "info"), map[string]any{"heightM": 1}, bearer(tok)), 403)
	if rows, _ := f.st.ListRepeaterInfo(); len(rows) != 0 {
		t.Errorf("stored rows = %+v", rows)
	}
}

func TestRepeaterEditForbiddenLeavesRowUnchanged(t *testing.T) {
	f := repeaterFixture(t)
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	bob := f.registerAndActivate(t, "bob@example.org", "Bob", pw)
	expectStatus(t, f.do("POST", repPath(rpkA, "claim"), nil, as(alice)), 200)
	expectStatus(t, f.do("PUT", repPath(rpkA, "info"), map[string]any{"heightM": 7}, as(alice)), 200)
	expectStatus(t, f.do("PUT", repPath(rpkA, "info"), map[string]any{"heightM": 99}, as(bob)), 403)
	got, err := f.st.GetRepeaterInfo(rpkA)
	if err != nil || got.HeightM == nil || *got.HeightM != 7 {
		t.Errorf("row after 403 = %+v, %v", got, err)
	}
}

func TestRepeaterUnclaimBadPubkey(t *testing.T) {
	f := repeaterFixture(t)
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	expectStatus(t, f.do("DELETE", "/api/repeaters/xyz/claim", nil, as(alice)), 400)
}

func adminClaimPath(pk string, uid int64) string {
	return "/api/admin/repeaters/" + pk + "/claims/" + strconv.FormatInt(uid, 10)
}

func TestAdminRemoveRepeaterClaim(t *testing.T) {
	f := repeaterFixture(t, "admin@example.org")
	admin := f.registerAndActivate(t, "admin@example.org", "Admin", pw)
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	bob := f.registerAndActivate(t, "bob@example.org", "Bob", pw)
	expectStatus(t, f.do("POST", repPath(rpkA, "claim"), nil, as(alice)), 200)

	expectStatus(t, f.do("DELETE", adminClaimPath(rpkA, alice.me.ID), nil, as(bob)), 403)
	if cs, _ := f.st.RepeaterClaimsByUser(alice.me.ID); len(cs) != 1 {
		t.Fatalf("claim removed by a non-admin: %+v", cs)
	}
	expectStatus(t, f.do("DELETE", adminClaimPath(rpkA, alice.me.ID), nil, as(admin)), 204)
	if cs, _ := f.st.RepeaterClaimsByUser(alice.me.ID); len(cs) != 0 {
		t.Fatalf("claim still there: %+v", cs)
	}
	rows := auditOf(t, f, alice.me.ID, "repeater.unclaim")
	if len(rows) != 1 || rows[0].Detail["pubkey"] != rpkA || rows[0].Detail["by"] != "admin" ||
		rows[0].ActorUserID == nil || *rows[0].ActorUserID != admin.me.ID {
		t.Fatalf("audit = %+v", rows)
	}
	expectStatus(t, f.do("DELETE", adminClaimPath(rpkA, alice.me.ID), nil, as(admin)), 404)
	expectStatus(t, f.do("DELETE", adminClaimPath("zz", alice.me.ID), nil, as(admin)), 400)
	expectStatus(t, f.do("DELETE", "/api/admin/repeaters/"+rpkA+"/claims/abc", nil, as(admin)), 400)
}

func TestAdminUserDetailRepeaterClaims(t *testing.T) {
	f := repeaterFixture(t, "admin@example.org")
	admin := f.registerAndActivate(t, "admin@example.org", "Admin", pw)
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	expectStatus(t, f.do("POST", repPath(rpkA, "claim"), nil, as(alice)), 200)

	w := f.do("GET", "/api/admin/users/"+strconv.FormatInt(alice.me.ID, 10), nil, as(admin))
	expectStatus(t, w, 200)
	d := decode[adminUserDetailJSON](t, w)
	if len(d.RepeaterClaims) != 1 || d.RepeaterClaims[0].Pubkey != rpkA || d.RepeaterClaims[0].ClaimedAt == "" {
		t.Fatalf("repeaterClaims = %+v", d.RepeaterClaims)
	}
	w = f.do("GET", "/api/admin/users/"+strconv.FormatInt(admin.me.ID, 10), nil, as(admin))
	expectStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), `"repeaterClaims":[]`) {
		t.Fatalf("no claims should be []: %s", w.Body.String())
	}
	expectStatus(t, f.do("POST", userPath(alice.me.ID, "/disable"), nil, as(admin)), 200)
	w = f.do("GET", "/api/admin/users/"+strconv.FormatInt(alice.me.ID, 10), nil, as(admin))
	expectStatus(t, w, 200)
	d = decode[adminUserDetailJSON](t, w)
	if len(d.RepeaterClaims) != 1 || d.RepeaterClaims[0].Pubkey != rpkA {
		t.Fatalf("claims of a disabled user = %+v", d.RepeaterClaims)
	}
}

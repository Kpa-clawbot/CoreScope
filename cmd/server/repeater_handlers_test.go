package main

import (
	"net/http/httptest"
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
		if w.Code == 200 && strings.Contains(w.Header().Get("Content-Type"), "json") {
			t.Errorf("GET %s answered 200 JSON with user management off", p)
		}
	}
}

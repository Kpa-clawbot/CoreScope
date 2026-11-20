package main

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/meshcore-analyzer/users"
)

// scopedToken logs in like a mobile client asking for scopes.
func (f *authFixture) scopedToken(t *testing.T, email, name string, scopes ...string) string {
	t.Helper()
	w := f.do("POST", "/api/auth/device-token",
		deviceTokenRequest{Email: email, Password: pw, DeviceName: name, Scopes: scopes})
	expectStatus(t, w, http.StatusOK)
	got := decode[deviceTokenResponse](t, w)
	if !slices.Equal(got.Scopes, scopes) {
		t.Fatalf("granted scopes = %v, want %v", got.Scopes, scopes)
	}
	return got.Token
}

func TestDeviceTokenScopesDefaultToRX(t *testing.T) {
	f := newAuthFixture(t)
	f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	got := f.deviceToken(t, "alice@example.org", pw, "Pixel")
	if !slices.Equal(got.Scopes, []string{deviceScopeRX}) {
		t.Fatalf("scopes = %v, want [rx]", got.Scopes)
	}
	sess, err := f.st.LookupSession(got.Token)
	if err != nil || sess.HasScope(deviceScopeRepeaterClaim) || sess.HasScope(deviceScopeRepeaterEdit) {
		t.Fatalf("an RX token holds repeater scopes: %+v, %v", sess, err)
	}
}

func TestDeviceTokenScopesRequested(t *testing.T) {
	f := newAuthFixture(t)
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	// Repeats collapse and the order is fixed.
	w := f.do("POST", "/api/auth/device-token", deviceTokenRequest{Email: "alice@example.org", Password: pw,
		DeviceName: "iPhone", Scopes: []string{deviceScopeRepeaterEdit, deviceScopeRepeaterClaim, deviceScopeRepeaterEdit}})
	expectStatus(t, w, http.StatusOK)
	got := decode[deviceTokenResponse](t, w)
	want := []string{deviceScopeRepeaterClaim, deviceScopeRepeaterEdit}
	if !slices.Equal(got.Scopes, want) {
		t.Fatalf("scopes = %v, want %v", got.Scopes, want)
	}
	sess, err := f.st.LookupSession(got.Token)
	if err != nil || !slices.Equal(sess.Scopes, want) || sess.HasScope(deviceScopeRX) {
		t.Fatalf("stored session = %+v, %v", sess, err)
	}
	rows := auditOf(t, f, alice.me.ID, "user.login")
	if len(rows) == 0 || rows[0].Detail["scopes"] != "repeater:claim,repeater:edit" {
		t.Fatalf("login audit = %+v", rows)
	}
}

func TestDeviceTokenScopesRejected(t *testing.T) {
	f := newAuthFixture(t)
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	for _, scopes := range [][]string{{}, {"admin"}, {deviceScopeRX, "repeater:delete"}, {""}} {
		w := f.do("POST", "/api/auth/device-token", deviceTokenRequest{Email: "alice@example.org", Password: pw,
			DeviceName: "x", Scopes: scopes})
		if w.Code != http.StatusBadRequest {
			t.Errorf("scopes %q = %d, want 400", scopes, w.Code)
		}
	}
	list, err := f.st.ListSessions(alice.me.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range list {
		if s.Kind == users.SessionKindDevice {
			t.Fatalf("a rejected request created a device session: %+v", s)
		}
	}
}

func TestBearerScopesForRoutes(t *testing.T) {
	both := []string{deviceScopeRepeaterClaim, deviceScopeRepeaterEdit}
	for _, c := range []struct {
		method, path string
		want         []string
	}{
		{"GET", "/api/auth/me", deviceScopes},
		{"POST", "/api/auth/logout", deviceScopes},
		{"PUT", "/api/account/settings", []string{deviceScopeRX}},
		{"DELETE", "/api/account/companions/" + rpkA, []string{deviceScopeRX}},
		{"POST", repPath(rpkA, "claim"), []string{deviceScopeRepeaterClaim}},
		{"DELETE", repPath(rpkA, "claim"), []string{deviceScopeRepeaterClaim}},
		{"PUT", repPath(rpkA, "info"), []string{deviceScopeRepeaterEdit}},
		{"GET", repPath(rpkA, "info"), both},
		{"GET", "/api/account/repeaters", both},
		{"HEAD", "/api/account/repeaters", both},
		{"POST", "/api/account/repeaters", nil},
		{"DELETE", "/api/account/repeaters", nil},
		{"GET", repPath(rpkA, "claim"), nil},
		{"POST", repPath(rpkA, "info"), nil},
		{"GET", "/api/repeaters/info", nil},
		{"PUT", "/api/repeaters//info", nil},
		{"PUT", repPath(rpkA, "info/x"), nil},
		{"DELETE", "/api/admin/repeaters/" + rpkA + "/claims/1", nil},
		{"GET", "/api/account/sessions", nil},
	} {
		if got := bearerScopesFor(c.method, c.path); !slices.Equal(got, c.want) {
			t.Errorf("bearerScopesFor(%s %s) = %v, want %v", c.method, c.path, got, c.want)
		}
	}
	for path, want := range map[string]bool{
		repPath(rpkA, "info"): true, repPath(rpkA, "claim"): true, "/api/auth/device-token": true,
		"/api/account/companions": true, "/api/account/repeaters": true, "/api/repeaters/info": false, "/api/account/sessions": false,
	} {
		if got := bearerCORSPath(path); got != want {
			t.Errorf("bearerCORSPath(%s) = %v, want %v", path, got, want)
		}
	}
}

// Each scope opens only its own routes; RX and repeater tokens stay apart.
func TestRepeaterTokenScopesPerRoute(t *testing.T) {
	f := repeaterFixture(t)
	f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	tokens := map[string]string{
		"rx":    f.deviceToken(t, "alice@example.org", pw, "RX").Token,
		"claim": f.scopedToken(t, "alice@example.org", "claim", deviceScopeRepeaterClaim),
		"edit":  f.scopedToken(t, "alice@example.org", "edit", deviceScopeRepeaterEdit),
	}
	for _, c := range []struct {
		token, method, path string
		body                any
		want                int
	}{
		{"rx", "POST", repPath(rpkA, "claim"), nil, 403},
		{"rx", "PUT", repPath(rpkA, "info"), map[string]any{"heightM": 1}, 403},
		{"edit", "POST", repPath(rpkA, "claim"), nil, 403},
		{"claim", "PUT", repPath(rpkA, "info"), map[string]any{"heightM": 1}, 403},
		{"claim", "GET", "/api/account/settings", nil, 403},
		{"edit", "GET", "/api/account/companions", nil, 403},
		{"claim", "GET", "/api/auth/me", nil, 200},
		{"edit", "GET", "/api/auth/me", nil, 200},
		{"edit", "PUT", repPath(rpkA, "info"), map[string]any{"heightM": 1}, 200},
		{"claim", "POST", repPath(rpkA, "claim"), nil, 200},
		{"claim", "DELETE", repPath(rpkA, "claim"), nil, 200},
	} {
		if w := f.do(c.method, c.path, c.body, bearer(tokens[c.token])); w.Code != c.want {
			t.Errorf("%s token: %s %s = %d (%s), want %d", c.token, c.method, c.path, w.Code, w.Body.String(), c.want)
		}
	}
	// A repeater-only token can still end its own login.
	expectStatus(t, f.do("POST", "/api/auth/logout", nil, bearer(tokens["claim"])), 200)
	if _, err := f.st.LookupSession(tokens["claim"]); err == nil {
		t.Fatal("logout left the claim token valid")
	}
}

// A claim token manages the caller's own claim only, beside other claimers.
func TestRepeaterTokenClaimsOwnClaimOnly(t *testing.T) {
	f := repeaterFixture(t)
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	f.registerAndActivate(t, "bob@example.org", "Bob", pw)
	expectStatus(t, f.do("POST", repPath(rpkA, "claim"), nil, as(alice)), 200)
	bob := f.scopedToken(t, "bob@example.org", "Bob's phone", deviceScopeRepeaterClaim)

	// Bob has no claim yet: his DELETE must not touch Alice's.
	expectStatus(t, f.do("DELETE", repPath(rpkA, "claim"), nil, bearer(bob)), 404)
	w := f.do("POST", repPath(rpkA, "claim"), nil, bearer(bob))
	expectStatus(t, w, 200)
	got := decode[repeaterInfoJSON](t, w)
	if len(got.Claimers) != 2 || got.ClaimedByMe == nil || !*got.ClaimedByMe {
		t.Fatalf("after Bob's claim: %+v", got)
	}
	// Repeating the claim stays a 200 without a second claim.
	got = decode[repeaterInfoJSON](t, f.do("POST", repPath(rpkA, "claim"), nil, bearer(bob)))
	if len(got.Claimers) != 2 {
		t.Fatalf("a repeated claim changed the claimers: %+v", got.Claimers)
	}
	w = f.do("DELETE", repPath(rpkA, "claim"), nil, bearer(bob))
	expectStatus(t, w, 200)
	got = decode[repeaterInfoJSON](t, w)
	if len(got.Claimers) != 1 || got.Claimers[0].DisplayName != "Alice" || *got.ClaimedByMe {
		t.Fatalf("after Bob's unclaim: %+v", got)
	}
	expectStatus(t, f.do("POST", repPath(rpkComp, "claim"), nil, bearer(bob)), 400)
	if rows := auditOf(t, f, alice.me.ID, "repeater.unclaim"); len(rows) != 0 {
		t.Fatalf("Alice's claim was audited as removed: %+v", rows)
	}
}

// An edit token follows the browser's rule: anyone on an unclaimed repeater,
// then only its active claimers, and admins always.
func TestRepeaterTokenEditFollowsCanEdit(t *testing.T) {
	f := repeaterFixture(t, "boss@example.org")
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	f.registerAndActivate(t, "bob@example.org", "Bob", pw)
	f.registerAndActivate(t, "boss@example.org", "Boss", pw)
	bobEdit := f.scopedToken(t, "bob@example.org", "Bob", deviceScopeRepeaterEdit)
	bobBoth := f.scopedToken(t, "bob@example.org", "Bob 2", deviceScopeRepeaterClaim, deviceScopeRepeaterEdit)
	boss := f.scopedToken(t, "boss@example.org", "Boss", deviceScopeRepeaterEdit)

	expectStatus(t, f.do("PUT", repPath(rpkA, "info"), map[string]any{"heightM": 10}, bearer(bobEdit)), 200)
	expectStatus(t, f.do("POST", repPath(rpkA, "claim"), nil, as(alice)), 200)
	w := f.do("PUT", repPath(rpkA, "info"), map[string]any{"heightM": 11}, bearer(bobEdit))
	expectStatus(t, w, 403)
	if info, _ := f.st.GetRepeaterInfo(rpkA); info == nil || info.HeightM == nil || *info.HeightM != 10 {
		t.Fatalf("a refused edit changed the row: %+v", info)
	}
	// Claiming makes Bob a maintainer again.
	expectStatus(t, f.do("POST", repPath(rpkA, "claim"), nil, bearer(bobBoth)), 200)
	expectStatus(t, f.do("PUT", repPath(rpkA, "info"), map[string]any{"heightM": 12}, bearer(bobBoth)), 200)
	// The admin edits a claimed repeater through a token too.
	w = f.do("PUT", repPath(rpkA, "info"), map[string]any{"heightM": 13}, bearer(boss))
	expectStatus(t, w, 200)
	if got := decode[repeaterInfoJSON](t, w); got.CanEdit == nil || !*got.CanEdit || *got.ClaimedByMe {
		t.Fatalf("admin response = %+v", got)
	}
	// A disabled account's token stops working.
	bobMe := decode[meResponse](t, f.do("GET", "/api/auth/me", nil, bearer(bobBoth)))
	bobID := bobMe.ID
	// A token reads its own scopes back (review on #2429), with no CSRF token.
	if !slices.Equal(bobMe.Scopes, []string{deviceScopeRepeaterClaim, deviceScopeRepeaterEdit}) || bobMe.CSRFToken != "" {
		t.Fatalf("/api/auth/me for a device token = %+v", bobMe)
	}
	if err := f.st.SetStatus(bobID, users.StatusDisabled); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, f.do("PUT", repPath(rpkA, "info"), map[string]any{"heightM": 14}, bearer(bobBoth)), 401)
}

// Admin claim removal stays a browser-only admin action.
func TestRepeaterTokenCannotUseAdminRoute(t *testing.T) {
	f := repeaterFixture(t, "boss@example.org")
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	f.registerAndActivate(t, "boss@example.org", "Boss", pw)
	expectStatus(t, f.do("POST", repPath(rpkA, "claim"), nil, as(alice)), 200)
	boss := f.scopedToken(t, "boss@example.org", "Boss", deviceScopeRepeaterClaim, deviceScopeRepeaterEdit)
	expectStatus(t, f.do("DELETE", adminClaimPath(rpkA, alice.me.ID), nil, bearer(boss)), 403)
	if c, _ := f.st.RepeaterClaims(rpkA); len(c) != 1 {
		t.Fatalf("claims = %+v", c)
	}
}

func TestRepeaterInfoGetWithToken(t *testing.T) {
	f := newAuthFixture(t)
	alice := seedAliceRepeater(t, f)
	f.registerAndActivate(t, "bob@example.org", "Bob", pw)
	aliceClaim := f.scopedToken(t, "alice@example.org", "Alice", deviceScopeRepeaterClaim)
	bobEdit := f.scopedToken(t, "bob@example.org", "Bob", deviceScopeRepeaterEdit)
	rx := f.deviceToken(t, "bob@example.org", pw, "RX").Token

	got := decode[repeaterInfoJSON](t, f.do("GET", repPath(rpkA, "info"), nil, bearer(aliceClaim)))
	if got.CanEdit == nil || !*got.CanEdit || got.ClaimedByMe == nil || !*got.ClaimedByMe {
		t.Fatalf("Alice's token: %+v", got)
	}
	got = decode[repeaterInfoJSON](t, f.do("GET", repPath(rpkA, "info"), nil, bearer(bobEdit)))
	if got.CanEdit == nil || *got.CanEdit || got.ClaimedByMe == nil || *got.ClaimedByMe {
		t.Fatalf("Bob's token: %+v", got)
	}
	// An RX token reads the public view, like an anonymous visitor.
	w := f.do("GET", repPath(rpkA, "info"), nil, bearer(rx))
	expectStatus(t, w, 200)
	if strings.Contains(w.Body.String(), "canEdit") {
		t.Fatalf("RX token got caller fields: %s", w.Body.String())
	}
	// A bearer that is no live device token reads the public view: a token
	// that is gone, a web session token, and an auth proxy's own header
	// (review on #2429: a 401 here hid the Repeater info section).
	for _, tok := range []string{"nonsense", alice.cookie.Value, "eyJhbGciOiJSUzI1NiJ9.proxy.jwt"} {
		w := f.do("GET", repPath(rpkA, "info"), nil, bearer(tok))
		if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "canEdit") {
			t.Errorf("bearer %.8s… = %d %s, want 200 with the public view", tok, w.Code, w.Body.String())
		}
	}
	// The same foreign header still gets 401 on a write, which needs a login.
	expectStatus(t, f.do("PUT", repPath(rpkA, "info"), map[string]any{"notes": "x"}, bearer("eyJhbGciOiJSUzI1NiJ9.proxy.jwt")), http.StatusUnauthorized)
	// The cookie still decides when both are sent, and anonymous reads stay public.
	got = decode[repeaterInfoJSON](t, f.do("GET", repPath(rpkA, "info"), nil, as(alice), bearer("nonsense")))
	if got.ClaimedByMe == nil || !*got.ClaimedByMe {
		t.Fatalf("cookie with a stray bearer: %+v", got)
	}
	if w := f.do("GET", repPath(rpkA, "info"), nil); strings.Contains(w.Body.String(), "claimedByMe") || w.Code != 200 {
		t.Fatalf("anonymous read = %d %s", w.Code, w.Body.String())
	}
}

func TestRepeaterTokenWritesAuditVia(t *testing.T) {
	f := repeaterFixture(t)
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	tok := f.scopedToken(t, "alice@example.org", "iPhone", deviceScopeRepeaterClaim, deviceScopeRepeaterEdit)
	expectStatus(t, f.do("POST", repPath(rpkA, "claim"), nil, bearer(tok)), 200)
	expectStatus(t, f.do("PUT", repPath(rpkA, "info"), map[string]any{"notes": "roof"}, bearer(tok)), 200)
	expectStatus(t, f.do("DELETE", repPath(rpkA, "claim"), nil, bearer(tok)), 200)
	expectStatus(t, f.do("POST", repPath(rpkA, "claim"), nil, as(alice)), 200)
	for action, want := range map[string][]string{
		"repeater.claim": {"", "device"}, "repeater.edit": {"device"}, "repeater.unclaim": {"device"},
	} {
		rows := auditOf(t, f, alice.me.ID, action)
		var via []string
		for _, r := range rows {
			via = append(via, r.Detail["via"])
		}
		if !slices.Equal(via, want) {
			t.Errorf("%s via = %q, want %q (newest first)", action, via, want)
		}
	}
}

// The browser path is unchanged: a cookie write still needs the CSRF token.
func TestRepeaterCookieWriteStillNeedsCSRF(t *testing.T) {
	f := repeaterFixture(t)
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	noCSRF := &client{cookie: alice.cookie}
	for _, c := range []struct{ method, what string }{{"POST", "claim"}, {"PUT", "info"}, {"DELETE", "claim"}} {
		if w := f.do(c.method, repPath(rpkA, c.what), map[string]any{}, as(noCSRF)); w.Code != http.StatusForbidden {
			t.Errorf("%s %s without CSRF = %d, want 403", c.method, c.what, w.Code)
		}
	}
	if c, _ := f.st.RepeaterClaims(rpkA); len(c) != 0 {
		t.Fatalf("claims = %+v", c)
	}
}

// My account, Devices tells an app token's scopes apart from CoreDrive RX.
func TestAccountSessionsListScopes(t *testing.T) {
	f := newAuthFixture(t)
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	f.deviceToken(t, "alice@example.org", pw, "RX")
	f.scopedToken(t, "alice@example.org", "iPhone", deviceScopeRepeaterClaim, deviceScopeRepeaterEdit)
	w := f.do("GET", "/api/account/sessions", nil, as(alice))
	expectStatus(t, w, http.StatusOK)
	got := map[string][]string{}
	for _, s := range decode[[]sessionJSON](t, w) {
		got[s.Kind+":"+s.Label] = s.Scopes
	}
	if s, ok := got["web:"]; !ok || s != nil {
		t.Errorf("web session scopes = %v (present %v), want none", s, ok)
	}
	if s := got["device:RX"]; !slices.Equal(s, []string{deviceScopeRX}) {
		t.Errorf("RX scopes = %v", s)
	}
	if s := got["device:iPhone"]; !slices.Equal(s, []string{deviceScopeRepeaterClaim, deviceScopeRepeaterEdit}) {
		t.Errorf("app scopes = %v", s)
	}
	if strings.Contains(w.Body.String(), `"scopes":null`) {
		t.Errorf("web session has a null scopes field: %s", w.Body.String())
	}
}

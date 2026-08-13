package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/meshcore-analyzer/admindb"
)

func newTestAdminServer(t *testing.T) *Server {
	t.Helper()
	store, err := admindb.Open(filepath.Join(t.TempDir(), "admin.db"))
	if err != nil {
		t.Fatalf("admindb.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return &Server{admin: store}
}

func doLogin(t *testing.T, srv *Server, username, password string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(adminLoginRequest{Username: username, Password: password})
	req := httptest.NewRequest("POST", "/api/admin/login", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleAdminLogin(w, req)
	return w
}

func sessionCookie(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == adminSessionCookieName {
			return c
		}
	}
	t.Fatal("no session cookie set")
	return nil
}

func csrfCookie(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == adminCSRFCookieName {
			return c
		}
	}
	t.Fatal("no CSRF cookie set")
	return nil
}

func TestAdminLoginSuccess(t *testing.T) {
	srv := newTestAdminServer(t)
	if _, err := srv.admin.CreateAdmin("alice", "correct horse battery staple", admindb.RoleSuperAdmin, nil); err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}

	w := doLogin(t, srv, "alice", "correct horse battery staple")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	c := sessionCookie(t, w)
	if !c.HttpOnly {
		t.Error("expected HttpOnly cookie")
	}
	if c.SameSite != http.SameSiteStrictMode {
		t.Errorf("expected SameSite=Strict, got %v", c.SameSite)
	}
	if c.Secure {
		t.Error("expected Secure=false for a plain HTTP test request")
	}

	var resp adminView
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if resp.Username != "alice" || resp.Role != "super_admin" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestAdminLoginWrongPassword(t *testing.T) {
	srv := newTestAdminServer(t)
	if _, err := srv.admin.CreateAdmin("bob", "password12345", admindb.RoleAdmin, nil); err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	w := doLogin(t, srv, "bob", "wrong-password")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestAdminLoginMissingFields(t *testing.T) {
	srv := newTestAdminServer(t)
	w := doLogin(t, srv, "", "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestAdminLoginLockout(t *testing.T) {
	srv := newTestAdminServer(t)
	if _, err := srv.admin.CreateAdmin("carol", "password12345", admindb.RoleAdmin, nil); err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	for i := 0; i < loginMaxFailures; i++ {
		w := doLogin(t, srv, "carol", "wrong-password")
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: expected 401, got %d", i, w.Code)
		}
	}
	// Now locked out even with the correct password.
	w := doLogin(t, srv, "carol", "password12345")
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 after lockout, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAdminLogout(t *testing.T) {
	srv := newTestAdminServer(t)
	if _, err := srv.admin.CreateAdmin("dave", "password12345", admindb.RoleAdmin, nil); err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	loginResp := doLogin(t, srv, "dave", "password12345")
	c := sessionCookie(t, loginResp)

	req := httptest.NewRequest("POST", "/api/admin/logout", nil)
	req.AddCookie(c)
	w := httptest.NewRecorder()
	srv.handleAdminLogout(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	if _, err := srv.admin.ValidateSession(c.Value); err == nil {
		t.Fatal("expected session to be invalidated after logout")
	}
}

func TestRequireAdminNoCookie(t *testing.T) {
	srv := newTestAdminServer(t)
	handler := srv.requireAdmin(http.HandlerFunc(srv.handleAdminMe))
	req := httptest.NewRequest("GET", "/api/admin/me", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestRequireAdminInvalidCookie(t *testing.T) {
	srv := newTestAdminServer(t)
	handler := srv.requireAdmin(http.HandlerFunc(srv.handleAdminMe))
	req := httptest.NewRequest("GET", "/api/admin/me", nil)
	req.AddCookie(&http.Cookie{Name: adminSessionCookieName, Value: "bogus-token"})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestRequireAdminValidSessionReachesHandler(t *testing.T) {
	srv := newTestAdminServer(t)
	if _, err := srv.admin.CreateAdmin("erin", "password12345", admindb.RoleAdmin, nil); err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	c := sessionCookie(t, doLogin(t, srv, "erin", "password12345"))

	handler := srv.requireAdmin(http.HandlerFunc(srv.handleAdminMe))
	req := httptest.NewRequest("GET", "/api/admin/me", nil)
	req.AddCookie(c)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp adminView
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if resp.Username != "erin" || resp.Role != "admin" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestRequireSuperAdminRejectsPlainAdmin(t *testing.T) {
	srv := newTestAdminServer(t)
	if _, err := srv.admin.CreateAdmin("frank", "password12345", admindb.RoleAdmin, nil); err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	c := sessionCookie(t, doLogin(t, srv, "frank", "password12345"))

	handler := srv.requireSuperAdmin(http.HandlerFunc(srv.handleCreateAdmin))
	body, _ := json.Marshal(createAdminRequest{Username: "newguy", Password: "password12345", Role: "admin"})
	req := httptest.NewRequest("POST", "/api/admin/admins", bytes.NewReader(body))
	req.AddCookie(c)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRequireSuperAdminAllowsSuperAdmin(t *testing.T) {
	srv := newTestAdminServer(t)
	super, err := srv.admin.CreateAdmin("grace", "password12345", admindb.RoleSuperAdmin, nil)
	if err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	c := sessionCookie(t, doLogin(t, srv, "grace", "password12345"))

	handler := srv.requireSuperAdmin(http.HandlerFunc(srv.handleCreateAdmin))
	body, _ := json.Marshal(createAdminRequest{Username: "newguy", Password: "password12345", Role: "admin"})
	req := httptest.NewRequest("POST", "/api/admin/admins", bytes.NewReader(body))
	req.AddCookie(c)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var created adminView
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if created.Username != "newguy" || created.Role != "admin" {
		t.Fatalf("unexpected created admin: %+v", created)
	}
	if created.CreatedBy == nil || *created.CreatedBy != super.ID {
		t.Fatalf("expected createdBy=%d, got %+v", super.ID, created.CreatedBy)
	}
}

func TestCreateAdminDuplicateUsernameReturns409(t *testing.T) {
	srv := newTestAdminServer(t)
	if _, err := srv.admin.CreateAdmin("henry", "password12345", admindb.RoleSuperAdmin, nil); err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	c := sessionCookie(t, doLogin(t, srv, "henry", "password12345"))

	handler := srv.requireSuperAdmin(http.HandlerFunc(srv.handleCreateAdmin))
	body, _ := json.Marshal(createAdminRequest{Username: "henry", Password: "password12345", Role: "admin"})
	req := httptest.NewRequest("POST", "/api/admin/admins", bytes.NewReader(body))
	req.AddCookie(c)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreateAdminInvalidRole(t *testing.T) {
	srv := newTestAdminServer(t)
	if _, err := srv.admin.CreateAdmin("iris", "password12345", admindb.RoleSuperAdmin, nil); err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	c := sessionCookie(t, doLogin(t, srv, "iris", "password12345"))

	handler := srv.requireSuperAdmin(http.HandlerFunc(srv.handleCreateAdmin))
	body, _ := json.Marshal(createAdminRequest{Username: "newguy", Password: "password12345", Role: "owner"})
	req := httptest.NewRequest("POST", "/api/admin/admins", bytes.NewReader(body))
	req.AddCookie(c)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestListAdminsRequiresAuth(t *testing.T) {
	srv := newTestAdminServer(t)
	if _, err := srv.admin.CreateAdmin("jill", "password12345", admindb.RoleAdmin, nil); err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}

	handler := srv.requireAdmin(http.HandlerFunc(srv.handleListAdmins))

	// No auth → 401.
	req := httptest.NewRequest("GET", "/api/admin/admins", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}

	// Any authenticated admin (not just super_admin) can list.
	c := sessionCookie(t, doLogin(t, srv, "jill", "password12345"))
	req = httptest.NewRequest("GET", "/api/admin/admins", nil)
	req.AddCookie(c)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string][]adminView
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(resp["admins"]) != 1 {
		t.Fatalf("expected 1 admin, got %d", len(resp["admins"]))
	}
}

func TestAdminLoginSetsCSRFCookie(t *testing.T) {
	srv := newTestAdminServer(t)
	if _, err := srv.admin.CreateAdmin("nora", "password12345", admindb.RoleAdmin, nil); err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	w := doLogin(t, srv, "nora", "password12345")
	c := csrfCookie(t, w)
	if c.Value == "" {
		t.Fatal("expected non-empty CSRF token")
	}
	if c.HttpOnly {
		t.Error("CSRF cookie must NOT be HttpOnly — admin.js needs to read it")
	}
	if c.SameSite != http.SameSiteStrictMode {
		t.Errorf("expected SameSite=Strict, got %v", c.SameSite)
	}
}

func TestAdminLogoutClearsCSRFCookie(t *testing.T) {
	srv := newTestAdminServer(t)
	if _, err := srv.admin.CreateAdmin("oscar", "password12345", admindb.RoleAdmin, nil); err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	loginResp := doLogin(t, srv, "oscar", "password12345")
	sc := sessionCookie(t, loginResp)
	cc := csrfCookie(t, loginResp)

	req := httptest.NewRequest("POST", "/api/admin/logout", nil)
	req.AddCookie(sc)
	req.Header.Set(adminCSRFHeaderName, cc.Value)
	req.AddCookie(cc)
	w := httptest.NewRecorder()
	srv.requireAdmin(srv.requireCSRF(http.HandlerFunc(srv.handleAdminLogout))).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	cleared := csrfCookie(t, w)
	if cleared.Value != "" || cleared.MaxAge >= 0 {
		t.Fatalf("expected CSRF cookie to be cleared, got %+v", cleared)
	}
}

func TestRequireCSRFMissingCookie(t *testing.T) {
	srv := newTestAdminServer(t)
	if _, err := srv.admin.CreateAdmin("pat", "password12345", admindb.RoleSuperAdmin, nil); err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	sc := sessionCookie(t, doLogin(t, srv, "pat", "password12345"))

	handler := srv.requireSuperAdmin(srv.requireCSRF(http.HandlerFunc(srv.handleCreateAdmin)))
	body, _ := json.Marshal(createAdminRequest{Username: "newguy", Password: "password12345", Role: "admin"})
	req := httptest.NewRequest("POST", "/api/admin/admins", bytes.NewReader(body))
	req.AddCookie(sc)
	req.Header.Set(adminCSRFHeaderName, "some-token-with-no-matching-cookie")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRequireCSRFMissingHeader(t *testing.T) {
	srv := newTestAdminServer(t)
	if _, err := srv.admin.CreateAdmin("quinn", "password12345", admindb.RoleSuperAdmin, nil); err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	loginResp := doLogin(t, srv, "quinn", "password12345")
	sc := sessionCookie(t, loginResp)
	cc := csrfCookie(t, loginResp)

	handler := srv.requireSuperAdmin(srv.requireCSRF(http.HandlerFunc(srv.handleCreateAdmin)))
	body, _ := json.Marshal(createAdminRequest{Username: "newguy", Password: "password12345", Role: "admin"})
	req := httptest.NewRequest("POST", "/api/admin/admins", bytes.NewReader(body))
	req.AddCookie(sc)
	req.AddCookie(cc)
	// No X-CSRF-Token header set — this is exactly what an attacker's
	// cross-site request would look like: it can't read cc.Value to
	// forge the header.
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRequireCSRFMismatchedToken(t *testing.T) {
	srv := newTestAdminServer(t)
	if _, err := srv.admin.CreateAdmin("river", "password12345", admindb.RoleSuperAdmin, nil); err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	loginResp := doLogin(t, srv, "river", "password12345")
	sc := sessionCookie(t, loginResp)
	cc := csrfCookie(t, loginResp)

	handler := srv.requireSuperAdmin(srv.requireCSRF(http.HandlerFunc(srv.handleCreateAdmin)))
	body, _ := json.Marshal(createAdminRequest{Username: "newguy", Password: "password12345", Role: "admin"})
	req := httptest.NewRequest("POST", "/api/admin/admins", bytes.NewReader(body))
	req.AddCookie(sc)
	req.AddCookie(cc)
	req.Header.Set(adminCSRFHeaderName, cc.Value+"-tampered")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRequireCSRFValidTokenPasses(t *testing.T) {
	srv := newTestAdminServer(t)
	super, err := srv.admin.CreateAdmin("sam", "password12345", admindb.RoleSuperAdmin, nil)
	if err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	loginResp := doLogin(t, srv, "sam", "password12345")
	sc := sessionCookie(t, loginResp)
	cc := csrfCookie(t, loginResp)

	handler := srv.requireSuperAdmin(srv.requireCSRF(http.HandlerFunc(srv.handleCreateAdmin)))
	body, _ := json.Marshal(createAdminRequest{Username: "newguy", Password: "password12345", Role: "admin"})
	req := httptest.NewRequest("POST", "/api/admin/admins", bytes.NewReader(body))
	req.AddCookie(sc)
	req.AddCookie(cc)
	req.Header.Set(adminCSRFHeaderName, cc.Value)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var created adminView
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if created.CreatedBy == nil || *created.CreatedBy != super.ID {
		t.Fatalf("expected createdBy=%d, got %+v", super.ID, created.CreatedBy)
	}
}

func TestRequireCSRFRejectsUnauthenticatedEvenWithValidToken(t *testing.T) {
	srv := newTestAdminServer(t)
	// requireSuperAdmin (session check) must run before requireCSRF gets
	// a chance to matter — a stolen/guessed CSRF token alone must not be
	// enough without a valid session.
	handler := srv.requireSuperAdmin(srv.requireCSRF(http.HandlerFunc(srv.handleCreateAdmin)))
	body, _ := json.Marshal(createAdminRequest{Username: "newguy", Password: "password12345", Role: "admin"})
	req := httptest.NewRequest("POST", "/api/admin/admins", bytes.NewReader(body))
	req.Header.Set(adminCSRFHeaderName, "whatever")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 (no session), got %d: %s", w.Code, w.Body.String())
	}
}

func doChangePassword(t *testing.T, srv *Server, sc, cc *http.Cookie, current, newPw string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(changePasswordRequest{CurrentPassword: current, NewPassword: newPw})
	req := httptest.NewRequest("POST", "/api/admin/change-password", bytes.NewReader(body))
	if sc != nil {
		req.AddCookie(sc)
	}
	if cc != nil {
		req.AddCookie(cc)
		req.Header.Set(adminCSRFHeaderName, cc.Value)
	}
	w := httptest.NewRecorder()
	srv.requireAdmin(srv.requireCSRF(http.HandlerFunc(srv.handleChangePassword))).ServeHTTP(w, req)
	return w
}

func TestChangePasswordSuccess(t *testing.T) {
	srv := newTestAdminServer(t)
	if _, err := srv.admin.CreateAdmin("victor", "original-password", admindb.RoleAdmin, nil); err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	loginResp := doLogin(t, srv, "victor", "original-password")
	sc := sessionCookie(t, loginResp)
	cc := csrfCookie(t, loginResp)

	w := doChangePassword(t, srv, sc, cc, "original-password", "new-password-123")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Old password no longer works, new one does.
	if _, err := srv.admin.Authenticate("victor", "original-password"); err == nil {
		t.Fatal("old password should no longer authenticate")
	}
	if _, err := srv.admin.Authenticate("victor", "new-password-123"); err != nil {
		t.Fatalf("new password should authenticate: %v", err)
	}
}

func TestChangePasswordWrongCurrentPassword(t *testing.T) {
	srv := newTestAdminServer(t)
	if _, err := srv.admin.CreateAdmin("wendy", "original-password", admindb.RoleAdmin, nil); err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	loginResp := doLogin(t, srv, "wendy", "original-password")
	sc := sessionCookie(t, loginResp)
	cc := csrfCookie(t, loginResp)

	w := doChangePassword(t, srv, sc, cc, "totally-wrong", "new-password-123")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", w.Code, w.Body.String())
	}
	// Original password must still work.
	if _, err := srv.admin.Authenticate("wendy", "original-password"); err != nil {
		t.Fatalf("original password should still work: %v", err)
	}
}

func TestChangePasswordTooShort(t *testing.T) {
	srv := newTestAdminServer(t)
	if _, err := srv.admin.CreateAdmin("xavier", "original-password", admindb.RoleAdmin, nil); err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	loginResp := doLogin(t, srv, "xavier", "original-password")
	sc := sessionCookie(t, loginResp)
	cc := csrfCookie(t, loginResp)

	w := doChangePassword(t, srv, sc, cc, "original-password", "short")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestChangePasswordRequiresAuth(t *testing.T) {
	srv := newTestAdminServer(t)
	w := doChangePassword(t, srv, nil, nil, "whatever", "new-password-123")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestChangePasswordRequiresCSRF(t *testing.T) {
	srv := newTestAdminServer(t)
	if _, err := srv.admin.CreateAdmin("yara", "original-password", admindb.RoleAdmin, nil); err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	loginResp := doLogin(t, srv, "yara", "original-password")
	sc := sessionCookie(t, loginResp)

	w := doChangePassword(t, srv, sc, nil, "original-password", "new-password-123")
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

func TestChangePasswordInvalidatesOtherSessionsButNotCurrent(t *testing.T) {
	srv := newTestAdminServer(t)
	if _, err := srv.admin.CreateAdmin("zeke", "original-password", admindb.RoleAdmin, nil); err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}

	// Two independent logins — e.g. a laptop and a phone.
	loginResp1 := doLogin(t, srv, "zeke", "original-password")
	sc1 := sessionCookie(t, loginResp1)
	cc1 := csrfCookie(t, loginResp1)
	loginResp2 := doLogin(t, srv, "zeke", "original-password")
	sc2 := sessionCookie(t, loginResp2)

	w := doChangePassword(t, srv, sc1, cc1, "original-password", "new-password-123")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Session 1 (the one that made the change) stays valid.
	if _, err := srv.admin.ValidateSession(sc1.Value); err != nil {
		t.Fatalf("session making the change should stay valid: %v", err)
	}
	// Session 2 (a different device) is invalidated.
	if _, err := srv.admin.ValidateSession(sc2.Value); err == nil {
		t.Fatal("other session should have been invalidated by the password change")
	}
}

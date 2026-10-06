package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gorilla/mux"
)

func TestUserManagementOffIsUnchanged(t *testing.T) {
	srv, router := setupTestServer(t)
	if srv.auth != nil {
		t.Fatal("auth built without config")
	}
	for _, p := range []string{"/api/auth/me", "/api/admin/users", "/api/account/sessions"} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", p, nil))
		if w.Code != 404 {
			t.Errorf("%s = %d with the feature off; want 404", p, w.Code)
		}
	}
	base := httptest.NewRecorder()
	router.ServeHTTP(base, httptest.NewRequest("GET", "/api/config/client", nil))
	if strings.Contains(base.Body.String(), "userManagement") {
		t.Fatal("client config mentions userManagement while off")
	}
	// An explicit {"enabled": false} block is byte-identical to no block.
	dir := t.TempDir()
	srv.cfg.UserManagement = &UserManagementConfig{Enabled: false}
	if err := srv.initUserManagement(filepath.Join(dir, "meshcore.db")); err != nil || srv.auth != nil {
		t.Fatalf("initUserManagement off: auth=%v err=%v", srv.auth, err)
	}
	off := httptest.NewRecorder()
	router.ServeHTTP(off, httptest.NewRequest("GET", "/api/config/client", nil))
	if off.Body.String() != base.Body.String() {
		t.Fatal("client config differs between absent and disabled block")
	}
	if _, err := os.Stat(filepath.Join(dir, "users.db")); !os.IsNotExist(err) {
		t.Fatalf("users.db created next to the measurement DB with the feature off: %v", err)
	}
}

func TestClientConfigAdvertisesUserManagement(t *testing.T) {
	srv, router := setupTestServer(t)
	a, _ := newTestAuthService(t)
	srv.auth = a
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/api/config/client", nil))
	if !strings.Contains(w.Body.String(), `"userManagement":{"enabled":true}`) {
		t.Fatalf("client config = %s", w.Body.String())
	}
}

// Through the real router (RegisterRoutes, feature on): auth responses must
// never be cached by a CDN or browser.
func TestAuthRoutesThroughRealRouterAreNoStore(t *testing.T) {
	srv, _ := setupTestServer(t)
	a, _ := newTestAuthService(t)
	srv.auth = a
	router := mux.NewRouter()
	srv.RegisterRoutes(router)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/api/auth/me", nil))
	if w.Code == 404 {
		t.Fatalf("/api/auth/me = 404 with the feature on")
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q; want no-store", got)
	}
}

func TestInitUserManagementRefusesMeasurementDB(t *testing.T) {
	dir := t.TempDir()
	measurement := filepath.Join(dir, "meshcore.db")
	os.WriteFile(measurement, nil, 0o644)
	srv := &Server{cfg: &Config{UserManagement: &UserManagementConfig{
		Enabled: true, DBPath: measurement, PublicBaseURL: testBase,
		Mail: UserMailConfig{BrevoAPIKey: "k", FromEmail: "noreply@example.org"},
	}}}
	err := srv.initUserManagement(measurement)
	if err == nil || !strings.Contains(err.Error(), "measurement database") {
		t.Fatalf("err = %v", err)
	}
}

func TestInitUserManagementCreatesUsersDB(t *testing.T) {
	dir := t.TempDir()
	srv := &Server{cfg: &Config{UserManagement: &UserManagementConfig{
		Enabled: true, PublicBaseURL: testBase,
		Mail: UserMailConfig{BrevoAPIKey: "k", FromEmail: "noreply@example.org"},
	}}}
	if err := srv.initUserManagement(filepath.Join(dir, "meshcore.db")); err != nil {
		t.Fatal(err)
	}
	defer srv.closeUserManagement()
	if _, err := os.Stat(filepath.Join(dir, "users.db")); err != nil {
		t.Fatalf("users.db not created next to the measurement DB: %v", err)
	}
}

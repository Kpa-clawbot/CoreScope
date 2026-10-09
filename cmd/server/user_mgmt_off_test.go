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
	for _, p := range []string{"/api/auth/me", "/api/admin/users", "/api/admin/audit", "/api/admin/stats", "/api/account/sessions", "/api/account/settings", "/api/account/export", "/api/admin/users-backup"} {
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
	a, _ := newTestAuthService(t)
	u := validUM()
	u.DatabaseURL = a.set.databaseURL
	srv := &Server{cfg: &Config{UserManagement: u}}
	if err := srv.initUserManagement(u.DatabaseURL); err == nil || !strings.Contains(err.Error(), "measurement database") {
		t.Fatalf("err = %v", err)
	}
}

func TestInitUserManagementRequiresBootstrappedDatabase(t *testing.T) {
	postgresOnly(t)
	u := validUM()
	u.DatabaseURL = testDatabaseDSN(t)
	srv := &Server{cfg: &Config{UserManagement: u}}
	if err := srv.initUserManagement(testDatabaseDSN(t)); err == nil {
		t.Fatal("runtime created missing account schema")
	}
	db, err := openFixtureSQL(u.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var tables int
	if err := db.QueryRow(testNativeSQL(`SELECT COUNT(*) FROM sqlite_master WHERE type='table'`, `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='public'`)).Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("runtime created tables: %d, %v", tables, err)
	}
}

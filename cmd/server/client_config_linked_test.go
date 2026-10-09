package main

import (
	"bytes"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClientConfigRequireLinkedCompanion(t *testing.T) {
	srv, router := setupTestServer(t)
	get := func() string {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", "/api/config/client", nil))
		return w.Body.String()
	}
	if strings.Contains(get(), "clientRxRequireLinkedCompanion") {
		t.Fatal("field present by default")
	}
	srv.cfg.ClientRxCoverage = &ClientRxCoverageConfig{Enabled: true, RequireLinkedCompanion: true}
	if strings.Contains(get(), "clientRxRequireLinkedCompanion") {
		t.Fatal("field present without user management")
	}
	a, _ := newTestAuthService(t)
	srv.auth = a
	if !strings.Contains(get(), `"clientRxRequireLinkedCompanion":true`) {
		t.Fatalf("field missing with user management on: %s", get())
	}
	srv.cfg.ClientRxCoverage.RequireLinkedCompanion = false
	if strings.Contains(get(), "clientRxRequireLinkedCompanion") {
		t.Fatal("field present with the setting off")
	}
}

func TestRequireLinkedCompanionWarnsWithoutUserManagement(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	srv := &Server{cfg: &Config{ClientRxCoverage: &ClientRxCoverageConfig{Enabled: true, RequireLinkedCompanion: true}}}
	if err := srv.initUserManagement(filepath.Join(t.TempDir(), "meshcore.db")); err != nil || srv.auth != nil {
		t.Fatalf("initUserManagement: auth=%v err=%v", srv.auth, err)
	}
	if !strings.Contains(buf.String(), "requireLinkedCompanion") {
		t.Fatalf("no startup warning, log: %q", buf.String())
	}
}

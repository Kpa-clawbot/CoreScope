package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// With user management off every F route answers 404, like the rest of A–E.
func TestCompanionRoutesAbsentWhenOff(t *testing.T) {
	srv, router := setupTestServer(t)
	srv.cfg.ClientRxCoverage = &ClientRxCoverageConfig{Enabled: true}
	for _, c := range []struct{ method, path string }{
		{"POST", "/api/auth/device-token"},
		{"GET", "/api/account/companions"},
		{"POST", "/api/account/companions"},
		{"POST", "/api/account/companions/challenge"},
		{"DELETE", "/api/account/companions/" + strings.Repeat("ab", 32)},
		{"GET", "/api/rx-coverage?mine=1&bbox=50,3,52,4"},
	} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(c.method, c.path, nil))
		if w.Code != 404 {
			t.Errorf("%s %s = %d with the feature off; want 404", c.method, c.path, w.Code)
		}
	}
}

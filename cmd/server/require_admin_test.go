package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequireAdminKeyOrAdminSession(t *testing.T) {
	f, boss, uma := adminFixture(t)
	p := "/api/test/admin-only"
	f.router.Handle(p, f.srv.requireAdmin(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, okResponse{OK: true})
	}))).Methods("GET", "POST")

	expectStatus(t, f.do("GET", p, nil), 401) // no credentials: the API-key gate answers
	expectStatus(t, f.do("GET", p, nil, header("X-API-Key", testAPIKey)), 200)
	expectStatus(t, f.do("GET", p, nil, as(uma)), 403)                           // user session
	expectStatus(t, f.do("GET", p, nil, as(boss)), 200)                          // admin session, safe method
	expectStatus(t, f.do("POST", p, nil, as(&client{cookie: boss.cookie})), 403) // no CSRF token
	expectStatus(t, f.do("POST", p, nil, as(boss)), 200)
	// A request carrying X-API-Key is judged on the key alone.
	expectStatus(t, f.do("GET", p, nil, as(boss), header("X-API-Key", "wrong-key-wrong-key")), 401)
}

func TestRequireAdminWithoutUserManagementIsAPIKeyGate(t *testing.T) {
	srv := &Server{cfg: &Config{APIKey: testAPIKey}, perfStats: NewPerfStats()}
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, okResponse{OK: true})
	})
	do := func(srv *Server, key string) int {
		req := httptest.NewRequest("GET", "/x", nil)
		if key != "" {
			req.Header.Set("X-API-Key", key)
		}
		w := httptest.NewRecorder()
		srv.requireAdmin(h).ServeHTTP(w, req)
		return w.Code
	}
	if c := do(srv, ""); c != 401 {
		t.Fatalf("no key = %d", c)
	}
	if c := do(srv, testAPIKey); c != 200 {
		t.Fatalf("key = %d", c)
	}
	nokey := &Server{cfg: &Config{}, perfStats: NewPerfStats()}
	if c := do(nokey, testAPIKey); c != 403 {
		t.Fatalf("no apiKey configured = %d, want 403", c)
	}
}

func TestPerfResetAcceptsAdminSession(t *testing.T) {
	srv, router := setupTestServerWithAPIKey(t, testAPIKey)
	f := newAuthFixture(t, "boss@example.org")
	boss := f.registerAndActivate(t, "boss@example.org", "Boss", pw)
	srv.auth = f.srv.auth // requireAdmin reads s.auth per request
	req := httptest.NewRequest("POST", "/api/perf/reset", nil)
	req.Header.Set("Origin", testBase)
	req.AddCookie(boss.cookie)
	req.Header.Set(csrfHeader, boss.csrf)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	expectStatus(t, w, http.StatusOK)
}

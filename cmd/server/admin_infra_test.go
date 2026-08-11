package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/meshcore-analyzer/admindb"
	"github.com/meshcore-analyzer/infraqueue"
)

const testPubkey = "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899" // 64 hex chars

func newTestInfraServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	store, err := admindb.Open(filepath.Join(dir, "admin.db"))
	if err != nil {
		t.Fatalf("admindb.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return &Server{admin: store, db: &DB{path: filepath.Join(dir, "meshcore.db")}}
}

func doSetInfra(t *testing.T, srv *Server, sc, cc *http.Cookie, pubkey string, infra bool) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(setInfrastructureRequest{Pubkey: pubkey, Infrastructure: infra})
	req := httptest.NewRequest("POST", "/api/admin/nodes/infrastructure", bytes.NewReader(body))
	if sc != nil {
		req.AddCookie(sc)
	}
	if cc != nil {
		req.AddCookie(cc)
		req.Header.Set(adminCSRFHeaderName, cc.Value)
	}
	w := httptest.NewRecorder()
	srv.requireAdmin(srv.requireCSRF(http.HandlerFunc(srv.handleSetInfrastructureFlag))).ServeHTTP(w, req)
	return w
}

func TestSetInfrastructureFlagEnqueues(t *testing.T) {
	srv := newTestInfraServer(t)
	if _, err := srv.admin.CreateAdmin("alice", "password12345", admindb.RoleAdmin, nil); err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	loginResp := doLogin(t, srv, "alice", "password12345")
	sc := sessionCookie(t, loginResp)
	cc := csrfCookie(t, loginResp)

	w := doSetInfra(t, srv, sc, cc, testPubkey, true)
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	id, _ := resp["requestId"].(string)
	if id == "" {
		t.Fatal("expected non-empty requestId")
	}
	if resp["statusUrl"] != "/api/admin/nodes/infrastructure/status?id="+id {
		t.Errorf("unexpected statusUrl: %v", resp["statusUrl"])
	}

	exists, err := infraqueue.RequestExists(srv.db.path, id)
	if err != nil || !exists {
		t.Fatalf("expected request file to exist: exists=%v err=%v", exists, err)
	}
	req, err := infraqueue.ReadRequest(mustRequestPath(t, srv.db.path, id))
	if err != nil {
		t.Fatalf("ReadRequest: %v", err)
	}
	if req.PublicKey != testPubkey || !req.Infrastructure {
		t.Errorf("unexpected request contents: %+v", req)
	}
	if req.ActorUsername != "alice" {
		t.Errorf("expected actor username 'alice', got %q", req.ActorUsername)
	}
}

func mustRequestPath(t *testing.T, dbPath, id string) string {
	t.Helper()
	p, err := infraqueue.RequestPath(dbPath, id)
	if err != nil {
		t.Fatalf("RequestPath: %v", err)
	}
	return p
}

func TestSetInfrastructureFlagPlainAdminAllowed(t *testing.T) {
	// Per spec: super_admin's only extra capability is creating new
	// admins. Infra management must work for a plain "admin" role too.
	srv := newTestInfraServer(t)
	if _, err := srv.admin.CreateAdmin("bob", "password12345", admindb.RoleAdmin, nil); err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	loginResp := doLogin(t, srv, "bob", "password12345")
	sc := sessionCookie(t, loginResp)
	cc := csrfCookie(t, loginResp)

	w := doSetInfra(t, srv, sc, cc, testPubkey, true)
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for plain admin, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSetInfrastructureFlagInvalidPubkey(t *testing.T) {
	srv := newTestInfraServer(t)
	if _, err := srv.admin.CreateAdmin("carol", "password12345", admindb.RoleAdmin, nil); err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	loginResp := doLogin(t, srv, "carol", "password12345")
	sc := sessionCookie(t, loginResp)
	cc := csrfCookie(t, loginResp)

	for _, bad := range []string{"", "not-hex-zzz", "aabb", "'; DROP TABLE nodes; --"} {
		w := doSetInfra(t, srv, sc, cc, bad, true)
		if w.Code != http.StatusBadRequest {
			t.Errorf("pubkey %q: expected 400, got %d", bad, w.Code)
		}
	}
}

func TestSetInfrastructureFlagRequiresAuth(t *testing.T) {
	srv := newTestInfraServer(t)
	w := doSetInfra(t, srv, nil, nil, testPubkey, true)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestSetInfrastructureFlagRequiresCSRF(t *testing.T) {
	srv := newTestInfraServer(t)
	if _, err := srv.admin.CreateAdmin("dave", "password12345", admindb.RoleAdmin, nil); err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	loginResp := doLogin(t, srv, "dave", "password12345")
	sc := sessionCookie(t, loginResp)

	// No CSRF cookie/header at all.
	w := doSetInfra(t, srv, sc, nil, testPubkey, true)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 with no CSRF token, got %d", w.Code)
	}
}

func TestInfrastructureFlagStatusFlow(t *testing.T) {
	srv := newTestInfraServer(t)
	if _, err := srv.admin.CreateAdmin("erin", "password12345", admindb.RoleAdmin, nil); err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	loginResp := doLogin(t, srv, "erin", "password12345")
	sc := sessionCookie(t, loginResp)
	cc := csrfCookie(t, loginResp)

	enqueueResp := doSetInfra(t, srv, sc, cc, testPubkey, true)
	var enqueued map[string]interface{}
	json.Unmarshal(enqueueResp.Body.Bytes(), &enqueued)
	id := enqueued["requestId"].(string)

	statusHandler := srv.requireAdmin(http.HandlerFunc(srv.handleInfrastructureFlagStatus))

	// Still pending — the ingestor hasn't processed it in this test.
	req := httptest.NewRequest("GET", "/api/admin/nodes/infrastructure/status?id="+id, nil)
	req.AddCookie(sc)
	w := httptest.NewRecorder()
	statusHandler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var pendingResp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &pendingResp)
	if pendingResp["status"] != "pending" {
		t.Fatalf("expected status=pending, got %v", pendingResp["status"])
	}

	// Simulate the ingestor completing the request.
	if err := infraqueue.WriteResult(srv.db.path, infraqueue.Result{ID: id, Applied: true}); err != nil {
		t.Fatalf("WriteResult: %v", err)
	}
	req = httptest.NewRequest("GET", "/api/admin/nodes/infrastructure/status?id="+id, nil)
	req.AddCookie(sc)
	w = httptest.NewRecorder()
	statusHandler.ServeHTTP(w, req)
	var doneResp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &doneResp)
	if doneResp["status"] != "done" || doneResp["applied"] != true {
		t.Fatalf("expected status=done applied=true, got %+v", doneResp)
	}
}

func TestInfrastructureFlagStatusError(t *testing.T) {
	srv := newTestInfraServer(t)
	if _, err := srv.admin.CreateAdmin("frank", "password12345", admindb.RoleAdmin, nil); err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	loginResp := doLogin(t, srv, "frank", "password12345")
	sc := sessionCookie(t, loginResp)

	id := infraqueue.NewID()
	if err := infraqueue.WriteResult(srv.db.path, infraqueue.Result{ID: id, Applied: false, Error: "no node found with that public key"}); err != nil {
		t.Fatalf("WriteResult: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/admin/nodes/infrastructure/status?id="+id, nil)
	req.AddCookie(sc)
	w := httptest.NewRecorder()
	srv.requireAdmin(http.HandlerFunc(srv.handleInfrastructureFlagStatus)).ServeHTTP(w, req)

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["status"] != "error" {
		t.Fatalf("expected status=error, got %+v", resp)
	}
}

func TestInfrastructureFlagStatusUnknownID(t *testing.T) {
	srv := newTestInfraServer(t)
	if _, err := srv.admin.CreateAdmin("grace", "password12345", admindb.RoleAdmin, nil); err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	loginResp := doLogin(t, srv, "grace", "password12345")
	sc := sessionCookie(t, loginResp)

	req := httptest.NewRequest("GET", "/api/admin/nodes/infrastructure/status?id=0123456789abcdef", nil)
	req.AddCookie(sc)
	w := httptest.NewRecorder()
	srv.requireAdmin(http.HandlerFunc(srv.handleInfrastructureFlagStatus)).ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestInfrastructureFlagStatusMissingID(t *testing.T) {
	srv := newTestInfraServer(t)
	if _, err := srv.admin.CreateAdmin("henry", "password12345", admindb.RoleAdmin, nil); err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	loginResp := doLogin(t, srv, "henry", "password12345")
	sc := sessionCookie(t, loginResp)

	req := httptest.NewRequest("GET", "/api/admin/nodes/infrastructure/status", nil)
	req.AddCookie(sc)
	w := httptest.NewRecorder()
	srv.requireAdmin(http.HandlerFunc(srv.handleInfrastructureFlagStatus)).ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

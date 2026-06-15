package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
)

func serveResolve(srv *Server, path string) *httptest.ResponseRecorder {
	router := mux.NewRouter()
	router.HandleFunc("/api/nodes/resolve", srv.handleResolvePrefix).Methods("GET")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest("GET", path, nil))
	return rr
}

func TestResolvePrefix(t *testing.T) {
	db := setupTestDBv2(t)
	mustExecDB(t, db, `INSERT INTO nodes (public_key, name, role, last_seen, first_seen, advert_count)
		VALUES ('efef7943505052b47f1809488ea4b4d3942d4ed72d2b1953b90a9f5e62a65fb5','NodeUnique','repeater','t','t',1)`)
	mustExecDB(t, db, `INSERT INTO nodes (public_key, name, role, last_seen, first_seen, advert_count)
		VALUES ('aa11000000000000000000000000000000000000000000000000000000000000','NodeA','repeater','t','t',1)`)
	mustExecDB(t, db, `INSERT INTO nodes (public_key, name, role, last_seen, first_seen, advert_count)
		VALUES ('aa22000000000000000000000000000000000000000000000000000000000000','NodeB','repeater','t','t',1)`)
	srv := &Server{db: db}

	// unique 3-byte prefix → name
	var r1 ResolvePrefixResp
	json.Unmarshal(serveResolve(srv, "/api/nodes/resolve?prefix=efef79").Body.Bytes(), &r1)
	if r1.Name != "NodeUnique" || r1.Ambiguous {
		t.Fatalf("unique: %+v", r1)
	}
	// colliding 1-byte prefix (aa…) → ambiguous, no name
	var r2 ResolvePrefixResp
	json.Unmarshal(serveResolve(srv, "/api/nodes/resolve?prefix=aa").Body.Bytes(), &r2)
	if !r2.Ambiguous || r2.Name != "" {
		t.Fatalf("ambiguous: %+v", r2)
	}
	// not found → empty name, not ambiguous
	var r3 ResolvePrefixResp
	json.Unmarshal(serveResolve(srv, "/api/nodes/resolve?prefix=dead").Body.Bytes(), &r3)
	if r3.Name != "" || r3.Ambiguous {
		t.Fatalf("notfound: %+v", r3)
	}
	// bad prefix → 400
	if serveResolve(srv, "/api/nodes/resolve?prefix=xyz").Code != 400 {
		t.Fatal("non-hex prefix should be 400")
	}
}

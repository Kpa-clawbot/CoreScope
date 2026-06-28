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
	// Two nodes sharing the 4-hex prefix aabb → ambiguous at the new minimum.
	mustExecDB(t, db, `INSERT INTO nodes (public_key, name, role, last_seen, first_seen, advert_count)
		VALUES ('aabb110000000000000000000000000000000000000000000000000000000000','NodeA','repeater','t','t',1)`)
	mustExecDB(t, db, `INSERT INTO nodes (public_key, name, role, last_seen, first_seen, advert_count)
		VALUES ('aabb220000000000000000000000000000000000000000000000000000000000','NodeB','repeater','t','t',1)`)
	srv := &Server{db: db}

	// unique 3-byte prefix → name
	var r1 ResolvePrefixResp
	json.Unmarshal(serveResolve(srv, "/api/nodes/resolve?prefix=efef79").Body.Bytes(), &r1)
	if r1.Name != "NodeUnique" || r1.Ambiguous {
		t.Fatalf("unique: %+v", r1)
	}
	// colliding 4-hex prefix (aabb…) → ambiguous, no name
	var r2 ResolvePrefixResp
	json.Unmarshal(serveResolve(srv, "/api/nodes/resolve?prefix=aabb").Body.Bytes(), &r2)
	if !r2.Ambiguous || r2.Name != "" {
		t.Fatalf("ambiguous: %+v", r2)
	}
	// not found → empty name, not ambiguous
	var r3 ResolvePrefixResp
	json.Unmarshal(serveResolve(srv, "/api/nodes/resolve?prefix=dead").Body.Bytes(), &r3)
	if r3.Name != "" || r3.Ambiguous {
		t.Fatalf("notfound: %+v", r3)
	}
	// non-hex prefix → 400
	if serveResolve(srv, "/api/nodes/resolve?prefix=xyz").Code != 400 {
		t.Fatal("non-hex prefix should be 400")
	}
	// #15: prefixes shorter than 4 hex are rejected (kills 256-prefix enumeration)
	for _, short := range []string{"a", "aa", "abc"} {
		if code := serveResolve(srv, "/api/nodes/resolve?prefix="+short).Code; code != 400 {
			t.Fatalf("prefix %q (<4 hex) should be 400, got %d", short, code)
		}
	}
}

// TestResolvePrefixHidesBlacklistedAndHidden verifies the #15 parity fix: a
// unique match that is blacklisted or whose name is hidden (#1181) resolves as
// not-found, never leaking an identity the rest of the API hides.
func TestResolvePrefixHidesBlacklistedAndHidden(t *testing.T) {
	db := setupTestDBv2(t)
	const blPK = "bbcc110000000000000000000000000000000000000000000000000000000000"
	const hidPK = "ddee220000000000000000000000000000000000000000000000000000000000"
	mustExecDB(t, db, `INSERT INTO nodes (public_key, name, role, last_seen, first_seen, advert_count)
		VALUES ('`+blPK+`','BlacklistedNode','repeater','t','t',1)`)
	mustExecDB(t, db, `INSERT INTO nodes (public_key, name, role, last_seen, first_seen, advert_count)
		VALUES ('`+hidPK+`','🚫HiddenNode','repeater','t','t',1)`)
	srv := &Server{db: db, cfg: &Config{
		NodeBlacklist:      []string{blPK},
		HiddenNamePrefixes: []string{"🚫"},
	}}

	var rb ResolvePrefixResp
	json.Unmarshal(serveResolve(srv, "/api/nodes/resolve?prefix=bbcc11").Body.Bytes(), &rb)
	if rb.Name != "" || rb.Pubkey != "" || rb.Ambiguous {
		t.Fatalf("blacklisted node must resolve as not-found: %+v", rb)
	}
	var rh ResolvePrefixResp
	json.Unmarshal(serveResolve(srv, "/api/nodes/resolve?prefix=ddee22").Body.Bytes(), &rh)
	if rh.Name != "" || rh.Pubkey != "" || rh.Ambiguous {
		t.Fatalf("hidden-prefix node must resolve as not-found: %+v", rh)
	}
}

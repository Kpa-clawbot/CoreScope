package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRxCoverageMine(t *testing.T) {
	f := newAuthFixture(t)
	f.srv.db = seedCoverageDB(t)
	f.srv.cfg.ClientRxCoverage = &ClientRxCoverageConfig{Enabled: true}
	f.router.HandleFunc("/api/rx-coverage", f.srv.handleRxCoverage).Methods("GET")
	mine, other := pubHex(companionKey), pubHex(otherKey)
	now := time.Now().UTC().Format(time.RFC3339)
	for i, pk := range []string{mine, other, other} {
		mustExecDB(t, f.srv.db, fmt.Sprintf(`INSERT INTO client_receptions (rx_pubkey,heard_key,heard_keylen,snr,lat,lon,rx_at,ingested_at,src)
			VALUES ('%s','aabbcc',3,-6,%f,3.72,'%s','t','rxlog')`, pk, 51.05+float64(i)*0.05, now))
	}
	const q = "/api/rx-coverage?bbox=50,3,52,4&z=10"
	get := func(path string, mods ...reqMod) *httptest.ResponseRecorder { return f.do("GET", path, nil, mods...) }
	empty := get(q + "&rx=" + strings.Repeat("0", 64)).Body.String()

	expectStatus(t, get(q+"&mine=1"), http.StatusUnauthorized)
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	w := get(q+"&mine=1", as(alice))
	expectStatus(t, w, http.StatusOK)
	if w.Body.String() != empty {
		t.Fatalf("nothing linked: got %s, want the empty collection", w.Body.String())
	}

	tok := f.deviceToken(t, "alice@example.org", pw, "Pixel").Token
	expectStatus(t, f.linkCompanion(t, tok, companionKey, "Car"), http.StatusOK)
	w = get(q+"&mine=1", as(alice))
	expectStatus(t, w, http.StatusOK)
	if w.Body.String() != get(q+"&rx="+mine).Body.String() {
		t.Fatalf("mine=1 differs from rx=<linked companion>: %s", w.Body.String())
	}
	if w.Body.String() == get(q).Body.String() {
		t.Fatal("mine=1 returned everyone's coverage")
	}
	// Coverage is not in a device token's scope.
	expectStatus(t, get(q+"&mine=1", bearer(tok)), http.StatusForbidden)
	// A foreign bearer header next to the cookie does not hide the session.
	expectStatus(t, get(q+"&mine=1", as(alice), bearer("id-token-from-a-proxy")), http.StatusOK)
}

func TestRxCoverageMineWithoutUserManagement(t *testing.T) {
	srv := &Server{db: seedCoverageDB(t), cfg: &Config{ClientRxCoverage: &ClientRxCoverageConfig{Enabled: true}}}
	w := httptest.NewRecorder()
	srv.handleRxCoverage(w, httptest.NewRequest("GET", "/api/rx-coverage?bbox=50,3,52,4&mine=1", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("mine=1 with user management off = %d, want 404", w.Code)
	}
}

// gaps=1 and mine=1 together: the track query cannot narrow to a set of
// companions, so the reply carries no gaps member rather than everyone's track.
func TestRxCoverageMineOmitsGaps(t *testing.T) {
	f := newAuthFixture(t)
	f.srv.db = seedGapsDB(t)
	f.srv.cfg.ClientRxCoverage = &ClientRxCoverageConfig{Enabled: true}
	f.srv.cfg.ClientRfSamples = &ClientRfSamplesConfig{Enabled: true}
	f.router.HandleFunc("/api/rx-coverage", f.srv.handleRxCoverage).Methods("GET")
	now := time.Now().UTC().Format(time.RFC3339)
	insTrack(t, f.srv.db, pubHex(otherKey), now, 51.05, 3.72, 0)
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	const q = "/api/rx-coverage?bbox=50,3,52,4&z=12&days=7&gaps=1"
	if w := f.do("GET", q, nil); !strings.Contains(w.Body.String(), `"gaps"`) {
		t.Fatalf("without mine=1 the gap cell must be there: %s", w.Body.String())
	}
	w := f.do("GET", q+"&mine=1", nil, as(alice))
	expectStatus(t, w, http.StatusOK)
	if strings.Contains(w.Body.String(), `"gaps"`) {
		t.Fatalf("mine=1 must not carry gaps: %s", w.Body.String())
	}
}

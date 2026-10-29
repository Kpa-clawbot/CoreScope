package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The noise layer narrows to one companion (?rx=) or to the caller's linked
// companions (?mine=1) the same way the signal layer does.
func TestRfNoiseRxAndMine(t *testing.T) {
	f := newAuthFixture(t)
	f.srv.db = seedRfNoiseDB(t)
	f.srv.cfg.ClientRfSamples = &ClientRfSamplesConfig{Enabled: true}
	f.router.HandleFunc("/api/rf-noise", f.srv.handleRfNoise).Methods("GET")
	mine, other := pubHex(companionKey), pubHex(otherKey)
	now := time.Now().UTC().Format(time.RFC3339)
	for i, pk := range []string{mine, other, other} {
		mustExecDB(t, f.srv.db, fmt.Sprintf(`INSERT INTO client_rf_samples (rx_pubkey,sampled_at,ingested_at,lat,lon,stationary,uptime_secs,noise_floor)
			VALUES ('%s','%s','t',%f,3.72,0,100,-110)`, pk, now, 51.05+float64(i)*0.05))
	}
	const q = "/api/rf-noise?bbox=50,3,52,4&z=10"
	get := func(path string, mods ...reqMod) *httptest.ResponseRecorder { return f.do("GET", path, nil, mods...) }
	all := get(q).Body.String()
	empty := get(q + "&rx=" + strings.Repeat("0", 64)).Body.String()
	if strings.Count(all, `"Feature"`) != 3 || strings.Count(empty, `"Feature"`) != 0 {
		t.Fatalf("rx= does not narrow: all=%s empty=%s", all, empty)
	}
	if got := get(q + "&rx=" + strings.ToUpper(other)).Body.String(); strings.Count(got, `"Feature"`) != 2 {
		t.Fatalf("rx=<other> (upper case) = %s, want its 2 cells", got)
	}

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
	if w.Body.String() != get(q+"&rx="+mine).Body.String() || w.Body.String() == all {
		t.Fatalf("mine=1 is not the linked companion's noise: %s", w.Body.String())
	}
	expectStatus(t, get(q+"&mine=1", bearer(tok)), http.StatusForbidden)
}

func TestRfNoiseMineWithoutUserManagement(t *testing.T) {
	srv := &Server{db: seedRfNoiseDB(t), cfg: &Config{ClientRfSamples: &ClientRfSamplesConfig{Enabled: true}}}
	w := httptest.NewRecorder()
	srv.handleRfNoise(w, httptest.NewRequest("GET", "/api/rf-noise?bbox=50,3,52,4&mine=1", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("mine=1 with user management off = %d, want 404", w.Code)
	}
}

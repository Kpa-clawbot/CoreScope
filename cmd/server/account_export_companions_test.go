package main

import (
	"net/http"
	"strings"
	"testing"
)

// Companion linking stores companion links and device labels; both belong
// in the account export (tokens and challenges stay out).
func TestAccountExportCompanionsAndDeviceSessions(t *testing.T) {
	f := newAuthFixture(t)
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)

	w := f.do("GET", "/api/account/export", nil, as(alice))
	expectStatus(t, w, http.StatusOK)
	if !strings.Contains(w.Body.String(), `"companions":[]`) {
		t.Fatalf("no companions should export as []: %s", w.Body.String())
	}

	tok := f.deviceToken(t, "alice@example.org", pw, "Pixel 8").Token
	expectStatus(t, f.linkCompanion(t, tok, companionKey, "Car"), http.StatusOK)
	w = f.do("GET", "/api/account/export", nil, as(alice))
	expectStatus(t, w, http.StatusOK)
	x := decode[accountExport](t, w)
	if len(x.Companions) != 1 || x.Companions[0].Pubkey != pubHex(companionKey) || x.Companions[0].Name != "Car" ||
		x.Companions[0].LinkedAt == "" {
		t.Fatalf("companions = %+v", x.Companions)
	}
	kinds := map[string]string{}
	for _, s := range x.Sessions {
		kinds[s.Kind] = s.Label
	}
	if label, ok := kinds["device"]; !ok || label != "Pixel 8" {
		t.Fatalf("sessions = %+v", x.Sessions)
	}
	if _, ok := kinds["web"]; !ok {
		t.Fatalf("web session missing: %+v", x.Sessions)
	}
	if strings.Contains(w.Body.String(), tok) {
		t.Fatal("the export contains the device token")
	}
}

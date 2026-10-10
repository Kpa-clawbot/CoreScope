package main

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func TestAdminUserDetailCompanions(t *testing.T) {
	f := newAuthFixture(t, "admin@example.org")
	admin := f.registerAndActivate(t, "admin@example.org", "Admin", pw)
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	tok := f.deviceToken(t, "alice@example.org", pw, "Pixel").Token
	expectStatus(t, f.linkCompanion(t, tok, companionKey, "Car"), http.StatusOK)

	w := f.do("GET", "/api/admin/users/"+strconv.FormatInt(alice.me.ID, 10), nil, as(admin))
	expectStatus(t, w, http.StatusOK)
	d := decode[adminUserDetailJSON](t, w)
	if len(d.Companions) != 1 || d.Companions[0].Pubkey != pubHex(companionKey) || d.Companions[0].Name != "Car" {
		t.Fatalf("companions = %+v", d.Companions)
	}
	w = f.do("GET", "/api/admin/users/"+strconv.FormatInt(admin.me.ID, 10), nil, as(admin))
	expectStatus(t, w, http.StatusOK)
	if !strings.Contains(w.Body.String(), `"companions":[]`) {
		t.Fatalf("no companions should be [], body %s", w.Body.String())
	}
}

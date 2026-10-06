//go:build e2etest

package main

import "testing"

func TestE2ELastMailReturnsNewestFakeMail(t *testing.T) {
	f := newAuthFixture(t) // registerAuthRoutes must add the hook itself
	expectStatus(t, f.do("GET", "/__e2e/last-mail", nil), 404)
	f.registerAndActivate(t, "e2e@example.test", "E2E", "correct horse battery")
	w := f.do("GET", "/__e2e/last-mail", nil)
	expectStatus(t, w, 200)
	m := decode[e2eMail](t, w)
	if m.To != "e2e@example.test" || m.Text == "" {
		t.Fatalf("unexpected mail: %+v", m)
	}
}

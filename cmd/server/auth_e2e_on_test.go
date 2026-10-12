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

func TestE2EUnsubscribeLink(t *testing.T) {
	f := newAuthFixture(t)
	expectStatus(t, f.do("GET", "/__e2e/unsubscribe-link?email=nobody@example.test", nil), 404)
	c := f.registerAndActivate(t, "e2e@example.test", "E2E", "correct horse battery")
	w := f.do("GET", "/__e2e/unsubscribe-link?email=e2e@example.test", nil)
	expectStatus(t, w, 200)
	p, err := f.st.NotifyPrefsFor(c.me.ID)
	if err != nil {
		t.Fatal(err)
	}
	if l := decode[e2eLink](t, w); l.Link != testBase+"/#/account/unsubscribe?token="+p.UnsubToken {
		t.Fatalf("link = %q", l.Link)
	}
}

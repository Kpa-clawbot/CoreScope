//go:build !e2etest

package main

import "testing"

// The e2e probe must never exist in a normal build.
func TestE2ELastMailAbsentInNormalBuild(t *testing.T) {
	f := newAuthFixture(t)
	expectStatus(t, f.do("GET", "/__e2e/last-mail", nil), 404)
	if e2eRoutes != nil || fakeMailerAllowed {
		t.Fatal("e2e hooks are set in a normal build")
	}
}

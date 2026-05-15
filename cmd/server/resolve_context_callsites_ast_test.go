package main

import (
	"regexp"
	"testing"
)

// TestResolveWithContextRegexBlindspot demonstrates the failure mode of the
// ad-hoc regex used by TestAllResolveWithContextCallSitesPassNonNilContext.
//
// Issue #1199 (item 1): the regex `resolveWithContext\s*\(\s*([^,]+?)\s*,\s*
// ([^,]+?)\s*,` greedily splits on the FIRST comma. Any nested call in arg1
// (e.g. `getHop(a, b)`) makes the regex capture `b)` as arg2 and the real
// `nil` arg2 sails past the gate.
//
// RED: this test asserts the regex correctly flags the synthetic offender
// below. It does not — the regex returns 0 offenders for input that clearly
// passes nil as the resolver's context arg. Test fails ⇒ proves the gate has
// a hole. GREEN follow-up replaces the regex with a go/parser AST walk.
func TestResolveWithContextRegexBlindspot(t *testing.T) {
	src := `package x

func f() {
	pm.resolveWithContext(getHop(a, b), nil, graph)
}
`
	re := regexp.MustCompile(`resolveWithContext\s*\(\s*([^,]+?)\s*,\s*([^,]+?)\s*,`)
	matches := re.FindAllStringSubmatch(src, -1)
	offenders := 0
	for _, m := range matches {
		if m[2] == "nil" {
			offenders++
		}
	}
	if offenders == 0 {
		t.Fatalf("regex blindspot confirmed: synthetic source contains a nil context "+
			"call that the regex misses (parsed %d call(s), 0 flagged). The static-grep "+
			"gate must be replaced with a go/parser AST walk.", len(matches))
	}
}

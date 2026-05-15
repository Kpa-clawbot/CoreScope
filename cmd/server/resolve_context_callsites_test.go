package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestAllResolveWithContextCallSitesPassNonNilContext is a static grep gate
// against #1197: every call to pm.resolveWithContext(...) in store.go must
// pass a non-nil context argument. Reverting any one call site to `nil` would
// silently re-introduce the regression #1197 is meant to prevent.
//
// Allowed exceptions: callers that must pass nil are listed explicitly here
// (e.g. unit tests, GetSubpathDetail's user-supplied raw-hop list — currently
// none in production code).
func TestAllResolveWithContextCallSitesPassNonNilContext(t *testing.T) {
	body, err := os.ReadFile("store.go")
	if err != nil {
		t.Fatalf("read store.go: %v", err)
	}
	src := string(body)

	// Match: pm.resolveWithContext(<arg1>, <arg2>, ...) capture arg2.
	// Allow whitespace, tolerate identifiers/expressions in arg1.
	re := regexp.MustCompile(`resolveWithContext\s*\(\s*([^,]+?)\s*,\s*([^,]+?)\s*,`)
	matches := re.FindAllStringSubmatchIndex(src, -1)
	if len(matches) == 0 {
		t.Fatalf("no resolveWithContext call sites found — test scaffold broken")
	}

	var offenders []string
	for _, m := range matches {
		full := src[m[0]:m[1]]
		arg2 := strings.TrimSpace(src[m[4]:m[5]])
		if arg2 == "nil" {
			// Compute line number for diagnostics.
			line := 1 + strings.Count(src[:m[0]], "\n")
			offenders = append(offenders, fmtCallSite(line, full))
		}
	}
	if len(offenders) > 0 {
		t.Fatalf("found %d call site(s) of pm.resolveWithContext that pass nil context "+
			"(re-introduces regression #1197 — must pass non-nil contextPubkeys):\n  %s",
			len(offenders), strings.Join(offenders, "\n  "))
	}
}

func fmtCallSite(line int, snippet string) string {
	return "store.go:" + itoa(line) + " — " + snippet
}

func itoa(i int) string {
	// Avoid pulling strconv into a tiny helper; trivial inline.
	if i == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(b[pos:])
}

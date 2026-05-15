package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestAllResolveWithContextCallSitesPassNonNilContext is a static grep gate
// against #1197: every call to pm.resolveWithContext(...) in production code
// (any non-test *.go file under cmd/server/) must pass a non-nil context
// argument. Reverting any one call site to `nil` would silently re-introduce
// the regression #1197 is meant to prevent.
//
// Scope rationale: the original gate only scanned store.go and missed
// routes.go:1428 (handleNodePaths) which still passed `nil`. Extending the
// scope to all production *.go files in cmd/server/ closes that hole.
//
// Allowed exceptions: callers that must pass nil (currently none in
// production code) should be enumerated in `allowedNilCallers` below.
func TestAllResolveWithContextCallSitesPassNonNilContext(t *testing.T) {
	allowedNilCallers := map[string]bool{
		// "<file>:<line>": true,
	}

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob *.go: %v", err)
	}

	// Match: resolveWithContext(<arg1>, <arg2>, ...) — capture arg2.
	re := regexp.MustCompile(`resolveWithContext\s*\(\s*([^,]+?)\s*,\s*([^,]+?)\s*,`)

	var offenders []string
	totalCallSites := 0
	scannedFiles := 0
	for _, f := range files {
		// Skip *_test.go (unit tests legitimately pass nil for fixture-driven
		// behavior) and the test scaffold itself.
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		scannedFiles++
		src := string(body)
		matches := re.FindAllStringSubmatchIndex(src, -1)
		for _, m := range matches {
			totalCallSites++
			full := src[m[0]:m[1]]
			arg2 := strings.TrimSpace(src[m[4]:m[5]])
			if arg2 != "nil" {
				continue
			}
			line := 1 + strings.Count(src[:m[0]], "\n")
			site := f + ":" + itoa(line)
			if allowedNilCallers[site] {
				continue
			}
			offenders = append(offenders, site+" — "+full)
		}
	}

	if scannedFiles == 0 {
		t.Fatalf("no production *.go files scanned — test scaffold broken")
	}
	if totalCallSites == 0 {
		t.Fatalf("no resolveWithContext call sites found across %d files — test scaffold broken", scannedFiles)
	}
	if len(offenders) > 0 {
		t.Fatalf("found %d call site(s) of pm.resolveWithContext that pass nil context "+
			"(re-introduces regression #1197 — must pass non-nil contextPubkeys):\n  %s",
			len(offenders), strings.Join(offenders, "\n  "))
	}
}

func itoa(i int) string {
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

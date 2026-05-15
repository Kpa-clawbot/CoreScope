package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestAllResolveWithContextCallSitesPassNonNilContext is a static AST-based
// gate against #1197/#1199: every call to pm.resolveWithContext(...) in
// production code (any non-test *.go file under cmd/server/) must pass a
// non-nil context as the second argument. Reverting any one call site to
// `nil` would silently re-introduce the regression #1197 is meant to prevent.
//
// History: the original gate (issue #1197) was a regex grep that split on
// the first comma. Issue #1199 (item 1) showed that input like
// `pm.resolveWithContext(getHop(a, b), nil, graph)` slipped past — the regex
// captured `b)` as arg2. Same hazard for any gofmt-induced multi-line
// reflow. This test now uses go/parser to walk the AST: arg2 is the SECOND
// formal argument by position, robust against nesting and formatting.
//
// Allowed exceptions: callers that must pass nil (currently none in
// production code) should be enumerated in `allowedNilCallers` below by
// "<file>:<line>".
func TestAllResolveWithContextCallSitesPassNonNilContext(t *testing.T) {
	allowedNilCallers := map[string]bool{
		// "<file>:<line>": true,
	}

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob *.go: %v", err)
	}

	type callSite struct {
		file string
		line int
		text string
	}

	var offenders []callSite
	totalCallSites := 0
	scannedFiles := 0
	fset := token.NewFileSet()
	for _, f := range files {
		// Skip *_test.go (unit tests legitimately pass nil for fixture-driven
		// behavior) and the test scaffold itself.
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(fset, f, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		scannedFiles++
		ast.Inspect(af, func(n ast.Node) bool {
			ce, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := ce.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel == nil || sel.Sel.Name != "resolveWithContext" {
				return true
			}
			if len(ce.Args) < 2 {
				return true
			}
			totalCallSites++
			arg2 := ce.Args[1]
			id, ok := arg2.(*ast.Ident)
			if !ok || id.Name != "nil" {
				return true
			}
			pos := fset.Position(ce.Pos())
			site := callSite{file: f, line: pos.Line, text: exprText(arg2)}
			key := fmt.Sprintf("%s:%d", f, pos.Line)
			if allowedNilCallers[key] {
				return true
			}
			offenders = append(offenders, site)
			return true
		})
	}

	if scannedFiles == 0 {
		t.Fatalf("no production *.go files scanned — test scaffold broken")
	}
	if totalCallSites == 0 {
		t.Fatalf("no resolveWithContext call sites found across %d files — test scaffold broken", scannedFiles)
	}
	if len(offenders) > 0 {
		sort.Slice(offenders, func(i, j int) bool {
			if offenders[i].file != offenders[j].file {
				return offenders[i].file < offenders[j].file
			}
			return offenders[i].line < offenders[j].line
		})
		var lines []string
		for _, o := range offenders {
			lines = append(lines, fmt.Sprintf("%s:%d — arg2=%s", o.file, o.line, o.text))
		}
		t.Fatalf("found %d call site(s) of pm.resolveWithContext that pass nil context "+
			"(re-introduces regression #1197 — must pass non-nil contextPubkeys):\n  %s",
			len(offenders), strings.Join(lines, "\n  "))
	}
}

func exprText(e ast.Expr) string {
	if id, ok := e.(*ast.Ident); ok {
		return id.Name
	}
	return fmt.Sprintf("%T", e)
}

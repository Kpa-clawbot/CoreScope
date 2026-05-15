package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// minPMResolveWithContextCallSites is a floor on how many production-code
// call sites of `pm.resolveWithContext(...)` the AST walker must find. If
// the selector matcher is accidentally narrowed (e.g. typo in the receiver
// name, or refactor that renames the method) the count will drop below the
// floor and the test will fail loudly instead of silently passing with
// zero offenders. Bump this if legitimate call sites are added/removed.
const minPMResolveWithContextCallSites = 3

// nilContextOffender describes a `pm.resolveWithContext(x, nil, ...)` call
// site found in production code. file is the source filename, line the 1-based
// line number, text a stable rendering of arg2 (always "nil" today, but kept
// for future expansion to other forbidden expressions).
type nilContextOffender struct {
	file string
	line int
	text string
}

// findPMResolveNilContextOffenders walks one parsed *ast.File and returns
// every call site of `pm.resolveWithContext(...)` whose second argument is
// the identifier `nil`. The selector receiver is constrained to the literal
// identifier `pm` to prevent the matcher from accidentally firing on
// unrelated types that happen to expose a `resolveWithContext` method.
//
// Returns offenders, total matched call sites (including non-offenders), and
// any first-encountered error (currently unused, reserved for future
// expansion). totalCallSites is reported separately so callers can enforce
// a floor — see minPMResolveWithContextCallSites.
func findPMResolveNilContextOffenders(fset *token.FileSet, file *ast.File, filename string) (offenders []nilContextOffender, totalCallSites int) {
	ast.Inspect(file, func(n ast.Node) bool {
		ce, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := ce.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil || sel.Sel.Name != "resolveWithContext" {
			return true
		}
		// Constrain receiver to the literal identifier `pm`. This prevents
		// drive-by matches on `foo.resolveWithContext(...)` for any other
		// type. See #1199 review (adv #3).
		recv, ok := sel.X.(*ast.Ident)
		if !ok || recv.Name != "pm" {
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
		offenders = append(offenders, nilContextOffender{
			file: filename,
			line: pos.Line,
			text: renderExpr(fset, arg2),
		})
		return true
	})
	return offenders, totalCallSites
}

// renderExpr round-trips an ast.Expr back to source text via go/printer so
// the failure message names the real expression (e.g. `nil`, `getCtx()`,
// `someVar`) instead of an ast type tag. Falls back to a Go-syntax
// description if printing fails.
func renderExpr(fset *token.FileSet, e ast.Expr) string {
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, fset, e); err != nil {
		return fmt.Sprintf("<unprintable %T: %v>", e, err)
	}
	return buf.String()
}

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

	var offenders []nilContextOffender
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
		fileOffenders, fileTotal := findPMResolveNilContextOffenders(fset, af, f)
		totalCallSites += fileTotal
		for _, o := range fileOffenders {
			key := fmt.Sprintf("%s:%d", o.file, o.line)
			if allowedNilCallers[key] {
				continue
			}
			offenders = append(offenders, o)
		}
	}

	if scannedFiles == 0 {
		t.Fatalf("no production *.go files scanned — test scaffold broken")
	}
	if totalCallSites < minPMResolveWithContextCallSites {
		t.Fatalf("found only %d pm.resolveWithContext call site(s) across %d files "+
			"(floor is %d) — selector matcher likely too narrow, or call sites were "+
			"removed without updating the floor",
			totalCallSites, scannedFiles, minPMResolveWithContextCallSites)
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

// TestFindPMResolveNilContextOffenders_SelfTest is the anti-tautology guard
// for the AST walker (#1199 r1 kent MF-1). The deleted regex blindspot test
// served the same purpose for the old regex matcher: if the matcher quietly
// stops detecting violations, the production gate above will pass vacuously.
// This test feeds the walker a synthetic Go source string with a known mix
// of clean and violating call sites and asserts the walker flags exactly
// the violators — no more, no less.
//
// If the walker is broken (e.g. selector predicate inverted, arg2 index
// off-by-one, nil-Ident check removed), this test fails. If the walker's
// selector is broadened (e.g. accepts any receiver), the negative cases for
// `other.resolveWithContext(h, nil, g)` and `Foo.resolveWithContext(h, nil, g)`
// will start being flagged and the assertion below will fail.
func TestFindPMResolveNilContextOffenders_SelfTest(t *testing.T) {
	src := `package fake

func _() {
	var pm *prefixMap
	var other *prefixMap
	var h string
	var ctx []string
	var g interface{}

	// CLEAN — must NOT be flagged.
	pm.resolveWithContext(h, ctx, g)
	pm.resolveWithContext(getHop("a", "b"), ctx, g)

	// VIOLATING — must be flagged.
	pm.resolveWithContext(h, nil, g)
	pm.resolveWithContext(getHop("a", "b"), nil, g)

	// NON-pm receiver — must NOT be flagged (selector constrained to pm).
	other.resolveWithContext(h, nil, g)
	Foo{}.resolveWithContext(h, nil, g)

	// Different method name — must NOT be flagged.
	pm.resolveSomethingElse(h, nil, g)
}

func getHop(a, b string) string { return a + b }

type prefixMap struct{}
func (p *prefixMap) resolveWithContext(h string, ctx []string, g interface{}) {}
func (p *prefixMap) resolveSomethingElse(h string, ctx []string, g interface{}) {}

type Foo struct{}
func (Foo) resolveWithContext(h string, ctx []string, g interface{}) {}
`

	fset := token.NewFileSet()
	af, err := parser.ParseFile(fset, "synthetic.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse synthetic source: %v", err)
	}

	offenders, totalCallSites := findPMResolveNilContextOffenders(fset, af, "synthetic.go")

	// Expect 4 pm.resolveWithContext call sites total (2 clean + 2 nil),
	// of which 2 are nil-context offenders. The two non-pm receivers and
	// the resolveSomethingElse call MUST be ignored.
	const wantTotal = 4
	const wantOffenders = 2
	if totalCallSites != wantTotal {
		t.Errorf("totalCallSites = %d, want %d (selector should match pm.resolveWithContext only)",
			totalCallSites, wantTotal)
	}
	if len(offenders) != wantOffenders {
		t.Errorf("len(offenders) = %d, want %d", len(offenders), wantOffenders)
		for _, o := range offenders {
			t.Logf("  offender: %s:%d arg2=%s", o.file, o.line, o.text)
		}
	}

	// Both flagged offenders must render arg2 as the literal text "nil"
	// (proves renderExpr is round-tripping ast → source, not returning a
	// type tag like "*ast.Ident").
	for _, o := range offenders {
		if o.text != "nil" {
			t.Errorf("offender at line %d: arg2 text = %q, want %q", o.line, o.text, "nil")
		}
	}
}

// TestRenderExprRoundTripsSource is a focused assertion that renderExpr
// uses go/printer (not %T) — guards against regressing exprText back to
// the dead-branch state that always returned the type name "*ast.Ident".
func TestRenderExprRoundTripsSource(t *testing.T) {
	cases := []struct{ src, want string }{
		{"nil", "nil"},
		{"ctx", "ctx"},
		{`getHop("a", "b")`, `getHop("a", "b")`},
		{"foo.bar", "foo.bar"},
	}
	fset := token.NewFileSet()
	for _, tc := range cases {
		expr, err := parser.ParseExprFrom(fset, "expr.go", tc.src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %q: %v", tc.src, err)
		}
		got := renderExpr(fset, expr)
		if got != tc.want {
			t.Errorf("renderExpr(%q) = %q, want %q", tc.src, got, tc.want)
		}
	}
}

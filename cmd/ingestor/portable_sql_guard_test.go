package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The ingestor's runtime SQL must run unchanged on SQLite and Postgres. This
// guard fails on the SQLite-only forms that have portable replacements (see
// portable_sql.go) anywhere in the non-test sources, except for the listed
// forms inside the functions below. Matching is by function name, parsed with
// go/ast, so the list cannot drift with line numbers. An allowlisted
// (function, form) pair that no longer matches fails too, so the list only
// shrinks.
var sqliteOnlyFuncs = map[string]struct {
	forms  []string
	reason string
}{
	"applySchema": {[]string{"INSERT OR IGNORE"},
		"legacy SQLite schema and migrations; Postgres gets its own schema"},
	// The per-backend choices for the ingest hot path: SQLite keeps its
	// original, cheaper SQL; Postgres gets the portable form.
	"insertTransmissionFunc": {[]string{".LastInsertId("}, "SQLite branch of the transmissions insert"},
	"lastSeenClampSQL":       {[]string{"two-arg MAX/MIN"}, "SQLite branch of the last_seen clamp"},
}

// sqliteOnlySQL matches the forms inside string literals, case-insensitively
// and across line breaks.
var sqliteOnlySQL = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bINSERT\s+OR\s+IGNORE\b`),
	regexp.MustCompile(`(?i)\bINSERT\s+OR\s+REPLACE\b`),
	regexp.MustCompile(`(?i)\bREPLACE\s+INTO\b`),
	regexp.MustCompile(`(?i)\bIS\s+NOT\s+'`),
}

// twoArgMaxMin finds max( or min( calls with a comma at their top level:
// SQLite's scalar two-argument forms, which Postgres lacks (one-argument MAX
// and MIN are aggregates and portable). A call left open at the end of the
// literal is reported too, since its arguments are built in Go.
var maxMinCall = regexp.MustCompile(`(?i)\b(MAX|MIN)\s*\(`)

func twoArgMaxMin(sql string) bool {
	for _, loc := range maxMinCall.FindAllStringIndex(sql, -1) {
		depth, quoted := 1, false
		i := loc[1]
		for ; i < len(sql) && depth > 0; i++ {
			switch c := sql[i]; {
			case c == '\'':
				quoted = !quoted
			case quoted:
			case c == '(':
				depth++
			case c == ')':
				depth--
			case c == ',' && depth == 1:
				return true
			}
		}
		if depth > 0 {
			return true
		}
	}
	return false
}

type sqlGuardHit struct {
	fn, where, what string
}

// scanSQLiteOnly reports every SQLite-only form in src, attributed to the
// enclosing top-level function ("" outside any function).
func scanSQLiteOnly(t *testing.T, fset *token.FileSet, file *ast.File) []sqlGuardHit {
	t.Helper()
	var hits []sqlGuardHit
	var fn string
	var visit func(ast.Node) bool
	visit = func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.BasicLit:
			if x.Kind != token.STRING {
				return true
			}
			v, err := strconv.Unquote(x.Value)
			if err != nil {
				t.Fatalf("%s: unquote: %v", fset.Position(x.Pos()), err)
			}
			for _, re := range sqliteOnlySQL {
				if m := re.FindString(v); m != "" {
					m = strings.ToUpper(strings.Join(strings.Fields(m), " "))
					hits = append(hits, sqlGuardHit{fn, fset.Position(x.Pos()).String(), m})
				}
			}
			if twoArgMaxMin(v) {
				hits = append(hits, sqlGuardHit{fn, fset.Position(x.Pos()).String(), "two-arg MAX/MIN"})
			}
		case *ast.SelectorExpr:
			if x.Sel.Name == "LastInsertId" {
				hits = append(hits, sqlGuardHit{fn, fset.Position(x.Pos()).String(), ".LastInsertId("})
			}
		}
		return true
	}
	for _, decl := range file.Decls {
		fn = ""
		if fd, ok := decl.(*ast.FuncDecl); ok {
			fn = fd.Name.Name
		}
		ast.Inspect(decl, visit)
	}
	return hits
}

func TestPortableSQLGuard(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	used := map[string]bool{}
	scanned := 0
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		scanned++
		for _, h := range scanSQLiteOnly(t, fset, file) {
			if allowed(h) {
				used[h.fn+"\x00"+h.what] = true
				continue
			}
			where := "package level"
			if h.fn != "" {
				where = "func " + h.fn
			}
			t.Errorf("%s (%s): SQLite-only %q in runtime SQL; use the portable form (portable_sql.go)", h.where, where, h.what)
		}
	}
	if scanned == 0 {
		t.Fatal("no sources scanned")
	}
	var stale []string
	for fn, entry := range sqliteOnlyFuncs {
		for _, form := range entry.forms {
			if !used[fn+"\x00"+form] {
				stale = append(stale, fn+": "+form)
			}
		}
	}
	sort.Strings(stale)
	for _, s := range stale {
		t.Errorf("allowlisted %s no longer matches (or the func is gone); remove it from sqliteOnlyFuncs", s)
	}
}

func allowed(h sqlGuardHit) bool {
	if h.fn == "" {
		return false
	}
	for _, form := range sqliteOnlyFuncs[h.fn].forms {
		if form == h.what {
			return true
		}
	}
	return false
}

// The scanner itself: it must catch each form in a raw or interpreted string,
// across line breaks, attribute it to the enclosing function, and ignore
// comments.
func TestPortableSQLGuardScanner(t *testing.T) {
	src := "package p\n" +
		"// INSERT OR IGNORE in a comment is fine\n" +
		"const top = `INSERT OR REPLACE INTO t VALUES (1)`\n" +
		"func a() { db.Exec(\"insert or\\n ignore into t values (1)\") }\n" +
		"func b() { db.Exec(`SELECT 1 WHERE x IS NOT 'tx'`); db.Exec(`REPLACE INTO t VALUES (1)`) }\n" +
		"func c() { id, _ := res.LastInsertId(); _ = id }\n" +
		"func d() { db.Exec(`INSERT INTO t VALUES (1) ON CONFLICT DO NOTHING`); db.Exec(`SELECT x IS DISTINCT FROM 'tx'`) }\n" +
		"func e() { db.Exec(`SELECT MAX(id), min(COALESCE(a, ','), 'x') FROM t`) }\n" +
		"func f() { db.Exec(`SELECT MAX(id), MIN(ts), COALESCE(a, b) FROM t`); _ = \"MAX(MIN(COALESCE(\" + col }\n"
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "p.go", src, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, h := range scanSQLiteOnly(t, fset, file) {
		got = append(got, h.fn+":"+h.what)
	}
	want := []string{":INSERT OR REPLACE", ":REPLACE INTO", "a:INSERT OR IGNORE", "b:IS NOT '", "b:REPLACE INTO", "c:.LastInsertId(", "e:two-arg MAX/MIN", "f:two-arg MAX/MIN"}
	sort.Strings(got)
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("scanner hits\n got %v\nwant %v", got, want)
	}
}

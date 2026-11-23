package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Location privacy (B3): every read of node coordinates (lat/lon of nodes or
// inactive_nodes) goes through node_coords.go, where servedNodePos applies
// the fuzzer. TestNodeCoordReadsInvariant fails on SQL selecting them
// anywhere else, and on a reader in node_coords.go that neither applies
// servedNodePos nor is a listed exact reader.

// nodeCoordFile is the one file that may read node coordinates.
const nodeCoordFile = "node_coords.go"

// exactNodeCoordReaders are the readers in nodeCoordFile that read the exact
// position, each for a classification whose result is all that leaves it.
var exactNodeCoordReaders = map[string]string{
	"exactNodePositions":   "geo_filter of /api/nodes: nodes near the polygon edge must not flip with their approximate position",
	"GetNodePubkeysInArea": "area membership (?area=), an operator-defined polygon classified like the geo_filter",
	"GetNodesForGeoPrune":  "admin-only geo-filter prune preview and confirm, classified by the geo_filter",
}

var (
	nodeTableRe = regexp.MustCompile(`(?i)\b(?:from|join)\s+["` + "`" + `]?(?:nodes|inactive_nodes)["` + "`" + `]?(?:\s+(?:as\s+)?(\w+))?`)
	// An unqualified lat/lon column (not cr.lat, not "lat" as a word inside
	// an identifier).
	bareCoordRe = regexp.MustCompile(`(?i)(?:^|[^\w.])(?:lat|lon)\b`)
	sqlKeywords = map[string]bool{"where": true, "on": true, "left": true, "right": true, "inner": true,
		"outer": true, "join": true, "cross": true, "natural": true, "order": true, "group": true,
		"limit": true, "using": true, "union": true, "having": true, "and": true, "or": true}
)

// nodeCoordUnit is one top-level declaration: its string literals and the
// identifiers it references.
type nodeCoordUnit struct {
	file, name string
	pos        token.Position
	lits       []string
	refs       map[string]bool
}

// readsNodeCoords reports whether the unit's SQL selects lat/lon from nodes
// or inactive_nodes: some literal names the table and some SQL literal (one
// with a space or comma) names lat/lon bare or through the table's alias.
func (u nodeCoordUnit) readsNodeCoords() bool {
	var tables bool
	aliases := map[string]bool{"nodes": true, "inactive_nodes": true}
	for _, l := range u.lits {
		for _, m := range nodeTableRe.FindAllStringSubmatch(l, -1) {
			tables = true
			if a := strings.ToLower(m[1]); a != "" && !sqlKeywords[a] {
				aliases[a] = true
			}
		}
	}
	if !tables {
		return false
	}
	names := make([]string, 0, len(aliases))
	for a := range aliases {
		names = append(names, regexp.QuoteMeta(a))
	}
	qualified := regexp.MustCompile(`(?i)\b(?:` + strings.Join(names, "|") + `)\.(?:lat|lon)\b`)
	for _, l := range u.lits {
		if !strings.ContainsAny(l, " ,\t\n") {
			continue // a key or a word, not SQL
		}
		if bareCoordRe.MatchString(l) || qualified.MatchString(l) {
			return true
		}
	}
	return false
}

// nodeCoordUnits parses one Go source into its top-level units.
func nodeCoordUnits(t *testing.T, fset *token.FileSet, name string, src []byte) []nodeCoordUnit {
	t.Helper()
	f, err := parser.ParseFile(fset, name, src, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	var units []nodeCoordUnit
	collect := func(n ast.Node, unitName string) {
		u := nodeCoordUnit{file: name, name: unitName, pos: fset.Position(n.Pos()), refs: map[string]bool{}}
		ast.Inspect(n, func(x ast.Node) bool {
			switch v := x.(type) {
			case *ast.BasicLit:
				if v.Kind == token.STRING {
					if s, err := strconv.Unquote(v.Value); err == nil {
						u.lits = append(u.lits, s)
					}
				}
			case *ast.Ident:
				u.refs[v.Name] = true
			}
			return true
		})
		units = append(units, u)
	}
	for _, d := range f.Decls {
		switch v := d.(type) {
		case *ast.FuncDecl:
			collect(v, v.Name.Name)
		case *ast.GenDecl:
			for _, s := range v.Specs {
				if vs, ok := s.(*ast.ValueSpec); ok {
					collect(vs, vs.Names[0].Name)
				}
			}
		}
	}
	return units
}

// nodeCoordViolations applies the invariant to the units of the package.
func nodeCoordViolations(units []nodeCoordUnit) []string {
	var out []string
	seenExact := map[string]bool{}
	for _, u := range units {
		where := u.pos.String() + " " + u.name
		if u.file != nodeCoordFile {
			if u.readsNodeCoords() {
				out = append(out, where+": selects node lat/lon outside "+nodeCoordFile)
			}
			if u.refs["nodeSelectCols"] && !u.refs["scanNodeRow"] {
				out = append(out, where+": uses nodeSelectCols without scanNodeRow")
			}
			continue
		}
		if _, exact := exactNodeCoordReaders[u.name]; exact {
			seenExact[u.name] = true
			continue
		}
		if u.readsNodeCoords() && !u.refs["servedNodePos"] {
			out = append(out, where+": reads node lat/lon without servedNodePos")
		}
		if u.name == "scanNodeRow" && !u.refs["servedNodePos"] {
			out = append(out, where+": scanNodeRow must apply servedNodePos")
		}
	}
	return out
}

func TestNodeCoordReadsInvariant(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var units []nodeCoordUnit
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		units = append(units, nodeCoordUnits(t, fset, name, src)...)
	}
	got := map[string]bool{}
	readers := 0
	for _, u := range units {
		got[u.file+":"+u.name] = true
		if u.file == nodeCoordFile && u.readsNodeCoords() {
			readers++
		}
	}
	for name := range exactNodeCoordReaders {
		if !got[nodeCoordFile+":"+name] {
			t.Errorf("exactNodeCoordReaders lists %s, which is not in %s", name, nodeCoordFile)
		}
	}
	if !got[nodeCoordFile+":scanNodeRow"] || !got[nodeCoordFile+":getAllNodes"] {
		t.Error("scanNodeRow and getAllNodes must live in " + nodeCoordFile)
	}
	// The detector must see the readers it guards, or it proves nothing.
	if readers < 6 {
		t.Errorf("only %d coordinate readers detected in %s", readers, nodeCoordFile)
	}
	v := nodeCoordViolations(units)
	sort.Strings(v)
	for _, s := range v {
		t.Error(s)
	}
}

// The detector itself, on made-up sources.
func TestNodeCoordReadsInvariantDetector(t *testing.T) {
	cases := []struct {
		name, file, src string
		bad             bool
	}{
		{"plain", "x.go", `func f() { q("SELECT public_key, lat, lon FROM nodes") }`, true},
		{"split", "x.go", `func f() { q("SELECT lat, lon " + "FROM nodes WHERE 1") }`, true},
		{"alias", "x.go", "func f() { q(`SELECT n.lat FROM inactive_nodes AS n`) }", true},
		{"alias join", "x.go", `func f() { q("SELECT c.id, n.lon FROM c JOIN nodes n ON n.public_key = c.k") }`, true},
		{"const", "x.go", `const c = "SELECT lat FROM nodes"`, true},
		{"cols helper without scan", "x.go", `func f(db *DB) { q("SELECT " + db.nodeSelectCols() + " FROM nodes") }`, true},
		{"unfuzzed in the coord file", nodeCoordFile, `func g() { q("SELECT lat, lon FROM nodes") }`, true},
		{"fuzzed in the coord file", nodeCoordFile, `func g(db *DB) { q("SELECT lat, lon FROM nodes"); db.servedNodePos("", a, b) }`, false},
		{"exact reader", nodeCoordFile, `func exactNodePositions() { q("SELECT lat, lon FROM nodes") }`, false},
		{"other table", "x.go", `func f() { q("SELECT lat, lon FROM client_receptions") }`, false},
		{"other table joined", "x.go", `func f() { q("SELECT cr.lat, cr.lon FROM client_receptions cr LEFT JOIN nodes n ON n.public_key = cr.k") }`, false},
		{"no coordinates", "x.go", `func f() { q("SELECT name FROM nodes"); m["lat"] = 1 }`, false},
		{"cols helper with scan", "x.go", `func f(db *DB) { q("SELECT " + db.nodeSelectCols() + " FROM nodes"); db.scanNodeRow(r) }`, false},
	}
	for _, tc := range cases {
		fset := token.NewFileSet()
		units := nodeCoordUnits(t, fset, tc.file, []byte("package main\n"+tc.src))
		if bad := len(nodeCoordViolations(units)) > 0; bad != tc.bad {
			t.Errorf("%s: violation=%v, want %v", tc.name, bad, tc.bad)
		}
	}
}
